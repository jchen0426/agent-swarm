// Package switching is the routing of the kill switch, and the refusals of
// the item acts and of the opening, as values that both front ends use, the
// admin API and the web (Plan 9a-1, R81). It decides where a request to the
// kill switch goes, notably an on during a drain, which goes to
// RecordKillSwitchOn, and which code and status each refusal answers, by how
// the instance stands. Each front end words the codes itself: the admin API's
// texts carry its own instructions, and the web has its own words.
package switching

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The codes of the refusals the routing gives, beside those of the
// coordinator's refusals, which it answers as given (Contract C4; R15).
const (
	CodeInvalid          = "invalid"
	CodeNoSuchItem       = "no_such_item"
	CodeKeyReused        = "key_reused"
	CodeStale            = "stale"
	CodeHeldByLaterBuild = "held_by_later_build"
	CodeNotHeld          = "not_held"
	CodeDraining         = "draining"
	CodeTakingOver       = "taking_over"
	CodeUnavailable      = "unavailable"
	// CodeStopping refuses an extension of a run that is stopping (R99),
	// to be sent again in a second.
	CodeStopping = "stopping"
)

// HeldByLaterBuild is the text of a refusal, held_by_later_build, of an item
// a later build holds (Contract C4).
const HeldByLaterBuild = "held by a later build"

// retryAfter is when a request answered taking_over, or stopping, is tried
// again.
const retryAfter = time.Second

// refusedTexts are the texts of the coordinator's refusals, by their codes
// (Contract C4).
var refusedTexts = map[string]string{
	app.CodeClosed:             "the item is closed; reopen it first",
	app.CodeReopenWindowPassed: "the item closed longer ago than the reopen window",
	app.CodeLimitReached:       "the items investigating would pass the limit and one",
	app.CodeKillSwitchOn:       "the kill switch is on",
	app.CodeDailyCapReached:    "the day's spend reached the daily cap",
	app.CodeAdminOnly:          "only an admin turns the kill switch off",
	app.CodeNotStarted:         "the item has no run to extend; promote it first",
	app.CodeNoAgentsLeft:       "the run has no agent left to invite",
	app.CodeWouldStopAtOnce:    "the run's usage would still exhaust the budget extended; extend it by more",
}

// RefusedText is the text of the coordinator's refusal of code, if it has
// one of its own (Contract C4).
func RefusedText(code string) (string, bool) {
	text, ok := refusedTexts[code]
	return text, ok
}

// Invalid is the text of a refusal, invalid, of what, an opening or a
// lifecycle request, that the coordinator refused as invalid with err: what,
// then the coordinator's reason, which follows its sentinel's words and never
// quotes the request.
func Invalid(what string, err error) string {
	if _, reason, ok := strings.Cut(err.Error(), app.ErrInvalid.Error()+": "); ok {
		return what + " is invalid: " + reason
	}
	return what + " is invalid"
}

// Ports are what the routing acts and reads through.
type Ports struct {
	// Coordinator is the coordinator (app.Coordinator): it sets the kill
	// switch, and records its on during a drain.
	Coordinator interface {
		SetKillSwitch(ctx context.Context, on bool, a *app.Act) (app.KillSwitchOutcome, error)
		RecordKillSwitchOn(ctx context.Context, epoch int64, a *app.Act) (app.KillSwitchOutcome, error)
	}
	// Leader is the leader: its holding's epoch, whether it holds the lease,
	// and whether it drains, by which a refusal as not held is answered, and
	// the kill switch's on during a drain recorded.
	Leader interface {
		Status() (epoch int64, held, draining bool)
	}
	// Log receives the refusals answered unavailable, a request that came
	// while the instance takes over, and an on that could not be recorded
	// during a drain, each with the error. The caller names the request in
	// it, by its attributes. Nil logs nothing.
	Log *slog.Logger
}

// log is p's log, or one that logs nothing.
func (p *Ports) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}

// Refusal is a request refused: the status a front end answers, its code
// (Contract C4), and, for taking_over, when to try it again. Err is the
// error refused, for a front end's words, such as an invalid request's
// reason (see Invalid); no front end shows it as is.
type Refusal struct {
	Status     int
	Code       string
	RetryAfter time.Duration
	Err        error
}

// Result is what a request to the kill switch came to: what the coordinator
// did, Deferred when the on was recorded during a drain for the next leader
// to act on; or its refusal.
type Result struct {
	Switched *app.KillSwitchOutcome
	Deferred bool
	Refusal  *Refusal
}

