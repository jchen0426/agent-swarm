package pages_test

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/pages"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// switchPath is the kill switch's control.
const switchPath = "/kill-switch"

// The words of an on whose preview cannot count: during a drain, D7's,
// as the on is recorded for the next leader; else the controller's ruling
// amending D7 (fix round 1, M-1), as this leader sets it.
const (
	drainingWords  = "Consequences cannot be counted right now. The on is recorded, and the instance that leads next stops every run."
	uncountedWords = "Consequences cannot be counted right now; turning it on still stops every run once it is recorded."
)

// switchOff is the table of a switch that is off, its latest row seq.
func switchOff(seq int64) *app.KillSwitch {
	return &app.KillSwitch{Seq: seq, OnSeq: max(0, seq-1), Since: time.Now().Add(-time.Hour), By: "sso:bob"}
}

// switchOn is the table of a switch that is on since its row seq.
func switchOn(seq int64) *app.KillSwitch {
	return &app.KillSwitch{On: true, Seq: seq, OnSeq: seq, Since: time.Now().Add(-time.Hour), By: "sso:bob"}
}

// switchForm is the kill switch's form values, as its page would post
// them, for a POST made by hand.
func switchForm(on bool, after int64, csrf string) url.Values {
	v := url.Values{"switch": {"off"}, "after": {strconv.FormatInt(after, 10)}, auth.TokenField: {csrf}}
	if on {
		v.Set("switch", "on")
	}
	return v
}

// The on offers one-tap preset reasons, runaway_cost, bad_output and
// security, each a submit button, and other with text, to a responder,
// with no fresh authentication; its After is the table's row; its preview
// says what it stops. A preset sends its code alone; other sends its text;
// other with no text is refused on the page; and a text the coordinator
// refuses is kept (D7; M8).
func TestTheOnOffersPresetReasonsAndNeedsNoFreshness(t *testing.T) {
	w := newWorld(t)
	w.views.setSwitch(switchOff(3))
	w.coord.ksPreview = app.KillSwitchOutcome{Preview: true, Seq: 3, Consequences: app.Consequences{
		Stops:     []app.RunStop{{Item: "a1", Run: "r1"}, {Item: "a1", Run: "r2"}, {Item: "a2", Run: "r3"}},
		Unstaffed: []item.ID{"a3"},
	}}
	w.coord.ksAnswer = app.KillSwitchOutcome{On: true, Seq: 4, Changed: true}
	b := w.browser(t, "carol", auth.Responder)

	page := b.Get(w.public + switchPath)

	require.Equal(t, http.StatusOK, page.Resp.StatusCode, text(t, page.Body))
	words := text(t, page.Body)
	assert.Contains(t, words, "Stops 3 runs on 2 items")
	assert.Contains(t, words, "Leaves 1 item not staffed")
	assert.Equal(t, []string{"runaway_cost", "bad_output", "security", "other"}, buttons(t, page.Body, "reason"))
	assert.Equal(t, "3", field(t, page.Body, switchPath, "after"), "After is the table's row")
	assert.Equal(t, "on", field(t, page.Body, switchPath, "switch"))
	for _, l := range links(t, page.Body) {
		assert.False(t, strings.HasPrefix(l, auth.FreshPath), "the on needs no fresh authentication: %s", l)
	}

	answer := b.Submit(page, switchPath, url.Values{"reason": {"runaway_cost"}, "text": {"stray words"}})
	require.Equal(t, http.StatusOK, answer.Resp.StatusCode, text(t, answer.Body))
	commits := w.coord.switchCommits()
	require.Len(t, commits, 1)
	assert.True(t, commits[0].on)
	assert.Equal(t, int64(3), commits[0].after)
	require.NotNil(t, commits[0].reason)
	assert.Equal(t, "runaway_cost:", *commits[0].reason, "a preset sends its code alone")
	assert.Equal(t, "sso:carol", commits[0].actor.Subject)
	assert.False(t, commits[0].actor.Admin)

	page = b.Get(w.public + switchPath)
	b.Submit(page, switchPath, url.Values{"reason": {"other"}, "text": {"  the bill doubled\r\n"}})
	commits = w.coord.switchCommits()
	require.Len(t, commits, 2)
	assert.Equal(t, "other:the bill doubled", *commits[1].reason)

	t.Run("other with no text is refused on the page", func(t *testing.T) {
		page := b.Get(w.public + switchPath)
		refused := b.Submit(page, switchPath, url.Values{"reason": {"other"}, "text": {"   "}})
		assert.Equal(t, http.StatusBadRequest, refused.Resp.StatusCode)
		assert.Contains(t, text(t, refused.Body), "Write the reason in the box")
		assert.Len(t, w.coord.switchCommits(), 2, "nothing was sent")
		assert.Equal(t, "3", field(t, refused.Body, switchPath, "after"))
	})
	t.Run("a text the coordinator refuses is kept", func(t *testing.T) {
		w.coord.queueSwitch(&w.coord.ksCommitE, errors.Join(app.ErrInvalid, errors.New("the text holds an escape")))
		page := b.Get(w.public + switchPath)
		refused := b.Submit(page, switchPath, url.Values{"reason": {"other"}, "text": {"cost \\u0041"}})
		assert.Equal(t, http.StatusBadRequest, refused.Resp.StatusCode)
		assert.Equal(t, "cost \\u0041", textarea(t, refused.Body, switchPath, "text"), "the typed text is kept")
		assert.Equal(t, "3", field(t, refused.Body, switchPath, "after"))
	})
}

