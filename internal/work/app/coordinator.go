package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// ErrInvalid marks a request the coordinator refuses, such as an item of a
// type it cannot run.
var ErrInvalid = errors.New("app: invalid request")

// Ports are what the coordinator acts through.
type Ports struct {
	Records   Records
	Boards    *Boards
	Spend     Spend
	Revisions Revisions
	Sessions  Sessions
	Turns     Turns
	Clock     Clock
	Timers    Timers
	IDs       IDs
}

// Options are the coordinator's settings (spec Section 15).
type Options struct {
	// DailyCap is the deployment's daily token cap, in cost-weighted
	// tokens.
	DailyCap int64
	// Grace is how long a round's end waits for the round's write calls,
	// which commit apart from the call's time: the gateways' record
	// timeout [5 s].
	Grace time.Duration
	// SettleWait is how long a round's end waits for the run's model calls
	// in flight [30 s], and SettlePoll how often it looks again [1 s].
	SettleWait time.Duration
	SettlePoll time.Duration
	// Retry is how long a step that failed waits before it is tried again
	// [5 s].
	Retry time.Duration
	// Log receives the steps that failed; nil logs nothing.
	Log *slog.Logger
}

// Coordinator drives work items through their lifecycle (spec Sections 4.1
// and 5): it opens and starts items, opens and ends rounds, and stops runs.
// It writes each decision as a record before it acts on it, and every
// action can be repeated, so that it resumes from the records after a crash.
// One lock serializes its decisions.
type Coordinator struct {
	p    Ports
	opts Options
	log  *slog.Logger
	ctx  context.Context

	mu      sync.Mutex
	runs    map[board.RunID]*running
	retries map[item.ID]func()
}

// running is what the coordinator holds in memory of a run's open round:
// the sessions it issued, the turns that run, by agent, with their ids, the
// turns the runner could not start yet, how the others ended, and the
// round's timers.
type running struct {
	item     item.ID
	round    int
	deadline time.Time
	tokens   map[string]Tokens
	turns    map[string]string
	starting map[string]*Turn
	exits    map[string]item.TurnClass
	ended    *ended
	timers   []func()
}

// ended is how and when a round ended, until its end is recorded.
type ended struct {
	by item.EndedBy
	at time.Time
}

// maxSteps bounds the steps one advance takes, so that a mistake in the
// records cannot loop forever.
const maxSteps = 32

// NewCoordinator returns a coordinator over p.
func NewCoordinator(p *Ports, opts *Options) (*Coordinator, error) {
	if p.missing() {
		return nil, errors.New("app: the coordinator needs every port")
	}
	if opts.DailyCap <= 0 || opts.Grace <= 0 || opts.SettleWait < 0 || opts.SettlePoll <= 0 || opts.Retry <= 0 {
		return nil, errors.New("app: the coordinator needs a daily cap, a grace, a settling poll and a retry above zero")
	}
	c := &Coordinator{p: *p, opts: *opts, log: opts.Log, ctx: context.Background(), runs: map[board.RunID]*running{}, retries: map[item.ID]func(){}}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

// missing reports whether a port is unset.
func (p *Ports) missing() bool {
	return p.Records == nil || p.Boards == nil || p.Spend == nil || p.Revisions == nil || p.Sessions == nil ||
		p.Turns == nil || p.Clock == nil || p.Timers == nil || p.IDs == nil
}

// OpenInput is a work item to open.
type OpenInput struct {
	Type  string
	Title string
	// Source says who opens the item: "person", or an inbound source.
	Source string
	// Routing holds trusted values only, which write tools' scope bindings
	// see as the item's (spec Section 3.3).
	Routing    map[string]string
	Principal  string
	Responders []string
}

// source is who opens an item: a lowercase word, as inbound sources are
// named.
var source = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// maxTitleRunes caps an item's title.
const maxTitleRunes = 200

// Open opens a work item and starts it at once (Plan 7b queues items). An
// item opened once the day's spend reached the daily cap is not staffed. Its
// title is redacted with the rules that apply always before it is recorded.
// Once the item's opening is recorded, Open returns its id: what the item's
// start does next is tried again until it succeeds.
func (c *Coordinator) Open(ctx context.Context, in *OpenInput) (item.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rev := c.p.Revisions.Current()
	if rev == nil {
		return "", errors.New("app: the registry has no configuration yet")
	}
	if err := runnable(rev, in.Type); err != nil {
		return "", err
	}
	if !source.MatchString(in.Source) {
		return "", fmt.Errorf("%w: an item's source is a lowercase word", ErrInvalid)
	}
	day, err := c.p.Spend.SpentSince(ctx, dayStart(c.p.Clock.Now()))
	if err != nil {
		return "", fmt.Errorf("read the day's spend: %w", err)
	}
	s := &item.State{ID: item.ID(c.p.IDs.NewID()), Incident: item.IncidentID(c.p.IDs.NewID())}
	bodies := []item.Body{{Opened: &item.Opened{
		Type: in.Type, Title: title(rev, in.Title), Source: in.Source, Routing: maps.Clone(in.Routing),
		Principal: in.Principal, Responders: slices.Clone(in.Responders), Arm: item.ArmVisible, How: item.NoWindow,
	}}}
	if day >= c.opts.DailyCap {
		bodies = append(bodies, item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})
	}
	if err := c.record(ctx, s, "", bodies...); err != nil {
		return "", err
	}
	// The item's start is the coordinator's from here on, not the request's.
	c.advance(c.ctx, s.ID)
	return s.ID, nil
}

