package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// running is what the coordinator holds in memory of a run's open round:
// the sessions it issued, the turns that run, by agent, with their ids, the
// turns the runner could not start yet, those of them whose launch is
// pending, how the others ended, whether this instance took the round up too
// late, which turns it started too late, the latest kill switch row the run's
// creation knew, and the round's timers.
type running struct {
	item     item.ID
	round    int
	opened   time.Time
	deadline time.Time
	// late says this instance took the round up with less than a third of its
	// span left (see hold): however the round ends, it ends unseen.
	late bool
	// killSwitchSeen is the latest kill switch row the run's creation knew,
	// which hold copies from the run's records: begin starts no turn of a run
	// that owes a later on-row (Decision 3).
	killSwitchSeen int64
	// startedLate holds the agents whose turns started with less than a
	// third of the round's span left, in a round taken up in time (see
	// begin): one still running at the deadline, or reported interrupted,
	// counts as unstarted.
	startedLate map[string]bool
	tokens      map[string]Tokens
	turns       map[string]string
	starting    map[string]*Turn
	// launching holds, by agent, the id of each starting turn whose launch
	// Start left pending (R5). Launched removes a mark when it reports how the
	// launch ended while the round lasts, or that it failed once the deadline
	// ended the round: the deadline keeps the marks, so that such a failure
	// still posts its notice. Any other mark lasts until hold, or missed,
	// clears the run's marks before it posts the next round's start, so that
	// no late notice lands in that round.
	launching map[string]string
	exits     map[string]item.TurnClass
	ended     *ended
	timers    []func()
}

// tooLate reports whether the round has less than a third of its span left
// at now, the span from its opening to its deadline.
func (r *running) tooLate(now time.Time) bool {
	return r.deadline.Sub(now) < r.deadline.Sub(r.opened)/lateShare
}

// agents returns, sorted, the agents of the run that this instance issued
// sessions to, started turns for, or tries to start turns for.
func (r *running) agents() []string {
	agents := slices.Collect(maps.Keys(r.tokens))
	agents = slices.AppendSeq(agents, maps.Keys(r.turns))
	agents = slices.AppendSeq(agents, maps.Keys(r.starting))
	slices.Sort(agents)
	return slices.Compact(agents)
}

// ended is how and when a round ended, until its end is recorded.
type ended struct {
	by item.EndedBy
	at time.Time
	// unseen says this instance did not see the round's turns: it did not
	// act on the round, or took it up too late (item.Ending).
	unseen bool
}

// lateShare is the part of a round's span that must be left when this
// instance takes the round up, or starts a turn in it, for the round or the
// turn to count: a third.
const lateShare = 3

// Exited reports how an agent's turn ended. An end reported twice, late, for
// a round that is over, of another turn, or as a class the runner does not
// report changes nothing. A turn reported interrupted counts as the deadline
// counts it when this instance started it with less than a third of the
// round's span left, or never knew it started: as unstarted. The runner stops
// a turn at the deadline too, and its report may come before this instance's
// deadline does. Any other end counts as reported. A round ends once every
// turn ended, unless each ended at the model vendor or unavailable: such a
// round ends at its deadline, so that an outage of the vendor or of the
// platform cannot spend the run's rounds in seconds (see waits).
func (c *Coordinator) Exited(ctx context.Context, e Exit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return
	}
	r := c.runs[e.Run]
	if r == nil || r.round != e.Round || r.ended != nil {
		return
	}
	starting := false
	if id, ok := r.turns[e.Agent]; !ok || id != e.Turn {
		// A turn whose Start failed may have started after all, if the
		// runner's answer was what was lost, and end before the retry: its
		// end counts, by the id the retry would use.
		if held := r.starting[e.Agent]; held == nil || held.ID != e.Turn {
			return
		}
		starting = true
	}
	if !reported(e.Class) {
		c.log.WarnContext(ctx, "a turn's end of a class the runner does not report is ignored", "run", e.Run, "agent", e.Agent, "class", e.Class)
		return
	}
	class := e.Class
	if class == item.TurnInterrupted && (starting || r.startedLate[e.Agent]) {
		class = item.TurnUnstarted
	}
	delete(r.turns, e.Agent)
	delete(r.starting, e.Agent)
	r.exits[e.Agent] = class
	c.whenAllEnded(e.Run, r)
}

