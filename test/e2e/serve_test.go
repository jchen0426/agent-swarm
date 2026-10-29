//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The first scenarios of Decision 17 (Plan 7d-1): agentswarm serve runs the
// coordinator, the leader, both gateways and the process runner in one
// process, under the harness's parent, against the scripted vendor and a
// scripted MCP server; the scenarios read what a person would, the item's
// view, serve's log, inspect and the audit chain.

// logs is the scripted server the agents read. It has two tools, so that two
// agents' evidence comes from two sources, as a hypothesis needs to converge.
var logs = harness.ServerSpec{Name: "logs", Tools: []harness.ToolSpec{
	{Name: "search", Description: "Search the logs.", InputSchema: `{"type":"object","properties":{"q":{"type":"string"}}}`},
	{Name: "deploys", Description: "List the recent deploys.", InputSchema: `{"type":"object","properties":{}}`},
}}

// The reads' outputs, which the agents quote.
const (
	searchOutput  = "checkout answers 500 since deploy 42 rolled out a bad release"
	deploysOutput = "deploy 42 of checkout at 10:02 was a bad release"
	quote         = "bad release"
)

// team is the configuration of the scenarios: agents alpha and beta, each
// granted the logs server, and the incident type, whose budget allows rounds.
func team(rounds int) harness.ConfigSpec {
	return harness.ConfigSpec{
		Agents: []harness.AgentSpec{
			{Name: "alpha", Servers: []string{"logs"}},
			{Name: "beta", Servers: []string{"logs"}},
		},
		WorkType: harness.WorkTypeSpec{Rounds: rounds},
	}
}

// quick is the deployment of the scenarios: a round's end waits a second for
// its write calls, which is the gateways' record timeout.
var quick = harness.DeploymentSpec{Timing: harness.Timing{
	Coordinator: harness.CoordinatorTiming{Grace: time.Second},
	LLM:         harness.LLMTiming{RecordTimeout: time.Second},
	MCP:         harness.MCPTiming{RecordTimeout: time.Second},
}}

// incident is the opening of the scenarios' items.
var incident = harness.Opening{Type: "incident", Title: "checkout fails", Routing: map[string]string{"service": "checkout"}}

// The waits of the scenarios: for serve to lead, for an opening, and for a
// run to come to the state a scenario waits for.
const (
	leadWait = time.Minute
	openWait = time.Minute
	runWait  = 3 * time.Minute
)

// serving starts a harness with the logs server, migrates its database,
// writes cfg and a deployment d for instance a, and runs a's serve until it
// leads and a revision is current.
//
//nolint:gocritic // hugeParam: the harness takes a scenario's specs by value.
func serving(t *testing.T, cfg harness.ConfigSpec, d harness.DeploymentSpec) (*harness.Env, *harness.Instance) {
	t.Helper()
	env, _ := configured(t, []harness.ServerSpec{logs}, cfg)
	return env, leading(t, env, d)
}

// reading scripts the logs server's tools to answer what the agents quote.
func reading(env *harness.Env) {
	env.Server("logs").Script("search", func(*harness.ToolCall) harness.ToolReply { return harness.Result(searchOutput) })
	env.Server("logs").Script("deploys", func(*harness.ToolCall) harness.ToolReply { return harness.Result(deploysOutput) })
}

// stopped says that an item's run stopped and the item awaits its close,
// which it records last, after its board's stop entry.
func stopped(v *harness.ItemView) bool { return v.Stop != "" && v.State == "awaiting_close" }

// The texts the gateways' answers carry, which the scripted model reads.
var (
	calledAs  = regexp.MustCompile(`call_id: (\S+)`)
	postedAs  = regexp.MustCompile(`posted evidence (\S+)`)
	reads     = map[string]string{"alpha": "logs__search", "beta": "logs__deploys"}
	readsWith = map[string]any{"alpha": map[string]any{"q": "error"}, "beta": map[string]any{}}
)

// lastText returns the text of the last message of a request, a user's: its
// tool results' text, or its text; "" for the first request of a turn, whose
// last message is the round's prompt.
func lastText(r *harness.VendorRequest) string {
	if len(r.Messages) < 2 {
		return ""
	}
	var texts []string
	for _, b := range r.Messages[len(r.Messages)-1].Blocks {
		texts = append(texts, b.Text)
	}
	return strings.Join(texts, "\n")
}

