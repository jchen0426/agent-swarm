package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Actor is who acts (Decision 13): the person Plan 9a signs in, or, through
// the admin API, the subject admin, an admin since its token is an admin's.
// Records keep the subject. Responder and Admin are the actor's roles, which
// May reads (R76): the web layer gives Admin only while the admin's
// authentication is fresh, and Responder to every responder and admin.
type Actor struct {
	Subject   string
	Responder bool
	Admin     bool
}

// Act is a person's request (Decision 13): who, why, what makes a retry
// safe, and whether to act or only to show the consequences.
type Act struct {
	// Actor names who acts: an act whose subject is empty is refused, since
	// its record keeps the subject.
	Actor Actor
	// Key is the Idempotency-Key of an act on an item, 1 to 128 printable
	// ASCII characters (R20), which the act's record keeps hashed, never as it
	// is; empty for a preview, which takes none.
	Key string
	// After is the kill switch's latest row the sender saw: the kill switch's
	// alone.
	After int64
	// Reason is the act's one reason, if any, whose text is checked and
	// redacted as a person's text before it is recorded (see PersonText).
	Reason *board.Reason
	// Preview asks for the act's consequences alone, recorded and acted on by
	// nothing.
	Preview bool
}

// Outcome is what an act on an item did, or would do.
type Outcome struct {
	// Changed is false for an act whose effect held already, which records
	// nothing (R10).
	Changed      bool
	Consequences Consequences
}

// Consequences are an act's effects in codes, ids and numbers, never a
// person's text (Contract C4): the runs that stop; the items that start,
// close or are not staffed; when an item closes by itself, or that its
// auto-close is paused; the items investigating and the limit; and, for an
// extension, the run's budget before and after it, and whether it resumes
// the run (R57); for a link or an unlink, the incident the item leaves and
// joins (Contract C16).
type Consequences struct {
	Stops     []RunStop
	Starts    []item.ID
	Closes    []item.ID
	Unstaffed []item.ID
	// ClosesAt is zero when it does not apply.
	ClosesAt        time.Time
	AutoClosePaused bool
	Investigating   int
	Limit           int
	// Budget is nil but for an extension.
	Budget  *BudgetChange
	Resumes bool
	// Incident is nil but for a link or an unlink.
	Incident *IncidentChange
}

// BudgetChange is an extension's effect on its run's budget (R57): the
// budget before it and after it, each its creation's plus every extension's
// (item.Run.Budget), and what the run used, as its latest ended round says.
type BudgetChange struct{ Before, After, Used item.Budget }

// IncidentChange is a link's or an unlink's effect on its item's incident
// (Contract C16): the incident it leaves, the one it joins, and the items of
// that one after the act, sorted.
type IncidentChange struct {
	From, To item.IncidentID
	Items    []item.ID
}

// RunStop is a run an act stops, or would stop: its item, its latest round,
// 0 before its first, and the reason it stops for.
type RunStop struct {
	Item   item.ID
	Run    board.RunID
	Round  int
	Reason item.StopReason
}

// Refused carries the code of an act the item's state forbids (Contract C4);
// errors.As finds it.
type Refused struct{ Code string }

func (r *Refused) Error() string { return "app: the act is refused: " + r.Code }