// Exited reports how an agent's turn ended. An end reported twice, late, for
// a round that is over, of another turn, or as a class the runner does not
// report changes nothing. A round ends once every turn ended, unless each
// ended at the model vendor: such a round ends at its deadline, so that a
// vendor's outage cannot spend the run's rounds in seconds.
func (c *Coordinator) Exited(ctx context.Context, e Exit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[e.Run]
	if r == nil || r.round != e.Round || r.ended != nil {
		return
	}
	if id, ok := r.turns[e.Agent]; !ok || id != e.Turn {
		return
	}
	if !reported(e.Class) {
		c.log.WarnContext(ctx, "a turn's end of a class the runner does not report is ignored", "run", e.Run, "agent", e.Agent, "class", e.Class)
		return
	}
	delete(r.turns, e.Agent)
	r.exits[e.Agent] = e.Class
	c.whenAllEnded(e.Run, r)
}

// reported reports whether the runner reports a turn's end as class.
func reported(class item.TurnClass) bool {
	switch class {
	case item.TurnEnded, item.TurnFinished, item.TurnInterrupted, item.TurnRefused, item.TurnVendor, item.TurnCrashed:
		return true
	}
	return false
}

func (c *Coordinator) whenAllEnded(run board.RunID, r *running) {
	if len(r.turns) > 0 || len(r.starting) > 0 {
		return
	}
	for _, class := range r.exits {
		if class != item.TurnVendor {
			c.end(run, r, item.EndedByTurns, c.p.Clock.Now())
			return
		}
	}
}

// deadline ends a round at its deadline: the turns still running are
// stopped, and count as interrupted; those the runner never started count
// as unstarted.
func (c *Coordinator) deadline(run board.RunID, round int, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if r == nil || r.round != round || r.ended != nil {
		return
	}
	for agent := range r.turns {
		c.p.Turns.Stop(run, agent)
		r.exits[agent] = item.TurnInterrupted
	}
	for agent := range r.starting {
		c.p.Turns.Stop(run, agent)
		r.exits[agent] = item.TurnUnstarted
	}
	clear(r.turns)
	clear(r.starting)
	c.end(run, r, item.EndedByDeadline, at)
}

// end marks a round ended, and lets its write calls have their grace before
// its end is decided.
func (c *Coordinator) end(run board.RunID, r *running, by item.EndedBy, at time.Time) {
	r.ended = &ended{by: by, at: at}
	round := r.round
	c.arm(r, c.p.Clock.Now().Add(c.opts.Grace), func() { c.settle(run, round) })
}

// settle waits for the run's model calls in flight, up to the settling
// wait, then decides the round's end.
func (c *Coordinator) settle(run board.RunID, round int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if r == nil || r.round != round || r.ended == nil {
		return
	}
	again := func(after time.Duration) { c.arm(r, c.p.Clock.Now().Add(after), func() { c.settle(run, round) }) }
	n, err := c.p.Spend.InFlight(c.ctx, string(run))
	if err != nil {
		c.failed(c.ctx, r.item, err)
		again(c.opts.Retry)
		return
	}
	if n > 0 && c.p.Clock.Now().Before(r.ended.at.Add(c.opts.Grace+c.opts.SettleWait)) {
		again(c.opts.SettlePoll)
		return
	}
	if err := c.decide(c.ctx, run, r, n == 0); err != nil {
		c.failed(c.ctx, r.item, err)
		again(c.opts.Retry)
	}
}