// An admin whose freshness lapsed is a responder still, and turns the
// switch on with no fresh authentication (I1; R76).
func TestAnAdminWhoIsNotFreshTurnsTheSwitchOn(t *testing.T) {
	w := newWorld(t)
	w.views.setSwitch(switchOff(0))
	w.coord.ksAnswer = app.KillSwitchOutcome{On: true, Seq: 1, Changed: true}
	stale := w.session(t, "erin", 10*time.Minute, time.Hour, auth.Admin)

	resp, body := w.get(t, switchPath, stale)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, []string{"runaway_cost", "bad_output", "security", "other"}, buttons(t, []byte(body), "reason"))

	form := switchForm(true, 0, w.token(t, stale))
	form.Set("reason", "security")
	resp, body = w.post(t, switchPath, stale, form)

	require.Equal(t, http.StatusOK, resp.StatusCode, text(t, []byte(body)))
	commits := w.coord.switchCommits()
	require.Len(t, commits, 1)
	assert.Equal(t, app.Actor{Subject: "sso:erin", Responder: true}, commits[0].actor)
	assert.Contains(t, text(t, []byte(body)), "The kill switch is on")
}

// An on whose preview cannot count, during a drain, while the instance
// takes over, or with the preview's outcome unknown, is still offered, with
// D7's words, and its After is the table's row (D7; C1).
func TestTheOnIsOfferedWhenItsPreviewCannotCount(t *testing.T) {
	for _, tc := range []struct {
		name           string
		held, draining bool
		err            error
		words, not     string
	}{
		{name: "draining", held: true, draining: true, err: app.ErrNotHeld, words: drainingWords, not: uncountedWords},
		{name: "taking over", held: true, err: app.ErrNotHeld, words: uncountedWords, not: "instance that leads next"},
		{name: "unavailable", held: true, err: errors.New("the preview's read was lost"), words: uncountedWords, not: "instance that leads next"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.leader.set(tc.held, tc.draining)
			w.views.setSwitch(switchOff(6))
			w.coord.queueSwitch(&w.coord.ksPreviewE, tc.err)
			b := w.browser(t, "carol", auth.Responder)

			page := b.Get(w.public + switchPath)

			require.Equal(t, http.StatusOK, page.Resp.StatusCode, text(t, page.Body))
			assert.Contains(t, text(t, page.Body), tc.words)
			assert.NotContains(t, text(t, page.Body), tc.not)
			assert.Equal(t, []string{"runaway_cost", "bad_output", "security", "other"}, buttons(t, page.Body, "reason"))
			assert.Equal(t, "6", field(t, page.Body, switchPath, "after"))
			assert.Equal(t, 1, w.coord.switchPreviews())
		})
	}
}

