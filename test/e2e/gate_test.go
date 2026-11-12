//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
//	AGENTSWARM_BROWSER_GATE=45 TESTCONTAINERS_RYUK_DISABLED=true AGENTSWARM_BIN=$PWD/bin/agentswarm \
//	  go test -count=1 -timeout 120m -tags e2e -run TestBrowserGateEnvironment -v ./test/e2e/
//
// Everything it serves is on loopback alone: the stub, the IdP and serve's
// listeners, and its database, whose port the harness publishes on
// 127.0.0.1 alone (harness.Options.Loopback), which the environment checks
// before it prints. Its fake IdP signs anyone in at once, so it never leaves
// loopback (R163). It refuses to start unless TESTCONTAINERS_RYUK_DISABLED
// is true, which turns testcontainers' Ryuk off, as Ryuk publishes its own
// port on every interface; the harness's own cleanup removes its containers
// at the test's end, and make test-clean what a killed run leaves. It prints
// what the person needs, then sends the alert's events until the minutes
// pass.
//
// With AGENTSWARM_BROWSER_GATE_CHROME naming Chrome for Testing (Plan 9a-3),
// it also runs a throwaway browser, signed in, and a control socket, through
// which a controller walks the gate, in a new run directory under
// AGENTSWARM_BROWSER_GATE_OUT (see gateBrowser):
//
//	AGENTSWARM_BROWSER_GATE=100 TESTCONTAINERS_RYUK_DISABLED=true AGENTSWARM_BIN=$PWD/bin/agentswarm \
//	  AGENTSWARM_BROWSER_GATE_CHROME="<Chrome for Testing's binary>" AGENTSWARM_BROWSER_GATE_OUT=$PWD/.browser-gate \
//	  go test -count=1 -timeout 120m -tags e2e -run TestBrowserGateEnvironment -v ./test/e2e/

const (
	// gateEnv names the minutes the environment serves.
	gateEnv = "AGENTSWARM_BROWSER_GATE"
	// gateOutEnv names the directory the environment's browser makes its run
	// directory in (R187).
	gateOutEnv = "AGENTSWARM_BROWSER_GATE_OUT"
	// gateSignInWait bounds the browser's sign-in of its first tab.
	gateSignInWait = 2 * time.Minute
	// gateProbeTimeout bounds GET /world's question to each instance's
	// /leaderz, which a stopped instance never answers.
	gateProbeTimeout = time.Second
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
	// ryukEnv is testcontainers' setting that turns Ryuk, its reaper, off:
	// it takes "true" alone as off.
	ryukEnv = "TESTCONTAINERS_RYUK_DISABLED"
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

// ryukOff fails the test at once unless ryukEnv is "true", as testcontainers
// reads it: Ryuk, when it runs, publishes its own port on every interface,
// and the environment serves on loopback alone (R163).
func ryukOff(t *testing.T) {
	t.Helper()
	if v := os.Getenv(ryukEnv); v != "true" {
		t.Fatalf("%s is %q: the browser gate's environment runs with testcontainers' Ryuk off, %s=true, "+
			"as Ryuk publishes its own port on every interface, and the environment serves on loopback alone", ryukEnv, v, ryukEnv)
	}
}

// loopbackAlone fails the test unless the environment's database is
// published on loopback alone (R163): its container's bindings, as docker
// inspect reads them, at least one, each on 127.0.0.1. A database the
// harness is given, which it does not publish, fails it.
func loopbackAlone(t *testing.T, env *harness.Env) {
	t.Helper()
	id := env.PostgresID()
	require.NotEmpty(t, id, "the environment starts a database of its own, which it publishes on loopback alone: unset AGENTSWARM_E2E_DATABASE")
	raw := docker(t, "container", "inspect", "--format", "{{json .NetworkSettings.Ports}}", id)
	var ports map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &ports), "the database's ports: %s", raw)
	bound := 0
	for port, bindings := range ports {
		for _, b := range bindings {
			bound++
			require.Equal(t, "127.0.0.1", b.HostIP, "the database's port %s is published on %s:%s, not on loopback alone: %s", port, b.HostIP, b.HostPort, raw)
		}
	}
	require.Positive(t, bound, "the database's port is published: %s", raw)
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
// while it runs; and what GET /world reports of it (R186).
type gateWorld struct {
	t         *testing.T
	names     []string
	instances map[string]*harness.Instance
	// keyFiles are the instances' session key's files; database is the
	// database's container; items are the items' boards, by role; linkAt is
	// from when the second item is linked, and until when the minutes end.
	keyFiles      map[string]string
	database      string
	items         map[string]string
	linkAt, until time.Time

	mu sync.Mutex
	// deliveries are the alerts the environment sent and the acts it asked
	// for, in order.
	deliveries []worldDelivery
}

