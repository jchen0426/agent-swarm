package docker

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// labelsOf returns the labels the runner's sandbox gives the turn's
// container (Contract C13), as the holder's, with deadline as the turn's
// deadline: the sweep reads what a runner writes.
func (e *env) labelsOf(holder string, turn *app.Turn, deadline time.Time) map[string]string {
	o := e.o
	o.Holder = holder
	at := *turn
	at.Deadline = deadline
	_, cfg, _ := o.sandbox(&at)
	return cfg.Labels
}

// TestOpenRefusesAHolderAnotherServeHoldsBeforeItSweeps (Review Focus 4;
// Decision 8): Open takes the holder's lock before its start sweep. With the
// lock free, Open lists the tenant's containers, and removes the running
// one its holder's previous process left; with the lock another serve holds,
// Open fails naming holder, the fake saw no List, Kill or Remove, and that
// serve's running container is still there.
func TestOpenRefusesAHolderAnotherServeHoldsBeforeItSweeps(t *testing.T) {
	for _, row := range []struct {
		name   string
		locked bool
	}{{name: "the lock free"}, {name: "the lock another serve holds", locked: true}} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newFixture(t)
				id := e.fe.leave(left{labels: e.labelsOf(e.o.Holder, newTurn("alpha"), time.Now().Add(time.Minute)), running: true, created: time.Now()})
				if row.locked {
					unlock, err := lockHolder(e.o.Store, e.o.Tenant, e.o.Holder)
					require.NoError(t, err)
					t.Cleanup(unlock)
				}

				r, err := Open(t.Context(), e.ports(), e.o)

				if r != nil {
					t.Cleanup(func() { _ = r.Close() })
				}
				if !row.locked {
					require.NoError(t, err)
					assert.Contains(t, e.fe.said(), "List agentswarm.tenant="+e.o.Tenant, "Open did not list the tenant's containers")
					assert.False(t, e.fe.holds(id), "the previous process's container is still there")
					return
				}
				require.Error(t, err)
				assert.True(t, strings.HasPrefix(err.Error(), "holder: "), "the refusal %q does not name holder", err)
				for _, method := range []string{"List", "Kill", "Remove"} {
					assert.Empty(t, e.fe.callsOf(method), "Open called %s", method)
				}
				assert.True(t, e.fe.holds(id), "the other serve's container is gone")
			})
		})
	}
}

// TestTheStartSweepRemovesThePreviousProcesssContainers (Decision 5): at
// Open, before any turn, the runner tracks nothing, so every container of
// its own holder is its previous process's: a running one is killed and then
// removed, a stopped and a never-started one removed, each by its id, though
// none is due by the minute's rules, its deadline to come and its end, or
// its creation, a moment ago; each removal is logged, with its holder.
// Another holder's, running, stopped or never started, are left.
func TestTheStartSweepRemovesThePreviousProcesssContainers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixture(t)
		now := time.Now()
		leave := func(holder string, running bool, finished time.Time) string {
			return e.fe.leave(left{labels: e.labelsOf(holder, newTurn("alpha"), now.Add(time.Minute)), running: running, created: now, finished: finished})
		}
		running, stopped, never := leave(e.o.Holder, true, time.Time{}), leave(e.o.Holder, false, now), leave(e.o.Holder, false, time.Time{})
		others := []string{leave("other", true, time.Time{}), leave("other", false, now), leave("other", false, time.Time{})}

		e.open(t)

		assert.Equal(t, []string{"Inspect " + running, "Kill " + running, "Remove " + running}, e.fe.saidOf(running))
		assert.Equal(t, []string{"Inspect " + stopped, "Remove " + stopped}, e.fe.saidOf(stopped))
		assert.Equal(t, []string{"Inspect " + never, "Remove " + never}, e.fe.saidOf(never))
		for _, id := range []string{running, stopped, never} {
			assert.False(t, e.fe.holds(id), "the previous process's container %s is still there", id)
		}
		for _, id := range others {
			assert.True(t, e.fe.holds(id), "another holder's container %s is gone", id)
			for _, c := range e.fe.saidOf(id) {
				assert.False(t, strings.HasPrefix(c, "Kill ") || strings.HasPrefix(c, "Remove "), "the sweep sent %q to another holder's container", c)
			}
		}
		lines := logged(t, e.logs, "the sweep removed a container")
		removed := make([]string, 0, len(lines))
		for _, line := range lines {
			assert.Equal(t, "INFO", line["level"])
			assert.Equal(t, e.o.Holder, line["holder"])
			removed = append(removed, fmt.Sprint(line["container"]))
		}
		assert.ElementsMatch(t, []string{running, stopped, never}, removed)
	})
}

