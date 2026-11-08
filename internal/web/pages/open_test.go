package pages_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// options are the values of the options of the element named name (a select
// by its name, a datalist by its id) on body.
func options(t *testing.T, body []byte, name string) []string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	var out []string
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || (n.Data != "select" && n.Data != "datalist") || (attr(n, "name") != name && attr(n, "id") != name) {
			continue
		}
		for o := range n.Descendants() {
			if o.Type == html.ElementNode && o.Data == "option" {
				out = append(out, attr(o, "value"))
			}
		}
	}
	return out
}

// The opening form offers the revision's types and the services of the
// items open now; it fixes the person as the principal, unless the session
// is a fresh admin, who may name another; and it opens the item under the
// MAC'd key, as the person's principal, with the routing's service (R78,
// R85, R91).
func TestTheOpeningFormFixesAResponderAsPrincipalAndOffersOpenServices(t *testing.T) {
	w := newWorld(t)
	w.views.rows = []app.Row{
		{ID: "a1", State: "investigating", Service: "checkout"},
		{ID: "a2", State: "queued", Service: "payments"},
		{ID: "a3", State: "awaiting_close", Service: "checkout"},
		{ID: "a4", State: "closed", Service: "retired"},
		{ID: "a5", State: "queued"},
	}
	b := w.browser(t, "alice", auth.Responder)

	form := b.Get(w.public + "/items/new")
	require.Equal(t, http.StatusOK, form.Resp.StatusCode, text(t, form.Body))
	assert.Equal(t, []string{"change", "incident"}, options(t, form.Body, "type"), "the revision's work item types")
	assert.Equal(t, []string{"checkout", "payments"}, options(t, form.Body, "services"), "the services of the items open now, once each")
	assert.Empty(t, field(t, form.Body, "/items/new", "principal"), "a responder has no field to name a principal")
	key := field(t, form.Body, "/items/new", "k")
	require.NotEmpty(t, key)

	answer := b.Submit(form, "/items/new", url.Values{"type": {"incident"}, "title": {"  Checkout is slow  "}, "service": {" checkout "}})
	require.Equal(t, http.StatusOK, answer.Resp.StatusCode, text(t, answer.Body))
	opens := w.coord.openings()
	require.Len(t, opens, 1)
	in := opens[0]
	id := app.ItemID("acme", "web:"+key)
	assert.Equal(t, id, in.ID)
	assert.Equal(t, "sso:alice", in.Principal)
	assert.Equal(t, "sso:alice", in.Actor.Subject)
	assert.Equal(t, "incident", in.Type)
	assert.Equal(t, "Checkout is slow", in.Title)
	assert.Equal(t, "person", in.Source)
	assert.Equal(t, map[string]string{"service": "checkout"}, in.Routing)
	assert.Empty(t, in.Responders)
	assert.Equal(t, app.OpeningDigest(&in), in.Digest)
	assert.Contains(t, links(t, answer.Body), "/items/"+string(id))

	// No service sends no routing.
	form = b.Get(w.public + "/items/new")
	b.Submit(form, "/items/new", url.Values{"type": {"change"}, "title": {"t"}})
	require.Len(t, w.coord.openings(), 2)
	assert.Empty(t, w.coord.openings()[1].Routing)

	// A responder who names another principal is refused by the coordinator.
	form = b.Get(w.public + "/items/new")
	refused := b.Submit(form, "/items/new", url.Values{"type": {"incident"}, "title": {"t"}, "principal": {"sso:bob"}})
	assert.Equal(t, http.StatusBadRequest, refused.Resp.StatusCode)
	assert.Equal(t, 2, w.coord.openedItems())
	assert.NotEmpty(t, field(t, refused.Body, "/items/new", "k"), "a refused opening is mended in a new form")
	assert.Equal(t, "t", field(t, refused.Body, "/items/new", "title"), "what was typed is kept")

	// A fresh admin names another subject, in the sso: form.
	admin := w.browser(t, "erin", auth.Admin)
	form = admin.Get(w.public + "/items/new")
	assert.Equal(t, "sso:erin", field(t, form.Body, "/items/new", "principal"))
	got := admin.Submit(form, "/items/new", url.Values{"type": {"incident"}, "title": {"t"}, "principal": {"sso:bob"}})
	require.Equal(t, http.StatusOK, got.Resp.StatusCode, text(t, got.Body))
	assert.Equal(t, "sso:bob", w.coord.openings()[len(w.coord.openings())-1].Principal)

	// A viewer is refused in words and offered no form.
	viewer := w.browser(t, "vic", auth.Viewer)
	page := viewer.Get(w.public + "/items/new")
	assert.Equal(t, http.StatusConflict, page.Resp.StatusCode, "a refusal of the coordinator is 409, as the admin API answers it")
	assert.Empty(t, field(t, page.Body, "/items/new", "k"))
	assert.Contains(t, text(t, page.Body), "Only a responder or an admin may do this.")

	// A key whose MAC does not hold is refused, and nothing opens.
	before := len(w.coord.openings())
	bad := b.Submit(b.Get(w.public+"/items/new"), "/items/new", url.Values{"type": {"incident"}, "title": {"t"}, "mac": {"forged"}})
	assert.Equal(t, http.StatusBadRequest, bad.Resp.StatusCode)
	assert.Len(t, w.coord.openings(), before)
}

// A form posted twice, its key the same, opens one item: the second answer
// names the first's item (I2; R85).
func TestADoubleSubmittedOpeningOpensOneItem(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/new")
	values := url.Values{"type": {"incident"}, "title": {"t"}, "service": {"checkout"}}
	first := b.Submit(form, "/items/new", values)
	second := b.Submit(form, "/items/new", values)
	require.Equal(t, http.StatusOK, first.Resp.StatusCode)
	require.Equal(t, http.StatusOK, second.Resp.StatusCode)
	opens := w.coord.openings()
	require.Len(t, opens, 2)
	assert.Equal(t, opens[0].ID, opens[1].ID)
	assert.Equal(t, opens[0].Digest, opens[1].Digest)
	assert.Equal(t, 1, w.coord.openedItems())
}

// An answer that the opening's outcome is not known, unavailable, renders
// the form again with the same key and MAC and the same values, fixed, so
// that the retry is the same opening and opens one item (I2; R85; I7).
func TestARetriedOpeningAfterUnavailableKeepsItsKeyAndOpensOneItem(t *testing.T) {
	w := newWorld(t)
	w.coord.queueOpen(errors.New("the database is gone"))
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/new")
	key, mac := field(t, form.Body, "/items/new", "k"), field(t, form.Body, "/items/new", "mac")
	first := b.Submit(form, "/items/new", url.Values{"type": {"incident"}, "title": {"t"}, "service": {"checkout"}})
	require.Equal(t, http.StatusServiceUnavailable, first.Resp.StatusCode, text(t, first.Body))
	assert.Equal(t, key, field(t, first.Body, "/items/new", "k"))
	assert.Equal(t, mac, field(t, first.Body, "/items/new", "mac"))
	assert.Contains(t, text(t, first.Body), "Send the same form again")
	assert.Equal(t, 0, w.coord.openedItems())

	second := b.Submit(first, "/items/new", nil)
	require.Equal(t, http.StatusOK, second.Resp.StatusCode, text(t, second.Body))
	opens := w.coord.openings()
	require.Len(t, opens, 2)
	assert.Equal(t, opens[0].ID, opens[1].ID)
	assert.Equal(t, opens[0].Digest, opens[1].Digest, "the retry is the same opening")
	assert.Equal(t, 1, w.coord.openedItems())
}
