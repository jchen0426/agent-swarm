//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The helpers the scenarios of Decision 17 share: they start a serve, open an
// item and wait for its stop, and read what a person reads of it: its view
// (C4), its records and calls (C5) and serve's log (C3).

// limited is the team of the scenarios, whose runs have at most rounds normal
// rounds, each of which lasts at most limit.
func limited(rounds int, limit time.Duration) harness.ConfigSpec {
	cfg := team(rounds)
	cfg.WorkType.RoundLimit = limit
	return cfg
}

// configured starts a harness with the servers, migrates its database and
// writes cfg, so that a scenario may act on the servers before serve starts.
//
//nolint:gocritic // hugeParam: the harness takes a scenario's specs by value.
func configured(t *testing.T, servers []harness.ServerSpec, cfg harness.ConfigSpec) (*harness.Env, *harness.Repo) {
	t.Helper()
	env := harness.New(t, harness.Options{Servers: servers})
	env.Migrate(t)
	return env, env.Config(t, cfg)
}

// leading runs instance a's serve on a deployment d until it leads and a
// revision is current.
//
//nolint:gocritic // hugeParam: the harness takes a scenario's specs by value.
func leading(t *testing.T, env *harness.Env, d harness.DeploymentSpec) *harness.Instance {
	t.Helper()
	a := env.Serve(t, "a", env.Deployment(t, "a", d))
	a.Log().WaitFor(t, "lease taken", nil, leadWait)
	a.Log().WaitFor(t, "revision current", nil, leadWait)
	return a
}

// investigate opens an item under key, and returns its view once its run has
// stopped and it awaits its close.
func investigate(t *testing.T, a *harness.Instance, key string) harness.ItemView {
	t.Helper()
	id := a.Admin().OpenItem(t, key, incident, openWait)
	return a.Admin().WaitItem(t, id, stopped, runWait)
}

// notHeld is the admin API's answer to an opening while the instance does not
// hold the lease (C4).
const notHeld = "this instance does not hold the lease"

// openWhenHeld opens an item under key, asking again while the instance does
// not hold the lease (409), as a serve answers between its take of the lease
// and its coordinator's takeover, and returns the first other answer. When
// none comes within timeout, it returns the last 409.
func openWhenHeld(t *testing.T, a *harness.Instance, key string, timeout time.Duration) (int, harness.Opened) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		code, opened := a.Admin().Open(t, key, incident)
		if code != http.StatusConflict || opened.Error != notHeld || time.Now().After(deadline) {
			return code, opened
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// requireRounds fails the test unless a run has n rounds, and shows the
// item's view as JSON when it has not: a round's report is raw JSON, which
// testify shows as bytes.
func requireRounds(t *testing.T, v *harness.ItemView, n int, why ...any) {
	t.Helper()
	if len(v.Rounds) != n {
		view, err := json.Marshal(v)
		require.NoError(t, err)
		require.Failf(t, "the run has another number of rounds", "%d rounds, not %d; the item's view: %s\n%s",
			len(v.Rounds), n, view, fmt.Sprint(why...))
	}
}

// notQuiet asserts what "not quiet" means of a run that stopped: none of its
// rounds is quiet, and it did not stop quiet.
func notQuiet(t *testing.T, v *harness.ItemView) {
	t.Helper()
	require.NotEmpty(t, v.Rounds, "the run had rounds")
	for _, r := range v.Rounds {
		assert.False(t, r.Quiet, "round %d is quiet: %+v", r.Round, r.Evidence)
	}
	assert.NotEqual(t, "quiet", v.Stop)
}

// ended is a round_ended record's body as inspect prints it (C5): how each
// invited agent's turn ended, the day's spend the round's end read, and the
// agents removed after the round.
type ended struct {
	Round    int               `json:"round"`
	Turns    map[string]string `json:"turns"`
	DaySpent int64             `json:"daySpent"`
	Removed  []string          `json:"removed"`
}

// roundsEnded returns an item's round_ended records, in order.
func roundsEnded(t *testing.T, a *harness.Instance, item string) []ended {
	t.Helper()
	var out []ended
	for _, line := range a.Inspect(t, "item", item) {
		if line["kind"] != "round_ended" {
			continue
		}
		raw, err := json.Marshal(line["body"])
		require.NoError(t, err)
		var body struct {
			RoundEnded *ended `json:"roundEnded"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		require.NotNil(t, body.RoundEnded, "a round_ended record holds its round's end: %s", raw)
		out = append(out, *body.RoundEnded)
	}
	return out
}

// callLine is a run's model or tool call as inspect prints it (C5).
type callLine struct {
	Call    string `json:"call"`
	Agent   string `json:"agent"`
	Round   int    `json:"round"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Failure string `json:"failure"`
	Reason  string `json:"reason"`
}

// callsOf returns a run's calls of a kind, model or tool, in the order they
// began.
func callsOf(t *testing.T, a *harness.Instance, run, kind string) []callLine {
	t.Helper()
	var out []callLine
	for _, line := range a.Inspect(t, "calls", run) {
		raw, err := json.Marshal(line)
		require.NoError(t, err)
		var c callLine
		require.NoError(t, json.Unmarshal(raw, &c))
		if c.Call == kind {
			out = append(out, c)
		}
	}
	return out
}

// callsNamed returns a run's tool calls of a name, server/tool or a board
// tool's, in the order they began.
func callsNamed(t *testing.T, a *harness.Instance, run, name string) []callLine {
	t.Helper()
	var out []callLine
	for _, c := range callsOf(t, a, run, "tool") {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// vendorFailure is the vendor's answer to a request it fails: the status, and
// an error body of the Messages API's.
func vendorFailure(status int, kind, message string) harness.VendorReply {
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
	return harness.Status(status, string(body))
}

// The vendor's failures the scenarios script.
var (
	overloaded   = vendorFailure(529, "overloaded_error", "Overloaded")
	apiError     = vendorFailure(http.StatusInternalServerError, "api_error", "Internal server error")
	unauthorized = vendorFailure(http.StatusUnauthorized, "authentication_error", "invalid x-api-key")
	rateLimited  = vendorFailure(http.StatusTooManyRequests, "rate_limit_error", "Number of requests has exceeded your rate limit")
)

// promptRound reads the round's number from a turn's prompt, its first
// message.
var promptRound = regexp.MustCompile(`^Round (\d+) `)

// roundOf returns the round whose turn sent a request, or 0 when its first
// message is no round's prompt.
func roundOf(r *harness.VendorRequest) int {
	if len(r.Messages) == 0 || len(r.Messages[0].Blocks) == 0 {
		return 0
	}
	m := promptRound.FindStringSubmatch(r.Messages[0].Blocks[0].Text)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// life is a turn's life as serve logged it: when it started, when it exited,
// and the class it exited with.
type life struct {
	started, exited time.Time
	class           string
}

// lives returns the lives of the turns serve logged, by turn.
func lives(t *testing.T, a *harness.Instance) map[string]*life {
	t.Helper()
	out := map[string]*life{}
	for _, line := range a.Log().Lines() {
		msg := line["msg"]
		if msg != "turn started" && msg != "turn exited" {
			continue
		}
		turn, _ := line["turn"].(string)
		at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["time"]))
		require.NoError(t, err)
		l := out[turn]
		if l == nil {
			l = &life{}
			out[turn] = l
		}
		if msg == "turn started" {
			l.started = at
			continue
		}
		l.exited = at
		l.class, _ = line["class"].(string)
	}
	return out
}
