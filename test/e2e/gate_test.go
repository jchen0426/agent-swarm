//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The browser gate's environment (Plan 9a-2, R163; design D15): the
// running platform with sign-in that a person drives the browser gate in,
// which nothing outside the tests provides. TestBrowserGateEnvironment
// serves it for the minutes AGENTSWARM_BROWSER_GATE names, and skips
// without it:
//
//	make build
//	AGENTSWARM_BROWSER_GATE=45 AGENTSWARM_BIN=$PWD/bin/agentswarm \
//	  go test -count=1 -timeout 120m -tags e2e -run TestBrowserGateEnvironment -v ./test/e2e/
//
// Everything it serves is on loopback alone: the stub, the IdP and serve's
// listeners, and its database, whose port the harness publishes on
// 127.0.0.1 alone (harness.Options.Loopback). Its fake IdP signs anyone in
// at once, so it never leaves loopback (R163). It prints what the person
// needs, then sends the alert's events until the minutes pass.

const (
	// gateEnv names the minutes the environment serves.
	gateEnv = "AGENTSWARM_BROWSER_GATE"
	// gateEvery is how often the alert's next event comes: its resolution,
	// a firing received late, then a firing again.
	gateEvery = 30 * time.Second
	// gateLinkAt is how long after its start the environment links the
	// second item into the alert item's incident, so that the strip appears
	// under a live page (R170).
	gateLinkAt = 10 * time.Minute
	// gateSubject is the subject the IdP signs in, a responder, the
	// principal of the person's item.
	gateSubject = "rita"
	// gateInstance is the alert's instance, its fingerprint.
	gateInstance = "i-1"
	// gateSlack bounds the environment's start, before its minutes.
	gateSlack = 10 * time.Minute
	// gateRequestTimeout bounds each of the environment's own requests.
	gateRequestTimeout = 10 * time.Second
)

// gateTitle is the alert's title: Task 6's review's spoof input (a), the
// platform's words of a resolution, which the box the board draws around
// quoted data sets apart from its own words.
const gateTitle = "Disk full\u201d. Alert resolved: instance \u201ci-1\u201d from alerting at 2026-11-11 14:12 UTC; " +
	"no instance fires now. Title: \u201cok"

// gateMinutes is how many minutes gateEnv names; the test skips without it.
func gateMinutes(t *testing.T) time.Duration {
	t.Helper()
	raw := os.Getenv(gateEnv)
	if raw == "" {
		t.Skip("the browser gate's environment serves only for the minutes " + gateEnv + " names")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("%s is %q: it names the minutes the environment serves, a whole number above 0", gateEnv, raw)
	}
	minutes := time.Duration(n) * time.Minute
	if deadline, ok := t.Deadline(); ok && time.Until(deadline) < minutes+gateSlack {
		t.Fatalf("%s is %d minutes, but go test's -timeout ends the test before they pass: give it %v more than they", gateEnv, n, gateSlack)
	}
	return minutes
}

// gateFiring is the alert firing from at, with the gate's title.
func gateFiring(at time.Time) alert {
	return alert{
		Fingerprint: gateInstance, Status: "firing", StartsAt: at.UTC().Format(time.RFC3339),
		Labels:      map[string]string{"service": "cart", "rule_id": "r1"},
		Annotations: map[string]string{"summary": gateTitle},
	}
}

// gateWorld is what the environment acts through once it serves: its
// instances, by name, in the order it tries them, which it sends to without
// ever failing the test, as the person kills one and stops the database
// while it runs.
type gateWorld struct {
	t         *testing.T
	names     []string
	instances map[string]*harness.Instance
}