// worldDelivery is an alert the environment sent, or an act it asked for,
// as GET /world reports it: when its answer came, what it was, and the
// instance that took it, or why none did.
type worldDelivery struct {
	At    time.Time `json:"at"`
	What  string    `json:"what"`
	Taken string    `json:"taken,omitempty"`
	None  string    `json:"none,omitempty"`
}

// note keeps a delivery, or an act, for GET /world.
func (g *gateWorld) note(what, taken, none string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deliveries = append(g.deliveries, worldDelivery{At: time.Now(), What: what, Taken: taken, None: none})
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
			g.note(what, name, "")
			return
		}
		why = append(why, unanswered(name, status, err))
	}
	g.t.Logf("%s: %s: no instance took it (%s)", time.Now().Format(time.TimeOnly), what, strings.Join(why, "; "))
	g.note(what, "", strings.Join(why, "; "))
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
			g.note(what, name, "")
			return true
		}
		why = append(why, unanswered(name, status, err))
	}
	g.t.Logf("%s: %s: no instance took it (%s)", time.Now().Format(time.TimeOnly), what, strings.Join(why, "; "))
	g.note(what, "", strings.Join(why, "; "))
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
// database, then serves until the minutes pass. With a browser named, it
// serves its own too, until the minutes pass or POST /end ends the run
// (gateBrowser).
func TestBrowserGateEnvironment(t *testing.T) {
	minutes := gateMinutes(t)
	ryukOff(t)
	// No item closes by itself, and no run runs out of budget, while the
	// environment serves; the items the person opens investigate beside its
	// three, ten at once at most.
	cfg := alerting()
	cfg.WorkType.AutoClose = minutes + time.Hour
	cfg.WorkType.Rounds = 1000
	cfg.WorkType.WallClock = minutes + time.Hour
	env := webServing(t, cfg, func(o *harness.Options) { o.Loopback = true })
	loopbackAlone(t, env)
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
	keyA, keyB := sessionKeyFile(t, a), sessionKeyFile(t, b)
	t.Logf("The session key's files, one key in both: a's %s; b's %s.", keyA, keyB)
	if id := env.PostgresID(); id != "" {
		t.Logf("The database: docker pause %s stops it; docker unpause %s starts it again.", id, id)
	}

	g := &gateWorld{
		t: t, names: []string{"a", "b"}, instances: map[string]*harness.Instance{"a": a, "b": b},
		keyFiles: map[string]string{"a": keyA, "b": keyB}, database: env.PostgresID(),
		items: map[string]string{
			"alert":  web.URL() + "/items/" + alertItem,
			"second": web.URL() + "/items/" + second,
			"person": web.URL() + "/items/" + person,
		},
		linkAt: linkAt, until: until,
	}
	// POST /end closes ended; without a browser it stays nil, which no
	// select takes.
	var ended <-chan struct{}
	if os.Getenv(harness.ChromeEnv) != "" {
		ended = gateBrowser(t, env, g)
	}
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
		case <-ended:
			t.Logf("%s: POST /end: the controller ended the run, and the environment stops.", time.Now().Format(time.TimeOnly))
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

// The run's verdict is PASS only for a run that had not failed, whose
// browser closed cleanly, whose complete NetLog shows no connect off
// loopback (R185) and that had no certificate event (R180); anything else
// is FAIL, the first words saying so, as the controller reads them. It
// needs neither the environment nor a browser.
func TestTheRunsVerdictPassesAWholeRunAlone(t *testing.T) {
	cases := []struct {
		name string
		run  gateRun
		why  string
	}{
		{"the test had failed before, as when the socket's stop gave up or sign-in failed", gateRun{failed: true}, "failed before"},
		{"Close's error", gateRun{closeErr: errors.New("a write to events.jsonl failed")}, "a write to events.jsonl failed"},
		{"a connect off loopback", gateRun{offLoopback: []string{"a TCP connect to 192.0.2.1:443, off the environment"}}, "192.0.2.1:443"},
		{"a NetLog that could not be judged", gateRun{offLoopbackErr: errors.New("not a NetLog")}, "not a NetLog"},
		{"a certificate event", gateRun{certs: []string{"tab a, event 9: {}"}}, "tab a, event 9"},
		{"an events.jsonl that could not be read", gateRun{certsErr: errors.New("line 3 is no event")}, "line 3 is no event"},
	}
	for _, c := range cases {
		line, pass := c.run.verdict("/run")
		assert.False(t, pass, c.name)
		assert.True(t, strings.HasPrefix(line, "The run's verdict: FAIL, on 1 finding(s)"), "%s: %s", c.name, line)
		assert.Contains(t, line, c.why, c.name)
		assert.NotContains(t, line, "PASS", c.name)
	}
	line, pass := (&gateRun{}).verdict("/run")
	assert.True(t, pass)
	assert.True(t, strings.HasPrefix(line, "The run's verdict: PASS."), line)
	_, pass = (&gateRun{failed: true, certs: []string{"x"}}).verdict("/run")
	assert.False(t, pass)
}

// The lease's reader says what it could not read, in /world's answer: a log
// that reads as nothing, as one that could not be read, and a change whose
// time it cannot read, which it leaves out, as it cannot be placed; the
// changes it read come in the order of their times, both logs merged. It
// needs neither the environment nor a browser.
func TestTheLeaseReaderSaysWhatItCannotRead(t *testing.T) {
	logs := map[string][]map[string]any{
		"a": {
			{"time": "2026-11-12T10:00:00.5Z", "msg": "serve started"},
			{"time": "2026-11-12T10:00:01.5Z", "msg": "lease taken", "epoch": 1.0},
			{"time": "2026-11-12T10:05:00Z", "msg": "lease released", "epoch": 1.0},
			{"time": "a time of no form", "msg": "lease lost", "epoch": 1.0, "reason": "a renewal found the lease lost"},
		},
		"b": {
			{"time": "2026-11-12T10:05:02Z", "msg": "lease taken", "epoch": 2.0},
		},
		"c": nil,
	}
	changes, problems := leaseChangesOf([]string{"a", "b", "c"}, logs)
	var read []string
	for _, c := range changes {
		read = append(read, c.Instance+" "+c.Change)
		assert.False(t, c.At.IsZero(), "%s %s has its time", c.Instance, c.Change)
	}
	assert.Equal(t, []string{"a lease taken", "a lease released", "b lease taken"}, read)
	assert.Equal(t, "b", leaseHolder(changes))
	require.Len(t, problems, 2, "%q", problems)
	assert.Contains(t, problems[0], "lease lost")
	assert.Contains(t, problems[0], "a time of no form")
	assert.Contains(t, problems[1], "instance c's")
	assert.Contains(t, problems[1], "nothing")
}

// gateSheet is the control socket's commands, as the environment prints
// them (R186).
const gateSheet = `  GET  /status                                     the browser's version and state, "open" or "pipe closed: <why>", the run directory and the tabs
  GET  /world                                      the instances, their pids, the leader, the lease's changes, the database, the key files, the deliveries
  GET  /events?tab=a&after=0&kind=record,log       a tab's events after a seq, of the kinds given; "last" is the seq to read after next
  GET  /polls?tab=a&after=0                        a tab's polls that changed after a seq, whole: time, status, gap since its document's last answer, order,
                                                   the regions changed and the writes to #announce; "last" as /events' is
  GET  /netlog                                     R185's check on the NetLog so far, provisional while Chrome runs
  POST /tab {"name":"p","url":"<a board>"}          a tab in a window of its own; each name once a run
  POST /close {"tab":"p"}
  POST /navigate {"tab":"a","url":"<a URL>"}        the stub's and the IdP's origins, and about:blank, alone
  POST /back {"tab":"a"}     POST /reload {"tab":"a"}
  POST /eval {"tab":"a","expr":"document.title"}    in the gate world, and recorded: it reads, and writes to no DOM sink
  POST /key {"tab":"a","key":"Tab"}                 Tab, Shift+Tab, Enter, Space, Escape, ArrowUp, ArrowDown, ArrowLeft, ArrowRight
  POST /type {"tab":"a","text":"..."}     POST /click {"tab":"a","selector":"#live-pause"}
  POST /emulate {"tab":"a","width":390,"height":844,"dpr":3,"mobile":true,"dark":false,"fontSize":32}   {"tab":"a"} alone clears it
  POST /hide {"tab":"a"}     POST /show {"tab":"a"}
  POST /screenshot {"tab":"a","name":"phone"}       screenshots/phone.png; each name once
  POST /replay {"tab":"a"}                          the next poll answered with an earlier 200 of a lower order (R190)
  POST /cookies/clear                               no body, so curl -X POST
  POST /end                                         no body, so curl -X POST; ends the run at once: the socket stops, the browser closes, the run's checks follow`

// gateBrowser runs the environment's own browser (Plan 9a-3), as
// harness.ChromeEnv names Chrome for Testing, and returns a channel that
// POST /end closes. Its run directory is a new one under gateOutEnv's
// (R187). It trusts the stub's and the IdP's presented leaves alone (R180),
// and may connect to their ports alone (R185). Its tab a signs in, in a
// window of its own, at the items list, through the IdP, which signs anyone
// in at once. The control socket, <run dir>/control.sock, serves g's world
// (R186). At the test's end the socket stops and its commands answer; both
// instances are continued and the database unpaused (R189); then the
// browser closes and the run's verdict follows (gateVerdict), before the
// instances stop.
func gateBrowser(t *testing.T, env *harness.Env, g *gateWorld) <-chan struct{} {
	t.Helper()
	out := os.Getenv(gateOutEnv)
	if out == "" {
		t.Fatalf("%s names Chrome for Testing, but %s names no directory for the run's evidence (R187)", harness.ChromeEnv, gateOutEnv)
	}
	if !filepath.IsAbs(out) {
		t.Fatalf("%s is %q, a relative path, which go test would read from the package's directory: name it absolutely", gateOutEnv, out)
	}
	require.NoError(t, os.MkdirAll(out, 0o700))
	runDir := filepath.Join(out, time.Now().UTC().Format("20060102T150405Z"))
	require.NoError(t, os.Mkdir(runDir, 0o700), "a run directory of the run's own")
	runDir, err := filepath.EvalSymlinks(runDir)
	require.NoError(t, err)

	web := env.Web()
	stub, idp := strings.TrimPrefix(web.URL(), "https://"), strings.TrimPrefix(web.IDP().URL, "https://")
	ca, err := os.ReadFile(env.CA())
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(ca), "the harness CA's PEM, %s", env.CA())
	pins, err := harness.PinsOf(pool, stub, idp)
	require.NoError(t, err, "the pins of the stub's and the IdP's leaves (R180)")
	c, err := harness.StartChrome(t, harness.ChromeOptions{
		Binary: os.Getenv(harness.ChromeEnv), Pins: pins, RunDir: runDir, Allowed: []string{stub, idp},
	})
	require.NoError(t, err)
	// The cleanups run in the reverse of their order here, all before the
	// instances' own: the socket's stop; putBack, whose failure the verdict
	// then counts; then the verdict, which closes the browser.
	t.Cleanup(func() { gateVerdict(t, c, runDir) })
	t.Cleanup(g.putBack)

	ctx, cancel := context.WithTimeout(t.Context(), gateSignInWait)
	defer cancel()
	tab, err := c.Tab(ctx, "a", true)
	require.NoError(t, err)
	require.NoError(t, tab.Navigate(ctx, web.URL()+"/items"), "tab a signs in through the IdP")
	var at string
	require.NoError(t, tab.Eval(ctx, "location.href", &at))
	require.Equal(t, web.URL()+"/items", at, "the IdP signed tab a in, and sign-in returned it to the items list")

	sock := filepath.Join(runDir, "control.sock")
	ended := make(chan struct{})
	var once sync.Once
	require.NoError(t, harness.ServeControl(t, c, harness.ControlOptions{
		Path: sock, Origins: []string{web.URL(), web.IDP().URL}, World: g.report,
		End: func() { once.Do(func() { close(ended) }) },
	}))
	t.Logf("The browser: %s, Chrome for Testing, headless, in a throwaway profile, trusting the stub's and the IdP's leaves alone (R180) "+
		"and kept to loopback (R185). Tab a is signed in as %s, in a window of its own, at %s/items.", c.Version(), gateSubject, web.URL())
	t.Logf("The run directory: %s: events.jsonl, bodies/, screenshots/, netlog.json, chrome.log and crash/ (R187).", runDir)
	t.Logf("The control socket: %s. Each command takes and answers JSON (R186), a GET as it is and a POST with -d, or -X POST with no body:\n"+
		"  curl -s --unix-socket %s http://gate/<command> [-d '<JSON>']\n%s",
		sock, sock, gateSheet)
	t.Logf("R189: kill -STOP <pid> freezes an instance, and kill -CONT <pid> continues it; GET /world names the leader and the standby, and each " +
		"change of the lease. At the end the socket stops, both instances are continued and the database unpaused, then the browser " +
		"closes and the run's verdict follows, before the instances stop.")
	return ended
}