// TestTheSweepNeverTouchesATrackedContainer (Decision 5): a container whose
// agentswarm.turn names a turn the runner has is tracked, and the minute's
// sweep neither inspects, kills nor removes it, whatever its holder, its
// state and its times: here the turn's own container, and four others of
// its label, each long due were it untracked.
func TestTheSweepNeverTouchesATrackedContainer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		turn := newTurn("alpha")
		turn.Deadline = time.Now().Add(time.Hour)
		e.start(t, turn)
		synctest.Wait()
		require.Len(t, e.launched(), 1)
		own := e.fe.byTurn(turn.ID)
		require.NotNil(t, own, "the turn has no container")
		long := time.Now().Add(-time.Hour)
		tracked := []string{
			own.id,
			e.fe.leave(left{labels: e.labelsOf(e.o.Holder, turn, long), running: true, created: long}),
			e.fe.leave(left{labels: e.labelsOf(e.o.Holder, turn, long), created: long, finished: long}),
			e.fe.leave(left{labels: e.labelsOf(e.o.Holder, turn, long), created: long}),
			e.fe.leave(left{labels: e.labelsOf("other", turn, long), running: true, created: long}),
		}
		e.fe.forgetCalls()

		time.Sleep(time.Minute)
		synctest.Wait()

		require.NotEmpty(t, e.fe.callsOf("List"), "no sweep at the minute")
		for _, id := range tracked {
			assert.Empty(t, e.fe.saidOf(id), "the sweep touched the tracked container %s", id)
			assert.True(t, e.fe.holds(id), "the tracked container %s is gone", id)
		}
	})
}

// sweptOnce plays one untracked container through the minute's first pass,
// and checks what the sweep did: it inspected it, and, were it due, killed
// it if it ran, removed it by its id, and logged the removal with the
// container's holder; otherwise it left it. place gives the container, by
// the pass's instant, at.
func sweptOnce(t *testing.T, place func(e *env, at time.Time) left, removed bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		at := time.Now().Add(time.Minute)
		l := place(e, at)
		id := e.fe.leave(l)

		time.Sleep(time.Until(at))
		synctest.Wait()

		want := []string{"Inspect " + id}
		if removed && l.running {
			want = append(want, "Kill "+id)
		}
		if removed {
			want = append(want, "Remove "+id)
		}
		assert.Equal(t, want, e.fe.saidOf(id))
		assert.Equal(t, !removed, e.fe.holds(id), "the engine holds the container")
		lines := logged(t, e.logs, "the sweep removed a container")
		if !removed {
			assert.Empty(t, lines)
			return
		}
		require.Len(t, lines, 1)
		assert.Equal(t, id, lines[0]["container"])
		assert.Equal(t, l.labels[labelHolder], lines[0]["holder"])
	})
}

// TestTheSweepRemovesItsOwnUntrackedContainersWhenDue (Decision 5): at the
// minute's pass, the sweep takes each untracked container of its own holder
// once it is due: a stopped one over a minute after its end; a never-started
// one, such as a creation a stop cancelled leaves, over 10 s after its
// creation; a running one over a minute past its deadline plus the slack, as
// a turn stopped while its start was in flight leaves, or, with no deadline
// label, an hour after its creation, as another holder's. One not due yet is
// left. A container not running has a deadline an hour off, so that only its
// own rule can make it due.
func TestTheSweepRemovesItsOwnUntrackedContainersWhenDue(t *testing.T) {
	for _, row := range []struct {
		name    string
		running bool
		// Each is a time before the pass's instant: the container's creation;
		// its end, zero for one that runs or never ran; and, for a running
		// one, how long its deadline plus the slack has passed.
		created, finished, overdue time.Duration
		// unlabelled drops the deadline label.
		unlabelled bool
		removed    bool
	}{
		{name: "stopped 61 s ago", created: 2 * time.Minute, finished: 61 * time.Second, removed: true},
		{name: "stopped 59 s ago", created: 2 * time.Minute, finished: 59 * time.Second},
		{name: "never started, created 11 s ago", created: 11 * time.Second, removed: true},
		{name: "never started, created 9 s ago", created: 9 * time.Second},
		{name: "running, 61 s past its deadline plus the slack", running: true, created: 5 * time.Minute, overdue: 61 * time.Second, removed: true},
		{name: "running, 59 s past its deadline plus the slack", running: true, created: 5 * time.Minute, overdue: 59 * time.Second},
		{name: "running, no deadline label, created an hour and a second ago", running: true, created: time.Hour + time.Second, unlabelled: true, removed: true},
		{name: "running, no deadline label, created 59 min 59 s ago", running: true, created: time.Hour - time.Second, unlabelled: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			sweptOnce(t, func(e *env, at time.Time) left {
				deadline := at.Add(time.Hour)
				if row.running {
					deadline = at.Add(-e.o.Slack - row.overdue)
				}
				l := left{labels: e.labelsOf(e.o.Holder, newTurn("alpha"), deadline), running: row.running, created: at.Add(-row.created)}
				if row.finished > 0 {
					l.finished = at.Add(-row.finished)
				}
				if row.unlabelled {
					delete(l.labels, labelDeadline)
				}
				return l
			}, row.removed)
		})
	}
}