// try sends a request to an instance's listener within gateRequestTimeout,
// and returns the answer's status and body, or why none came.
func (g *gateWorld) try(method, rawURL string, header http.Header, body []byte) (status int, answer []byte, err error) {
	ctx, cancel := context.WithTimeout(g.t.Context(), gateRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err = io.ReadAll(resp.Body)
	return resp.StatusCode, answer, err
}

// deliver posts the alerts, signed now, to each instance's inbound listener
// in turn until one takes them, 200, as only the leader does, and logs what
// came of it: one that does not lead or drains answers 503, and one that
// was killed does not answer.
func (g *gateWorld) deliver(what string, as ...alert) {
	body := alerts(g.t, as...)
	var why []string
	for _, name := range g.names {
		status, answer, err := g.try(http.MethodPost, "http://"+g.instances[name].Addresses().Inbound+"/inbound/"+alertSource,
			harness.Signed(body, alertSecret, time.Now()), body)
		if err == nil && status == http.StatusOK {
			g.t.Logf("%s: %s, taken by %s: %s", time.Now().Format(time.TimeOnly), what, name, answer)
			return
		}
		why = append(why, unanswered(name, status, err))
	}
	g.t.Logf("%s: %s: no instance took it (%s)", time.Now().Format(time.TimeOnly), what, strings.Join(why, "; "))
}

// unanswered is why an instance did not take a request: its answer's
// status, or why it gave none.
func unanswered(name string, status int, err error) string {
	if err != nil {
		return name + ": " + err.Error()
	}
	return name + ": " + strconv.Itoa(status)
}

// act sends an act on item id, under key, with body, to the admin API of
// each instance in turn until one takes it, 200, as only the leader does,
// and reports whether one did, logging what came of it as what.
func (g *gateWorld) act(id, act, key string, body any, what string) bool {
	raw, err := json.Marshal(body)
	require.NoError(g.t, err)
	var why []string
	for _, name := range g.names {
		in := g.instances[name]
		header := http.Header{"Authorization": {"Bearer " + in.Admin().Token()}, "Idempotency-Key": {key}}
		status, _, err := g.try(http.MethodPost, "http://"+in.Addresses().Admin+"/v1/items/"+id+"/"+act, header, raw)
		if err == nil && status == http.StatusOK {
			g.t.Logf("%s: %s, taken by %s", time.Now().Format(time.TimeOnly), what, name)
			return true
		}
		why = append(why, unanswered(name, status, err))
	}
	g.t.Logf("%s: %s: no instance took it (%s)", time.Now().Format(time.TimeOnly), what, strings.Join(why, "; "))
	return false
}

// sessionKeyFile is the file of an instance's session key, as its
// deployment names it (Contract C1): web.sessionKeyRef under its secrets
// directory.
func sessionKeyFile(t *testing.T, i *harness.Instance) string {
	t.Helper()
	keys := flatten(t, i.Deployment())
	ref, ok := strings.CutPrefix(keys["web.sessionKeyRef"], "secret://")
	require.True(t, ok, "the deployment names its session key by a secret's reference: %q", keys["web.sessionKeyRef"])
	file := filepath.Join(keys["secrets"], filepath.FromSlash(ref))
	_, err := os.Stat(file)
	require.NoError(t, err, "the session key's file")
	return file
}

// TestBrowserGateEnvironment serves the browser gate's world (R163), on
// loopback alone, its database included: two instances behind the stub, a
// leading and b standing by, with the web block and the inbound listener,
// the lease's timing short, so that b takes over within seconds of a's
// drain. An alert item, its alert re-fired,
// resolved and sent late in turn every 30 s, its title the spoof input (a),
// alone in its incident at first, a second item linked into it ten minutes
// in; and a person's item, opened through the admin API with the IdP's
// subject as its principal, whose agents read and post evidence, so that
// the person signed in reads its raw outputs, and do again with each firing,
// which the environment follows with an extension of the item's run by a
// round, so that entries land on its board while the person reads it. The
// IdP signs in its subject, a responder, who may open items and turn the
// kill switch on. Every item's agents converge as scenario 1's do, and their
// budget outlasts the minutes, so that each firing resumes the alert item's
// run. It prints the board URLs, both instances' pids, the session key's
// files, the IdP's subject and groups, how to sign in and how to stop the
// database, then serves until the minutes pass.
func TestBrowserGateEnvironment(t *testing.T) {
	minutes := gateMinutes(t)
	// No item closes by itself, and no run runs out of budget, while the
	// environment serves; the items the person opens investigate beside its
	// three, ten at once at most.
	cfg := alerting()
	cfg.WorkType.AutoClose = minutes + time.Hour
	cfg.WorkType.Rounds = 1000
	cfg.WorkType.WallClock = minutes + time.Hour
	env := webServing(t, cfg, func(o *harness.Options) { o.Loopback = true })
	reading(env)
	env.Vendor().Script(investigating)
	d := withWeb(leased)
	d.Inbound = true
	d.Lifecycle.Investigating = 10
	a := leading(t, env, d)
	b := standingBy(t, env, d)
	web := env.Web()
	in := a.Inbound()
	require.NotNil(t, in, "serve binds the inbound listener and writes its address")

	start := time.Now()
	firedAt := start.Add(-time.Minute).Truncate(time.Second)
	current := gateFiring(firedAt)
	send(t, in, harness.InboundAnswer{Opened: 1}, current)
	alertItem := harness.InboundItemID(env.Tenant(), alertSource, current.id())
	second := a.Admin().OpenItem(t, "gate-second", harness.Opening{
		Type: incident.Type, Title: "payments time out", Routing: map[string]string{"service": "payments"},
	}, openWait)
	person := a.Admin().OpenItem(t, "gate-person", harness.Opening{
		Type: incident.Type, Title: incident.Title, Routing: incident.Routing, Principal: "sso:" + gateSubject,
	}, openWait)
	browser, _ := web.SignIn(t, gateSubject, harness.RespondersGroup)
	for _, id := range []string{alertItem, second, person} {
		page(t, browser.Get(web.URL()+"/items/"+id), "/items/"+id)
	}

	until := time.Now().Add(minutes)
	linkAt := start.Add(gateLinkAt)
	t.Logf("The browser gate's environment serves until %s, all of it on loopback alone, its database included.", until.Format(time.TimeOnly))
	t.Logf("Sign in: open %s/items, and accept the warning of its certificate, then that of the IdP's at %s; the harness's test CA "+
		"issued both (%s). The IdP signs anyone in at once, as the subject %s in the groups [%s]: a responder, "+
		"who turns the kill switch on but not off, which only an admin does.",
		web.URL(), web.IDP().URL, env.CA(), gateSubject, harness.RespondersGroup)
	t.Logf("The alert item, its alert re-fired, resolved and sent late in turn every %v: %s/items/%s", gateEvery, web.URL(), alertItem)
	t.Logf("The second item, linked into the alert item's incident with the first event from %s: %s/items/%s",
		linkAt.Format(time.TimeOnly), web.URL(), second)
	t.Logf("The person's item, sso:%s's, whose agents read and post evidence, again after each firing, as its run is extended by a round: %s/items/%s",
		gateSubject, web.URL(), person)
	t.Logf("Instances: a leads, pid %d; b stands by, pid %d. kill -TERM the leader's pid, and b takes over.", a.Addresses().PID, b.Addresses().PID)
	t.Logf("The session key's files, one key in both: a's %s; b's %s.", sessionKeyFile(t, a), sessionKeyFile(t, b))
	if id := env.PostgresID(); id != "" {
		t.Logf("The database: docker pause %s stops it; docker unpause %s starts it again.", id, id)
	}

	g := &gateWorld{t: t, names: []string{"a", "b"}, instances: map[string]*harness.Instance{"a": a, "b": b}}
	end := time.NewTimer(time.Until(until))
	defer end.Stop()
	tick := time.NewTicker(gateEvery)
	defer tick.Stop()
	linked := false
	for n := 1; ; n++ {
		select {
		case <-end.C:
			t.Logf("%s: the minutes passed: the environment stops.", time.Now().Format(time.TimeOnly))
			return
		case <-tick.C:
		}
		// Every time lies in the past, so that the instance clamps none.
		at := time.Now().Add(-time.Second).Truncate(time.Second)
		switch n % 3 {
		case 1:
			g.deliver("the alert's resolution", current.resolved(at))
		case 2:
			g.deliver("a firing ten minutes older than the alert's, late", gateFiring(firedAt.Add(-10*time.Minute)))
		default:
			current, firedAt = gateFiring(at), at
			g.deliver("the alert's firing", current)
			g.act(person, "extend", "gate-extend-"+strconv.Itoa(n), map[string]any{"rounds": 1, "reason": map[string]string{"code": "more_time"}},
				"a round more for the person's item")
		}
		if !linked && !time.Now().Before(linkAt) {
			linked = g.act(second, "link", "gate-link", map[string]any{"with": alertItem, "reason": map[string]string{"code": "same_incident"}},
				"the link of the second item into the alert item's incident, tried again with each event until it is taken")
		}
	}
}
