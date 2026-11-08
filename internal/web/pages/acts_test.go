package pages_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// Each act's page previews its consequences, then commits with the key and
// MAC the preview's page minted: the coordinator's commit carries the
// rendered key, and a close or reopen carries the reason pressed (R83, R85;
// design D8).
func TestEachActPreviewsThenCommitsWithTheSameKey(t *testing.T) {
	for _, tc := range []struct {
		kind   app.ActKind
		values url.Values
		reason string
	}{
		{kind: app.ActPromote},
		{kind: app.ActClose, values: url.Values{"reason": {"resolved"}}, reason: "resolved:"},
		{kind: app.ActReopen, values: url.Values{"reason": {"other"}, "text": {"it came back"}}, reason: "other:it came back"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			w := newWorld(t)
			w.coord.open("a1", "sso:alice")
			w.coord.consequen = app.Consequences{
				Stops: []app.RunStop{{Item: "a1", Run: "r1"}}, Investigating: 2, Limit: 5,
			}
			b := w.browser(t, "alice", auth.Responder)
			path := "/items/a1/" + string(tc.kind)

			form := b.Get(w.public + path)
			require.Equal(t, http.StatusOK, form.Resp.StatusCode)
			words := text(t, form.Body)
			assert.Contains(t, words, "Stops 1 run")
			assert.Contains(t, words, "2 items investigating, of a limit of 5")
			key := field(t, form.Body, path, "k")
			require.NotEmpty(t, key)
			raw, err := base64URL(key)
			require.NoError(t, err)
			assert.Len(t, raw, 16, "the key is 16 random bytes")
			assert.Equal(t, 1, w.coord.previews())
			assert.Empty(t, w.coord.commits(), "a preview commits nothing")

			answer := b.Submit(form, path, tc.values)
			require.Equal(t, http.StatusOK, answer.Resp.StatusCode, text(t, answer.Body))
			assert.Contains(t, text(t, answer.Body), "Done")
			commits := w.coord.commits()
			require.Len(t, commits, 1)
			assert.Equal(t, key, commits[0].key, "the commit carries the preview's key")
			assert.Equal(t, tc.kind, commits[0].kind)
			if tc.reason == "" {
				assert.Nil(t, commits[0].reason)
			} else {
				require.NotNil(t, commits[0].reason)
				assert.Equal(t, tc.reason, *commits[0].reason)
			}

			// Another rendering mints another key.
			again := b.Get(w.public + path)
			assert.NotEqual(t, key, field(t, again.Body, path, "k"))
		})
	}

	// "other" needs its text; the form comes back with its key.
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/a1/close")
	key := field(t, form.Body, "/items/a1/close", "k")
	answer := b.Submit(form, "/items/a1/close", url.Values{"reason": {"other"}, "text": {"  "}})
	assert.Equal(t, http.StatusBadRequest, answer.Resp.StatusCode)
	assert.Equal(t, key, field(t, answer.Body, "/items/a1/close", "k"))
	assert.Empty(t, w.coord.commits())
}

// A form posted twice, its key the same, is recorded once: the second
// answer is the first's repeat, and changes nothing (R42; design D8).
func TestADoubleSubmitChangesNothing(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/a1/close")

	first := b.Submit(form, "/items/a1/close", url.Values{"reason": {"resolved"}})
	second := b.Submit(form, "/items/a1/close", url.Values{"reason": {"resolved"}})
	require.Equal(t, http.StatusOK, first.Resp.StatusCode)
	require.Equal(t, http.StatusOK, second.Resp.StatusCode)
	commits := w.coord.commits()
	require.Len(t, commits, 2)
	assert.Equal(t, commits[0].key, commits[1].key)
	assert.Len(t, w.coord.records(), 1, "the act is recorded once")
}

// An answer that the act's outcome is not known yet, unavailable, renders
// the form again with the same key and MAC, so that the retry is the same
// act, and the retry commits with it (R85; I7).
func TestARetryAfterUnavailableKeepsItsKey(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	w.coord.queue(false, errors.New("the database is gone"))
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/a1/promote")
	key, mac := field(t, form.Body, "/items/a1/promote", "k"), field(t, form.Body, "/items/a1/promote", "mac")

	retry := b.Submit(form, "/items/a1/promote", nil)
	assert.Equal(t, http.StatusServiceUnavailable, retry.Resp.StatusCode)
	assert.Contains(t, text(t, retry.Body), "could not be done just now")
	assert.Equal(t, key, field(t, retry.Body, "/items/a1/promote", "k"))
	assert.Equal(t, mac, field(t, retry.Body, "/items/a1/promote", "mac"))

	done := b.Submit(retry, "/items/a1/promote", nil)
	require.Equal(t, http.StatusOK, done.Resp.StatusCode)
	commits := w.coord.commits()
	require.Len(t, commits, 2)
	assert.Equal(t, key, commits[1].key)
	assert.Len(t, w.coord.records(), 1)
}