// gateVerdict closes the browser, and gives the run's verdict (gateRun).
func gateVerdict(t *testing.T, c *harness.Chrome, runDir string) {
	r := gateRun{failed: t.Failed()}
	r.closeErr = c.Close()
	r.offLoopback, r.offLoopbackErr = c.OffLoopback()
	r.certs, r.certsErr = certificateLines(filepath.Join(runDir, "events.jsonl"))
	line, pass := r.verdict(runDir)
	if !pass {
		t.Error(line)
		return
	}
	t.Log(line)
}

// gateRun is what the run's verdict judges.
type gateRun struct {
	failed         bool     // the test had failed before the verdict
	closeErr       error    // the browser's Close's
	offLoopback    []string // R185's findings
	offLoopbackErr error    // why R185 could not judge
	certs          []string // R180's certificate events
	certsErr       error    // why events.jsonl could not be read
}

// verdict is the run's verdict line, and whether the run passed: it fails
// when the test had failed before it, as when the socket's stop gave up, a
// sign-in failed or the end could not put the world back; on Close's error,
// which it never drops; on each connect off loopback the complete NetLog
// shows (R185); and on each certificate event of events.jsonl, which holds
// every tab's, the closed ones' too (R180).
func (r *gateRun) verdict(runDir string) (line string, pass bool) {
	var failed []string
	if r.failed {
		failed = append(failed, "the run had failed before its verdict, as the errors above say")
	}
	if r.closeErr != nil {
		failed = append(failed, "the browser's Close: "+r.closeErr.Error())
	}
	if r.offLoopbackErr != nil {
		failed = append(failed, "R185: the NetLog could not be judged: "+r.offLoopbackErr.Error())
	}
	for _, f := range r.offLoopback {
		failed = append(failed, "R185: "+f)
	}
	if r.certsErr != nil {
		failed = append(failed, "R180: "+r.certsErr.Error())
	}
	for _, c := range r.certs {
		failed = append(failed, "R180: a navigation refused for its certificate: "+c)
	}
	if len(failed) > 0 {
		return fmt.Sprintf("The run's verdict: FAIL, on %d finding(s); the evidence is in %s:\n%s", len(failed), runDir, strings.Join(failed, "\n")), false
	}
	return fmt.Sprintf("The run's verdict: PASS. The browser closed cleanly, its complete NetLog shows no connect off loopback (R185), "+
		"and no navigation was refused for its certificate (R180). The evidence is in %s.", runDir), true
}

