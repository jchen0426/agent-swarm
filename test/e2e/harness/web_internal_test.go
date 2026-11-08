//go:build e2e

package harness

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upstream is a stand-in for an instance's web listener: /leaderz answers
// 200 while leads holds, 503 otherwise, and every other path answers the
// instance's name and the Host it received.
func upstream(t *testing.T, name string, leads *atomic.Bool) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/leaderz" {
			if leads.Load() {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, name+" "+r.Host)
	}))
	t.Cleanup(s.Close)
	return strings.TrimPrefix(s.URL, "http://")
}

// The web's TLS stub forwards to the instance whose /leaderz answers 200,
// keeping the client's Host; with none, to the one it forwarded to last;
// before any, to the first that started; and with no instance it answers
// 502.
func TestTheWebStubFollowsTheLeaderAndKeepsTheHost(t *testing.T) {
	ca, err := newTestCA()
	require.NoError(t, err)
	w := newWeb(t, ca)
	c := w.Browser(t)
	get := func() (int, string) {
		p := c.Get(w.URL() + "/items")
		return p.Resp.StatusCode, string(p.Body)
	}
	code, _ := get()
	assert.Equal(t, http.StatusBadGateway, code, "no instance has started")

	var aLeads, bLeads atomic.Bool
	w.register("a", upstream(t, "a", &aLeads))
	w.register("b", upstream(t, "b", &bLeads))
	host := strings.TrimPrefix(w.URL(), "https://")
	code, body := get()
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "a "+host, body, "before any leads, the first that started, with the client's Host")

	bLeads.Store(true)
	_, body = get()
	assert.Equal(t, "b "+host, body, "the leader")
	assert.Equal(t, "b", w.Upstream())

	bLeads.Store(false)
	_, body = get()
	assert.Equal(t, "b "+host, body, "with none leading, the last")

	aLeads.Store(true)
	_, body = get()
	assert.Equal(t, "a "+host, body, "the new leader")
}