// The codes of Refused (Contract C4).
const (
	CodeClosed             = "closed"
	CodeReopenWindowPassed = "reopen_window_passed"
	CodeLimitReached       = "limit_reached"
	CodeKillSwitchOn       = "kill_switch_on"
	CodeDailyCapReached    = "daily_cap_reached"
	CodeAdminOnly          = "admin_only"
	CodeReasonRequired     = "reason_required"
	CodeNotItemResponder   = "not_item_responder"
	CodeResponderOnly      = "responder_only"
	// CodeNotStarted refuses an extension of an item with no run (R62).
	CodeNotStarted = "not_started"
	// CodeNoAgentsLeft refuses the resume of a run with no agent left to
	// invite (Decision 16).
	CodeNoAgentsLeft = "no_agents_left"
	// CodeWouldStopAtOnce refuses the resume of a run whose usage the budget
	// extended would still exhaust, so that its round would stop at once
	// (Decision 16; R56).
	CodeWouldStopAtOnce = "would_stop_at_once"
	// CodeSelf refuses a link of an item to itself (R60).
	CodeSelf = "self"
	// CodeRulingsWindowPassed refuses a link or an unlink of a closed item,
	// either item of a link, past the window of late rulings of its close
	// (R60, R64).
	CodeRulingsWindowPassed = "rulings_window_passed"
)

// maxKey caps an act's key, in characters, each printable ASCII, as the
// admin API caps an opening's (R20).
const maxKey = 128

// reasonCode is the form of a reason's code (Decision 9).
var reasonCode = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// reasonField names a reason's text as the board's snapshot and the records
// hold it: the member text=<text> (Decision 9).
const reasonField = "text"

// holds reports whether the coordinator acts (Decision 13): it is held, and
// its context, which its holding ends, has not ended, as a drain's first
// moment ends it before the drain's fence. Every act answers ErrNotHeld,
// before it reads or writes anything, unless the coordinator holds.
func (c *Coordinator) holds() bool {
	return c.held && c.ctx.Err() == nil
}

// heldThrough is the answer of an act that answered out and err, once the act
// is done, under the coordinator's lock (Decision 13): as it is while the
// coordinator still holds, or ErrNotHeld once it no longer does, fenced while
// the act went on, as by a write that found the lease gone, or its context
// ended, as at a drain's start. Such an act may have landed, but an answer
// read from an account that a fence emptied, or that a drain is about to,
// would be partial: what landed is found on the instance that leads next, an
// act on an item by its key, and the kill switch by its state. Each act defers
// it once it begins.
func heldThrough[T any](c *Coordinator, out T, err error) (T, error) {
	if c.holds() {
		return out, err
	}
	var none T
	return none, ErrNotHeld
}

// checkAct checks the form of an act on an item (Decision 13): who acts, a
// subject, which its record keeps, since a close that names no one is an
// automatic close's (Contract C19); and, but for a preview, which takes no
// key, its key, of 1 to maxKey printable ASCII characters (R20), which the
// coordinator checks as the admin API does, since Plan 9a calls the acts
// directly. The reason is checkReason's. An error never quotes the key.
func checkAct(a *Act) error {
	switch {
	case a == nil:
		return fmt.Errorf("%w: no act", ErrInvalid)
	case a.Actor.Subject == "":
		return fmt.Errorf("%w: an act names who acts", ErrInvalid)
	case a.Preview:
		return nil
	case a.Key == "" || len(a.Key) > maxKey || strings.ContainsFunc(a.Key, func(r rune) bool { return r < ' ' || r > '~' }):
		return fmt.Errorf("%w: an act on an item carries an Idempotency-Key of 1 to %d printable ASCII characters", ErrInvalid, maxKey)
	}
	return nil
}

// checkReason checks r's form (see checkReasonForm), then its text through
// PersonText, as reasonField, with the rules that apply always of s's locked
// revision, or of the current one, for an item that never started or whose
// locked revision cannot be read; with neither, a text is refused with
// ErrInvalid, and a code alone is taken (Decision 9). It returns a copy of r,
// its text redacted, or nil for no reason. An error never quotes the reason.
func (c *Coordinator) checkReason(s *item.State, r *board.Reason) (*board.Reason, error) {
	if err := checkReasonForm(r); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	out := *r
	if out.Text == "" {
		return &out, nil
	}
	rev := c.rulesRevision(s)
	if rev == nil {
		return nil, fmt.Errorf("%w: a reason's text is checked against the configuration's redaction rules, and none can be read yet; give a code alone", ErrInvalid)
	}
	text, err := PersonText(always(rev), reasonField, out.Text)
	if err != nil {
		return nil, err
	}
	out.Text = text
	return &out, nil
}