// decide records how a round ended and what follows it, then acts on it.
// Before it decides, it notices on the board each model call of the run
// that was refused, failed or whose outcome is unknown, once per call.
func (c *Coordinator) decide(ctx context.Context, run board.RunID, r *running, settled bool) error {
	s, err := c.load(ctx, r.item)
	if err != nil {
		return err
	}
	if l := s.Run.Latest(); l == nil || l.Ended != nil || l.Opened.Round != r.round {
		c.advance(ctx, s.ID)
		return nil
	}
	spent, err := c.p.Spend.Spent(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's spend: %w", err)
	}
	day, err := c.p.Spend.SpentSince(ctx, dayStart(c.p.Clock.Now()))
	if err != nil {
		return fmt.Errorf("read the day's spend: %w", err)
	}
	failed, err := c.p.Spend.Failed(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's failed model calls: %w", err)
	}
	report, err := c.p.Boards.Report(ctx, run, r.round)
	if err != nil {
		return err
	}
	for _, call := range failed {
		if report.Noticed(call.ID) {
			continue
		}
		n := &board.Notice{Code: noticeCode(call.Status), Agent: call.Agent, CallID: call.ID}
		if _, err := c.p.Boards.Notice(ctx, run, board.Coordinator, n); err != nil {
			return err
		}
	}
	re := s.Run.End(&item.Ending{
		EndedBy: r.ended.by, EndedAt: r.ended.at, Turns: maps.Clone(r.exits), Spent: spent, Settled: settled,
		DaySpent: day, DailyCap: c.opts.DailyCap, Report: report,
	})
	if err := c.record(ctx, s, run, item.Body{RoundEnded: &re}); err != nil {
		return err
	}
	c.cancel(r)
	c.advance(ctx, s.ID)
	return nil
}

// noticeCode is the notice of a model call's outcome.
func noticeCode(status string) board.NoticeCode {
	switch status {
	case "refused":
		return board.NoticeModelRefused
	case "error":
		return board.NoticeModelFailed
	}
	return board.NoticeModelUnknown
}

// advance takes an item through every step its records allow now, until it
// waits for a round's end or awaits its close. A step that fails is tried
// again after the retry wait.
func (c *Coordinator) advance(ctx context.Context, id item.ID) {
	if cancel := c.retries[id]; cancel != nil {
		cancel()
		delete(c.retries, id)
	}
	for range maxSteps {
		s, err := c.load(ctx, id)
		if err == nil {
			var done bool
			if done, err = c.step(ctx, s); err == nil && !done {
				continue
			}
		}
		if err != nil {
			c.failed(ctx, id, err)
			c.retries[id] = c.p.Timers.At(c.p.Clock.Now().Add(c.opts.Retry), func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				c.advance(c.ctx, id)
			})
		}
		return
	}
	c.failed(ctx, id, errors.New("app: the item took more steps at once than an item can"))
}

func (c *Coordinator) failed(ctx context.Context, id item.ID, err error) {
	c.log.ErrorContext(ctx, "a step of a work item failed; it is tried again", "item", id, "err", err)
}

// step takes an item's next step, and reports whether it must wait: for a
// round's end, or for the item's close.
func (c *Coordinator) step(ctx context.Context, s *item.State) (wait bool, err error) {
	switch {
	case s.Closing != nil:
		c.forget(s)
		return true, nil
	case s.Started == nil:
		return false, c.start(ctx, s)
	case s.Run == nil:
		return false, c.create(ctx, s)
	case s.Run.Stopped != nil:
		return false, c.close(ctx, s)
	}
	l := s.Run.Latest()
	switch {
	case l == nil && len(s.Run.Created.Agents) == 0:
		return false, c.unstaffed(ctx, s)
	case l == nil || l.Ended != nil && l.Ended.Decision.Next != item.NextStop:
		return false, c.open(ctx, s)
	case l.Ended == nil:
		return true, c.hold(ctx, s)
	}
	a, err := c.p.Boards.Assess(ctx, s.Run.ID, time.Time{})
	if err != nil {
		return false, err
	}
	stop := s.Run.Stop(a.Conclusion)
	return false, c.record(ctx, s, s.Run.ID, item.Body{RunStopped: &stop})
}

