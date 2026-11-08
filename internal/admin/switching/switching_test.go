package switching_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// coordinator answers the kill switch as a test sets it: SetKillSwitch
// switched and switchErr, RecordKillSwitchOn recorded and recordErr. It keeps
// the order of its calls, and the epochs it was asked to record under.
type coordinator struct {
	switched  app.KillSwitchOutcome
	switchErr error
	recorded  app.KillSwitchOutcome
	recordErr error
	calls     []string
	epochs    []int64
}

func (c *coordinator) SetKillSwitch(_ context.Context, _ bool, _ *app.Act) (app.KillSwitchOutcome, error) {
	c.calls = append(c.calls, "SetKillSwitch")
	return c.switched, c.switchErr
}

func (c *coordinator) RecordKillSwitchOn(_ context.Context, epoch int64, _ *app.Act) (app.KillSwitchOutcome, error) {
	c.calls = append(c.calls, "RecordKillSwitchOn")
	c.epochs = append(c.epochs, epoch)
	return c.recorded, c.recordErr
}

// leader is the leader's status as a test sets it.
type leader struct {
	epoch          int64
	held, draining bool
}

func (l leader) Status() (epoch int64, held, draining bool) { return l.epoch, l.held, l.draining }

// The leader's standings: a leader holding the lease, taking over, as its
// coordinator is not held yet; a draining leader; one draining that lost the
// lease; and a standby.
var (
	takingOver   = leader{epoch: 7, held: true}
	draining     = leader{epoch: 7, held: true, draining: true}
	drainingLost = leader{epoch: 7, draining: true}
	standby      = leader{epoch: 7}
)

// logs holds what the routing logged, as JSON lines.
type logs struct{ buf bytes.Buffer }

func (l *logs) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(l.buf.String()) {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		out = append(out, m)
	}
	return out
}

// ports returns the routing's ports over c and l, logging into a fresh logs.
func ports(c *coordinator, l leader) (switching.Ports, *logs) {
	out := &logs{}
	return switching.Ports{Coordinator: c, Leader: l, Log: slog.New(slog.NewJSONHandler(&out.buf, nil))}, out
}

// on is an on's act, with a reason.
func on() *app.Act {
	return &app.Act{Actor: app.Actor{Subject: "admin", Admin: true}, After: 4, Reason: &board.Reason{Code: "outage"}}
}

// errLost is a write that found the lease gone.
var errLost = fmt.Errorf("record the kill switch: %w", app.ErrLeaseLost)

// An on that SetKillSwitch refuses as not held while the leader drains is
// recorded through RecordKillSwitchOn under the draining holding's epoch, and
// answered deferred with what the store answered (Decision 5): the admin
// API's and the web's on alike. An off, or a preview, during the drain is
// refused draining, and recorded by neither; a leader that drains and lost
// the lease refuses the on, and its preview, not_held.
func TestSetDuringADrainRecordsTheOnDeferred(t *testing.T) {
	c := &coordinator{switchErr: app.ErrNotHeld, recorded: app.KillSwitchOutcome{On: true, Seq: 5, Changed: true, Deferred: true}}
	p, l := ports(c, draining)

	got := switching.Set(t.Context(), p, true, on())

	require.Nil(t, got.Refusal)
	assert.True(t, got.Deferred)
	assert.Equal(t, &app.KillSwitchOutcome{On: true, Seq: 5, Changed: true, Deferred: true}, got.Switched)
	assert.Equal(t, []string{"SetKillSwitch", "RecordKillSwitchOn"}, c.calls)
	assert.Equal(t, []int64{7}, c.epochs)
	assert.Empty(t, l.lines(t))

	for name, tc := range map[string]struct {
		leader  leader
		on      bool
		preview bool
		code    string
	}{
		"an off":                          {leader: draining, code: switching.CodeDraining},
		"an on's preview":                 {leader: draining, on: true, preview: true, code: switching.CodeDraining},
		"an off's preview":                {leader: draining, preview: true, code: switching.CodeDraining},
		"an on, the lease lost":           {leader: drainingLost, on: true, code: switching.CodeNotHeld},
		"an on's preview, the lease lost": {leader: drainingLost, on: true, preview: true, code: switching.CodeNotHeld},
		"an off, the lease lost":          {leader: drainingLost, code: switching.CodeDraining},
	} {
		c := &coordinator{switchErr: app.ErrNotHeld}
		p, _ := ports(c, tc.leader)
		act := on()
		act.Preview = tc.preview

		got := switching.Set(t.Context(), p, tc.on, act)

		require.NotNil(t, got.Refusal, name)
		assert.Equal(t, switching.Refusal{Status: http.StatusConflict, Code: tc.code, Err: app.ErrNotHeld}, *got.Refusal, name)
		assert.Nil(t, got.Switched, name)
		assert.False(t, got.Deferred, name)
		assert.Equal(t, []string{"SetKillSwitch"}, c.calls, "%s: not recorded", name)
	}

	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
		level  string
	}{
		"the guard's refusal": {err: app.ErrNotHeld, status: http.StatusConflict, code: switching.CodeNotHeld, level: "WARN"},
		"a lost answer":       {err: errors.New("the answer was lost"), status: http.StatusServiceUnavailable, code: switching.CodeUnavailable, level: "ERROR"},
		"a refusal":           {err: &app.Refused{Code: app.CodeResponderOnly}, status: http.StatusConflict, code: app.CodeResponderOnly, level: "WARN"},
	} {
		c := &coordinator{switchErr: app.ErrNotHeld, recordErr: tc.err}
		p, l := ports(c, draining)

		got := switching.Set(t.Context(), p, true, on())

		require.NotNil(t, got.Refusal, name)
		assert.Equal(t, tc.status, got.Refusal.Status, name)
		assert.Equal(t, tc.code, got.Refusal.Code, name)
		assert.Equal(t, []string{"SetKillSwitch", "RecordKillSwitchOn"}, c.calls, "%s: sent to neither method again", name)
		lines := l.lines(t)
		require.Len(t, lines, 1, name)
		assert.Equal(t, tc.level, lines[0]["level"], name)
		assert.InDelta(t, 7, lines[0]["epoch"], 0, name)
		assert.Equal(t, tc.err.Error(), lines[0]["err"], name)
	}
}

