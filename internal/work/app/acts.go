package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Actor is who acts (Decision 13): the person Plan 9a signs in, or, through
// the admin API, the subject admin, an admin since its token is an admin's.
// Records keep the subject.
type Actor struct {
	Subject string
	Admin   bool
}

// Act is a person's request (Decision 13): who, why, what makes a retry
// safe, and whether to act or only to show the consequences.
type Act struct {
	Actor Actor
	// Key is the Idempotency-Key of an act on an item, 1 to 128 printable
	// ASCII characters (R20), which the act's record keeps hashed, never as it
	// is; empty for a preview.
	Key string
	// After is the kill switch's latest row the sender saw: the kill switch's
	// alone.
	After int64
	// Reason is the act's one reason, if any.
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
// auto-close is paused; and the items investigating and the limit.
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
)

// maxKey caps an act's key, in characters, each printable ASCII, as the
// admin API caps an opening's (R20).
const maxKey = 128

// reasonCode is the form of a reason's code (Decision 9).
var reasonCode = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// checkAct checks the form of an act on an item: its key, of 1 to maxKey
// printable ASCII characters (R20), which the coordinator checks as the admin
// API does, since Plan 9a calls the acts directly; and its reason (see
// checkReasonForm). An error never quotes the key or the reason.
func checkAct(a *Act) error {
	switch {
	case a == nil:
		return fmt.Errorf("%w: no act", ErrInvalid)
	case a.Key == "" || len(a.Key) > maxKey || strings.ContainsFunc(a.Key, func(r rune) bool { return r < ' ' || r > '~' }):
		return fmt.Errorf("%w: an act on an item carries an Idempotency-Key of 1 to %d printable ASCII characters", ErrInvalid, maxKey)
	}
	return checkReasonForm(a.Reason)
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
// of the canonical JSON of its kind, its item and its reason's code, never of
// its text, which redaction may change. A record keeps them, never the key.
func requestOf(kind string, id item.ID, a *Act) item.Request {
	code := ""
	if a.Reason != nil {
		code = a.Reason.Code
	}
	// Strings alone, which always encode, under keys that encode sorted.
	canonical, _ := json.Marshal(map[string]string{"kind": kind, "item": string(id), "reason": code})
	return item.Request{Key: hashOf([]byte(a.Key)), Digest: hashOf(canonical)}
}

// hashOf is "sha256:" and the hexadecimal SHA-256 of b.
func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
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