// investigating is the scripted model of a run that converges. In a normal
// round each agent reads the logs with a tool of its own, posts what it read
// as evidence, posts the same hypothesis citing it, and ends its turn: two
// agents and two sources, so the hypothesis converges. In the wrap-up round,
// which offers no post_hypothesis, it ends its turn at once.
func investigating(r *harness.VendorRequest) harness.VendorReply {
	if !slices.Contains(r.Tools, "post_hypothesis") {
		return harness.EndTurn()
	}
	last := lastText(r)
	switch {
	case last == "":
		return harness.ToolUse(reads[r.Agent], readsWith[r.Agent])
	case calledAs.MatchString(last):
		return harness.ToolUse("post_evidence", map[string]any{
			"claim": "Checkout fails since a bad release.", "call_id": calledAs.FindStringSubmatch(last)[1], "quote": quote,
		})
	case postedAs.MatchString(last):
		return harness.ToolUse("post_hypothesis", map[string]any{
			"key":       map[string]string{"service": "checkout", "failureMode": "bad-release"},
			"statement": "A bad release of checkout makes it fail.", "evidence_ids": []string{postedAs.FindStringSubmatch(last)[1]},
		})
	}
	return harness.EndTurn()
}

// converging runs a converging run's item to its stop on a fresh serve, and
// returns the harness, the serve and the item's view.
func converging(t *testing.T) (*harness.Env, *harness.Instance, harness.ItemView) {
	t.Helper()
	env, a := serving(t, team(0), quick)
	reading(env)
	env.Vendor().Script(investigating)

	id := a.Admin().OpenItem(t, "converging", incident, openWait)
	return env, a, a.Admin().WaitItem(t, id, stopped, runWait)
}

func TestServeRefusesAPostgresOlderThan17(t *testing.T) {
	env := harness.New(t, harness.Options{Postgres: "postgres:16-alpine", Servers: []harness.ServerSpec{logs}})
	env.Config(t, team(0))

	a := env.Serve(t, "a", env.Deployment(t, "a", quick))

	assert.Equal(t, 1, a.Wait(t, time.Minute), "serve exits 1")
	assert.Zero(t, a.Addresses(), "serve bound no listener")
	assert.Contains(t, a.Output(t), "PostgreSQL 16.", "serve says the server's version")
}

// Serve writes its addresses and becomes ready (C2, C4). Under the process
// runner each listener is on the loopback, and serve warns that its turns run
// unsandboxed; under Docker the agents' listeners are the sockets in the
// instance's sockets directory, as serve resolved it, its status shows the
// runner sandboxed, its engine answering and no image missing, as a list on
// the wire, never null, and serve gives no such warning.
func TestServeWritesItsAddressesAndBecomesReady(t *testing.T) {
	env, a := serving(t, team(0), quick)

	addrs := a.Addresses()
	require.Positive(t, addrs.PID)
	require.NoError(t, syscall.Kill(addrs.PID, 0), "the process id is serve's")
	listeners := map[string]string{"mcp": addrs.MCP, "llm": addrs.LLM, "admin": addrs.Admin}
	if env.Runner() == "docker" {
		dir, err := filepath.EvalSymlinks(a.SocketsDir())
		require.NoError(t, err, "the instance's sockets directory")
		assert.Equal(t, "unix:"+filepath.Join(dir, "mcp.sock"), addrs.MCP)
		assert.Equal(t, "unix:"+filepath.Join(dir, "llm.sock"), addrs.LLM)
		listeners = map[string]string{"admin": addrs.Admin}
	}
	for name, addr := range listeners {
		host, port, err := net.SplitHostPort(addr)
		require.NoError(t, err, name)
		assert.Equal(t, "127.0.0.1", host, name)
		assert.NotEqual(t, "0", port, "%s: the port bound, not the one the deployment asked for", name)
	}
	code, body := a.Admin().Healthz(t)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok", body)
	code, body = a.Admin().Readyz(t)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ready", body)
	// The leader logs its take a moment before it publishes the holding.
	status := a.Admin().WaitStatus(t, func(s *harness.InstanceStatus) bool { return s.Leading }, leadWait)
	assert.Positive(t, status.Epoch)
	assert.Equal(t, "compatible", status.Schema)
	assert.False(t, status.Draining)
	if env.Runner() == "docker" {
		assert.Equal(t, harness.RunnerStatus{Kind: "docker", Sandboxed: true, Engine: "ok", MissingImages: []string{}}, status.Runner)
		assert.Empty(t, logged(a.Log().Lines(), "turns run unsandboxed"), "a serve whose turns run in sandboxes does not say they run unsandboxed")
	} else {
		assert.Equal(t, harness.RunnerStatus{Kind: "process"}, status.Runner, "no engine and no missing images on the wire")
		unsandboxed := a.Log().WaitFor(t, "turns run unsandboxed", nil, time.Second)
		assert.Equal(t, "WARN", unsandboxed["level"])
		assert.Equal(t, "process", unsandboxed["runner"])
	}
	started := a.Log().WaitFor(t, "serve started", nil, time.Second)
	assert.Equal(t, "a", started["holder"])
	assert.Equal(t, "INFO", started["level"])
	assert.Positive(t, started["schemaLevel"], "the build's schema level")
	taken := a.Log().WaitFor(t, "lease taken", nil, time.Second)
	assert.InDelta(t, float64(status.Epoch), taken["epoch"], 0)
	assert.NotEmpty(t, taken["wall"])
}