// An item act's refusal as not held, and an opening's, read the leader's
// status, as the kill switch's does (I3): draining while it drains, the lease
// held or lost, 409; taking over while it holds the lease and does not drain,
// and the coordinator is not held, 503 with a retry after a second; and not
// held otherwise, a standby, or a write that found the lease gone, 409. An
// act taking over is logged at Warn; an opening's standing is logged never.
// A request to the kill switch answers so too, but for the drain's on.
func TestActAndOpeningReadTheLeadersStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		leader     leader
		err        error
		status     int
		code       string
		retryAfter time.Duration
	}{
		{name: "a draining leader", leader: draining, err: app.ErrNotHeld, status: http.StatusConflict, code: switching.CodeDraining},
		{name: "a draining leader that lost the lease", leader: drainingLost, err: errLost, status: http.StatusConflict, code: switching.CodeDraining},
		{name: "a leader taking over", leader: takingOver, err: app.ErrNotHeld, status: http.StatusServiceUnavailable, code: switching.CodeTakingOver, retryAfter: time.Second},
		{name: "a leader whose write found the lease gone", leader: takingOver, err: errLost, status: http.StatusConflict, code: switching.CodeNotHeld},
		{name: "a standby", leader: standby, err: app.ErrNotHeld, status: http.StatusConflict, code: switching.CodeNotHeld},
		{name: "a standby whose write found the lease gone", leader: standby, err: errLost, status: http.StatusConflict, code: switching.CodeNotHeld},
	} {
		want := &switching.Refusal{Status: tc.status, Code: tc.code, RetryAfter: tc.retryAfter, Err: tc.err}
		logged := 0
		if tc.code == switching.CodeTakingOver {
			logged = 1
		}

		p, l := ports(&coordinator{}, tc.leader)
		assert.Equal(t, want, switching.Act(t.Context(), p, tc.err), "an act: %s", tc.name)
		assert.Len(t, l.lines(t), logged, "an act: %s", tc.name)

		p, l = ports(&coordinator{}, tc.leader)
		assert.Equal(t, want, switching.Opening(t.Context(), p, tc.err), "an opening: %s", tc.name)
		assert.Empty(t, l.lines(t), "an opening: %s", tc.name)

		c := &coordinator{switchErr: tc.err}
		p, l = ports(c, tc.leader)
		got := switching.Set(t.Context(), p, false, on())
		assert.Equal(t, want, got.Refusal, "an off: %s", tc.name)
		assert.Len(t, l.lines(t), logged, "an off: %s", tc.name)
	}
}

