package app_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/inbound"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// inboundLeader is a leader that holds the lease and does not drain.
type inboundLeader struct{}

func (inboundLeader) Status() (epoch int64, held, draining bool) { return 1, true, false }

// fixedRevision is the registry as the inbound handler reads it.
type fixedRevision struct{ rev *config.Revision }

func (r fixedRevision) Current() *config.Revision { return r.rev }

// secretText is the inbound source's secret file.
type secretText string

func (s secretText) Secret(context.Context, string) (string, error) { return string(s), nil }

// A group retried after a failure midway takes each event once (Review
// Focus 1), through the handler, against a real coordinator: a group of three
// alerts of one dedup key, whose third event's append fails once, is answered
// 503 with the first two taken; the same request again is answered 200, the
// first two repeats and the third appended; each event has one row.
func TestAGroupRetriedAfterAFailureMidwayTakesEachEventOnce(t *testing.T) {
	h := newHarness(t)
	// The example's checks refuse its events until Plan 7c-2: the handler
	// reads a revision whose mapping has none.
	handler := inbound.NewHandler(&inbound.Ports{
		Coordinator: h.c, Leader: inboundLeader{}, Revisions: fixedRevision{withoutChecks(t)}, Secrets: secretText("s3cret"),
		Clock: h.clock.Now, Tenant: "tenant",
	})
	at := h.clock.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	var alerts []string
	for _, fp := range []string{"f1", "f2", "f3"} {
		alerts = append(alerts, fmt.Sprintf(`{"fingerprint":%q,"status":"firing","startsAt":%q,"endsAt":"","labels":{"service":"checkout","rule_id":"r1"},"annotations":{"summary":"checkout fails"}}`, fp, at))
	}
	body := []byte(`{"alerts":[` + strings.Join(alerts, ",") + `]}`)
	third := "f3:firing:" + at
	var refused atomic.Bool
	h.records.refuses(func(_ item.ID, drafts []item.Draft) error {
		for i := range drafts {
			if tr := drafts[i].Body.Triggered; tr != nil && tr.EventID == third && refused.CompareAndSwap(false, true) {
				return errStore
			}
		}
		return nil
	})
	post := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/inbound/alerting", bytes.NewReader(body))
		ts := strconv.FormatInt(h.clock.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte("s3cret"))
		mac.Write([]byte(ts + "." + string(body)))
		r.Header.Set(inbound.HeaderTimestamp, ts)
		r.Header.Set(inbound.HeaderSignature, "v1="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	first := post()

	require.Equal(t, http.StatusServiceUnavailable, first.Code, first.Body.String())
	require.True(t, refused.Load(), "the third event's append was refused")
	assert.Len(t, rowsOf(h, "alerting", "f1:firing:"+at), 1)
	assert.Len(t, rowsOf(h, "alerting", "f2:firing:"+at), 1)
	assert.Empty(t, rowsOf(h, "alerting", third), "the third is not taken")

	again := post()

	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	assert.JSONEq(t, `{"opened":0,"appended":1,"late":0,"repeat":2,"resolved_unmatched":0,"unroutable":0,"refused":0}`, again.Body.String())
	for _, fp := range []string{"f1", "f2", "f3"} {
		assert.Len(t, rowsOf(h, "alerting", fp+":firing:"+at), 1, "one row of %s", fp)
	}
	x := rowOf(h, "f1:firing:"+at).Item
	require.NotEmpty(t, x)
	assert.Len(t, triggers(h, x), 3, "one item_triggered of each event")
}

// withoutChecks is the example configuration's revision with its alerting
// mapping's checks removed.
func withoutChecks(t *testing.T) *config.Revision {
	t.Helper()
	root := os.DirFS("../../../examples/config")
	files := map[string][]byte{}
	require.NoError(t, fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files[path], err = fs.ReadFile(root, path)
		return err
	}))
	const alerts = "trigger-mappings/alerts.yaml"
	var kept []string
	for line := range strings.SplitSeq(string(files[alerts]), "\n") {
		if !strings.HasPrefix(line, "  checks:") {
			kept = append(kept, line)
		}
	}
	files[alerts] = []byte(strings.Join(kept, "\n"))
	fsys, err := memfs.New(files)
	require.NoError(t, err)
	cc, err := celeval.New()
	require.NoError(t, err)
	rev, err := config.Build(fsys, commit, cc)
	require.NoError(t, err)
	require.NotNil(t, rev.TriggerMapping("alerting"))
	return rev
}