// Scenario 1: a converged hypothesis gets its wrap-up round, which the agents
// end by end_turn, and the run stops converged; the chain verifies (R22).
func TestAConvergedRunStopsConvergedAfterItsWrapUpRound(t *testing.T) {
	env, _, v := converging(t)

	assert.Equal(t, "converged", v.Stop)
	require.Len(t, v.Rounds, 2, "the round that converged, then its wrap-up round")
	assert.False(t, v.Rounds[0].WrapUp)
	assert.False(t, v.Rounds[0].Quiet, "a round with entries is not quiet")
	wrapUp := v.Rounds[1]
	assert.True(t, wrapUp.WrapUp)
	assert.Equal(t, "turns", wrapUp.Ended)
	assert.ElementsMatch(t, []string{"alpha", "beta"}, wrapUp.Evidence.TurnEnds, "every agent ended its turn in the wrap-up round")
	assert.Zero(t, wrapUp.Evidence.FailedModelCalls)
	assert.Zero(t, wrapUp.Evidence.FailedToolCalls)
	assert.True(t, wrapUp.Evidence.Settled)
	assert.Equal(t, []string{"store_unlocked"}, env.AuditVerify(t), "the chain verifies")
}

// Scenario 2: the agents answer, post nothing and call end_turn, and the run
// stops quiet after one round.
func TestARunWhoseAgentsPostNothingStopsQuietAfterOneRound(t *testing.T) {
	env, a := serving(t, team(0), quick)
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn() })

	id := a.Admin().OpenItem(t, "quiet", incident, openWait)
	v := a.Admin().WaitItem(t, id, stopped, runWait)

	assert.Equal(t, "quiet", v.Stop)
	require.Len(t, v.Rounds, 1)
	r := v.Rounds[0]
	assert.True(t, r.Quiet)
	assert.Equal(t, "turns", r.Ended)
	assert.ElementsMatch(t, []string{"alpha", "beta"}, r.Evidence.TurnEnds)
	assert.Zero(t, r.Evidence.FailedModelCalls)
	assert.Zero(t, r.Evidence.FailedToolCalls)
	assert.True(t, r.Evidence.Settled)
}

// nudged answers alpha's first request of a turn without a tool, and then,
// after the nudge, as follows says; beta ends its turn at once.
func nudged(follows func() harness.VendorReply) func(*harness.VendorRequest) harness.VendorReply {
	return func(r *harness.VendorRequest) harness.VendorReply {
		switch {
		case r.Agent != "alpha":
			return harness.EndTurn()
		case lastText(r) == nudge:
			return follows()
		}
		return harness.Text("Nothing in the logs points anywhere.")
	}
}

// nudges counts, by agent, the requests that carried the nudge as their last
// message: one for each turn the runtime nudged.
func nudges(requests []harness.VendorRequest) map[string]int {
	out := map[string]int{}
	for i := range requests {
		if lastText(&requests[i]) == nudge {
			out[requests[i].Agent]++
		}
	}
	return out
}