// The web's on during a drain goes through switching.Set, which records it
// under the draining holding's epoch (RecordKillSwitchOn): its answer says
// it was deferred, with D7's words. A viewer's on during the drain is
// refused responder_only by the coordinator's check (R79; M-h).
func TestTheWebsOnDuringADrainIsRecordedDeferred(t *testing.T) {
	w := newWorld(t)
	w.leader.set(true, true)
	w.leader.mu.Lock()
	w.leader.epoch = 7
	w.leader.mu.Unlock()
	w.views.setSwitch(switchOff(2))
	w.coord.ksRecorded = app.KillSwitchOutcome{On: true, Seq: 3, Changed: true, Deferred: true}
	w.coord.queueSwitch(&w.coord.ksPreviewE, app.ErrNotHeld)
	w.coord.queueSwitch(&w.coord.ksCommitE, app.ErrNotHeld)
	b := w.browser(t, "carol", auth.Responder)

	page := b.Get(w.public + switchPath)
	answer := b.Submit(page, switchPath, url.Values{"reason": {"bad_output"}})

	require.Equal(t, http.StatusOK, answer.Resp.StatusCode, text(t, answer.Body))
	commits := w.coord.switchCommits()
	require.Len(t, commits, 2, "SetKillSwitch, refused as not held, then RecordKillSwitchOn")
	assert.False(t, commits[0].record)
	assert.True(t, commits[1].record)
	assert.Equal(t, int64(7), commits[1].epoch)
	assert.Equal(t, int64(2), commits[1].after)
	words := text(t, answer.Body)
	assert.Contains(t, words, "answer below is what happened")
	assert.Contains(t, words, "Deferred")
	assert.Contains(t, words, "The on is recorded, and the instance that leads next stops every run.")

	t.Run("a viewer's on during the drain is refused responder_only", func(t *testing.T) {
		viewer := w.session(t, "vic", 0, time.Hour, auth.Viewer)
		w.coord.queueSwitch(&w.coord.ksCommitE, app.ErrNotHeld)
		form := switchForm(true, 3, w.token(t, viewer))
		form.Set("reason", "security")
		resp, body := w.post(t, switchPath, viewer, form)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		assert.Contains(t, text(t, []byte(body)), wordsOf(app.CodeResponderOnly))
		calls := w.coord.switchCommits()
		require.Len(t, calls, 4)
		assert.True(t, calls[3].record, "the record's own check refused it")
	})
}

// wordsOf is the web's words of code.
func wordsOf(code string) string {
	w, _ := pages.Words(code)
	return w
}