// certificateLines are the certificate events of an events.jsonl, each its
// tab, its seq and its detail.
func certificateLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read the events: %w", err)
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	var lines []string
	for n := 1; ; n++ {
		raw, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			var e struct {
				Tab    string          `json:"tab"`
				Seq    int             `json:"seq"`
				Kind   string          `json:"kind"`
				Detail json.RawMessage `json:"detail"`
			}
			if jerr := json.Unmarshal(raw, &e); jerr != nil {
				return lines, fmt.Errorf("%s's line %d is no event: %w", path, n, jerr)
			}
			if e.Kind == "certificate" {
				lines = append(lines, fmt.Sprintf("tab %s, event %d: %s", e.Tab, e.Seq, e.Detail))
			}
		}
		switch {
		case errors.Is(err, io.EOF):
			return lines, nil
		case err != nil:
			return lines, fmt.Errorf("read the events: %w", err)
		}
	}
}

// worldReport is the world as GET /world answers it (R186).
type worldReport struct {
	Now   time.Time `json:"now"`
	Until time.Time `json:"until"` // when the minutes end
	// LinkAt is from when the second item is linked into the alert item's
	// incident, with the first event after it.
	LinkAt time.Time `json:"linkAt"`
	// Leader is who holds the lease by its changes, "" while none does.
	Leader    string          `json:"leader"`
	Instances []worldInstance `json:"instances"`
	Lease     []leaseChange   `json:"lease"`
	// Problems are what the lease's reader could not read, which leaves
	// leader and lease in doubt.
	Problems   []string          `json:"problems,omitempty"`
	Database   string            `json:"database"` // its container, for docker pause and docker unpause
	Items      map[string]string `json:"items"`    // the boards: alert, second and person
	Deliveries []worldDelivery   `json:"deliveries"`
}