// Scenario 3: half the turns answer without a tool; the nudge brings them to
// end_turn, and the round is quiet.
func TestANudgedTurnReachesEndTurn(t *testing.T) {
	env, a := serving(t, team(0), quick)
	env.Vendor().Script(nudged(harness.EndTurn))

	id := a.Admin().OpenItem(t, "nudged", incident, openWait)
	v := a.Admin().WaitItem(t, id, stopped, runWait)

	assert.Equal(t, "quiet", v.Stop)
	require.Len(t, v.Rounds, 1)
	assert.True(t, v.Rounds[0].Quiet)
	assert.ElementsMatch(t, []string{"alpha", "beta"}, v.Rounds[0].Evidence.TurnEnds, "the nudged turn reached end_turn")
	assert.Equal(t, map[string]int{"alpha": 1}, nudges(env.Vendor().Requests("a")), "the nudge, once, in the turn that answered without a tool")
}

// Scenario 3: a model that ignores the nudge ends its turn without a turn
// end, which leaves its round not quiet.
func TestAModelThatIgnoresTheNudgeLeavesItsRoundNotQuiet(t *testing.T) {
	env, a := serving(t, team(1), quick)
	env.Vendor().Script(nudged(func() harness.VendorReply { return harness.Text("Still nothing.") }))

	id := a.Admin().OpenItem(t, "ignored", incident, openWait)
	v := a.Admin().WaitItem(t, id, stopped, runWait)

	require.Len(t, v.Rounds, 1)
	assert.False(t, v.Rounds[0].Quiet, "alpha has no turn end")
	assert.Equal(t, []string{"beta"}, v.Rounds[0].Evidence.TurnEnds)
	assert.NotEqual(t, "quiet", v.Stop)
	assert.Equal(t, map[string]int{"alpha": 1}, nudges(env.Vendor().Requests("a")), "the nudge, once, and no second one")
}

// sessionToken is the form of a session's token at either gateway.
var sessionToken = regexp.MustCompile(`asw[ml]_[A-Za-z0-9]{43}`)

// textOf returns what a value of the test prints as JSON. A value that holds
// bytes prints them in base64, where no secret shows: the requests a server
// received are read with requestsText.
func textOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// requestsText returns requests as text: each one's line, its method and its
// path with its query, then its headers, then its body.
func requestsText[R harness.VendorRequest | harness.MCPRequest](requests []R, parts func(*R) (line string, header http.Header, body []byte)) string {
	var b strings.Builder
	for i := range requests {
		line, header, body := parts(&requests[i])
		b.WriteString(line + "\n")
		for name, values := range header {
			b.WriteString(name + ": " + strings.Join(values, ", ") + "\n")
		}
		b.Write(body)
		b.WriteString("\n")
	}
	return b.String()
}

// requestLine is a request's method, and its path with its query.
func requestLine(method, path, query string) string {
	if query != "" {
		path += "?" + query
	}
	return method + " " + path
}

// quietFor is how long serve's log must not grow before the vendor answers
// a request that scenario 14 holds under Docker (R29).
const quietFor = 200 * time.Millisecond

// convergingRead runs converging's run under Docker, and reads, while each
// turn runs, docker inspect of the turn's container (R29). The vendor cannot
// tell which container a request comes from, each round starts new
// containers, and a turn may log its start after its first request, so the
// vendor holds every request until the containers read are the turns serve
// logged as started and serve's log has not grown for quietFor, or until the
// test ends (hold), and then answers as investigating does. The reader runs
// on the test's goroutine, where ContainerConfig may fail the test, and reads
// each container once, until the run stops. It returns the item's view, and
// each container's configuration by its id: "" for one removed before it was
// read.
func convergingRead(t *testing.T, env *harness.Env, a *harness.Instance) (v harness.ItemView, configs map[string]string) {
	t.Helper()
	var mu sync.Mutex
	configs = map[string]string{}
	read := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(configs)
	}
	ctx := t.Context()
	env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
		hold(ctx, runWait, func() int { return len(a.Log().Lines()) }, func() bool { return read() == len(a.TurnsStarted()) })
		return investigating(r)
	})
	id := a.Admin().OpenItem(t, "converging", incident, openWait)
	until := time.Now().Add(runWait)
	for {
		for _, s := range a.TurnsStarted() {
			mu.Lock()
			_, done := configs[s.Container]
			mu.Unlock()
			if !done {
				config := harness.ContainerConfig(t, s.Container)
				mu.Lock()
				configs[s.Container] = config
				mu.Unlock()
			}
		}
		if code, view := a.Admin().Item(t, id); code == http.StatusOK && stopped(&view) {
			return view, configs
		}
		require.True(t, time.Now().Before(until), "the run did not stop within %v", runWait)
		time.Sleep(25 * time.Millisecond)
	}
}