// start locks the configuration the item runs under; an item whose type
// the current revision cannot run is not staffed.
func (c *Coordinator) start(ctx context.Context, s *item.State) error {
	rev := c.p.Revisions.Current()
	if rev == nil {
		return errors.New("app: the registry has no configuration yet")
	}
	if runnable(rev, s.Opened.Type) != nil {
		return c.record(ctx, s, "", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})
	}
	lock, err := c.p.Revisions.Lock(rev)
	if err != nil {
		return fmt.Errorf("lock revision %s: %w", rev.Commit(), err)
	}
	return c.record(ctx, s, "", item.Body{Started: &item.Started{Commit: lock.Commit, Digest: lock.Digest, Images: lock.RuntimeImages}})
}

// create creates the item's run, the primary run that people see, with the
// settings and budget of its type, inviting the agents subscribed to it.
func (c *Coordinator) create(ctx context.Context, s *item.State) error {
	rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
	if err != nil {
		return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
	}
	ws := rev.WorkItemType(s.Opened.Type).Settings()
	return c.record(ctx, s, board.RunID(c.p.IDs.NewID()), item.Body{RunCreated: &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: boardSettings(&ws),
		Budget:     item.Budget{WallClock: ws.Budget.WallClock, Tokens: ws.Budget.Tokens, Rounds: ws.Budget.Rounds},
		RoundLimit: ws.RoundLimit, Agents: invited(rev, s.Opened.Type, ws.MaxAgents),
	}})
}

// unstaffed stops a run with no agent to invite, and says so on its board.
func (c *Coordinator) unstaffed(ctx context.Context, s *item.State) error {
	if _, err := c.p.Boards.Notice(ctx, s.Run.ID, board.Coordinator, &board.Notice{Code: board.NoticeNoAgents}); err != nil {
		return err
	}
	stop := s.Run.Stop("")
	return c.record(ctx, s, s.Run.ID, item.Body{RunStopped: &stop})
}

// open opens the run's next round as its last decided, or its first after
// the evidence the item opened with.
func (c *Coordinator) open(ctx context.Context, s *item.State) error {
	if s.Run.Latest() == nil {
		rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
		if err != nil {
			return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
		}
		in := &board.PlatformEvidenceInput{Claim: triggerClaim(rev, s.Opened), Ref: "item:" + string(s.ID), Source: board.Source(s.Opened.Source)}
		if _, err := c.p.Boards.PostPlatformEvidence(ctx, s.Run.ID, in); err != nil {
			return err
		}
	}
	o := s.Run.Open(c.p.Clock.Now())
	return c.record(ctx, s, s.Run.ID, item.Body{RoundOpened: &o})
}

// hold acts on the run's open round, once per round on this instance: it
// opens the round on the board, then gives the agents' sessions new tokens
// and puts them in it, ends those of agents that left, starts the turns of
// the agents that have not ended theirs, and arms the round's deadline.
func (c *Coordinator) hold(ctx context.Context, s *item.State) error {
	run := s.Run
	o := &run.Latest().Opened
	r := c.runs[run.ID]
	if r == nil {
		r = &running{item: s.ID, tokens: map[string]Tokens{}}
		c.runs[run.ID] = r
	}
	if r.round == o.Round {
		return nil
	}
	if _, err := c.p.Boards.StartRound(ctx, run.ID, &board.RoundStart{Round: o.Round, WrapUp: o.WrapUp}); err != nil {
		return err
	}
	report, err := c.p.Boards.Report(ctx, run.ID, o.Round)
	if err != nil {
		return err
	}
	rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
	if err != nil {
		return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
	}
	for agent := range r.tokens {
		if !slices.Contains(o.Agents, agent) {
			c.p.Sessions.End(run.ID, agent)
			delete(r.tokens, agent)
		}
	}
	for _, agent := range o.Agents {
		// Each round's sessions have tokens of their own: a turn of an
		// earlier round still running cannot act in this one.
		r.tokens[agent] = c.p.Sessions.Issue(&Session{Run: run.ID, Agent: agent, Commit: s.Started.Commit, Item: s.Opened.Routing})
		if err := c.p.Sessions.Move(run.ID, agent, o.Round, c.p.Clock.Local(o.Deadline), o.Budget); err != nil {
			return err
		}
	}
	c.cancel(r)
	r.round, r.deadline, r.ended = o.Round, o.Deadline, nil
	r.turns, r.starting, r.exits = map[string]string{}, map[string]*Turn{}, map[string]item.TurnClass{}
	for _, agent := range o.Agents {
		if slices.Contains(report.Facts.Ended, agent) {
			r.exits[agent] = item.TurnEnded
			continue
		}
		c.begin(ctx, run.ID, r, c.turn(s, rev, o, agent, r.tokens[agent]))
	}
	round, deadline := o.Round, o.Deadline
	c.arm(r, deadline, func() { c.deadline(run.ID, round, deadline) })
	c.whenAllEnded(run.ID, r)
	return nil
}