// worldInstance is an instance as GET /world shows it: its pid, its web
// listener, its session key's file, and its answer to /leaderz now, as the
// stub asks it before each request it forwards: "200 OK" while it leads, or
// why none came, as of a stopped instance.
type worldInstance struct {
	Name    string `json:"name"`
	PID     int    `json:"pid"`
	Web     string `json:"web"`
	KeyFile string `json:"keyFile"`
	Leaderz string `json:"leaderz"`
}

// leaseChange is a change of the lease an instance logged (R189): "lease
// taken", "lease released" or "lease lost", when, its epoch, and a loss's
// reason.
type leaseChange struct {
	Instance string    `json:"instance"`
	Change   string    `json:"change"`
	At       time.Time `json:"at"`
	Epoch    any       `json:"epoch,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

// report is the world as GET /world answers it (R186). It follows both
// instances' logs, as they stand, for the lease's changes (R189), and asks
// each instance's /leaderz.
func (g *gateWorld) report() any {
	changes, problems := g.leaseChanges()
	r := worldReport{
		Now: time.Now(), Until: g.until, LinkAt: g.linkAt, Leader: leaseHolder(changes), Lease: changes, Problems: problems,
		Database: g.database, Items: g.items, Instances: make([]worldInstance, len(g.names)),
	}
	var wg sync.WaitGroup
	for i, name := range g.names {
		addrs := g.instances[name].Addresses()
		r.Instances[i] = worldInstance{Name: name, PID: addrs.PID, Web: addrs.Web, KeyFile: g.keyFiles[name]}
		wg.Go(func() { r.Instances[i].Leaderz = leaderz(addrs.Web) })
	}
	wg.Wait()
	g.mu.Lock()
	r.Deliveries = slices.Clone(g.deliveries)
	g.mu.Unlock()
	return r
}

// leaseChanges are the changes of the lease both instances logged, in the
// order of their times (leaseChangesOf).
func (g *gateWorld) leaseChanges() (changes []leaseChange, problems []string) {
	logs := make(map[string][]map[string]any, len(g.names))
	for _, name := range g.names {
		logs[name] = g.instances[name].Log().Lines()
	}
	return leaseChangesOf(g.names, logs)
}

// leaseChangesOf reads the lease's changes from each instance's log lines,
// in the order of their times, and says what it could not read: a log that
// reads as nothing, as Log.Lines answers one it could not read, and a change
// whose time it cannot read, which it leaves out, as it cannot be placed.
func leaseChangesOf(names []string, logs map[string][]map[string]any) (changes []leaseChange, problems []string) {
	changes = []leaseChange{}
	for _, name := range names {
		if len(logs[name]) == 0 {
			problems = append(problems, fmt.Sprintf("instance %s's log reads as nothing: it could not be read, or serve wrote none", name))
			continue
		}
		for _, line := range logs[name] {
			change, _ := line["msg"].(string)
			if change != "lease taken" && change != "lease released" && change != "lease lost" {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["time"]))
			if err != nil {
				problems = append(problems, fmt.Sprintf("instance %s's %q line, left out: its time %q is not RFC 3339's", name, change, line["time"]))
				continue
			}
			reason, _ := line["reason"].(string)
			changes = append(changes, leaseChange{Instance: name, Change: change, At: at, Epoch: line["epoch"], Reason: reason})
		}
	}
	slices.SortStableFunc(changes, func(x, y leaseChange) int { return x.At.Compare(y.At) })
	return changes, problems
}

// leaseHolder is who holds the lease by its changes: the instance that took
// it last, until it released or lost it; "" while none holds it.
func leaseHolder(changes []leaseChange) string {
	holder := ""
	for _, c := range changes {
		switch {
		case c.Change == "lease taken":
			holder = c.Instance
		case c.Instance == holder:
			holder = ""
		}
	}
	return holder
}

// leaderz is the answer to GET /leaderz at an instance's web listener, or
// why none came within gateProbeTimeout.
func leaderz(addr string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gateProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/leaderz", http.NoBody)
	if err != nil {
		return err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "no answer: " + err.Error()
	}
	_ = resp.Body.Close()
	return resp.Status
}

// putBack continues both instances and unpauses the database (R189), so
// that a fault the walk left keeps neither from stopping: SIGCONT continues
// a stopped process, and does nothing to one that runs. An instance that
// has Exited, as the leader the walk stops has, is not signalled, as its pid
// may be another process's by now; the gate kills no parent, so that a serve
// that runs on is never taken for one that exited. It touches the
// environment's own database alone, by its id.
func (g *gateWorld) putBack() {
	for _, name := range g.names {
		in := g.instances[name]
		pid := in.Addresses().PID
		if pid <= 0 || in.Exited() {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGCONT); err != nil && !errors.Is(err, syscall.ESRCH) {
			g.t.Errorf("the end could not continue instance %s, pid %d: %v", name, pid, err)
		}
	}
	if g.database == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	paused, err := exec.CommandContext(ctx, "docker", "container", "inspect", "--format", "{{.State.Paused}}", "--", g.database).Output()
	if err != nil {
		g.t.Errorf("the end could not read whether the database %s is paused: %v", g.database, err)
		return
	}
	if strings.TrimSpace(string(paused)) != "true" {
		return
	}
	if out, err := exec.CommandContext(ctx, "docker", "unpause", g.database).CombinedOutput(); err != nil {
		g.t.Errorf("the end could not unpause the database %s: %v\n%s", g.database, err, out)
		return
	}
	g.t.Logf("%s: the end unpaused the database %s, which the walk left paused.", time.Now().Format(time.TimeOnly), g.database)
}
