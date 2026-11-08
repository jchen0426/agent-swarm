//go:build e2e

package e2e

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The scenarios of Plan 9a-1 of the web interface (m18): a person signs in
// at the fake IdP and acts through webtest's browser, which reaches serve's
// web listener through the harness's TLS stub at web.publicURL, as through
// the deployment's TLS-terminating proxy. They run under the process runner
// alone (R-c), and skip under Docker. They read what a person reads, the
// pages, and judge the runs by the admin API's views and serve's log, as
// the kill switch's scenarios do (killswitch_test.go), whose helpers they
// share.

// The pages' words the scenarios look for.
const (
	killSwitchBanner = "Stopped by the kill switch"
	standbyNotice    = "This instance does not lead"
	freshLink        = "Re-authenticate to act as an admin"
	viewerRefusal    = "Only a responder or an admin turns the kill switch on."
	roleRefusal      = "Only a responder or an admin may do this."
	drainingOnWords  = "Consequences cannot be counted right now. The on is recorded, and the instance that leads next stops every run."
	offButton        = `<input type="hidden" name="switch" value="off">`
)

// webDrainHeld is how long the vendor holds the answers that keep scenario
// 27's drain open: long enough for a person to load the kill switch's page
// and send its on while serve drains.
const webDrainHeld = 15 * time.Second

// webServing starts a harness with the logs server, migrates its database
// and writes cfg, and skips the test under the Docker runner, as the web
// scenarios run under the process runner alone.
//
//nolint:gocritic // hugeParam: the harness takes a scenario's specs by value.
func webServing(t *testing.T, cfg harness.ConfigSpec) *harness.Env {
	t.Helper()
	env, _ := configured(t, []harness.ServerSpec{logs}, cfg)
	if env.Runner() == "docker" {
		t.Skip("the web scenarios run under the process runner alone")
	}
	return env
}

// withWeb is a deployment d with the web block.
//
//nolint:gocritic // hugeParam: the harness takes a scenario's specs by value.
func withWeb(d harness.DeploymentSpec) harness.DeploymentSpec {
	d.Web = true
	return d
}

// hanging is a model that never answers: every turn waits on the vendor
// until the test ends, or its run stops.
func hanging(*harness.VendorRequest) harness.VendorReply { return harness.Hang() }

// openedID reads the item's id from the opening form's answer.
var openedID = regexp.MustCompile(`Item <span class="id">([^<]+)</span>: <a href="/items/`)

// csrfToken reads the CSRF token of the sign-out form every page carries.
var csrfToken = regexp.MustCompile(`<input type="hidden" name="csrf" value="([^"]+)">`)

// freshHref reads the link that authenticates a person again.
var freshHref = regexp.MustCompile(`href="(/auth/fresh[^"]*)"`)

// page asserts that a page answered 200 at path, and returns its body.
func page(t *testing.T, p *webtest.Page, path string) string {
	t.Helper()
	require.Equal(t, http.StatusOK, p.Resp.StatusCode, "%s answers 200:\n%s", path, p.Body)
	require.Equal(t, path, p.Resp.Request.URL.Path, "the page is %s", path)
	return string(p.Body)
}

