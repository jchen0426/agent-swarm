package pages_test

import (
	"errors"
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
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// presets are the on's reason buttons, in order.
var presets = []string{"runaway_cost", "bad_output", "security", "other"}

// The on's preview on the control's page is bounded: a coordinator that
// does not answer in time, its lock held, is a preview that cannot count,
// and the on is offered all the same, at the table's row (review I-2).
func TestTheOnsPreviewIsBoundedAndTheOnIsStillOffered(t *testing.T) {
	t.Cleanup(pages.SetPreviewTimeout(100 * time.Millisecond))
	w := newWorld(t)
	w.views.setSwitch(switchOff(6))
	block := make(chan struct{})
	w.coord.ksBlock = block
	t.Cleanup(func() { close(block) })
	b := w.browser(t, "carol", auth.Responder)

	start := time.Now()
	page := b.Get(w.public + switchPath)

	assert.Less(t, time.Since(start), 2*time.Second, "the page does not wait on the preview")
	require.Equal(t, http.StatusOK, page.Resp.StatusCode, text(t, page.Body))
	assert.Equal(t, presets, buttons(t, page.Body, "reason"))
	assert.Equal(t, "6", field(t, page.Body, switchPath, "after"))
	assert.Contains(t, text(t, page.Body), uncountedWords)
}

// A preview refused not_held, the lease found gone by a write since the
// frame saw it held, is a standby's: the control is disabled with the
// standby's words and the leader's name, never words about an act just
// sent, which a GET did not send (review M-2).
func TestAPreviewRefusedNotHeldDisablesTheControlAsOnAStandby(t *testing.T) {
	w := newWorld(t)
	w.views.setSwitch(switchOff(2))
	w.coord.queueSwitch(&w.coord.ksPreviewE, app.ErrLeaseLost)
	b := w.browser(t, "carol", auth.Responder)

	page := b.Get(w.public + switchPath)

	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	words := text(t, page.Body)
	assert.Contains(t, words, "This instance does not lead now, so the kill switch cannot be set here")
	assert.Contains(t, words, "instance-a")
	assert.NotContains(t, words, "just sent")
	assert.Empty(t, field(t, page.Body, switchPath, "switch"))
}

// While the table cannot be read, the on's preview, which reads the table
// itself, gives the row to name (spec 8.3; review M-3, as the controller
// ruled): its answer offers the on with After at the preview's row. With
// neither answering, no form is offered, and the page says why. A viewer's
// page asks no preview.
func TestAnUnreadTableOffersTheOnAtThePreviewsRow(t *testing.T) {
	w := newWorld(t)
	w.views.mu.Lock()
	w.views.ksErr = errors.New("the controls table is down")
	w.views.mu.Unlock()
	w.coord.ksPreview = app.KillSwitchOutcome{Preview: true, Seq: 9}
	b := w.browser(t, "carol", auth.Responder)

	page := b.Get(w.public + switchPath)

	require.Equal(t, http.StatusOK, page.Resp.StatusCode, text(t, page.Body))
	assert.Equal(t, presets, buttons(t, page.Body, "reason"))
	assert.Equal(t, "9", field(t, page.Body, switchPath, "after"), "After is the preview's row")
	assert.Contains(t, text(t, page.Body), "row 9")

	t.Run("the preview finds the switch on: no on", func(t *testing.T) {
		w.coord.ksPreview = app.KillSwitchOutcome{Preview: true, On: true, Seq: 10}
		page := b.Get(w.public + switchPath)
		assert.Empty(t, field(t, page.Body, switchPath, "switch"))
		assert.Contains(t, text(t, page.Body), "row 10")
	})
	t.Run("neither answers: no form, and why", func(t *testing.T) {
		w.coord.queueSwitch(&w.coord.ksPreviewE, errors.New("the coordinator's read failed"))
		page := b.Get(w.public + switchPath)
		assert.Empty(t, field(t, page.Body, switchPath, "switch"))
		assert.Contains(t, text(t, page.Body), "could not be read, from its table or by the coordinator")
	})
	t.Run("a viewer's page asks no preview", func(t *testing.T) {
		before := w.coord.switchPreviews()
		page := w.browser(t, "vic", auth.Viewer).Get(w.public + switchPath)
		assert.Empty(t, field(t, page.Body, switchPath, "switch"))
		assert.Equal(t, before, w.coord.switchPreviews())
	})
	t.Run("a refusal whose re-read finds neither offers no form", func(t *testing.T) {
		w.views.mu.Lock()
		w.views.ksErr = nil
		w.views.ks = *switchOff(3)
		w.views.mu.Unlock()
		form := b.Get(w.public + switchPath)
		require.Equal(t, "3", field(t, form.Body, switchPath, "after"))
		w.coord.queueSwitch(&w.coord.ksCommitE, errors.New("the write's answer was lost"))
		w.coord.queueSwitch(&w.coord.ksPreviewE, errors.New("the coordinator's read failed"))
		w.coord.ksOnCommit = func() {
			w.views.mu.Lock()
			w.views.ksErr = errors.New("the controls table is down")
			w.views.mu.Unlock()
		}
		answer := b.Submit(form, switchPath, url.Values{"reason": {"security"}})
		assert.Equal(t, http.StatusServiceUnavailable, answer.Resp.StatusCode)
		assert.Empty(t, field(t, answer.Body, switchPath, "switch"))
		words := text(t, answer.Body)
		assert.Contains(t, words, "could not be read, from its table or by the coordinator")
		assert.Contains(t, words, "No form is offered here now.")
	})
}

// No POST route, /kill-switch above all, acts on a form whose Origin is
// another site's, null or missing: each is refused 403 before the
// coordinator is asked (R96; review M-4).
func TestNoPOSTRouteActsOnAForeignNullOrMissingOrigin(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:erin")
	c := w.session(t, "erin", 0, time.Hour, auth.Admin)
	routes := pages.PostRoutes()
	require.Contains(t, routes, switchPath)
	for _, route := range routes {
		path := strings.NewReplacer("{id}", "a1", "{act}", "close").Replace(route)
		form := switchForm(true, 0, w.token(t, c))
		form.Set("reason", "security")
		for name, origin := range map[string]string{"foreign": "https://evil.example", "null": "null", "missing": ""} {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.public+path, strings.NewReader(form.Encode()))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			req.AddCookie(c)
			resp, _ := w.do(t, req)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, "%s, %s", route, name)
		}
	}
	assert.Zero(t, w.coord.switched(), "the kill switch was not asked")
	assert.Empty(t, w.coord.commits(), "no item act was asked")
	assert.Zero(t, w.coord.previews())
}