// begin starts a turn. A turn the runner could not start is tried again
// after the retry wait, with the same id, while its round lasts.
func (c *Coordinator) begin(ctx context.Context, run board.RunID, r *running, t *Turn) {
	if err := c.p.Turns.Start(ctx, t); err != nil {
		c.failed(ctx, r.item, fmt.Errorf("start the turn of %s: %w", t.Agent, err))
		r.starting[t.Agent] = t
		round, agent := r.round, t.Agent
		c.arm(r, c.p.Clock.Now().Add(c.opts.Retry), func() { c.startAgain(run, round, agent) })
		return
	}
	delete(r.starting, t.Agent)
	r.turns[t.Agent] = t.ID
}

// startAgain tries again to start a turn the runner could not start, while
// its round lasts.
func (c *Coordinator) startAgain(run board.RunID, round int, agent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if r == nil || r.round != round || r.ended != nil || r.starting[agent] == nil || !c.p.Clock.Now().Before(r.deadline) {
		return
	}
	c.begin(c.ctx, run, r, r.starting[agent])
}

// close acts on a run that stopped: its sessions and turns end, then the
// stop entry, with the conclusion its record holds, ends its board, which
// may take tries, and the item awaits its close, with the run's reason as
// the item's.
func (c *Coordinator) close(ctx context.Context, s *item.State) error {
	run := s.Run
	st := run.Stopped
	c.p.Sessions.EndRun(run.ID)
	if l := run.Latest(); l != nil {
		for _, agent := range l.Opened.Agents {
			c.p.Turns.Stop(run.ID, agent)
		}
	}
	in := &board.StopInput{Reason: string(st.Reason), Conclusion: st.Conclusion, Rounds: st.Rounds, WallClock: st.WallClock, Tokens: st.Spent}
	if _, err := c.p.Boards.Stop(ctx, run.ID, in); err != nil {
		return err
	}
	return c.record(ctx, s, "", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.ItemReason(st.Reason)}})
}

// forget drops what the coordinator holds of an item that awaits its close.
func (c *Coordinator) forget(s *item.State) {
	if s.Run == nil {
		return
	}
	if r := c.runs[s.Run.ID]; r != nil {
		c.cancel(r)
		delete(c.runs, s.Run.ID)
	}
}