// hold holds a request that scenario 14's vendor answers under Docker (R29):
// it returns once ready holds and logged, the length of serve's log, has
// not changed for quietFor; once wait has passed; or once ctx, the test's,
// is done, so that a held answer ends with its test.
func hold(ctx context.Context, wait time.Duration, logged func() int, ready func() bool) {
	lines, since := -1, time.Now()
	for until := time.Now().Add(wait); ctx.Err() == nil && time.Now().Before(until); time.Sleep(20 * time.Millisecond) {
		if n := logged(); n != lines {
			lines, since = n, time.Now()
			continue
		}
		if time.Since(since) >= quietFor && ready() {
			return
		}
	}
}

// R29's hold ends with its test: a request that scenario 14 holds is let go
// once the test's context is done, though nothing it waits for ever comes, so
// that a failed scenario's held answers do not poll serve's log into the
// tests after it.
func TestAHeldRequestIsLetGoWhenItsTestEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()

	hold(ctx, 5*time.Second, func() int { return 0 }, func() bool { return false })

	assert.Less(t, time.Since(start), time.Second, "the hold ends with its test, not at its bound")
}

// ranWhenRead reports whether a container's docker inspect shows it running
// when it was read.
func ranWhenRead(t *testing.T, config string) bool {
	t.Helper()
	var inspected []struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	require.NoError(t, json.Unmarshal([]byte(config), &inspected), "docker inspect's output")
	require.Len(t, inspected, 1)
	return inspected[0].State.Running
}

// Scenario 14: no session token appears in serve's output, the turns'
// files, anything the vendor or the MCP server received, the request's line
// and headers included, or inspect's output for the item, its board and its
// calls. Nor do the deployment's secrets: the vendor's key and the MCP
// server's credential reach their own server alone, in a request's header,
// and the admin token neither. Under Docker, neither appears in any turn
// container's configuration, its command, environment and labels among it,
// read while the turn ran: a token travels on the turn's standard input
// alone.
func TestNoSessionTokenLeavesThePlatform(t *testing.T) {
	require.Len(t, sessionToken.FindAllString("aswm_"+strings.Repeat("A", 43)+" aswl_"+strings.Repeat("9", 43), -1), 2,
		"the pattern finds a token of either gateway")
	env, a := serving(t, team(0), quick)
	reading(env)
	var (
		v       harness.ItemView
		configs map[string]string
	)
	if env.Runner() == "docker" {
		v, configs = convergingRead(t, env, a)
	} else {
		env.Vendor().Script(investigating)
		v = a.Admin().WaitItem(t, a.Admin().OpenItem(t, "converging", incident, openWait), stopped, runWait)
	}
	require.Equal(t, "converged", v.Stop, "the run used both gateways")
	vendor, server := env.Vendor().Requests("a"), env.Server("logs").Requests("a")
	require.NotEmpty(t, vendor)
	require.NotEmpty(t, server)
	turns := a.TurnOutputs(t)
	require.NotEmpty(t, turns)
	run := runOf(t, env, v.ID)
	vendorKey, credential, adminToken := env.Vendor().Key("a"), env.Server("logs").Credential("a"), a.Admin().Token()
	require.NotEmpty(t, adminToken)

	vendorText := requestsText(env.Vendor().All(), func(r *harness.VendorRequest) (string, http.Header, []byte) {
		return requestLine(r.Method, r.Path, r.Query), r.Header, r.Body
	})
	serverText := requestsText(env.Server("logs").All(), func(r *harness.MCPRequest) (string, http.Header, []byte) {
		return requestLine(r.Method, r.Path, r.Query), r.Header, r.Body
	})
	require.Contains(t, vendorText, "POST /v1/messages\n", "the vendor's text holds its requests' lines")
	require.Contains(t, vendorText, vendorKey, "their headers")
	require.Contains(t, vendorText, "post_hypothesis", "and their bodies")
	require.Contains(t, serverText, "POST /mcp\n", "the server's text holds its requests' lines")
	require.Contains(t, serverText, credential, "their headers")
	require.Contains(t, serverText, "tools/call", "and their bodies")
	var inspected []map[string]any
	for _, args := range [][]string{{"item", v.ID}, {"board", run}, {"calls", run}} {
		lines := a.Inspect(t, args...)
		require.NotEmpty(t, lines, args)
		inspected = append(inspected, lines...)
	}
	output, turnsText, inspectText := a.Output(t), textOf(t, turns), textOf(t, inspected)

	assert.Empty(t, sessionToken.FindAllString(output, -1), "serve's output")
	assert.Empty(t, sessionToken.FindAllString(turnsText, -1), "the turns' files")
	assert.Empty(t, sessionToken.FindAllString(vendorText, -1), "the vendor's requests")
	assert.Empty(t, sessionToken.FindAllString(serverText, -1), "the MCP server's requests")
	assert.Empty(t, sessionToken.FindAllString(inspectText, -1), "inspect's output")
	for what, secret := range map[string]string{"the vendor's key": vendorKey, "the MCP server's credential": credential, "the admin token": adminToken} {
		assert.NotContains(t, output, secret, "%s in serve's output", what)
		assert.NotContains(t, turnsText, secret, "%s in the turns' files", what)
		assert.NotContains(t, inspectText, secret, "%s in inspect's output", what)
	}
	assert.NotContains(t, serverText, vendorKey, "the vendor's key at the MCP server")
	assert.NotContains(t, vendorText, credential, "the MCP server's credential at the vendor")
	assert.NotContains(t, vendorText, adminToken, "the admin token at the vendor")
	assert.NotContains(t, serverText, adminToken, "the admin token at the MCP server")
	if env.Runner() != "docker" {
		return
	}

	started := a.TurnsStarted()
	require.NotEmpty(t, started)
	var configsText strings.Builder
	for _, s := range started {
		config := configs[s.Container]
		require.NotEmpty(t, config, "turn %s's container was read before it was removed", s.Turn)
		assert.True(t, ranWhenRead(t, config), "turn %s's container was read while the turn ran", s.Turn)
		configsText.WriteString(config + "\n")
	}
	require.Contains(t, configsText.String(), `"agentswarm.turn"`, "the containers' text holds their configurations")
	assert.Empty(t, sessionToken.FindAllString(configsText.String(), -1), "the turn containers' configurations")
	for what, secret := range map[string]string{"the vendor's key": vendorKey, "the MCP server's credential": credential, "the admin token": adminToken} {
		assert.NotContains(t, configsText.String(), secret, "%s in a turn container's configuration", what)
	}
}