// TestTheSweepLeavesAnotherHoldersContainersUntilFiveMinutesPastTheirDeadline
// (Decision 5): another holder's container, whatever its state, falls due
// only once its agentswarm.deadline is over 5 minutes past, so that a live
// turn of another instance, or a clock step of a few minutes, is never
// touched: a stopped one long since ended, or one never started long since
// made, is left before then. One whose deadline label is missing or
// unreadable falls due an hour after its creation.
func TestTheSweepLeavesAnotherHoldersContainersUntilFiveMinutesPastTheirDeadline(t *testing.T) {
	for _, row := range []struct {
		name    string
		running bool
		// Each is a time before the pass's instant: the container's creation,
		// its end (zero for one that runs or never ran), and its deadline.
		created, finished, past time.Duration
		// unlabelled drops the deadline label; garbled replaces it.
		unlabelled bool
		garbled    string
		removed    bool
	}{
		{name: "running, 5 min 1 s past its deadline", running: true, created: 10 * time.Minute, past: 5*time.Minute + time.Second, removed: true},
		{name: "running, 4 min 59 s past its deadline", running: true, created: 10 * time.Minute, past: 4*time.Minute + 59*time.Second},
		{name: "stopped 4 min ago, 4 min 59 s past its deadline", created: 10 * time.Minute, finished: 4 * time.Minute, past: 4*time.Minute + 59*time.Second},
		{name: "never started, made 4 min ago, 4 min 59 s past its deadline", created: 4 * time.Minute, past: 4*time.Minute + 59*time.Second},
		{name: "no deadline label, created an hour and a second ago", running: true, created: time.Hour + time.Second, unlabelled: true, removed: true},
		{name: "no deadline label, created 59 min 59 s ago", running: true, created: time.Hour - time.Second, unlabelled: true},
		{name: "an unreadable deadline label, created an hour and a second ago", running: true, created: time.Hour + time.Second, garbled: "soon", removed: true},
		{name: "an unreadable deadline label, created 59 min 59 s ago", running: true, created: time.Hour - time.Second, garbled: "soon"},
	} {
		t.Run(row.name, func(t *testing.T) {
			sweptOnce(t, func(e *env, at time.Time) left {
				l := left{labels: e.labelsOf("other", newTurn("alpha"), at.Add(-row.past)), running: row.running, created: at.Add(-row.created)}
				if row.finished > 0 {
					l.finished = at.Add(-row.finished)
				}
				switch {
				case row.unlabelled:
					delete(l.labels, labelDeadline)
				case row.garbled != "":
					l.labels[labelDeadline] = row.garbled
				}
				return l
			}, row.removed)
		})
	}
}

// TestTheSweepActsOnIDsNeverOnNames (Decision 5): every Kill and Remove the
// sweep sends, at Open's start sweep and at the minute's pass, names a
// container's id from its listing, never its name, since a retry reuses a
// turn's name; and every container due is gone.
func TestTheSweepActsOnIDsNeverOnNames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixture(t)
		long := time.Now().Add(-time.Hour)
		leave := func(holder string, running bool, finished time.Time) string {
			return e.fe.leave(left{labels: e.labelsOf(holder, newTurn("alpha"), long), running: running, created: long, finished: finished})
		}
		ids := []string{leave(e.o.Holder, true, time.Time{}), leave(e.o.Holder, false, long), leave(e.o.Holder, false, time.Time{})}
		e.open(t)
		ids = append(ids, leave(e.o.Holder, true, time.Time{}), leave(e.o.Holder, false, long), leave(e.o.Holder, false, time.Time{}),
			leave("other", true, time.Time{}))

		time.Sleep(time.Minute)
		synctest.Wait()

		var sent []string
		for _, c := range e.fe.calls() {
			if c.method == "Kill" || c.method == "Remove" {
				sent = append(sent, c.arg)
			}
		}
		require.NotEmpty(t, sent, "the sweep killed and removed nothing")
		for _, arg := range sent {
			assert.Contains(t, ids, arg, "the sweep sent %q, not a container's id from its listing", arg)
		}
		for _, id := range ids {
			assert.False(t, e.fe.holds(id), "the container %s is still there", id)
		}
	})
}

