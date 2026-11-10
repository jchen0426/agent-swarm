package pages_test

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// pageWords are the words body shows, as a reader reads them (see
// shownWords).
func pageWords(t *testing.T, body []byte) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	return shownWords(doc)
}

// hold sets the lease's holder: its name, and when its hold expires.
func (h *holder) hold(name string, expires time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.name, h.expires = name, expires
}

// The standby judges the lease's expiry by the pages' clock, the database's
// time as the instance estimates it, not by the host's (R158): a hold whose
// expiry is past by the pages' clock and not by the host's is no one's, on
// the items list, an act's form and a board page alike, which read the same
// standby for the same holder; and one past by the host's clock and not by
// the pages' still names its leader.
func TestTheStandbyJudgesTheLeasesExpiryByThePagesClock(t *testing.T) {
	for name, tc := range map[string]struct {
		skew, expiresIn time.Duration
		leads           bool
	}{
		"past by the pages' clock alone": {skew: 10 * time.Minute, expiresIn: time.Minute},
		"past by the host's clock alone": {skew: -10 * time.Minute, expiresIn: -time.Minute, leads: true},
	} {
		t.Run(name, func(t *testing.T) {
			w := newSkewedWorld(t, tc.skew)
			w.leader.set(false, false)
			w.holder.hold("instance-b", time.Now().Add(tc.expiresIn))
			w.boards.set("a1", boardRead("a1", "investigating", nil))
			b := w.browser(t, "alice", auth.Responder)

			items := b.Get(w.public + "/items")
			require.Equal(t, http.StatusOK, items.Resp.StatusCode, text(t, items.Body))
			form := b.Get(w.public + "/items/a1/close")
			require.Equal(t, http.StatusConflict, form.Resp.StatusCode, text(t, form.Body))
			board := b.Get(w.public + "/items/a1")
			require.Equal(t, http.StatusOK, board.Resp.StatusCode, text(t, board.Body))
			for page, words := range map[string]string{
				"the items list": regionWords(t, items.Body, "standby"),
				"an act's form":  pageWords(t, form.Body),
				"a board page":   regionWords(t, board.Body, "standby"),
			} {
				if tc.leads {
					assert.Contains(t, words, "The leader is the instance named \u201cinstance-b\u201d", page)
					assert.NotContains(t, words, "No instance leads right now", page)
					continue
				}
				assert.Contains(t, words, "No instance leads right now", page)
				assert.NotContains(t, words, "instance-b", page)
			}
		})
	}
}