// runOf returns the run of an item, which its records name.
func runOf(t *testing.T, env *harness.Env, item string) string {
	t.Helper()
	run := env.Psql(t, "SELECT DISTINCT run_id FROM work_records WHERE item_id = '"+item+"' AND run_id <> ''")
	require.NotEmpty(t, run)
	require.NotContains(t, run, "\n", "an item has one run")
	return run
}

// Scenario 16: killing the harness's parent leaves nothing of the run: serve
// and every turn it started are gone, a turn's process under the process
// runner, and under Docker its container, which is no longer running. The
// runtime's turns end once their input does; turns that ignore it, and
// SIGTERM, end only by serve's kill.
func TestKillingTheHarnessParentLeavesNothingRunning(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stubborn bool
	}{
		{name: "the runtime's turns, waiting on the vendor"},
		{name: "turns that ignore their input and SIGTERM", stubborn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := quick
			d.Stubborn = tc.stubborn
			env, a := serving(t, team(0), d)
			env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.Hang() })
			a.Admin().OpenItem(t, "orphans", incident, openWait)
			running, serve := waitForTurns(t, a, 2), a.Addresses().PID
			for _, s := range running {
				require.False(t, harness.Gone(t, s), "turn %s runs", s.Turn)
			}
			require.False(t, processGone(serve), "serve runs")

			a.KillParent(t)

			// Once serve is gone its log is whole, so every turn it started,
			// a turn started after the read above included, is named in it.
			assert.True(t, within(5*time.Second, func() bool { return processGone(serve) && everyGone(t, a.TurnsStarted()) }),
				"serve and every turn it logged as started are gone")
			assert.Subset(t, a.TurnsStarted(), running, "the turns that ran before the kill are among them")
		})
	}
}