// rulesRevision is the revision whose rules check a person's text on s
// (Decision 9): the one s locked, or the current one, for an item that never
// started or whose locked revision cannot be read; nil with neither.
func (c *Coordinator) rulesRevision(s *item.State) *config.Revision {
	if s.Started != nil {
		if rev, err := c.p.Revisions.At(c.ctx, s.Started.Commit); err == nil {
			return rev
		}
	}
	return c.p.Revisions.Current()
}

// checkReasonForm checks a reason's form (Decision 9): a code of reasonCode's
// form, and a text of at most board.MaxReasonRunes characters. An act may
// take no reason. An error never quotes the reason.
func checkReasonForm(r *board.Reason) error {
	switch {
	case r == nil:
		return nil
	case !reasonCode.MatchString(r.Code):
		return fmt.Errorf("%w: a reason's code is a lowercase letter, then at most 31 lowercase letters and underscores", ErrInvalid)
	case utf8.RuneCountInString(r.Text) > board.MaxReasonRunes:
		return fmt.Errorf("%w: a reason's text holds at most %d characters", ErrInvalid, board.MaxReasonRunes)
	}
	return nil
}

// requestOf is an act's request (Decision 13): the key's hash, and the digest
// of the canonical JSON of its kind, its item, its reason's code, never of
// its text, which redaction may change, and the act's parameters, extra, as
// further keys (R98): an extension's dimensions named (see amountKeys), a
// link's other item. Promote, close and reopen pass none, so that their
// digests are those of the three keys alone, as before. A record keeps them,
// never the key.
func requestOf(kind string, id item.ID, a *Act, extra map[string]string) item.Request {
	code := ""
	if a.Reason != nil {
		code = a.Reason.Code
	}
	fields := map[string]string{"kind": kind, "item": string(id), "reason": code}
	for k, v := range extra {
		fields[k] = v
	}
	// Strings alone, which always encode, under keys that encode sorted.
	canonical, _ := json.Marshal(fields)
	return item.Request{Key: hashOf([]byte(a.Key)), Digest: hashOf(canonical)}
}

// hashOf is "sha256:" and the hexadecimal SHA-256 of b.
func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// findKey looks for req's key among the requests of s's records, the item's
// own (R42; Decision 13): found with req's digest, the act is a repeat, and
// findKey reports it; with another digest, the key named another act of the
// item, and findKey answers ErrKeyReused. It reads the item's records, not
// s's fold, which keeps the requests of the item's latest close and latest
// promotion alone: a close's key is found after a reopen too, and a reopen's
// at all. A load that fails is returned.
func (c *Coordinator) findKey(s *item.State, req item.Request) (found bool, err error) {
	r, err := c.findRecord(s, req)
	return r != nil, err
}

// findRecord is findKey that returns the record found, or nil.
func (c *Coordinator) findRecord(s *item.State, req item.Request) (*item.Record, error) {
	records, err := c.p.Records.Load(c.ctx, s.ID)
	if err != nil {
		return nil, fmt.Errorf("load item %s: %w", s.ID, err)
	}
	for i := range records {
		switch r := requestIn(&records[i].Body); {
		case r == nil || r.Key != req.Key:
		case r.Digest != req.Digest:
			return nil, ErrKeyReused
		default:
			return &records[i], nil
		}
	}
	return nil, nil
}