// A key whose MAC fails is refused invalid, and the coordinator is not
// asked: a key changed, or the MAC of another act, item or person (R85).
func TestAKeyWithABadMACIsRefused(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	w.coord.open("a2", "sso:alice")
	c := w.session(t, "alice", 0, time.Hour, auth.Responder)
	csrf := w.token(t, c)
	const key = "AAAAAAAAAAAAAAAAAAAAAA"
	good := w.form("sso:alice", app.ActClose, "a1", key, csrf)
	for name, form := range map[string]url.Values{
		"no key":          {"mac": good["mac"], auth.TokenField: {csrf}, "reason": {"resolved"}},
		"another key":     {"k": {"BBBBBBBBBBBBBBBBBBBBBB"}, "mac": good["mac"], auth.TokenField: {csrf}, "reason": {"resolved"}},
		"a changed MAC":   {"k": {key}, "mac": {good.Get("mac")[1:]}, auth.TokenField: {csrf}, "reason": {"resolved"}},
		"another act":     withReason(w.form("sso:alice", app.ActReopen, "a1", key, csrf)),
		"another item":    withReason(w.form("sso:alice", app.ActClose, "a2", key, csrf)),
		"another subject": withReason(w.form("sso:bob", app.ActClose, "a1", key, csrf)),
	} {
		resp, body := w.post(t, "/items/a1/close", c, form)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, name)
		assert.Contains(t, body, "not one this site gave you", name)
	}
	assert.Empty(t, w.coord.commits())

	resp, _ := w.post(t, "/items/a1/close", c, withReason(good))
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the good MAC is accepted")
}

func withReason(v url.Values) url.Values {
	v.Set("reason", "resolved")
	return v
}

// An act that comes while the instance takes over is answered "try again
// shortly", 503 with Retry-After, and its form keeps its key (R85; C4).
func TestTakingOverMapsToTryAgainShortly(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	w.coord.queue(false, app.ErrNotHeld)
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/a1/promote")
	key := field(t, form.Body, "/items/a1/promote", "k")

	answer := b.Submit(form, "/items/a1/promote", nil)
	assert.Equal(t, http.StatusServiceUnavailable, answer.Resp.StatusCode)
	assert.Equal(t, "1", answer.Resp.Header.Get("Retry-After"))
	assert.Contains(t, text(t, answer.Body), "try again shortly")
	assert.Equal(t, key, field(t, answer.Body, "/items/a1/promote", "k"))
}

// A close whose preview cannot answer, as during a drain or while the
// instance takes over, is still offered, labeled so (R84; M-b).
func TestACloseWhosePreviewCannotAnswerIsStillOffered(t *testing.T) {
	for name, tc := range map[string]struct {
		held, draining bool
		err            error
	}{
		"draining":    {held: true, draining: true, err: app.ErrNotHeld},
		"taking over": {held: true, err: app.ErrNotHeld},
		"unavailable": {held: true, err: errors.New("the database is gone")},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.leader.set(tc.held, tc.draining)
			w.coord.open("a1", "sso:alice")
			w.coord.queue(true, tc.err)
			form := w.browser(t, "alice", auth.Responder).Get(w.public + "/items/a1/close")
			require.Equal(t, http.StatusOK, form.Resp.StatusCode)
			assert.Contains(t, text(t, form.Body), "Consequences cannot be counted right now")
			assert.NotEmpty(t, field(t, form.Body, "/items/a1/close", "k"), "the close is offered")
		})
	}
}

// An admin's act on an item they are not on needs freshness (R76; N3): the
// coordinator refuses an admin who is not fresh, and the page renders
// "Re-authenticate to act as an admin" with the fresh link, back to the
// act's form as a GET, never replaying the POST; a fresh admin acts, and a
// responder who is not on the item gets no fresh link.
func TestAnAdminsItemActNeedsFreshness(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:carol")
	stale := w.session(t, "erin", 10*time.Minute, time.Hour, auth.Admin)
	fresh := "/auth/fresh?return=" + url.QueryEscape("/items/a1/close")

	resp, body := w.get(t, "/items/a1/close", stale)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, body, "Re-authenticate to act as an admin")
	assert.Contains(t, links(t, []byte(body)), fresh)
	assert.Empty(t, field(t, []byte(body), "/items/a1/close", "k"))

	resp, body = w.post(t, "/items/a1/close", stale,
		withReason(w.form("sso:erin", app.ActClose, "a1", "AAAAAAAAAAAAAAAAAAAAAA", w.token(t, stale))))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))
	assert.Contains(t, body, "Re-authenticate to act as an admin")
	assert.Contains(t, links(t, []byte(body)), fresh)
	assert.Empty(t, w.coord.records())

	// A stale admin's /items offers no act on the item, and says how to act
	// as an admin.
	w.views.rows = []app.Row{{ID: "a1", Title: "Disk full", Incident: "inc-a", Principal: "sso:carol", State: "queued", PositionKnown: true}}
	resp, body = w.get(t, "/items", stale)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, links(t, []byte(body)), "/items/a1/close")
	assert.Contains(t, links(t, []byte(body)), "/auth/fresh?return="+url.QueryEscape("/items"))

	b := w.browser(t, "erin", auth.Admin)
	form := b.Get(w.public + "/items/a1/close")
	require.Equal(t, http.StatusOK, form.Resp.StatusCode)
	assert.Equal(t, http.StatusOK, b.Submit(form, "/items/a1/close", url.Values{"reason": {"resolved"}}).Resp.StatusCode)
	assert.Len(t, w.coord.records(), 1, "a fresh admin acts")

	r := w.session(t, "dave", 0, time.Hour, auth.Responder)
	resp, body = w.get(t, "/items/a1/close", r)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.NotContains(t, body, "Re-authenticate")
	assert.NotContains(t, body, "/auth/fresh")
}

// base64URL decodes a form key.
func base64URL(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