// A form refused for its CSRF token after a new sign-in is rendered again
// with its key (design D14, D15; R157; re-review N2). A close form opened
// under one sign-in, sent, its answer lost, and sent again after the person
// signed in again carries the first sign-in's token, which is not the new
// session's: it comes back 200, saying "Your sign-in changed: check and send
// again", with its key and MAC, the new session's token and its reason
// fixed, and the coordinator is not asked; sent, it commits under its
// original key, which the coordinator answers as a repeat. The opening's
// form likewise opens one item.
func TestAFormRefusedForItsTokenAfterANewSignInIsRenderedAgainWithItsKey(t *testing.T) {
	t.Run("the close form", func(t *testing.T) {
		w := newWorld(t)
		w.coord.open("a1", "sso:alice")
		first := w.session(t, "alice", 10*time.Minute, time.Hour, auth.Responder)
		second := w.session(t, "alice", 0, time.Hour, auth.Responder)
		b := webtest.NewBrowser(t, w.client())
		b.SetCookie(w.public, first)
		const path = "/items/a1/close"
		form := b.Get(w.public + path)
		require.Equal(t, http.StatusOK, form.Resp.StatusCode, text(t, form.Body))
		key, mac := field(t, form.Body, path, "k"), field(t, form.Body, path, "mac")
		require.NotEmpty(t, key)
		require.Equal(t, w.token(t, first), field(t, form.Body, path, auth.TokenField))
		// Sent, and its answer lost: the coordinator recorded the act.
		require.Equal(t, http.StatusOK, b.Submit(form, path, url.Values{"reason": {"resolved"}}).Resp.StatusCode)
		require.Len(t, w.coord.records(), 1)
		previews, commits := w.coord.previews(), len(w.coord.commits())

		b.SetCookie(w.public, second)
		again := b.Submit(form, path, url.Values{"reason": {"resolved"}})
		require.Equal(t, http.StatusOK, again.Resp.StatusCode, text(t, again.Body))
		assert.Contains(t, text(t, again.Body), "Your sign-in changed: check and send again")
		assert.Equal(t, key, field(t, again.Body, path, "k"), "the same key")
		assert.Equal(t, mac, field(t, again.Body, path, "mac"), "the same MAC")
		assert.Equal(t, w.token(t, second), field(t, again.Body, path, auth.TokenField), "the new session's token")
		assert.Equal(t, "resolved", field(t, again.Body, path, "reason"), "the reason fixed")
		assert.Empty(t, buttons(t, again.Body, "reason"), "no other reason offered")
		assert.Contains(t, text(t, again.Body), "Reason: Resolved")
		assert.Equal(t, previews, w.coord.previews(), "the coordinator is not asked")
		assert.Len(t, w.coord.commits(), commits, "the coordinator is not asked")

		done := b.Submit(again, path, nil)
		require.Equal(t, http.StatusOK, done.Resp.StatusCode, text(t, done.Body))
		got := w.coord.commits()
		require.Len(t, got, 2)
		assert.Equal(t, key, got[1].key, "the original key")
		require.NotNil(t, got[1].reason)
		assert.Equal(t, "resolved:", *got[1].reason)
		assert.Len(t, w.coord.records(), 1, "the coordinator answers it as a repeat")
	})

	// A stale form whose reason was sent too long keeps its key, and says
	// so in the words a commit would answer, its text kept and its reason
	// chosen again, as nothing was fixed. (The plan's.)
	t.Run("a close form whose reason was sent too long", func(t *testing.T) {
		w := newWorld(t)
		w.coord.open("a1", "sso:alice")
		b := webtest.NewBrowser(t, w.client())
		b.SetCookie(w.public, w.session(t, "alice", 10*time.Minute, time.Hour, auth.Responder))
		const path = "/items/a1/close"
		form := b.Get(w.public + path)
		require.Equal(t, http.StatusOK, form.Resp.StatusCode, text(t, form.Body))
		key, mac := field(t, form.Body, path, "k"), field(t, form.Body, path, "mac")
		previews := w.coord.previews()
		second := w.session(t, "alice", 0, time.Hour, auth.Responder)
		b.SetCookie(w.public, second)

		long := strings.Repeat("a", 501)
		again := b.Submit(form, path, url.Values{"reason": {"other"}, "text": {long}})
		require.Equal(t, http.StatusBadRequest, again.Resp.StatusCode, text(t, again.Body))
		words := text(t, again.Body)
		assert.Contains(t, words, "Your sign-in changed: check and send again")
		assert.Contains(t, words, "A reason holds at most 500 characters; this one holds 501. Shorten it, then send it again.")
		assert.Equal(t, key, field(t, again.Body, path, "k"), "the same key")
		assert.Equal(t, mac, field(t, again.Body, path, "mac"), "the same MAC")
		assert.Equal(t, w.token(t, second), field(t, again.Body, path, auth.TokenField), "the new session's token")
		assert.Equal(t, long, textarea(t, again.Body, path, "text"), "the text kept")
		assert.Equal(t, []string{"resolved", "false_alarm", "duplicate", "other"}, buttons(t, again.Body, "reason"), "the reason chosen again")
		assert.Equal(t, previews, w.coord.previews(), "the coordinator is not asked")
		assert.Empty(t, w.coord.commits(), "the coordinator is not asked")
	})

	t.Run("the opening form", func(t *testing.T) {
		w := newWorld(t)
		first := w.session(t, "alice", 10*time.Minute, time.Hour, auth.Responder)
		second := w.session(t, "alice", 0, time.Hour, auth.Responder)
		b := webtest.NewBrowser(t, w.client())
		b.SetCookie(w.public, first)
		form := b.Get(w.public + openPath)
		require.Equal(t, http.StatusOK, form.Resp.StatusCode, text(t, form.Body))
		key, mac := field(t, form.Body, openPath, "k"), field(t, form.Body, openPath, "mac")
		require.NotEmpty(t, key)
		values := url.Values{"type": {"incident"}, "title": {"Checkout is slow"}, "service": {"checkout"}}
		// Sent, and its answer lost: the coordinator opened the item.
		require.Equal(t, http.StatusOK, b.Submit(form, openPath, values).Resp.StatusCode)
		require.Equal(t, 1, w.coord.openedItems())

		b.SetCookie(w.public, second)
		again := b.Submit(form, openPath, values)
		require.Equal(t, http.StatusOK, again.Resp.StatusCode, text(t, again.Body))
		assert.Contains(t, text(t, again.Body), "Your sign-in changed: check and send again")
		assert.Equal(t, key, field(t, again.Body, openPath, "k"), "the same key")
		assert.Equal(t, mac, field(t, again.Body, openPath, "mac"), "the same MAC")
		assert.Equal(t, w.token(t, second), field(t, again.Body, openPath, auth.TokenField), "the new session's token")
		for name, want := range map[string]string{"type": "incident", "title": "Checkout is slow", "service": "checkout", "principal": "sso:alice"} {
			assert.Equal(t, want, field(t, again.Body, openPath, name), "the %s fixed", name)
		}
		assert.Empty(t, options(t, again.Body, "type"), "no other type offered")
		assert.Len(t, w.coord.openings(), 1, "the coordinator is not asked")

		done := b.Submit(again, openPath, nil)
		require.Equal(t, http.StatusOK, done.Resp.StatusCode, text(t, done.Body))
		opens := w.coord.openings()
		require.Len(t, opens, 2)
		assert.Equal(t, app.ItemID("acme", "web:"+key), opens[1].ID, "the original key's item")
		assert.Equal(t, opens[0].ID, opens[1].ID)
		assert.Equal(t, opens[0].Digest, opens[1].Digest, "the same opening")
		assert.Equal(t, 1, w.coord.openedItems(), "one item opens")
	})
}