// Scenario 26: a responder signs in at the IdP through the TLS stub, opens
// an item with the opening form, sees it in the list and on its board, and
// turns the kill switch on with a preset reason. The on's answer says the
// switch is on; the run stops, killed, its turns end, and the board shows
// that the kill switch stopped it.
func TestAResponderSignsInAndStopsEveryRunFromTheWeb(t *testing.T) {
	env := webServing(t, team(0))
	a := leading(t, env, withWeb(quick))
	env.Vendor().Script(hanging)
	web := env.Web()

	b, items := web.SignIn(t, "rita", harness.RespondersGroup)
	assert.Contains(t, page(t, items, "/items"), "Signed in as", "the list says who is signed in")
	form := b.Get(web.URL() + "/items/new")
	page(t, form, "/items/new")
	opened := b.Submit(form, "/items/new", url.Values{
		"type": {incident.Type}, "title": {incident.Title}, "service": {incident.Routing["service"]},
	})
	body := page(t, opened, "/items/new")
	m := openedID.FindStringSubmatch(body)
	require.NotNil(t, m, "the opening's answer names the item:\n%s", body)
	id := html.UnescapeString(m[1])
	turns := waitForTurns(t, a, 2)
	calling(t, env, "a", 2)

	list := page(t, b.Get(web.URL()+"/items"), "/items")
	assert.Contains(t, list, id, "the list shows the item")
	assert.Contains(t, list, incident.Title, "and its title")
	board := page(t, b.Get(web.URL()+"/items/"+id), "/items/"+id)
	assert.Contains(t, board, id, "the board shows the item")
	assert.NotContains(t, board, killSwitchBanner, "nothing has stopped it yet")

	control := b.Get(web.URL() + "/kill-switch")
	assert.Contains(t, page(t, control, "/kill-switch"), "Turn on: Runaway cost", "the on offers its preset reasons")
	on := b.Submit(control, "/kill-switch", url.Values{"reason": {"runaway_cost"}})

	done := page(t, on, "/kill-switch")
	assert.Contains(t, done, "The kill switch is on", "the on's answer is what happened")
	assert.NotContains(t, done, "Deferred")
	allGone(t, turns, "every turn the run started is gone")
	killedRun(t, a, id)
	code, ks := a.Admin().KillSwitch(t)
	require.Equal(t, http.StatusOK, code, ks.Error)
	assert.True(t, ks.On, "the admin API reads the switch on")
	board = page(t, b.Get(web.URL()+"/items/"+id), "/items/"+id)
	assert.Contains(t, board, killSwitchBanner, "the board shows that the kill switch stopped the run")
	assert.Equal(t, "a", web.Upstream(), "the stub forwarded to the leader")
}

// Scenario 27: during a drain, held open by the vendor's delayed answers,
// /leaderz answers 200 on the draining leader and 503 on the standby, and the
// stub keeps forwarding to the leader. A responder's on through the web is
// recorded deferred for the next leader, which halts both runs at its
// takeover before either opens a round. Once the standby leads, its /leaderz
// answers 200, the stub forwards to it, and the person, still signed in,
// sees the board stopped by the kill switch, with no standby's notice.
func TestADrainingLeadersWebRecordsTheOnForTheNextLeader(t *testing.T) {
	d := withWeb(leased)
	d.Timing.Drain = 2 * webDrainHeld
	env := webServing(t, team(0))
	a := leading(t, env, d)
	b := standingBy(t, env, d)
	env.Vendor().Script(byInstance(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn().After(webDrainHeld) }))
	web := env.Web()
	browser, _ := web.SignIn(t, "rita", harness.RespondersGroup)
	require.Equal(t, "a", web.Upstream(), "the stub forwards to the leader")
	ids := runningTwo(t, env, a)

	a.Signal(t, syscall.SIGTERM)
	a.Log().WaitFor(t, "draining", nil, 10*time.Second)
	assert.Equal(t, http.StatusOK, a.Leaderz(t), "the draining leader's /leaderz answers 200")
	assert.Equal(t, http.StatusServiceUnavailable, b.Leaderz(t), "the standby's answers 503")
	control := browser.Get(web.URL() + "/kill-switch")
	body := page(t, control, "/kill-switch")
	assert.Equal(t, "a", web.Upstream(), "the stub keeps forwarding to the draining leader")
	assert.Contains(t, body, drainingOnWords, "the on is offered uncounted during the drain")
	on := browser.Submit(control, "/kill-switch", url.Values{"reason": {"bad_output"}})

	done := page(t, on, "/kill-switch")
	assert.Equal(t, "a", web.Upstream())
	assert.Contains(t, done, "The kill switch is on", "the on's answer is what happened")
	assert.Contains(t, done, "Deferred", "the on is recorded for the next leader")
	code, ks := a.Admin().KillSwitch(t)
	require.Equal(t, http.StatusOK, code, ks.Error)
	assert.True(t, ks.On, "the table holds the on")
	for _, id := range ids {
		code, v := a.Admin().Item(t, id)
		require.Equal(t, http.StatusOK, code, v.Error)
		assert.Equal(t, "investigating", v.State, "item %s's run goes on", id)
		assert.Empty(t, v.Stop, "the deferred on stopped no run of item %s", id)
	}

	taken := b.Log().WaitFor(t, "lease taken", nil, leadWait)
	require.Equal(t, 0, a.Wait(t, leadWait), "a drains and exits 0")
	set := b.Log().WaitFor(t, "kill switch set", byCause("takeover"), leadWait)
	assert.InDelta(t, 2, set["stopped"], 0, "b's takeover stopped both runs: %v", set)
	for _, id := range ids {
		killedRun(t, b, id)
		assert.Equal(t, []int{1}, roundsOpened(t, b, id), "b opens no round of item %s", id)
	}
	took := tookAt(t, taken)
	assert.Empty(t, vendorAfter(env, took), "the vendor receives no request after b's take, at %s", took.UTC().Format(time.RFC3339Nano))

	assert.Equal(t, http.StatusOK, b.Leaderz(t), "the new leader's /leaderz answers 200")
	board := page(t, browser.Get(web.URL()+"/items/"+ids[0]), "/items/"+ids[0])
	assert.Equal(t, "b", web.Upstream(), "the stub follows the lease to b")
	assert.Contains(t, board, killSwitchBanner, "the board shows that the kill switch stopped the run")
	assert.NotContains(t, board, standbyNotice, "b leads")
}