// turn is an agent's turn in a round.
func (c *Coordinator) turn(s *item.State, rev *config.Revision, o *item.RoundOpened, agent string, t Tokens) *Turn {
	turn := &Turn{
		ID: c.p.IDs.NewID(), Run: s.Run.ID, Agent: agent, Round: o.Round, WrapUp: o.WrapUp, Deadline: c.p.Clock.Local(o.Deadline),
		MCP: t.MCP, LLM: t.LLM,
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

// load folds an item's records.
func (c *Coordinator) load(ctx context.Context, id item.ID) (*item.State, error) {
	records, err := c.p.Records.Load(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load item %s: %w", id, err)
	}
	return item.Fold(records)
}

// record appends records of the item, each checked first against the
// item's records as committed: the lifecycle's rules refuse a record out of
// order, as a decision made twice would be.
func (c *Coordinator) record(ctx context.Context, s *item.State, run board.RunID, bodies ...item.Body) error {
	_, err := c.p.Records.Append(ctx, s.ID, func(history []item.Record) ([]item.Draft, error) {
		check, err := item.Fold(history)
		if err != nil {
			return nil, err
		}
		drafts := make([]item.Draft, 0, len(bodies))
		for i := range bodies {
			b := bodies[i]
			b.V = item.Version
			d := item.Draft{ID: c.p.IDs.NewID(), Item: s.ID, Incident: s.Incident, Run: run, Body: b}
			r := item.Record{Seq: int64(len(history) + i + 1), ID: d.ID, Item: d.Item, Incident: d.Incident, Run: run, Body: b}
			if err := check.Apply(&r); err != nil {
				return nil, err
			}
			drafts = append(drafts, d)
		}
		return drafts, nil
	})
	if err != nil {
		return fmt.Errorf("record item %s: %w", s.ID, err)
	}
	return nil
}

func (c *Coordinator) arm(r *running, at time.Time, fn func()) {
	r.timers = append(r.timers, c.p.Timers.At(at, fn))
}

func (c *Coordinator) cancel(r *running) {
	for _, cancel := range r.timers {
		cancel()
	}
	r.timers = nil
}

// runnable refuses a type the coordinator cannot run: one that does not
// exist, or whose key reads its values from a tool when an item opens,
// which Plan 7c builds.
func runnable(rev *config.Revision, typ string) error {
	wt := rev.WorkItemType(typ)
	if wt == nil {
		return fmt.Errorf("%w: the configuration has no such work item type", ErrInvalid)
	}
	for _, f := range wt.Settings().Key {
		if f.Values.Read != nil {
			return fmt.Errorf("%w: the type's key reads its values when an item opens, which the coordinator does not do yet", ErrInvalid)
		}
	}
	return nil
}

// boardSettings are a run's board settings, from its type's.
func boardSettings(ws *config.WorkSettings) board.Settings {
	schema := key.Schema{Fields: make([]key.Field, 0, len(ws.Key))}
	for _, f := range ws.Key {
		schema.Fields = append(schema.Fields, key.Field{Name: f.Name, Values: slices.Clone(f.Values.List)})
	}
	return board.Settings{
		Schema: schema,
		Convergence: board.Convergence{
			MinContributors: ws.Convergence.MinContributors, MinSources: ws.Convergence.MinSources, RefutationMargin: ws.Convergence.RefutationMargin,
		},
		Limits: board.Limits{EntriesPerRound: ws.Limits.EntriesPerRound, OpenClaims: ws.Limits.OpenClaims, LeaseRounds: ws.Limits.LeaseRounds},
	}
}

// invited returns the agents an item of type typ invites: those subscribed
// to the type, in the revision's order, but those that pin a yanked skill
// version, up to max.
func invited(rev *config.Revision, typ string, maxAgents int) []string {
	out := []string{}
	for _, a := range rev.Agents() {
		if len(out) == maxAgents {
			break
		}
		if slices.Contains(a.Spec.Subscribe.WorkItemTypes, typ) && len(rev.YankedPins(a.Metadata.Name)) == 0 {
			out = append(out, a.Metadata.Name)
		}
	}
	return out
}

// always returns the redaction rules that apply always: the session tokens'
// rule, then the configuration's.
func always(rev *config.Revision) *redact.Redactor {
	rules := []redact.Rule{redact.SessionTokens()}
	for _, r := range rev.AlwaysRedactionRules() {
		rules = append(rules, redact.FromConfig(r))
	}
	return redact.New(rules...)
}

// title redacts an item's title as it is recorded, cut to maxTitleRunes. A
// title whose escapes may hide what a rule matches is replaced whole.
func title(rev *config.Revision, raw string) string {
	red := always(rev)
	if red.Escaped(raw) {
		return "[a title the redaction rules could not read]"
	}
	return cut(red.Text(strings.TrimSpace(raw)), maxTitleRunes)
}

// triggerClaim is the evidence an item opens with, composed by rule: who
// opened it, its title and its routing values, redacted.
func triggerClaim(rev *config.Revision, o *item.Opened) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Opened by %s: %s", o.Source, o.Title)
	for _, k := range slices.Sorted(maps.Keys(o.Routing)) {
		fmt.Fprintf(&b, "; %s=%s", k, o.Routing[k])
	}
	return cut(always(rev).Text(b.String()), board.MaxClaimRunes)
}

// cut cuts s to at most n characters, the last of them a mark that it was
// cut.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// dayStart is the start of t's day, UTC, from which the daily cap counts.
func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