// A second signal, during the drain the first began, makes serve kill every
// turn and exit 1 at once: under the process runner each turn's process
// group, and under Docker each turn's container, whose removals serve waits
// for at most a second.
func TestASecondSignalKillsEveryTurnAndExitsAtOnce(t *testing.T) {
	env, a := serving(t, team(0), quick)
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn().After(time.Minute) })
	a.Admin().OpenItem(t, "signals", incident, openWait)
	waitForTurns(t, a, 2)
	require.Eventually(t, func() bool { return len(env.Vendor().Requests("a")) >= 2 }, runWait, 50*time.Millisecond,
		"the turns wait on the vendor")
	a.Signal(t, syscall.SIGTERM)
	a.Log().WaitFor(t, "draining", nil, 10*time.Second)

	a.Signal(t, syscall.SIGTERM)

	assert.Equal(t, 1, a.Wait(t, 2*time.Second), "serve exits 1 at once")
	started := a.TurnsStarted()
	require.NotEmpty(t, started)
	for _, s := range started {
		assert.True(t, harness.Gone(t, s), "turn %s is gone", s.Turn)
	}
	a.Log().WaitFor(t, "serve stopped", func(l map[string]any) bool { return l["code"] == float64(1) }, time.Second)
}

// drainHeld is how long the vendor holds the answers that keep a drain open:
// long enough for a test to read the admin API while serve drains.
const drainHeld = 8 * time.Second

// Serve drains under the lease it holds (Decision 6). While the drain waits
// for a model call in flight, the admin API answers its reads and says that
// serve drains, and opens no item, since the leader has fenced its
// coordinator; once the call has ended, serve releases the lease, closes its
// listeners and exits 0.
func TestADrainingServeAnswersItsReadsAndOpensNoItem(t *testing.T) {
	env, a := serving(t, team(0), quick)
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn().After(drainHeld) })
	a.Admin().OpenItem(t, "before", incident, openWait)
	waitForTurns(t, a, 2)
	require.Eventually(t, func() bool { return len(env.Vendor().Requests("a")) >= 2 }, runWait, 50*time.Millisecond,
		"the turns wait on the vendor")
	a.Signal(t, syscall.SIGTERM)
	a.Log().WaitFor(t, "draining", nil, 10*time.Second)

	ready, why := a.Admin().Readyz(t)
	code, status := a.Admin().Status(t)
	opening, opened := a.Admin().Open(t, "during", incident)

	assert.Equal(t, http.StatusServiceUnavailable, ready)
	assert.Equal(t, "draining", why)
	require.Equal(t, http.StatusOK, code)
	assert.True(t, status.Draining, "the status says serve drains")
	assert.Equal(t, http.StatusConflict, opening, "no item opens while serve drains")
	assert.Equal(t, "this instance is draining; open the item on the instance that leads next", opened.Error)
	assert.Equal(t, 0, a.Wait(t, time.Minute), "serve exits 0 once it has drained")
	a.Log().WaitFor(t, "lease released", nil, time.Second)
}

func TestInspectPrintsALaterVersionRaw(t *testing.T) {
	env, a := serving(t, team(0), quick)
	id := a.Admin().OpenItem(t, "current", incident, openWait)
	env.Psql(t, `INSERT INTO work_records (tenant_id, item_id, seq, id, incident_id, run_id, kind, body, recorded_at)
VALUES ('`+env.Tenant()+`', 'later', 1, 'later-1', 'later', '', 'item_opened', '{"v": 2, "opened": {"shape": "of a later build"}}', clock_timestamp())`)

	later := a.Inspect(t, "item", "later")
	current := a.Inspect(t, "item", id)

	require.Len(t, later, 1)
	assert.InDelta(t, 1, later[0]["seq"], 0)
	assert.Equal(t, "item_opened", later[0]["kind"])
	assert.InDelta(t, 2, later[0]["v"], 0)
	assert.NotEmpty(t, later[0]["at"])
	assert.NotContains(t, later[0], "body", "a body of a later version is not read")
	require.IsType(t, "", later[0]["raw"])
	assert.JSONEq(t, `{"v": 2, "opened": {"shape": "of a later build"}}`, later[0]["raw"].(string), "the body as stored")
	require.NotEmpty(t, current)
	assert.Equal(t, "item_opened", current[0]["kind"])
	assert.InDelta(t, 1, current[0]["v"], 0)
	assert.Contains(t, current[0], "body", "a body of this version is printed")
	assert.NotContains(t, current[0], "raw")
}