// Set turns the kill switch on or off, or previews it, as act says (Decision
// 5; Contract C4). A request the coordinator refused as not held is routed by
// how the instance stands: while the leader holds the lease and drains, an
// on, not its preview, is recorded under the draining holding (see recordOn),
// and a preview, or an off, is refused draining, since a fenced coordinator
// holds no account to preview from; while it holds the lease and does not
// drain yet, the request is refused taking_over, to be tried again, which
// finds the drain; and otherwise not_held, a draining leader that lost the
// lease included for the on and any preview, as it holds no holding to
// record under. Any other refusal is answered as Act answers it.
func Set(ctx context.Context, p Ports, on bool, act *app.Act) Result {
	out, err := p.Coordinator.SetKillSwitch(ctx, on, act)
	switch {
	case unheld(err):
		epoch, held, draining := p.Leader.Status()
		code := standing(held, draining, err)
		if (on || act.Preview) && draining && !held {
			code = CodeNotHeld
		}
		if code == CodeDraining && on && !act.Preview {
			return recordOn(ctx, &p, epoch, act)
		}
		return Result{Refusal: standingRefusal(ctx, &p, code, err)}
	case err != nil:
		return Result{Refusal: refused(ctx, &p, err)}
	}
	return Result{Switched: &out}
}

// recordOn records the kill switch's on during a drain, under the draining
// holding's epoch (Decision 5; see Coordinator.RecordKillSwitchOn): deferred,
// as the store answered it. RecordKillSwitchOn logs none of its failures, so
// each is logged here, with the epoch. Its refusal as not held, the store's
// guard's once the draining holding ended, is refused not_held, and the on is
// sent to neither method again: the client's retry finds the next leader, and
// SetKillSwitch there (R24). Any other is answered as a refusal of
// SetKillSwitch would be, a lost answer unavailable.
func recordOn(ctx context.Context, p *Ports, epoch int64, act *app.Act) Result {
	out, err := p.Coordinator.RecordKillSwitchOn(ctx, epoch, act)
	if err == nil {
		return Result{Switched: &out, Deferred: true}
	}
	const msg = "the kill switch's on could not be recorded during the drain"
	if unheld(err) {
		p.log().WarnContext(ctx, msg, "epoch", epoch, "err", err)
		return Result{Refusal: &Refusal{Status: http.StatusConflict, Code: CodeNotHeld, Err: err}}
	}
	r := refusal(err)
	if r.Status == http.StatusServiceUnavailable {
		p.log().ErrorContext(ctx, msg, "epoch", epoch, "err", err)
	} else {
		p.log().WarnContext(ctx, msg, "epoch", epoch, "err", err)
	}
	return Result{Refusal: r}
}

// Act is the refusal of an item act, promote, close, reopen or extend, that
// the coordinator refused with err (Contract C4). A refusal as not held is
// answered by how the instance stands, from the leader's status (see
// standing), a request that came while the instance takes over logged at
// Warn. Any other is answered as refusal gives it, and one answered 503 is
// logged: at Error, but an act refused while a kill switch write's outcome
// is unknown (R37), at Warn, as the coordinator logs the write's own
// failure, and the client sends the act again once a read settles the
// switch; and an extension of a run that is stopping (R99; M1), at Warn, as
// the client sends it again in a second. It logs with ctx.
func Act(ctx context.Context, p Ports, err error) *Refusal {
	if unheld(err) {
		_, held, draining := p.Leader.Status()
		return standingRefusal(ctx, &p, standing(held, draining, err), err)
	}
	return refused(ctx, &p, err)
}

// refused is refusal's answer to err, logged as Act says.
func refused(ctx context.Context, p *Ports, err error) *Refusal {
	r := refusal(err)
	switch {
	case r.Status != http.StatusServiceUnavailable:
	case errors.Is(err, app.ErrUnsettled), errors.Is(err, app.ErrStopping):
		p.log().WarnContext(ctx, "a lifecycle request failed", "err", err)
	default:
		p.log().ErrorContext(ctx, "a lifecycle request failed", "err", err)
	}
	return r
}