// TestTheSweepRunsEveryMinute (Decision 5): the sweep lists the tenant's
// containers, by its label, at Open, and again every minute, fixed in code:
// at 1 and at 2 minutes, and not before.
func TestTheSweepRunsEveryMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixture(t)
		e.open(t)
		lists := func() int { return len(e.fe.callsOf("List")) }
		require.Equal(t, 1, lists(), "Open did not sweep")

		for n := 1; n <= 2; n++ {
			time.Sleep(time.Minute - time.Nanosecond)
			synctest.Wait()
			assert.Equal(t, n, lists(), "a sweep before the minute %d", n)
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			assert.Equal(t, n+1, lists(), "no sweep at the minute %d", n)
		}
		for _, c := range e.fe.callsOf("List") {
			assert.Equal(t, "agentswarm.tenant="+e.o.Tenant, c.arg)
		}
	})
}

// TestARemovalTheEngineRefusesIsLeftToTheNextPass (Decision 5): a removal the
// engine refuses, as its conflict over a removal already in progress (a 409,
// an ordinary error of the port) refuses one, is logged a container could
// not be removed, at Warn, with the container and the error, and changes
// nothing else: the start sweep does not refuse the start, and the next pass
// removes the container.
func TestARemovalTheEngineRefusesIsLeftToTheNextPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixture(t)
		long := time.Now().Add(-time.Hour)
		id := e.fe.leave(left{labels: e.labelsOf(e.o.Holder, newTurn("alpha"), long), created: long, finished: long})
		e.fe.setHook(onFirst("Remove", func(arg string) error {
			return fmt.Errorf("docker: remove the container %s: removal of container %s is already in progress", arg, arg)
		}))

		e.open(t)

		lines := logged(t, e.logs, "a container could not be removed")
		require.Len(t, lines, 1)
		assert.Equal(t, "WARN", lines[0]["level"])
		assert.Equal(t, id, lines[0]["container"])
		assert.Contains(t, lines[0]["err"], "already in progress")
		assert.True(t, e.fe.holds(id), "the refused removal removed the container")
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.False(t, e.fe.holds(id), "the next pass did not remove the container")
		assert.Len(t, logged(t, e.logs, "the sweep removed a container"), 1)
	})
}

// TestAPassCutShortIsLogged (Decision 5; R7): nothing of a minute's pass
// fails silently, and no engine holds it for good. A pass whose listing
// meets no engine, or that the engine holds past the pass's 10 s bound,
// logs the sweep did not finish, at Warn, and ends there, checking no image;
// one whose listing the engine refuses logs it too, and goes on to check its
// images; one whose image check meets no engine logs the image check did not
// finish, and lists no image missing for it. Status says what the pass's
// last call found, and the next pass runs whole.
func TestAPassCutShortIsLogged(t *testing.T) {
	fails := func(method string, err func(arg string) error) func(fe *fakeEngine) func() {
		return func(fe *fakeEngine) func() {
			fe.setHook(onFirst(method, err))
			return func() {}
		}
	}
	for _, row := range []struct {
		name string
		// set makes the engine fail the pass, and returns what lets it go.
		set       func(fe *fakeEngine) (release func())
		msg, says string
		// after is how long after the minute the pass ends; images says it
		// checked its images; engine is Status's Engine after it.
		after  time.Duration
		images bool
		engine string
	}{
		{
			name: "a listing that meets no engine", set: fails("List", func(arg string) error { return down("List", arg) }),
			msg: "the sweep did not finish", says: "the engine does not answer", engine: "unreachable",
		},
		{
			name: "a listing held past 10 s", set: func(fe *fakeEngine) func() { return fe.block("List") },
			msg: "the sweep did not finish", says: "deadline exceeded", after: 10 * time.Second, engine: "unreachable",
		},
		{
			name: "a listing the engine refuses", set: fails("List", func(string) error { return errors.New("the engine refused the listing") }),
			msg: "the sweep did not finish", says: "refused the listing", images: true, engine: "ok",
		},
		{
			name: "an image check that meets no engine", set: fails("Image", func(arg string) error { return down("Image", arg) }),
			msg: "the image check did not finish", says: "the engine does not answer", images: true, engine: "unreachable",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				release := row.set(e.fe)

				time.Sleep(time.Minute + row.after - time.Nanosecond)
				synctest.Wait()
				assert.Empty(t, logged(t, e.logs, row.msg), "the pass ended early")
				time.Sleep(time.Nanosecond)
				synctest.Wait()

				lines := logged(t, e.logs, row.msg)
				require.Len(t, lines, 1)
				assert.Equal(t, "WARN", lines[0]["level"])
				assert.Contains(t, lines[0]["err"], row.says)
				assert.Equal(t, row.images, len(e.fe.callsOf("Image")) > 0, "whether the pass checked its images")
				assert.Equal(t, row.engine, e.r.Status().Engine)
				assert.Empty(t, e.r.Status().MissingImages, "an image was listed missing for the engine's silence")
				release()
				e.fe.forgetCalls()
				time.Sleep(time.Minute)
				synctest.Wait()
				assert.NotEmpty(t, e.fe.callsOf("Image"), "the next pass did not check the images")
				assert.Len(t, logged(t, e.logs, row.msg), 1, "the next pass did not finish")
				assert.Equal(t, "ok", e.r.Status().Engine)
			})
		})
	}
}

