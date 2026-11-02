package app

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

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
	_, err := c.record(ctx, s, rec{run: s.Run.ID, body: item.Body{RoundOpened: &o}})
	return err
}

// hold acts on the run's open round, once per round on this instance: it
// opens the round on the board, notices there each skill version its agents
// pin that the registry's current revision yanks (see noticeYanks), then
// gives the agents' sessions new tokens and puts them in it, ends those of
// agents that left, starts the turns of the agents that have not ended
// theirs, and arms the round's deadline. It keeps the latest kill switch row
// the run's creation knew, which begin reads (Decision 3). A round it takes
// up with less than a third of its span left is held as usual, so that its
// agents use the time that is left, but it ends unseen (item.Ending): turns
// that the deadline interrupts at once cannot tell a round no agent had time
// for from one that found nothing. It forgets the earlier round's launching
// turns before it opens the round on the board (R5). A notice's post that
// finds the lease gone fences the coordinator, a yank notice's or one a
// turn's start posts, and ends it there: it starts no other turn, and arms
// nothing (Decision 7).
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
	// A launch of the earlier round reported from now on posts nothing, even
	// if this hold fails before its reset, leaving the run on that round while
	// the board is on this one: its notice would land in this round (R5).
	clear(r.launching)
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
	c.noticeYanks(ctx, s, rev, o.Agents)
	if !c.held {
		// A notice's post found the lease gone, and fenced the coordinator: it
		// starts no turn, and arms nothing.
		return nil
	}
	for agent := range r.tokens {
		if !slices.Contains(o.Agents, agent) {
			c.p.Sessions.End(run.ID, agent)
			delete(r.tokens, agent)
		}
	}
	for _, agent := range o.Agents {
		// Each round's sessions have tokens of their own: a turn of an
		// earlier round still running cannot act in this one. The
		// gateways' writes for them act for this term.
		r.tokens[agent] = c.p.Sessions.Issue(&Session{Run: run.ID, Agent: agent, Commit: s.Started.Commit, Item: s.Opened.Routing, Epoch: c.term.Epoch})
		if err := c.p.Sessions.Move(run.ID, agent, o.Round, c.p.Clock.Local(o.Deadline), o.Budget); err != nil {
			return err
		}
	}
	c.cancel(r)
	r.round, r.opened, r.deadline, r.ended = o.Round, o.OpenedAt, o.Deadline, nil
	r.killSwitchSeen = run.Created.KillSwitchSeen
	r.late = r.tooLate(c.p.Clock.Now())
	r.turns, r.starting, r.launching, r.exits = map[string]string{}, map[string]*Turn{}, map[string]string{}, map[string]item.TurnClass{}
	r.startedLate = map[string]bool{}
	for _, agent := range o.Agents {
		if slices.Contains(report.Facts.Ended, agent) {
			r.exits[agent] = item.TurnEnded
			continue
		}
		c.begin(ctx, run.ID, r, c.turn(s, rev, o, agent, r.tokens[agent]))
		if !c.held {
			// A notice's post found the lease gone, and fenced the
			// coordinator: it starts no other turn, and arms nothing.
			return nil
		}
	}
	round, deadline := o.Round, o.Deadline
	c.arm(r, deadline, func() { c.deadline(run.ID, round, deadline) })
	c.whenAllEnded(run.ID, r)
	return nil
}

// noticeYanks posts on the run's board, at a round's start, a skill_yanked
// notice for each skill version that an agent of the round pins in the item's
// locked revision, rev, and that the registry's current revision yanks (spec
// Section 3.2, Decision 10): the item keeps its locked version, and its board
// says so, once per run, agent and version, as the board takes the notice.
// While no revision is current, it posts none. A notice that cannot be posted
// is logged as a step that failed, and the round goes on; one whose post
// finds the lease gone fences the coordinator, and no other is posted.
func (c *Coordinator) noticeYanks(ctx context.Context, s *item.State, rev *config.Revision, agents []string) {
	current := c.p.Revisions.Current()
	if current == nil {
		return
	}
	for _, agent := range agents {
		a := rev.Agent(agent)
		if a == nil {
			continue
		}
		for _, pin := range a.Spec.Skills {
			ref, err := config.ParseSkillRef(pin)
			if err != nil || !current.Yanked(ref) {
				continue
			}
			n := &board.Notice{Code: board.NoticeSkillYanked, Agent: agent, Skill: ref.String()}
			if _, err := c.p.Boards.Notice(ctx, s.Run.ID, board.Coordinator, n); err != nil {
				c.failed(ctx, s.ID, fmt.Errorf("notice that %s pins the yanked skill %s: %w", agent, ref, err))
				if !c.held {
					return
				}
			}
		}
	}
}