// The off is offered to a fresh admin alone, with the items the latest on
// left not staffed, from UnstaffedBy, as its preview (M7). An admin whose
// freshness lapsed gets the fresh link back to the control; a responder
// gets the words that only an admin turns it off. A list that cannot be
// read says so, and the off is offered all the same.
func TestTheOffNeedsAFreshAdminAndListsTheUnstaffedItems(t *testing.T) {
	w := newWorld(t)
	w.views.setSwitch(switchOn(2))
	w.views.unstaffed = map[int64][]item.ID{2: {"a1", "a3"}}
	w.coord.ksAnswer = app.KillSwitchOutcome{Seq: 3, Changed: true}

	fresh := w.browser(t, "erin", auth.Admin)
	page := fresh.Get(w.public + switchPath)
	require.Equal(t, http.StatusOK, page.Resp.StatusCode, text(t, page.Body))
	words := text(t, page.Body)
	assert.Contains(t, words, "a1")
	assert.Contains(t, words, "a3")
	assert.Equal(t, "off", field(t, page.Body, switchPath, "switch"))
	assert.Equal(t, "2", field(t, page.Body, switchPath, "after"))
	assert.Empty(t, buttons(t, page.Body, "reason"), "the on is not offered while the switch is on")

	stale := w.session(t, "erin", 10*time.Minute, time.Hour, auth.Admin)
	resp, body := w.get(t, switchPath, stale)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, links(t, []byte(body)), auth.FreshPath+"?return=%2Fkill-switch")
	assert.Contains(t, text(t, []byte(body)), "Re-authenticate to act as an admin")
	assert.Empty(t, field(t, []byte(body), switchPath, "switch"), "no off for an admin who is not fresh")

	t.Run("a stale admin's off is refused with the fresh link", func(t *testing.T) {
		resp, body := w.post(t, switchPath, stale, switchForm(false, 2, w.token(t, stale)))
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		assert.Contains(t, links(t, []byte(body)), auth.FreshPath+"?return=%2Fkill-switch")
		assert.Empty(t, resp.Header.Get("Location"))
	})

	responder := w.session(t, "carol", 0, time.Hour, auth.Responder)
	resp, body = w.get(t, switchPath, responder)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, text(t, []byte(body)), "Only an admin turns the kill switch off")
	for _, l := range links(t, []byte(body)) {
		assert.False(t, strings.HasPrefix(l, auth.FreshPath), "a responder gets no fresh link: %s", l)
	}
	assert.Empty(t, field(t, []byte(body), switchPath, "switch"))

	answer := fresh.Submit(page, switchPath, nil)
	require.Equal(t, http.StatusOK, answer.Resp.StatusCode, text(t, answer.Body))
	commits := w.coord.switchCommits()
	require.Len(t, commits, 2, "the stale admin's off, refused, then the fresh admin's")
	assert.False(t, commits[1].on)
	assert.Equal(t, int64(2), commits[1].after)
	assert.True(t, commits[1].actor.Admin)

	t.Run("a list that cannot be read says so", func(t *testing.T) {
		w.views.mu.Lock()
		w.views.unstaffedErr = errors.New("the records are down")
		w.views.mu.Unlock()
		page := fresh.Get(w.public + switchPath)
		require.Equal(t, http.StatusOK, page.Resp.StatusCode)
		assert.Contains(t, text(t, page.Body), "could not be read")
		assert.Equal(t, "off", field(t, page.Body, switchPath, "switch"))
	})
}

// An admin demoted at the IdP, who authenticates again through the fresh
// flow, holds no admin role after it, so the control offers no off and no
// fresh link, and an off posted all the same is refused admin_only: the
// switch stays on (n11).
func TestADemotedAdminCannotTurnTheSwitchOff(t *testing.T) {
	w := newIDPWorld(t)
	w.views.setSwitch(switchOn(2))
	w.idp.SetClaims(map[string]any{"sub": "alice", "groups": []string{"admins"}})
	b := webtest.NewBrowser(t, w.client())

	page := b.Get(w.public + switchPath)
	require.Equal(t, http.StatusOK, page.Resp.StatusCode, text(t, page.Body))
	require.Equal(t, switchPath, page.Resp.Request.URL.Path, "signed in, back at the control")
	require.Equal(t, "off", field(t, page.Body, switchPath, "switch"), "a fresh admin is offered the off")

	w.clock.advance(10 * time.Minute)
	page = b.Get(w.public + switchPath)
	fresh := auth.FreshPath + "?return=%2Fkill-switch"
	require.Contains(t, links(t, page.Body), fresh)

	w.idp.SetClaims(map[string]any{"sub": "alice", "groups": []string{"responders"}})
	page = b.Get(w.public + fresh)

	require.Equal(t, http.StatusOK, page.Resp.StatusCode, text(t, page.Body))
	assert.Equal(t, switchPath, page.Resp.Request.URL.Path, "the fresh flow returns to the control")
	assert.Empty(t, field(t, page.Body, switchPath, "switch"), "no off for a demoted admin")
	assert.NotContains(t, links(t, page.Body), fresh, "no fresh link: the session holds no admin role")
	assert.Contains(t, text(t, page.Body), "Only an admin turns the kill switch off")

	var cookie *http.Cookie
	for _, c := range b.Cookies(w.public) {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	resp, body := w.post(t, switchPath, cookie, switchForm(false, 2, w.token(t, cookie)))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, text(t, []byte(body)), wordsOf(app.CodeAdminOnly))
	assert.NotContains(t, links(t, []byte(body)), fresh)
	commits := w.coord.switchCommits()
	require.Len(t, commits, 1)
	assert.Equal(t, app.Actor{Subject: "sso:alice", Responder: true}, commits[0].actor)
}