// Each refusal of an item act, and of a request to the kill switch, has its
// code and its status (Contract C4; R15): each code of the coordinator's
// refusals, a 409, but reason_required and self, each a 400; an item a
// later build holds, a key reused and a newer row, 409; no such item, 404; an
// invalid request, 400; an extension of a run that is stopping, 503 stopping, to be sent
// again in a second, logged at Warn (R99; M1); and any other error, a
// promotion while the kill switch's write is unsettled among them, 503
// unavailable, logged, at Warn while unsettled and at Error otherwise. An opening's refusals map the same, but for its own
// errors (see TestTheOpeningRouteAnswersItsCodes). The refusal carries the
// error, for a front end's words.
func TestSetActAndOpeningMapEveryRefusalCode(t *testing.T) {
	type mapped struct {
		err        error
		status     int
		code       string
		level      string
		retryAfter time.Duration
	}
	cases := map[string]mapped{
		"reason_required":     {err: &app.Refused{Code: app.CodeReasonRequired}, status: http.StatusBadRequest, code: app.CodeReasonRequired},
		"self":                {err: fmt.Errorf("link: %w", &app.Refused{Code: app.CodeSelf}), status: http.StatusBadRequest, code: app.CodeSelf},
		"held_by_later_build": {err: app.ErrSkipped, status: http.StatusConflict, code: switching.CodeHeldByLaterBuild},
		"an item of a later version": {
			err: fmt.Errorf("record: %w", item.ErrVersion), status: http.StatusConflict, code: switching.CodeHeldByLaterBuild,
		},
		"a board event of a later version": {err: board.ErrVersion, status: http.StatusConflict, code: switching.CodeHeldByLaterBuild},
		"key_reused":                       {err: app.ErrKeyReused, status: http.StatusConflict, code: switching.CodeKeyReused},
		"stale":                            {err: app.ErrStale, status: http.StatusConflict, code: switching.CodeStale},
		"no_such_item":                     {err: app.ErrNoItem, status: http.StatusNotFound, code: switching.CodeNoSuchItem},
		"invalid":                          {err: fmt.Errorf("%w: a reason's code is a lowercase word", app.ErrInvalid), status: http.StatusBadRequest, code: switching.CodeInvalid},
		"unavailable, unsettled":           {err: app.ErrUnsettled, status: http.StatusServiceUnavailable, code: switching.CodeUnavailable, level: "WARN"},
		"unavailable":                      {err: errors.New("the database blinked"), status: http.StatusServiceUnavailable, code: switching.CodeUnavailable, level: "ERROR"},
		"stopping": {
			err: fmt.Errorf("extend: %w", app.ErrStopping), status: http.StatusServiceUnavailable, code: switching.CodeStopping, level: "WARN", retryAfter: time.Second,
		},
	}
	for _, code := range []string{
		app.CodeClosed, app.CodeReopenWindowPassed, app.CodeLimitReached, app.CodeKillSwitchOn, app.CodeDailyCapReached, app.CodeAdminOnly,
		app.CodeNotItemResponder, app.CodeResponderOnly, app.CodeNotStarted, app.CodeNoAgentsLeft, app.CodeWouldStopAtOnce,
		app.CodeRulingsWindowPassed,
	} {
		cases[code] = mapped{err: fmt.Errorf("promote: %w", &app.Refused{Code: code}), status: http.StatusConflict, code: code}
	}
	for name, tc := range cases {
		want := &switching.Refusal{Status: tc.status, Code: tc.code, RetryAfter: tc.retryAfter, Err: tc.err}
		logged := 0
		if tc.level != "" {
			logged = 1
		}

		p, l := ports(&coordinator{}, takingOver)
		assert.Equal(t, want, switching.Act(t.Context(), p, tc.err), "an act: %s", name)
		lines := l.lines(t)
		require.Len(t, lines, logged, "an act: %s", name)
		if logged == 1 {
			assert.Equal(t, tc.level, lines[0]["level"], name)
			assert.Equal(t, tc.err.Error(), lines[0]["err"], name)
		}

		c := &coordinator{switchErr: tc.err}
		p, l = ports(c, takingOver)
		got := switching.Set(t.Context(), p, true, on())
		assert.Equal(t, want, got.Refusal, "the kill switch: %s", name)
		assert.Nil(t, got.Switched, name)
		assert.Len(t, l.lines(t), logged, "the kill switch: %s", name)
		assert.Equal(t, []string{"SetKillSwitch"}, c.calls, name)
	}

	// The opening's refusals, beside its own errors: each code of the
	// coordinator's refusals, an item a later build holds, a key reused, and
	// an invalid opening.
	for name, tc := range cases {
		if tc.code == switching.CodeStale || tc.code == switching.CodeNoSuchItem || tc.code == switching.CodeUnavailable || tc.code == switching.CodeStopping {
			continue
		}
		p, l := ports(&coordinator{}, takingOver)
		assert.Equal(t, &switching.Refusal{Status: tc.status, Code: tc.code, Err: tc.err}, switching.Opening(t.Context(), p, tc.err), "an opening: %s", name)
		assert.Empty(t, l.lines(t), "an opening: %s", name)
	}

	// A request to the kill switch that the coordinator answers is answered
	// with what it did, a preview's consequences included.
	c := &coordinator{switched: app.KillSwitchOutcome{On: true, Seq: 5, Changed: true}}
	p, _ := ports(c, takingOver)
	got := switching.Set(t.Context(), p, true, on())
	assert.Equal(t, switching.Result{Switched: &app.KillSwitchOutcome{On: true, Seq: 5, Changed: true}}, got)
}

