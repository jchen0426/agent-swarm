package pages_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/pages"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// A standby's read of the lease's holder is bounded: a read that hangs, as
// on a pool with no connection free, renders the leader as not known
// within a short time, and never holds the page (Decision 4; review I1).
func TestAStandbysHolderReadIsBoundedAndNeverBlocksThePage(t *testing.T) {
	w := newWorld(t)
	w.leader.set(false, false)
	w.holder.hang = true
	start := time.Now()
	page := w.browser(t, "alice", auth.Responder).Get(w.public + "/items")
	took := time.Since(start)
	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	assert.Contains(t, text(t, page.Body), "The leader's name could not be read")
	assert.Less(t, took, 3*time.Second, "the read is bounded")
}

// A promotion's or reopen's preview answered unavailable or taking over
// says that nothing was tried and offers the act again, never that the act
// may have happened (review M1).
func TestAPreviewThatCannotAnswerSaysNothingWasTried(t *testing.T) {
	for _, kind := range []string{"promote", "reopen"} {
		for name, err := range map[string]error{"unavailable": errors.New("down"), "taking over": app.ErrNotHeld} {
			w := newWorld(t)
			w.coord.open("a1", "sso:alice")
			w.coord.queue(true, err)
			page := w.browser(t, "alice", auth.Responder).Get(w.public + "/items/a1/" + kind)
			words := text(t, page.Body)
			assert.Contains(t, words, "nothing was tried", "%s, %s", kind, name)
			assert.NotContains(t, words, "may or may not", "%s, %s", kind, name)
			assert.Contains(t, links(t, page.Body), "/items/a1/"+kind, "%s, %s", kind, name)
		}
	}
}

// The form a retry sends after unavailable carries the reason first sent,
// fixed and shown as words, so that the same key is never sent with
// another reason (review M2).
func TestARetryFormKeepsItsReasonFixed(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	w.coord.queue(false, errors.New("down"))
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/a1/close")

	retry := b.Submit(form, "/items/a1/close", url.Values{"reason": {"other"}, "text": {"the disk was replaced"}})
	require.Equal(t, http.StatusServiceUnavailable, retry.Resp.StatusCode)
	assert.Empty(t, buttons(t, retry.Body, "reason"), "no reason to choose again")
	assert.Equal(t, "other", field(t, retry.Body, "/items/a1/close", "reason"))
	assert.Equal(t, "the disk was replaced", field(t, retry.Body, "/items/a1/close", "text"))
	assert.Contains(t, text(t, retry.Body), "the disk was replaced")

	done := b.Submit(retry, "/items/a1/close", nil)
	require.Equal(t, http.StatusOK, done.Resp.StatusCode)
	commits := w.coord.commits()
	require.Len(t, commits, 2)
	assert.Equal(t, commits[0].key, commits[1].key)
	require.NotNil(t, commits[1].reason)
	assert.Equal(t, "other:the disk was replaced", *commits[1].reason)
}

// A preset reason sends its code alone: the box's words go with other only
// (review M3).
func TestAPresetReasonSendsNoText(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/a1/close")
	require.Equal(t, http.StatusOK, b.Submit(form, "/items/a1/close", url.Values{"reason": {"resolved"}, "text": {"stray words"}}).Resp.StatusCode)
	commits := w.coord.commits()
	require.Len(t, commits, 1)
	require.NotNil(t, commits[0].reason)
	assert.Equal(t, "resolved:", *commits[0].reason)
}

// A text over the coordinator's 500 characters, counted as it counts them,
// its line endings as LF, is refused on the page with the key and the text
// kept; a coordinator's final invalid answer renders a new form, with a new
// key, the text kept (review M4).
func TestAnInvalidTextKeepsWhatWasTyped(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	b := w.browser(t, "alice", auth.Responder)
	form := b.Get(w.public + "/items/a1/close")
	key := field(t, form.Body, "/items/a1/close", "k")

	long := strings.Repeat("é", 501)
	answer := b.Submit(form, "/items/a1/close", url.Values{"reason": {"other"}, "text": {long}})
	assert.Equal(t, http.StatusBadRequest, answer.Resp.StatusCode)
	assert.Contains(t, text(t, answer.Body), "500")
	assert.Equal(t, key, field(t, answer.Body, "/items/a1/close", "k"))
	assert.Equal(t, long, textarea(t, answer.Body, "/items/a1/close", "text"))
	assert.Empty(t, w.coord.commits())

	// 250 lines of one character are 499 characters with LF, as the
	// coordinator counts them, though CRLF makes them 748 bytes.
	lines := strings.TrimSuffix(strings.Repeat("x\r\n", 250), "\r\n")
	ok := b.Submit(form, "/items/a1/close", url.Values{"reason": {"other"}, "text": {lines}})
	require.Equal(t, http.StatusOK, ok.Resp.StatusCode, text(t, ok.Body))
	commits := w.coord.commits()
	require.Len(t, commits, 1)
	assert.NotContains(t, *commits[0].reason, "\r")

	w.coord.queue(false, fmt.Errorf("%w: the text holds a character a reader could miss", app.ErrInvalid))
	form = b.Get(w.public + "/items/a1/reopen")
	key = field(t, form.Body, "/items/a1/reopen", "k")
	answer = b.Submit(form, "/items/a1/reopen", url.Values{"reason": {"other"}, "text": {"typed with care"}})
	assert.Equal(t, http.StatusBadRequest, answer.Resp.StatusCode)
	next := field(t, answer.Body, "/items/a1/reopen", "k")
	assert.NotEmpty(t, next)
	assert.NotEqual(t, key, next, "a final answer takes a new key")
	assert.Equal(t, "typed with care", textarea(t, answer.Body, "/items/a1/reopen", "text"))
}

// not_held and draining may come after the act was recorded: their words
// say the outcome is not known here and point to the items list, never
// that nothing was done (review M5).
func TestNotHeldAndDrainingWordsDoNotClaimNothingWasDone(t *testing.T) {
	for _, code := range []string{"not_held", "draining"} {
		words, ok := pages.Words(code)
		require.True(t, ok)
		assert.Contains(t, words, "check the items list", code)
		assert.Contains(t, words, "not known", code)
		assert.NotContains(t, words, "does not act", code)
		assert.NotContains(t, words, "no new acts", code)
	}
}