// Scenario 28: a viewer is offered no on, and an on posted with the viewer's
// own session and CSRF token is refused, responder_only, and appends no row. With the
// switch on, an admin who signed in ten minutes ago, outside the fresh
// window, is offered no off but a link to authenticate again; through it the
// IdP asks them to sign in again (prompt=login), and back on the control the
// off is offered, and turns the switch off.
func TestAnAdminReauthenticatesToTurnTheSwitchOffAndAViewerCannotTurnItOn(t *testing.T) {
	env := webServing(t, team(0))
	a := leading(t, env, withWeb(quick))
	web := env.Web()

	viewer, _ := web.SignIn(t, "vera", harness.ViewersGroup)
	control := viewer.Get(web.URL() + "/kill-switch")
	body := page(t, control, "/kill-switch")
	assert.Contains(t, body, viewerRefusal, "a viewer is offered no on")
	assert.NotContains(t, body, `<form method="post" action="/kill-switch">`)
	m := csrfToken.FindStringSubmatch(body)
	require.NotNil(t, m, "the page carries the viewer's CSRF token:\n%s", body)
	forged := &webtest.Page{Resp: control.Resp, Body: []byte(`<form method="post" action="/kill-switch">` +
		`<input type="hidden" name="csrf" value="` + m[1] + `">` +
		`<input type="hidden" name="switch" value="on"><input type="hidden" name="after" value="0"></form>`)}
	refused := viewer.Submit(forged, "/kill-switch", url.Values{"reason": {"security"}})
	// The coordinator refuses an act of a role that may not, responder_only,
	// with 409, as the admin API answers it.
	assert.Equal(t, http.StatusConflict, refused.Resp.StatusCode, "a viewer's on is refused:\n%s", refused.Body)
	assert.Contains(t, string(refused.Body), roleRefusal, "in words")
	code, before := a.Admin().KillSwitch(t)
	require.Equal(t, http.StatusOK, code, before.Error)
	require.False(t, before.On, "the refused on changed nothing")
	require.Zero(t, before.Seq, "and appended no row")

	code, on := a.Admin().SetKillSwitch(t, true, before.Seq, runawayAgents)
	require.Equal(t, http.StatusOK, code, on.Error)
	require.True(t, on.On)

	web.IDP().SetLogin(time.Now().Add(-10 * time.Minute))
	admin, _ := web.SignIn(t, "ada", harness.AdminsGroup)
	control = admin.Get(web.URL() + "/kill-switch")
	body = page(t, control, "/kill-switch")
	assert.Contains(t, body, freshLink, "an admin outside the fresh window is asked to authenticate again")
	assert.NotContains(t, body, offButton, "and is offered no off")
	m = freshHref.FindStringSubmatch(body)
	require.NotNil(t, m, "the page links to authenticate again:\n%s", body)
	asked := len(web.IDP().Authorizations())

	back := admin.Get(web.URL() + html.UnescapeString(m[1]))

	body = page(t, back, "/kill-switch")
	authorizations := web.IDP().Authorizations()
	require.Len(t, authorizations, asked+1, "authenticating again asks the IdP once")
	assert.Equal(t, "login", authorizations[asked].Get("prompt"), "the IdP is asked to sign the admin in again")
	assert.Contains(t, body, offButton, "back on the control, the off is offered")
	off := admin.Submit(back, "/kill-switch", nil)

	done := page(t, off, "/kill-switch")
	assert.Contains(t, done, "The kill switch is off", "the off's answer is what happened")
	code, after := a.Admin().KillSwitch(t)
	require.Equal(t, http.StatusOK, code, after.Error)
	assert.False(t, after.On, "the admin API reads the switch off")
	assert.Equal(t, on.Seq+1, after.Seq, "the off is the row after the on")
}