// Launched reports how a pending launch ended. It takes the coordinator's
// lock. One the coordinator no longer waits for (another turn's ID, a round
// that ended, a run that stopped, a fence) changes nothing, but for one case:
// a failed launch of a turn that was launching when its round's deadline
// ended the round, while the run is still on that round in the same holding,
// posts its notice and arms no retry.
//
// A launch that succeeded moves the turn to the round's running turns, and
// judges then whether it started too late, as begin does for a Start that
// did not fail; one that failed does what begin does for a Start that failed
// (see unstarted), for the launch's reason. The coordinator acts on it under
// its own context, which carries its holding's epoch, whatever context the
// runner reports with: a launch that failed at the deadline is reported as
// the deadline ends the round, as the engine's queued launches are while it
// does not answer (Decision 7).
//
//nolint:gocritic // hugeParam: the contract (C10) passes the launch by value.
func (c *Coordinator) Launched(ctx context.Context, l Launch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[l.Run]
	if !c.held || r == nil || r.round != l.Round {
		return
	}
	if id, ok := r.launching[l.Agent]; !ok || id != l.Turn {
		return
	}
	if r.ended != nil {
		if l.Err != nil && r.ended.by == item.EndedByDeadline {
			delete(r.launching, l.Agent)
			c.launchFailed(c.ctx, l.Run, r, l.Agent, l.Reason, l.Err)
		}
		return
	}
	t := r.starting[l.Agent]
	if t == nil || t.ID != l.Turn {
		return
	}
	delete(r.launching, l.Agent)
	if l.Err != nil {
		c.unstarted(c.ctx, l.Run, r, t, l.Reason, l.Err)
		return
	}
	c.started(r, t)
}

// reported reports whether the runner reports a turn's end as class.
func reported(class item.TurnClass) bool {
	switch class {
	case item.TurnEnded, item.TurnFinished, item.TurnInterrupted, item.TurnRefused, item.TurnVendor, item.TurnUnavailable, item.TurnCrashed:
		return true
	}
	return false
}

func (c *Coordinator) whenAllEnded(run board.RunID, r *running) {
	if len(r.turns) > 0 || len(r.starting) > 0 {
		return
	}
	for _, class := range r.exits {
		if !waits(class) {
			c.end(run, r, item.EndedByTurns, c.p.Clock.Now())
			return
		}
	}
}

// waits reports whether a turn that ended as class leaves its round to its
// deadline when every turn of the round ended so: the model vendor failed
// it, or the platform could not serve it (Decision 13). Such turns end in
// seconds while the outage lasts.
func waits(class item.TurnClass) bool {
	return class == item.TurnVendor || class == item.TurnUnavailable
}

// deadline ends a round at its deadline: the turns still running are
// stopped, and count as interrupted, but those started with less than a
// third of the round's span left count, as those the runner never started
// do, as unstarted. A turn still launching is one the runner never started:
// it is stopped, which cancels its launch, and counts as unstarted. The
// deadline clears the starting turns but keeps the launching ones' ids, so
// that the failure the runner reports of such a launch, as the round ends,
// still posts its notice (see Launched). It stops the turns in the order of
// their agents (R33).
func (c *Coordinator) deadline(run board.RunID, round int, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if !c.held || r == nil || r.round != round || r.ended != nil {
		return
	}
	for _, agent := range slices.Sorted(maps.Keys(r.turns)) {
		c.p.Turns.Stop(run, agent)
		r.exits[agent] = item.TurnInterrupted
		if r.startedLate[agent] {
			r.exits[agent] = item.TurnUnstarted
		}
	}
	for _, agent := range slices.Sorted(maps.Keys(r.starting)) {
		c.p.Turns.Stop(run, agent)
		r.exits[agent] = item.TurnUnstarted
	}
	clear(r.turns)
	clear(r.starting)
	c.end(run, r, item.EndedByDeadline, at)
}

// begin starts a turn. A turn the runner could not start is tried again
// after the retry wait, with the same id, while its round lasts (see
// unstarted). A turn whose launch Start left pending stays among the round's
// starting turns, marked as launching, with no retry armed, until Launched
// reports how its launch ended (Decision 4). A turn started with less than a
// third of the round's span left, as when the runner is back late, counts as
// unstarted if it still runs at the deadline (see deadline), or if the
// runner reports it interrupted (see Exited): it had no time to find
// anything. A round taken up too late is unseen already, and its turns count
// as they did.
//
// begin starts no turn while the kill switch is on, nor of a run that owes
// the view's latest on-row, whatever the switch says now (Decision 3, the
// second line, behind the drop of every halted run): the turn stays among the
// round's starting turns, with no retry armed and no notice posted, since its
// run is to be halted. While a kill switch write's outcome is unknown, the
// view off, begin starts no turn either, and posts no notice, but arms the
// turn's retry after the retry wait, as for a launch that failed, since the
// outcome may be that nothing changed (R37; see startLater).
func (c *Coordinator) begin(ctx context.Context, run board.RunID, r *running, t *Turn) {
	switch {
	case c.killSwitch.On || c.killSwitch.OnSeq > r.killSwitchSeen:
		r.starting[t.Agent] = t
		return
	case c.unsettled:
		c.startLater(run, r, t)
		return
	}
	pending, err := c.p.Turns.Start(ctx, t)
	switch {
	case err != nil:
		c.unstarted(ctx, run, r, t, ReasonLaunch, err)
	case pending:
		r.starting[t.Agent] = t
		r.launching[t.Agent] = t.ID
	default:
		c.started(r, t)
	}
}

// started moves a turn the runner started to the round's running turns, and
// judges whether it started too late (see begin).
func (c *Coordinator) started(r *running, t *Turn) {
	delete(r.starting, t.Agent)
	r.turns[t.Agent] = t.ID
	if !r.late && r.tooLate(c.p.Clock.Now()) {
		r.startedLate[t.Agent] = true
	}
}