// A stale opening's values, shown again for the person to check, are inert,
// each alone in its <bdi> (R154; review m2): a typed title and service each
// holding U+202E and U+200B show ⟨U+202E⟩ and ⟨U+200B⟩ within the form, no
// text of the page shows either rune raw, and the type and the principal
// sit in their <bdi> too.
func TestAStaleOpeningShowsItsTypedValuesInert(t *testing.T) {
	w := newWorld(t)
	b := webtest.NewBrowser(t, w.client())
	b.SetCookie(w.public, w.session(t, "alice", 10*time.Minute, time.Hour, auth.Responder))
	form := b.Get(w.public + openPath)
	require.Equal(t, http.StatusOK, form.Resp.StatusCode, text(t, form.Body))
	b.SetCookie(w.public, w.session(t, "alice", 0, time.Hour, auth.Responder))

	again := b.Submit(form, openPath, url.Values{"type": {"incident"}, "title": {"Disk x\u202ey\u200bz"}, "service": {"check\u202eout\u200b"}})
	require.Equal(t, http.StatusOK, again.Resp.StatusCode, text(t, again.Body))
	doc, err := html.Parse(bytes.NewReader(again.Body))
	require.NoError(t, err)
	var shown []string
	for n := range doc.Descendants() {
		switch {
		case n.Type == html.TextNode:
			assert.NotContains(t, n.Data, "\u202e", "a raw bidi control in %q", n.Data)
			assert.NotContains(t, n.Data, "\u200b", "a raw zero-width space in %q", n.Data)
		case n.Type == html.ElementNode && n.Data == "form" && attr(n, "action") == openPath:
			for d := range n.Descendants() {
				if d.Type == html.ElementNode && d.Data == "bdi" {
					shown = append(shown, nodeText(d))
				}
			}
		}
	}
	assert.Equal(t, []string{"incident", "Disk x⟨U+202E⟩y⟨U+200B⟩z", "check⟨U+202E⟩out⟨U+200B⟩", "sso:alice"}, shown,
		"the type, the title, the service and the principal, each in its <bdi>, marked")
	assert.Empty(t, w.coord.openings(), "the coordinator is not asked")
}

// A stale form of another person is still refused (R157): the act's form and
// the opening's, minted for one subject and posted under another's session,
// whose token is not the form's, answer today's 403 and its words, render no
// form, and ask the coordinator nothing.
func TestAStaleFormOfAnotherPersonIsStillRefused(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:alice", "sso:bob")
	b := webtest.NewBrowser(t, w.client())
	b.SetCookie(w.public, w.session(t, "alice", 10*time.Minute, time.Hour, auth.Responder))
	forms := map[string]*webtest.Page{"/items/a1/close": b.Get(w.public + "/items/a1/close"), openPath: b.Get(w.public + openPath)}
	previews := w.coord.previews()

	b.SetCookie(w.public, w.session(t, "bob", 0, time.Hour, auth.Responder))
	for path, form := range forms {
		require.Equal(t, http.StatusOK, form.Resp.StatusCode, text(t, form.Body))
		out := b.Submit(form, path, url.Values{"reason": {"resolved"}, "type": {"incident"}, "title": {"t"}})
		assert.Equal(t, http.StatusForbidden, out.Resp.StatusCode, path)
		assert.Contains(t, text(t, out.Body), "Refused This form is out of date or not from this session. Reload the page and try again.", path)
		assert.NotContains(t, text(t, out.Body), "Your sign-in changed", path)
		assert.Empty(t, field(t, out.Body, path, "k"), "%s: no form", path)
	}
	assert.Equal(t, previews, w.coord.previews())
	assert.Empty(t, w.coord.commits())
	assert.Empty(t, w.coord.openings())
}

// openPath is the opening form's path.
const openPath = "/items/new"
