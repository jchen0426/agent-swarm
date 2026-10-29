package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// revisionOfRules is a revision of nothing but redaction rules that apply
// always, each named and matching its pattern.
func revisionOfRules(t *testing.T, patterns map[string]string) *config.Revision {
	t.Helper()
	files := map[string][]byte{}
	for name, pattern := range patterns {
		files["redaction-rules/"+name+".yaml"] = fmt.Appendf(nil, "kind: RedactionRule\nmetadata: {name: %s, owner: security@example.com}\nspec:\n  always: true\n  patterns: ['%s']\n", name, pattern)
	}
	fsys, err := memfs.New(files)
	require.NoError(t, err)
	cc, err := celeval.New()
	require.NoError(t, err)
	rev, err := config.Build(fsys, strings.Repeat("a", 40), cc)
	require.NoError(t, err)
	return rev
}

// The claim an opening composes is replaced whole when its escapes may hide
// what a rule that applies always removes, whether the rule reads the claim
// alone or as the member claim=<claim>, and as the board holds it, trimmed:
// the board holds it as that member of its snapshot, and a claim the LLM
// gateway cannot read makes it refuse every model call that reads the
// snapshot. Open checks the routing it records; this is the backstop for
// records it did not check, and for text only the claim as a whole reads. An
// opening that hides nothing is composed and redacted as ever.
func TestTheOpeningClaimIsReplacedWhenItsEscapesMayHideAMatch(t *testing.T) {
	rev := revisionOfRules(t, map[string]string{
		"api-keys": `sk-[a-z0-9]{8}`, "openings": `^Opened by person: yyy`, "claims": `claim=Opened by person: xxx`, "endings": `zzz$`,
	})
	replaced := "Opened by person: [an opening the redaction rules could not read]"
	for name, c := range map[string]struct {
		opened *item.Opened
		claim  string
	}{
		"a routing value":  {opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"service": "sk-\\u0061bcdefgh"}}, claim: replaced},
		"a routing key":    {opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"sk-\\u0061bcdefgh": "checkout"}}, claim: replaced},
		"the claim alone":  {opened: &item.Opened{Source: "person", Title: "\\u0079yy"}, claim: replaced},
		"the claim member": {opened: &item.Opened{Source: "person", Title: "\\u0078xx"}, claim: replaced},
		"the claim trimmed": {
			opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"service": "zz\\u007a "}},
			claim:  replaced,
		},
		"a plain secret": {
			opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"service": "sk-abcdefgh"}},
			claim:  "Opened by person: checkout fails; service=[redacted:api-keys]",
		},
		"nothing to hide": {
			opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"service": "checkout"}},
			claim:  "Opened by person: checkout fails; service=checkout",
		},
	} {
		assert.Equal(t, c.claim, triggerClaim(rev, c.opened), name)
	}
}

// counting is the Turns port of TestStartAgainSkipsALaunchingTurn: it counts
// the starts it is asked for, and leaves each launch pending.
type counting struct{ starts int }

func (c *counting) Start(context.Context, *Turn) (bool, error) {
	c.starts++
	return true, nil
}

func (*counting) Stop(board.RunID, string) {}

func (*counting) StopAll() {}

// still is a Clock whose time stands still.
type still struct{ now time.Time }

func (s still) Now() time.Time { return s.now }

func (still) Local(t time.Time) time.Time { return t }

// startAgain skips a turn whose launch is pending (Decision 4): the runner
// has it, and reports its launch through Launched. The test pins a defensive
// skip that no path reaches today, since a launching turn arms no retry; a
// later path that armed one would otherwise launch one agent's turn twice.
// Here a coordinator built by its fields holds a run whose agent's turn
// launches, within its round, and startAgain runs for that agent.
func TestStartAgainSkipsALaunchingTurn(t *testing.T) {
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	runner := &counting{}
	turn := &Turn{ID: "turn-1", Run: "run-1", Agent: "alpha", Round: 1, Deadline: now.Add(time.Minute)}
	c := &Coordinator{p: Ports{Turns: runner, Clock: still{now: now}}, held: true, ctx: t.Context(), runs: map[board.RunID]*running{
		"run-1": {
			item: "item-1", round: 1, opened: now.Add(-time.Minute), deadline: now.Add(time.Minute), startedLate: map[string]bool{},
			turns: map[string]string{}, starting: map[string]*Turn{"alpha": turn}, launching: map[string]string{"alpha": "turn-1"},
			exits: map[string]item.TurnClass{},
		},
	}}

	c.startAgain("run-1", 1, "alpha")

	assert.Zero(t, runner.starts, "the launching turn was started again")
}