// unstarted acts on a turn that could not start, as its Start or its launch
// failed for reason (Decision 7; see launchFailed), and keeps it among the
// round's starting turns, to start it again after the retry wait, with the
// same id, while its round lasts. A notice that cannot be posted changes
// nothing of that, unless its post found the lease gone and fenced the
// coordinator, which then arms nothing.
func (c *Coordinator) unstarted(ctx context.Context, run board.RunID, r *running, t *Turn, reason LaunchReason, err error) {
	c.launchFailed(ctx, run, r, t.Agent, reason, err)
	if !c.held {
		return
	}
	c.startLater(run, r, t)
}

// startLater keeps t among its round's starting turns, and arms its start
// again after the retry wait, with the same id, while its round lasts (see
// startAgain).
func (c *Coordinator) startLater(run board.RunID, r *running, t *Turn) {
	r.starting[t.Agent] = t
	round, agent := r.round, t.Agent
	c.arm(r, c.p.Clock.Now().Add(c.opts.Retry), func() { c.startAgain(run, round, agent) })
}

// launchFailed logs a launch that failed, for reason, as a step that failed,
// which fences the coordinator on a lost lease, then posts on the run's board
// the turn_unstarted notice that names the agent and the reason (Decision 7):
// one per agent and round, as the board takes it. A launch the holding
// refused, whose error wraps ErrUnheld or whose reason is holding, posts
// none, since the instance is losing the lease and the write would be
// refused; nor does one that a stop, a fence or a drain cancelled. A notice
// that cannot be posted is logged as a step that failed, and its post too
// fences on a lost lease.
func (c *Coordinator) launchFailed(ctx context.Context, run board.RunID, r *running, agent string, reason LaunchReason, err error) {
	c.failed(ctx, r.item, fmt.Errorf("start the turn of %s (%s): %w", agent, reason, err))
	if !c.held || errors.Is(err, ErrUnheld) || reason == ReasonHolding || reason == ReasonCancelled {
		return
	}
	n := &board.Notice{Code: board.NoticeTurnUnstarted, Agent: agent, Reason: string(reason)}
	if _, err := c.p.Boards.Notice(ctx, run, board.Coordinator, n); err != nil {
		c.failed(ctx, r.item, fmt.Errorf("notice that the turn of %s could not start: %w", agent, err))
	}
}

// startAgain tries again to start a turn the runner could not start, while
// its round lasts. It skips a turn whose launch is pending: the runner has
// it, and reports its launch through Launched.
func (c *Coordinator) startAgain(run board.RunID, round int, agent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if r == nil || r.round != round || r.ended != nil || r.starting[agent] == nil || !c.p.Clock.Now().Before(r.deadline) {
		return
	}
	if _, launching := r.launching[agent]; launching {
		return
	}
	c.begin(c.ctx, run, r, r.starting[agent])
}

// turn is an agent's turn in a round.
func (c *Coordinator) turn(s *item.State, rev *config.Revision, o *item.RoundOpened, agent string, t Tokens) *Turn {
	turn := &Turn{
		ID: c.p.IDs.NewID(), Run: s.Run.ID, Agent: agent, Round: o.Round, WrapUp: o.WrapUp, Deadline: c.p.Clock.Local(o.Deadline),
		MCP: t.MCP, LLM: t.LLM, Commit: s.Started.Commit,
	}
	if a := rev.Agent(agent); a != nil {
		turn.Image, turn.Model, turn.Skills = s.Started.Images[a.Spec.Runtime], a.Spec.Model, slices.Clone(a.Spec.Skills)
	}
	turn.Prompt = prompt(turn, roundsLeft(s.Run))
	return turn
}

// roundsLeft is how many normal rounds the run's budget allows after its open
// round: rounds that ended by an outage count toward no budget, so a round's
// number does not say it.
func roundsLeft(run *item.Run) int {
	used := 0
	for i := len(run.Rounds) - 1; i >= 0; i-- {
		if e := run.Rounds[i].Ended; e != nil {
			used = e.Rounds
			break
		}
	}
	return max(run.Created.Budget.Rounds-used-1, 0)
}

// prompt is a turn's opening message. It holds the platform's words and ids
// only: what agents and people wrote reaches the model through read_board,
// which marks it as data.
func prompt(t *Turn, left int) string {
	if t.WrapUp != "" {
		return fmt.Sprintf("Round %d is the wrap-up round of this investigation: hypothesis %s converged. Read the board with read_board. "+
			"Post evidence only if it refutes that hypothesis, then call end_turn.", t.Round, t.WrapUp)
	}
	next := fmt.Sprintf("at most %d more rounds follow it", left)
	switch left {
	case 0:
		next = "it is the last"
	case 1:
		next = "at most one more round follows it"
	}
	return fmt.Sprintf("Round %d of this investigation: %s. Read the board with read_board, investigate with your tools, "+
		"post what you find, and call end_turn when you are done.", t.Round, next)
}