// The opening's refusals carry R15's codes (Plan 7b-1 R15, which left the
// opening route for Plan 9a): an item a later build holds, however it
// reached the coordinator's answer, 409 held_by_later_build; a key reused,
// 409 key_reused; an invalid opening, 400 invalid; an opening refused by a
// role, 409 by its code; and no current revision, an opening still being
// recorded, and any other error, 503 unavailable. The last two are logged at
// Error, beside what the caller's log names, the item; no current revision
// is not logged, as it passes once a configuration is current.
func TestTheOpeningRouteAnswersItsCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
		logged bool
	}{
		{name: "skipped", err: app.ErrSkipped, status: http.StatusConflict, code: switching.CodeHeldByLaterBuild},
		{name: "skipped, of a later version", err: fmt.Errorf("%w: %w", app.ErrSkipped, item.ErrVersion), status: http.StatusConflict, code: switching.CodeHeldByLaterBuild},
		{name: "a record of a later version", err: fmt.Errorf("record item x: %w", item.ErrVersion), status: http.StatusConflict, code: switching.CodeHeldByLaterBuild},
		{name: "the key reused", err: app.ErrKeyReused, status: http.StatusConflict, code: switching.CodeKeyReused},
		{name: "invalid", err: fmt.Errorf("%w: no such work item type", app.ErrInvalid), status: http.StatusBadRequest, code: switching.CodeInvalid},
		{name: "a viewer", err: &app.Refused{Code: app.CodeResponderOnly}, status: http.StatusConflict, code: app.CodeResponderOnly},
		{name: "no revision", err: app.ErrNoRevision, status: http.StatusServiceUnavailable, code: switching.CodeUnavailable},
		{
			name: "still being recorded", err: fmt.Errorf("record item x: %w: an item opens with its first record, once", item.ErrInvalid),
			status: http.StatusServiceUnavailable, code: switching.CodeUnavailable, logged: true,
		},
		{name: "the stores failed", err: errors.New("the database blinked"), status: http.StatusServiceUnavailable, code: switching.CodeUnavailable, logged: true},
	} {
		out := &logs{}
		p := switching.Ports{Leader: takingOver, Log: slog.New(slog.NewJSONHandler(&out.buf, nil)).With("item", "x")}

		got := switching.Opening(t.Context(), p, tc.err)

		assert.Equal(t, &switching.Refusal{Status: tc.status, Code: tc.code, Err: tc.err}, got, tc.name)
		lines := out.lines(t)
		if !tc.logged {
			assert.Empty(t, lines, tc.name)
			continue
		}
		require.Len(t, lines, 1, tc.name)
		assert.Equal(t, "ERROR", lines[0]["level"], tc.name)
		assert.Equal(t, "x", lines[0]["item"], tc.name)
		assert.Equal(t, tc.err.Error(), lines[0]["err"], tc.name)
	}
}

// A routing whose ports carry no log logs nothing, and answers all the same.
func TestARoutingWithoutALogAnswers(t *testing.T) {
	p := switching.Ports{Coordinator: &coordinator{switchErr: errors.New("the database blinked")}, Leader: takingOver}

	assert.Equal(t, switching.CodeUnavailable, switching.Act(t.Context(), p, errors.New("x")).Code)
	assert.Equal(t, switching.CodeUnavailable, switching.Opening(t.Context(), p, errors.New("x")).Code)
	assert.Equal(t, switching.CodeUnavailable, switching.Set(t.Context(), p, true, on()).Refusal.Code)
}