// The branches of the answers and the off the first tests left out
// (review M-5): an answer that changed nothing; an off whose on left no
// item unstaffed; an off posted by a responder, refused admin_only with
// no fresh link.
func TestTheSwitchsRemainingAnswers(t *testing.T) {
	t.Run("an on that changed nothing", func(t *testing.T) {
		w := newWorld(t)
		w.views.setSwitch(switchOff(1))
		w.coord.ksAnswer = app.KillSwitchOutcome{On: true, Seq: 2}
		b := w.browser(t, "carol", auth.Responder)
		answer := b.Submit(b.Get(w.public+switchPath), switchPath, url.Values{"reason": {"security"}})
		require.Equal(t, http.StatusOK, answer.Resp.StatusCode)
		assert.Contains(t, text(t, answer.Body), "Nothing changed: it was on already.")
	})
	t.Run("an off whose on left no item unstaffed", func(t *testing.T) {
		w := newWorld(t)
		w.views.setSwitch(switchOn(2))
		w.views.unstaffed = map[int64][]item.ID{}
		page := w.browser(t, "erin", auth.Admin).Get(w.public + switchPath)
		assert.Contains(t, text(t, page.Body), "None: the off leaves no item not staffed.")
		assert.Equal(t, "off", field(t, page.Body, switchPath, "switch"))
	})
	t.Run("a responder's off is refused admin_only with no fresh link", func(t *testing.T) {
		w := newWorld(t)
		w.views.setSwitch(switchOn(2))
		c := w.session(t, "carol", 0, time.Hour, auth.Responder)
		resp, body := w.post(t, switchPath, c, switchForm(false, 2, w.token(t, c)))
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		assert.Contains(t, text(t, []byte(body)), wordsOf(app.CodeAdminOnly))
		for _, l := range links(t, []byte(body)) {
			assert.False(t, strings.HasPrefix(l, auth.FreshPath), l)
		}
	})
}

// A preview refused while the instance takes over is not logged at Warn
// on each page's GET, which every responder refreshes in an incident
// (review M-6): it goes to Debug.
func TestAPreviewWhileTakingOverIsNotLoggedAtWarn(t *testing.T) {
	w := newWorld(t)
	w.views.setSwitch(switchOff(1))
	w.coord.queueSwitch(&w.coord.ksPreviewE, app.ErrNotHeld, app.ErrNotHeld, app.ErrNotHeld)
	b := w.browser(t, "carol", auth.Responder)
	for range 3 {
		page := b.Get(w.public + switchPath)
		require.Contains(t, text(t, page.Body), uncountedWords)
	}
	assert.NotContains(t, w.logs.String(), "takes over")
}

// At most one on's preview waits on the coordinator at a time: while one is
// still held, a later page asks none and offers the on uncounted at once,
// so that previews do not pile up behind a held lock and, once it frees,
// queue the on's own POST behind them (Task 8's re-review).
func TestOnlyOneOnsPreviewWaitsAtATime(t *testing.T) {
	t.Cleanup(pages.SetPreviewTimeout(50 * time.Millisecond))
	w := newWorld(t)
	w.views.setSwitch(switchOff(6))
	block := make(chan struct{})
	w.coord.ksBlock = block
	t.Cleanup(func() { close(block) })
	b := w.browser(t, "carol", auth.Responder)

	first := b.Get(w.public + switchPath)
	require.Equal(t, http.StatusOK, first.Resp.StatusCode)
	start := time.Now()
	second := b.Get(w.public + switchPath)

	require.Equal(t, http.StatusOK, second.Resp.StatusCode)
	assert.Less(t, time.Since(start), 40*time.Millisecond, "the second page does not wait for its own timeout")
	assert.Contains(t, text(t, second.Body), uncountedWords)
	assert.Equal(t, 1, w.coord.switchPreviews(), "one preview held, none asked beside it")
}