// missed ends a round whose deadline passed before this instance acted on
// it, once its board entry is posted, as its holder may have died before it
// was. Such a round is unseen: never quiet, and, as a wrap-up round, it
// concludes nothing (item.Ending). A round whose deadline passed between the last holding's expiry and
// this holding's start, while no instance held the lease, ends by an
// outage: none of its turns counts, and it counts toward no budget. Any
// other ends by its deadline: the turns that ended on the board ended; the
// others were interrupted by the last holding's deadline, or never started,
// when this holding could not act on the round before its deadline. It
// forgets the earlier round's launching turns before it posts the round's
// start, as hold does (R5).
func (c *Coordinator) missed(ctx context.Context, s *item.State) error {
	run, o := s.Run.ID, &s.Run.Latest().Opened
	old := c.runs[run]
	if old != nil {
		// A launch of the earlier round reported from now on posts nothing,
		// even if this end fails after the round's start, leaving the run on
		// that round while the board is on this one: its notice would land in
		// this round (R5).
		clear(old.launching)
	}
	if _, err := c.p.Boards.StartRound(ctx, run, &board.RoundStart{Round: o.Round, WrapUp: o.WrapUp}); err != nil {
		return err
	}
	n, err := c.p.Spend.InFlight(ctx, string(run), o.Round)
	if err != nil {
		return fmt.Errorf("read the round's model calls in flight: %w", err)
	}
	r := &running{
		item: s.ID, round: o.Round, deadline: o.Deadline, tokens: map[string]Tokens{}, turns: map[string]string{}, starting: map[string]*Turn{},
		exits: map[string]item.TurnClass{}, ended: &ended{by: item.EndedByDeadline, at: o.Deadline, unseen: true},
	}
	if o.Deadline.After(c.term.Previous) && !o.Deadline.After(c.term.At) {
		r.ended.by = item.EndedByOutage
	} else {
		report, err := c.p.Boards.Report(ctx, run, o.Round)
		if err != nil {
			return err
		}
		rest := item.TurnInterrupted
		if o.Deadline.After(c.term.At) {
			rest = item.TurnUnstarted
		}
		for _, agent := range o.Agents {
			r.exits[agent] = rest
			if slices.Contains(report.Facts.Ended, agent) {
				r.exits[agent] = item.TurnEnded
			}
		}
	}
	if old != nil {
		c.cancel(old)
		r.tokens = old.tokens
	}
	c.runs[run] = r
	if err := c.endRound(ctx, run, r, n == 0); err != nil {
		// The round is left as this instance found it, so that the retry
		// ends it again.
		if old != nil {
			c.runs[run] = old
		} else {
			delete(c.runs, run)
		}
		return err
	}
	return nil
}

// end marks a round ended, and lets its write calls have their grace before
// its end is decided. A round this instance took up too late ends unseen,
// however it ends.
func (c *Coordinator) end(run board.RunID, r *running, by item.EndedBy, at time.Time) {
	r.ended = &ended{by: by, at: at, unseen: r.late}
	round := r.round
	c.arm(r, c.p.Clock.Now().Add(c.opts.Grace), func() { c.settle(run, round) })
}

// settle waits for the round's model calls in flight, up to the settling
// wait, then decides the round's end. A call of an earlier round still in
// flight does not hold it (Decision 9). An end that meets a later version
// skips the item, and settle arms no retry of its own (see skip).
func (c *Coordinator) settle(run board.RunID, round int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if !c.held || r == nil || r.round != round || r.ended == nil {
		return
	}
	again := func(after time.Duration) { c.arm(r, c.p.Clock.Now().Add(after), func() { c.settle(run, round) }) }
	n, err := c.p.Spend.InFlight(c.ctx, string(run), round)
	if err == nil {
		if n > 0 && c.p.Clock.Now().Before(r.ended.at.Add(c.opts.Grace+c.opts.SettleWait)) {
			again(c.opts.SettlePoll)
			return
		}
		if err = c.endRound(c.ctx, run, r, n == 0); err == nil {
			c.advance(c.ctx, r.item)
			return
		}
	}
	if ofLaterVersion(err) {
		c.skip(c.ctx, r.item, err)
		return
	}
	c.failed(c.ctx, r.item, err)
	if c.held {
		again(c.opts.Retry)
	}
}

// endRound records how a round ended and what follows it, with the evidence
// its decision read (Decision 9): the agents' turn ends the board holds, the
// round's failed model calls and failed tool calls, and whether its model
// calls settled. Before it decides, it notices on the board each model call
// of the run that was refused, failed or whose outcome is unknown, once per
// call. A tool call of the round still without an outcome counts as failed:
// settle calls it once the round's write calls had their grace, the time
// their outcomes take to be recorded. A round whose end is recorded already
// is left as it is.
func (c *Coordinator) endRound(ctx context.Context, run board.RunID, r *running, settled bool) error {
	s, err := c.load(ctx, r.item)
	if err != nil {
		return err
	}
	if l := s.Run.Latest(); l == nil || l.Ended != nil || l.Opened.Round != r.round {
		return nil
	}
	spent, err := c.p.Spend.Spent(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's spend: %w", err)
	}
	day, err := c.p.Spend.SpentToday(ctx)
	if err != nil {
		return fmt.Errorf("read the day's spend: %w", err)
	}
	failed, err := c.p.Spend.Failed(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's failed model calls: %w", err)
	}
	failedTools, err := c.p.ToolCalls.Failed(ctx, string(run), r.round)
	if err != nil {
		return fmt.Errorf("read the round's failed tool calls: %w", err)
	}
	report, err := c.p.Boards.Report(ctx, run, r.round)
	if err != nil {
		return err
	}
	// The calls noticed in the round are those the report holds, which an
	// earlier try of this end may have posted, and those noticed now.
	noticed := report.Facts.FailedCalls
	for _, call := range failed {
		if report.Noticed(call.ID) {
			continue
		}
		n := &board.Notice{Code: noticeCode(call.Status), Agent: call.Agent, CallID: call.ID}
		if _, err := c.p.Boards.Notice(ctx, run, board.Coordinator, n); err != nil {
			return err
		}
		noticed++
	}
	re := s.Run.End(&item.Ending{
		EndedBy: r.ended.by, EndedAt: r.ended.at, Turns: maps.Clone(r.exits), TurnEnds: report.Facts.Ended, Spent: spent, Settled: settled,
		DaySpent: day, DailyCap: c.opts.DailyCap, FailedCalls: noticed, FailedToolCalls: failedTools, Report: report, Unseen: r.ended.unseen,
	})
	if _, err := c.record(ctx, s, rec{run: run, body: item.Body{RoundEnded: &re}}); err != nil {
		return err
	}
	c.cancel(r)
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