// Opening is the refusal of an opening that the coordinator refused with err
// (Contract C4; R15). A refusal as not held is answered by how the instance
// stands, from the leader's status (see standing), logged never. An item a
// later build holds, a key reused, an invalid opening and a refusal of the
// coordinator's are answered as refusal gives them. No current revision, an
// opening still being recorded, and any other error, are refused unavailable,
// to be tried again; the last two are logged at Error, with ctx, where the
// caller's log names the item.
func Opening(ctx context.Context, p Ports, err error) *Refusal {
	var r *app.Refused
	switch {
	case unheld(err):
		_, held, draining := p.Leader.Status()
		code := standing(held, draining, err)
		return standingAnswer(code, err)
	case errors.Is(err, app.ErrSkipped), errors.Is(err, item.ErrVersion), errors.Is(err, board.ErrVersion),
		errors.Is(err, app.ErrKeyReused), errors.Is(err, app.ErrInvalid), errors.As(err, &r):
		// An opening that races another of a later version can meet the
		// later record unwrapped by ErrSkipped (Plan 7d-1, Task 8).
		return refusal(err)
	case errors.Is(err, app.ErrNoRevision):
	case errors.Is(err, item.ErrInvalid):
		// An opening whose first write landed after its caller saw an error
		// can make a quick retry meet it as it records: the next retry gets
		// the item (Plan 7d-1, Task 8). A corrupt item answers so too, and is
		// logged.
		p.log().ErrorContext(ctx, "an opening could not be answered", "err", err)
	default:
		p.log().ErrorContext(ctx, "an opening failed", "err", err)
	}
	return &Refusal{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Err: err}
}

// unheld reports whether err refuses a request as not held: the coordinator
// does not act under the lease, or a write found the lease gone.
func unheld(err error) bool {
	return errors.Is(err, app.ErrNotHeld) || errors.Is(err, app.ErrLeaseLost)
}

// standing is how the instance stands for a request the coordinator refused
// as not held with err, by its leader's status, held and draining (Contract
// C4): draining, while the leader drains, and takes no new work; taking
// over, while it holds the lease, does not drain, and err is ErrNotHeld, as
// its coordinator then acts in a moment; and not holding the lease
// otherwise, as on a standby, or once a write found the lease gone.
func standing(held, draining bool, err error) string {
	switch {
	case draining:
		return CodeDraining
	case held && errors.Is(err, app.ErrNotHeld):
		return CodeTakingOver
	}
	return CodeNotHeld
}

// standingRefusal is standingAnswer's refusal, a request taking over logged
// at Warn, where a client would not try a 409 again.
func standingRefusal(ctx context.Context, p *Ports, code string, err error) *Refusal {
	if code == CodeTakingOver {
		p.log().WarnContext(ctx, "a lifecycle request came while this instance takes over", "err", err)
	}
	return standingAnswer(code, err)
}

// standingAnswer is the refusal of a request refused as not held with err,
// as the instance stands, code (see standing): draining or not holding the
// lease, 409; taking over, 503, to be tried again in a second.
func standingAnswer(code string, err error) *Refusal {
	if code == CodeTakingOver {
		return &Refusal{Status: http.StatusServiceUnavailable, Code: code, RetryAfter: retryAfter, Err: err}
	}
	return &Refusal{Status: http.StatusConflict, Code: code, Err: err}
}

// refusal is the refusal C4 gives a request that the coordinator refused
// with err, not as not held: an item a later build holds; a key that named
// another act of the item, or opened an item for another request; a row of
// the kill switch newer than after; no such item; an invalid request; a
// refusal of the item's state, of the lifecycle or of a role, by its code,
// reason_required a 400; an extension of a run that is stopping, stopping,
// 503, to be sent again in a second (R99); and any other, a lost answer the
// act could not check yet included, and a promotion while a kill switch
// write's outcome is unknown (R37), unavailable, 503, to be sent again.
func refusal(err error) *Refusal {
	status, code := http.StatusServiceUnavailable, CodeUnavailable
	var refused *app.Refused
	switch {
	case errors.Is(err, app.ErrStopping):
		return &Refusal{Status: http.StatusServiceUnavailable, Code: CodeStopping, RetryAfter: retryAfter, Err: err}
	case errors.Is(err, app.ErrSkipped), errors.Is(err, item.ErrVersion), errors.Is(err, board.ErrVersion):
		status, code = http.StatusConflict, CodeHeldByLaterBuild
	case errors.Is(err, app.ErrKeyReused):
		status, code = http.StatusConflict, CodeKeyReused
	case errors.Is(err, app.ErrStale):
		status, code = http.StatusConflict, CodeStale
	case errors.Is(err, app.ErrNoItem):
		status, code = http.StatusNotFound, CodeNoSuchItem
	case errors.Is(err, app.ErrInvalid):
		status, code = http.StatusBadRequest, CodeInvalid
	case errors.As(err, &refused) && refused.Code == app.CodeReasonRequired:
		status, code = http.StatusBadRequest, refused.Code
	case errors.As(err, &refused):
		status, code = http.StatusConflict, refused.Code
	}
	return &Refusal{Status: status, Code: code, Err: err}
}