// The on's answer is what happened: the runs it stopped, the items it left
// not staffed, and the items the rule still acts on (I6).
func TestTheOnsAnswerListsStoppedUnstaffedAndOwed(t *testing.T) {
	w := newWorld(t)
	w.views.setSwitch(switchOff(0))
	w.coord.ksAnswer = app.KillSwitchOutcome{
		On: true, Seq: 1, Changed: true,
		Stopped:   []app.RunStop{{Item: "a1", Run: "run-1", Round: 2}, {Item: "a2", Run: "run-2", Round: 1}},
		Unstaffed: []item.ID{"a3"},
		Owed:      []item.ID{"a4"},
	}
	b := w.browser(t, "carol", auth.Responder)

	page := b.Get(w.public + switchPath)
	answer := b.Submit(page, switchPath, url.Values{"reason": {"security"}})

	require.Equal(t, http.StatusOK, answer.Resp.StatusCode, text(t, answer.Body))
	words := text(t, answer.Body)
	assert.Contains(t, words, "answer below is what happened")
	assert.Contains(t, words, "The kill switch is on")
	for _, want := range []string{"run-1", "a1", "run-2", "a2", "a3", "a4"} {
		assert.Contains(t, words, want)
	}
	assert.Contains(t, words, "Stopped 2 runs")
	assert.Contains(t, words, "Left 1 item not staffed")
	assert.Contains(t, words, "Still owed")
}

// The off's answer lists each item the latest on left not staffed with its
// state now and a link to promote it, an item whose state could not be read
// says so, and a list that could not be read in full says so (I6; M-h).
func TestTheOffsAnswerListsStayedUnstaffedWithStatesAndTheUnreadFlag(t *testing.T) {
	w := newWorld(t)
	w.views.setSwitch(switchOn(2))
	w.coord.ksAnswer = app.KillSwitchOutcome{
		Seq: 3, Changed: true,
		StayedUnstaffed: []app.UnstaffedItem{
			{Item: "a1", State: item.StatusAwaitingClose}, {Item: "a2", State: item.StatusClosed}, {Item: "a3"},
		},
		StayedUnstaffedUnread: true,
	}
	b := w.browser(t, "erin", auth.Admin)

	page := b.Get(w.public + switchPath)
	answer := b.Submit(page, switchPath, nil)

	require.Equal(t, http.StatusOK, answer.Resp.StatusCode, text(t, answer.Body))
	words := text(t, answer.Body)
	assert.Contains(t, words, "answer below is what happened")
	assert.Contains(t, words, "The kill switch is off")
	assert.Contains(t, words, "a1: Awaiting close")
	assert.Contains(t, words, "a2: Closed")
	assert.Contains(t, words, "a3: its state could not be read")
	assert.Contains(t, words, "The list could not be read in full")
	got := links(t, answer.Body)
	assert.Contains(t, got, "/items/a1/promote")
	assert.Contains(t, got, "/items/a3/promote")
	assert.NotContains(t, got, "/items/a2/promote", "a closed item is not promoted")
}

