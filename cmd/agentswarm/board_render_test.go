package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/ids"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

// renderedBoards serve the web interface's pages as serve wires them, over
// the real views and board watch on the memory stores (R162): the records
// the test writes, the kill switch's table it sets, and the pages a viewer
// signed in gets.
type renderedBoards struct {
	t        *testing.T
	records  *memory.Records
	controls *memory.Controls
	srv      *http.Server
	cookie   *http.Cookie
}

func newRenderedBoards(t *testing.T) *renderedBoards {
	t.Helper()
	records, events, controls := memory.NewRecords(time.Now), memory.NewStore(time.Now), memory.NewControls(time.Now)
	views := app.NewViews(records, app.NewBoards(events, memory.NewCalls(), memory.NewRuns(), ids.UUIDv7{}), &app.ViewPorts{
		Controls: controls, ReopenWindow: 24 * time.Hour, ClosedRulings: 72 * time.Hour,
		Closed: records, Incidents: records, Titles: records, Entries: events, Siblings: records,
	})
	watch := app.NewBoardWatch(views, &app.WatchPorts{
		Probe: memory.NewProbe(records, events), Controls: controls, Held: func() bool { return true }, Now: time.Now, BuildWait: 10 * time.Second,
	})
	d := processDeployment(t, true)
	d.Web = &deploy.Web{
		PublicURL: "https://agents.example.com",
		OIDC: deploy.OIDC{
			Issuer: "https://idp.example.com", ClientID: "agentswarm", ClientSecretRef: "secret://web/client",
			SubjectClaim: "sub", GroupsClaim: "groups",
		},
		Groups:        deploy.Groups{Viewers: "v", Responders: "r", Admins: "a"},
		SessionKeyRef: "secret://web/session", SessionLength: 8 * time.Hour, FreshWindow: 5 * time.Minute, TimeZone: "UTC",
	}
	secret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	log := slog.New(slog.DiscardHandler)
	p := webPorts(d, fakeWebCoordinator{}, webLeader{}, compatibleSchema{}, webViews{}, webLease{}, webRevisions{}, watch, newWebStores(nil, d), log)
	srv, err := newWebServer(d, &p, webSecrets{key: secret}, log)
	require.NoError(t, err)
	keys, err := auth.NewKeys(secret, d.Web.OIDC.Issuer, d.Web.OIDC.ClientID)
	require.NoError(t, err)
	return &renderedBoards{t: t, records: records, controls: controls, srv: srv, cookie: webtest.SignedIn(t, keys, []auth.Role{auth.Viewer}, "vic")}
}

// opened is a person's opening of an incident for sso:carol.
var opened = item.Body{Opened: &item.Opened{
	Type: "incident", Title: "Checkout fails", Source: "person", Principal: "sso:carol", Arm: item.ArmVisible, How: item.NoWindow, By: "sso:carol",
}}

// write appends bodies, in order, as item id's next records, of its
// incident inc-<id>, and returns them as committed.
func (r *renderedBoards) write(id item.ID, bodies ...item.Body) []item.Record {
	r.t.Helper()
	committed, err := r.records.Append(r.t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
		drafts := make([]item.Draft, len(bodies))
		for i := range bodies {
			b := bodies[i]
			b.V = item.Version
			drafts[i] = item.Draft{ID: fmt.Sprintf("%s-rec-%d", id, len(history)+i+1), Item: id, Incident: item.IncidentID("inc-" + id), Body: b}
		}
		return drafts, nil
	})
	require.NoError(r.t, err)
	return committed
}

// words are the words item id's board page shows to the viewer, as a reader
// reads them: its text, each element that is not inline apart, the controls
// the hidden attribute hides left out, its spaces folded. The page answers
// 200.
func (r *renderedBoards) words(id item.ID) string {
	r.t.Helper()
	req := httptest.NewRequestWithContext(r.t.Context(), http.MethodGet, "/items/"+string(id), http.NoBody)
	req.Host = "agents.example.com"
	req.AddCookie(r.cookie)
	rec := httptest.NewRecorder()
	r.srv.Handler.ServeHTTP(rec, req)
	require.Equal(r.t, http.StatusOK, rec.Code, rec.Body.String())
	doc, err := html.Parse(rec.Body)
	require.NoError(r.t, err)
	inline := map[string]bool{"bdi": true, "q": true, "span": true, "a": true, "strong": true}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		hidden := false
		for _, a := range n.Attr {
			hidden = hidden || a.Key == "hidden"
		}
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
		case hidden:
		case n.Type == html.ElementNode && !inline[n.Data]:
			b.WriteByte(' ')
			for c := range n.ChildNodes() {
				walk(c)
			}
			b.WriteByte(' ')
		default:
			for c := range n.ChildNodes() {
				walk(c)
			}
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}

// shownAt is t as the pages show it in UTC, to the minute.
func shownAt(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 MST") }

// The rows that rest on the record-determined lifecycle facts (R169; plan
// review I1) render through the real views and board watch on the memory
// stores, behind the web server serve wires (R162): row 7, an item not
// staffed by its routing, with its cause; row 9, an item a person closed,
// with when and by whom, and until when it may be reopened; and row 14, an
// item the kill switch's on-row left not staffed, its banner shown with the
// switch since turned off.
func TestTheRecordRowsRenderThroughTheRealViews(t *testing.T) {
	r := newRenderedBoards(t)
	routing := item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseRouting, AutoClose: 2 * time.Hour}}
	r.write("n1", opened, routing)
	closed := r.write("c1", opened, routing, item.Body{Closed: &item.Closed{By: "sso:rita", Reason: &board.Reason{Code: "resolved"}}})
	_, _, err := r.controls.SetKillSwitch(t.Context(), true, "sso:bob", board.Reason{Code: "security"}, 0)
	require.NoError(t, err)
	r.write("k1", opened, item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed, UnstaffedBy: 1, AutoClose: 2 * time.Hour}})
	_, _, err = r.controls.SetKillSwitch(t.Context(), false, "sso:bob", board.Reason{}, 1)
	require.NoError(t, err)

	row7, row9, row14 := r.words("n1"), r.words("c1"), r.words("k1")

	assert.Contains(t, row7, "Not staffed: handle with your usual process")
	assert.Contains(t, row7, "Cause: its routing failed its check")
	assert.NotContains(t, row7, "Stopped by the kill switch")
	at := closed[len(closed)-1].At
	assert.Contains(t, row9, "Closed at "+shownAt(at)+" (just now) by sso:rita; may be reopened until "+shownAt(at.Add(24*time.Hour))+".")
	assert.Contains(t, row14, "Stopped by the kill switch")
	assert.Contains(t, row14, "Kill switch: off, since")
	assert.Contains(t, row14, "Cause: the kill switch was on")
}