// neverAnswered holds every call of method in the engine's hook, which
// watches no context, as an engine call that does not let go at its
// context's end, until the release it returns.
func neverAnswered(method string) func(fe *fakeEngine) (release func()) {
	return func(fe *fakeEngine) func() {
		gate := make(chan struct{})
		fe.setHook(func(m, _ string) error {
			if m == method {
				<-gate
			}
			return nil
		})
		return func() { close(gate) }
	}
}

// TestCloseStopsTheMinutesJobWaitingAtMostASecond (Decision 5, serve's exits;
// Contract C14): Close stops the minute's job, so that no sweep comes after
// it, and then releases the holder's lock and closes the engine's client. A
// pass in flight is cancelled, which logs nothing: one whose engine call
// ends with its context ends at once; one the engine never lets go of is
// waited for 1 s, fixed in code, and no more, the lock held and the client
// open meanwhile; and then the lock is released all the same.
func TestCloseStopsTheMinutesJobWaitingAtMostASecond(t *testing.T) {
	for _, row := range []struct {
		name string
		// hold holds the minute's pass in the engine, and returns its
		// release; nil holds none.
		hold func(fe *fakeEngine) (release func())
		took time.Duration
	}{
		{name: "no pass in flight"},
		{name: "a pass whose listing ends with its context", hold: func(fe *fakeEngine) func() { return fe.block("List") }},
		{name: "a pass the engine never lets go of", hold: neverAnswered("List"), took: time.Second},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				release := func() {}
				if row.hold != nil {
					release = sync.OnceFunc(row.hold(e.fe))
					t.Cleanup(release)
				}
				time.Sleep(time.Minute)
				synctest.Wait()
				require.Len(t, e.fe.callsOf("List"), 1, "no sweep at the minute")

				began := time.Now()
				closed := make(chan error, 1)
				go func() { closed <- e.r.Close() }()
				if row.took > 0 {
					time.Sleep(row.took - time.Nanosecond)
					synctest.Wait()
					_, err := lockHolder(e.o.Store, e.o.Tenant, e.o.Holder)
					require.Error(t, err, "Close released the holder's lock before its second was up")
					assert.NotContains(t, e.fe.said(), "Close", "Close closed the engine before its second was up")
				}
				require.NoError(t, <-closed)
				took := time.Since(began)

				assert.Equal(t, row.took, took)
				unlock, err := lockHolder(e.o.Store, e.o.Tenant, e.o.Holder)
				require.NoError(t, err, "Close kept the holder's lock")
				unlock()
				assert.Contains(t, e.fe.said(), "Close", "Close did not close the engine")
				release()
				time.Sleep(3 * time.Minute)
				synctest.Wait()
				assert.Len(t, e.fe.callsOf("List"), 1, "a sweep came after Close")
				for _, msg := range []string{"the sweep did not finish", "the image check did not finish"} {
					assert.Empty(t, logged(t, e.logs, msg), "the pass Close cancelled was logged as cut short")
				}
			})
		})
	}
}