// After a switch answer that is not final, stale, unavailable,
// taking_over, not_held or draining, the page reads the table again and
// shows the state now, with a fresh form whose After is that row (R82; m12).
func TestANonFinalSwitchAnswerRereadsTheTable(t *testing.T) {
	for _, tc := range []struct {
		code           string
		on             bool
		held, draining bool
		err            error
		status         int
		words          string
	}{
		{code: switching.CodeStale, on: true, held: true, err: app.ErrStale, status: http.StatusConflict,
			words: "The kill switch changed since this page was loaded, so nothing was done."},
		{code: switching.CodeUnavailable, on: true, held: true, err: errors.New("the write's answer was lost"), status: http.StatusServiceUnavailable,
			words: "The answer was lost: it is not known here whether it took effect."},
		{code: switching.CodeTakingOver, on: true, held: true, err: app.ErrNotHeld, status: http.StatusServiceUnavailable,
			words: "This instance is taking over as the leader: it is not known here whether it took effect."},
		{code: switching.CodeNotHeld, on: true, held: true, err: app.ErrLeaseLost, status: http.StatusConflict,
			words: "This instance no longer leads: it is not known here whether it took effect."},
		{code: switching.CodeDraining, held: true, draining: true, err: app.ErrNotHeld, status: http.StatusConflict,
			words: "This instance is shutting down, and takes no off now: turn the switch off on the next leader, which leads in a minute. If the switch below reads off, the off took effect."},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := newWorld(t)
			w.leader.set(tc.held, tc.draining)
			before, now := switchOff(3), switchOff(5)
			if !tc.on {
				before, now = switchOn(3), switchOn(5)
			}
			w.views.setSwitch(before)
			w.coord.queueSwitch(&w.coord.ksCommitE, tc.err)
			w.coord.ksOnCommit = func() { w.views.setSwitch(now) }
			b := w.browser(t, "erin", auth.Admin)
			page := b.Get(w.public + switchPath)
			require.Equal(t, "3", field(t, page.Body, switchPath, "after"))
			values := url.Values{"reason": {"runaway_cost"}}
			if !tc.on {
				values = nil
			}
			reads := w.views.reads()

			answer := b.Submit(page, switchPath, values)

			assert.Equal(t, tc.status, answer.Resp.StatusCode, text(t, answer.Body))
			words := text(t, answer.Body)
			// The switch's own words (fix round 1, I-1): the state now is
			// below, and which act its form offers, never the item acts'
			// advice to send the same form again.
			assert.Contains(t, words, tc.words)
			assert.Contains(t, words, "The kill switch's state now is shown below, read from its table.")
			offered := "The form below turns it on: send it only if on is what you want."
			if !tc.on {
				offered = "The form below turns it off: send it only if off is what you want."
			}
			assert.Contains(t, words, offered)
			assert.NotContains(t, words, "same form again")
			assert.NotContains(t, words, "items list")
			assert.Contains(t, words, "row 5")
			assert.Equal(t, "5", field(t, answer.Body, switchPath, "after"), "a fresh form at the row read now")
			assert.Greater(t, w.views.reads(), reads+1, "the table is read again after the answer")
			if tc.code == switching.CodeTakingOver {
				assert.Equal(t, "1", answer.Resp.Header.Get("Retry-After"))
			}
		})
	}
}

// A standby disables the control: the page names the leader, offers no
// form, and neither its GET nor a POST asks the coordinator (R-b).
func TestAStandbyDisablesTheControl(t *testing.T) {
	w := newWorld(t)
	w.leader.set(false, false)
	w.views.setSwitch(switchOff(4))
	b := w.browser(t, "erin", auth.Admin)

	page := b.Get(w.public + switchPath)

	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	words := text(t, page.Body)
	assert.Contains(t, words, "This instance does not lead now, so the kill switch cannot be set here")
	assert.Contains(t, words, "instance-a")
	assert.Empty(t, field(t, page.Body, switchPath, "switch"), "no form on a standby")
	assert.Empty(t, buttons(t, page.Body, "reason"))

	c := w.session(t, "erin", 0, time.Hour, auth.Admin)
	form := switchForm(true, 4, w.token(t, c))
	form.Set("reason", "security")
	resp, body := w.post(t, switchPath, c, form)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, text(t, []byte(body)), "did not try this")
	assert.Contains(t, text(t, []byte(body)), "instance-a")
	assert.Zero(t, w.coord.switched(), "the coordinator is not asked on a standby")
}