// requestIn is the request that b, the record of a person's act, keeps
// (Contract C19): a promotion's, a reopen's, a person's close's, an
// extension's, a link's or an unlink's; nil for any other record, an
// automatic close's among them, and for a platform act's, a firing resume
// or a source's flap link (R120): their keys hash "firing:" or "flap:" and
// an event id, which a person could send as a literal key.
func requestIn(b *item.Body) *item.Request {
	switch {
	case b.Promoted != nil:
		return &b.Promoted.Request
	case b.Extended != nil && b.Extended.Cause == item.ExtendFiring:
		return nil
	case b.Extended != nil:
		return &b.Extended.Request
	case b.Linked != nil && strings.HasPrefix(b.Linked.By, "source:"):
		return nil
	case b.Linked != nil:
		return &b.Linked.Request
	case b.Unlinked != nil:
		return &b.Unlinked.Request
	case b.Reopened != nil:
		return &b.Reopened.Request
	case b.Closed != nil:
		return b.Closed.Request
	}
	return nil
}

// carryOn checks an act on an item whose append failed with err (Decision
// 13), as Open checks an opening's (see unrecorded): the append may have
// landed, its answer lost on its way back. While this instance holds the
// lease, carryOn loads the item and looks for req's key among its records
// (see findKey). Found, the act's record landed: the act is a repeat, which
// carries on from the item's state now (see actFound), and carryOn reports
// it. Not found, the act changed nothing, and err is logged as a step that
// failed and returned, which the admin API answers 503. A lookup that fails
// too, its load or its key's, leaves the act's outcome unknown, which carryOn
// reports: the failure is logged, and the item's retry is armed (see retry),
// which after the retry wait loads the item again and advances it, its step
// carrying on from its state now, as a closed item's closed step drops its
// run; err is returned. The act acts on an unknown outcome as its record may
// have landed: a close drops the run, and a promotion counts its item (see
// closeNow and promoteNow). An err that found the lease gone fences the
// coordinator (see failed), and is returned with no load, as is one that
// came once the coordinator was fenced or its context ended (Decision 13):
// the act then answers ErrNotHeld (see heldThrough), and what landed is found
// by its key on the instance that leads next.
func (c *Coordinator) carryOn(ctx context.Context, id item.ID, req item.Request, err error) (repeat, unknown bool, _ error) {
	if !c.holds() || errors.Is(err, ErrLeaseLost) {
		c.failed(ctx, id, err)
		return false, false, err
	}
	s, lookup := c.load(ctx, id)
	if lookup == nil {
		if repeat, lookup = c.findKey(s, req); repeat {
			c.actFound(s)
			return true, false, nil
		}
	}
	c.failed(ctx, id, err)
	if lookup != nil {
		c.failed(ctx, id, lookup)
		c.retry(id)
	}
	return false, lookup != nil, err
}

// actFound carries on an act whose record its key found, as a repeat, or once
// its lost answer is found (Decision 13): it advances the item, whose step
// decides from its state now, as the closed step for an item closed, or the
// auto-close for one awaiting its close, and does nothing else on its own,
// but for one thing a close adds: an item closed now has its run dropped
// first (see drop), so that an advance that fails cannot leave a turn of it to
// start, and the run's snapshot forgotten, since the close's halt, if any,
// committed. So a close's key found after a reopen drops nothing.
func (c *Coordinator) actFound(s *item.State) {
	if s.Status() == item.StatusClosed && s.Run != nil {
		c.drop(s.Run.ID)
		delete(c.snapshots, s.Run.ID)
	}
	c.advance(c.ctx, s.ID)
}

// target loads the item an act acts on. An item this holding skips, or whose
// records hold one of a later version, is refused with ErrSkipped (Decision
// 19), and an id with no records with ErrNoItem.
func (c *Coordinator) target(ctx context.Context, id item.ID) (*item.State, error) {
	if c.isSkipped(id) {
		return nil, ErrSkipped
	}
	s, err := c.load(ctx, id)
	switch {
	case ofLaterVersion(err):
		return nil, fmt.Errorf("%w: %w", ErrSkipped, err)
	case err != nil:
		return nil, err
	case s.Opened == nil:
		return nil, ErrNoItem
	}
	return s, nil
}
