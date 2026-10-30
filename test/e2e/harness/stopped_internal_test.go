//go:build e2e

package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hasProc reports whether this host has /proc, where WaitStopped reads the
// threads of a process: Linux has, macOS has not.
func hasProc() bool {
	_, err := os.Stat("/proc")
	return err == nil
}

// A SIGSTOP stops a process only as each of its threads next returns to user
// space. WaitStopped returns once a stopped process has stopped, within its
// bound and reporting nothing, and fails the test when the process still runs
// at its bound.
func TestWaitStoppedReturnsForAStoppedProcessAndFailsForARunningOne(t *testing.T) {
	if !hasProc() {
		t.Skip("no /proc on this host: WaitStopped waits a fixed delay, which TestWaitStoppedWithoutProcWaitsAFixedDelay covers")
	}
	child := exec.Command("sleep", "60")
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	pid := child.Process.Pid

	running := &fakeTB{}
	var ran bool
	began := time.Now()
	running.run(func() { ran = WaitStopped(running, pid, 200*time.Millisecond) })
	waited := time.Since(began)

	assert.False(t, ran, "a process that runs has not stopped")
	assert.GreaterOrEqual(t, waited, 200*time.Millisecond, "WaitStopped waits its bound for a process that runs")
	errs, fatals := running.reported()
	assert.Empty(t, fatals, "it reports with Errorf, which a scripted server's handler may call")
	require.Len(t, errs, 1, "and fails the test")
	assert.Contains(t, errs[0], strconv.Itoa(pid), "naming the process")

	require.NoError(t, syscall.Kill(pid, syscall.SIGSTOP))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })
	stopped := &fakeTB{}
	var halted bool
	began = time.Now()
	stopped.run(func() { halted = WaitStopped(stopped, pid, time.Second) })

	assert.True(t, halted, "a stopped process has stopped")
	assert.Less(t, time.Since(began), time.Second, "within the bound")
	errs, fatals = stopped.reported()
	assert.Empty(t, errs)
	assert.Empty(t, fatals)
}

// putStat writes the stat line of thread tid of process 42 under proc, as
// /proc shows it: its command, in parentheses, may hold a parenthesis and a
// space, so the state is the field after the last ")".
func putStat(proc string, tid int, state string) error {
	dir := filepath.Join(proc, "42", "task", strconv.Itoa(tid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line := strconv.Itoa(tid) + " (agent (swarm) S) " + state + " 1 42 42 0 -1 4194560\n"
	return os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o600)
}

// writeStat is putStat in the test's own goroutine, which an error fails.
func writeStat(t *testing.T, proc string, tid int, state string) {
	t.Helper()
	require.NoError(t, putStat(proc, tid, state))
}

// WaitStopped waits for every thread of the process, not for the first one
// that stops: a thread that still runs can read what reaches the process.
func TestWaitStoppedWaitsForEveryThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := t.TempDir()
		writeStat(t, proc, 42, "T")
		writeStat(t, proc, 43, "S")
		test := &fakeTB{}
		done := make(chan bool, 1)
		go func() { done <- waitStopped(test, proc, 42, time.Second, noProcDelay) }()

		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("WaitStopped returned while a thread still ran")
		default:
		}
		writeStat(t, proc, 43, "T")

		assert.True(t, <-done, "every thread has stopped")
		errs, fatals := test.reported()
		assert.Empty(t, errs)
		assert.Empty(t, fatals)
	})
}

// WaitStopped waits its bound, and no longer: a thread that stops only once
// the bound has gone by fails the test, at the bound, naming the process.
func TestWaitStoppedFailsAtItsBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const bound = time.Second
		proc := t.TempDir()
		writeStat(t, proc, 42, "T")
		writeStat(t, proc, 43, "S")
		// Thread 43 stops half a second after the bound, so that a wait that
		// ignored its bound, or had a longer one, would see every thread stop.
		flipped := make(chan error, 1)
		go func() {
			time.Sleep(bound + 500*time.Millisecond)
			flipped <- putStat(proc, 43, "T")
		}()
		test := &fakeTB{}
		began := time.Now()

		stopped := waitStopped(test, proc, 42, bound, noProcDelay)
		waited := time.Since(began)

		require.NoError(t, <-flipped, "thread 43 stops before the bubble ends")
		assert.False(t, stopped, "a thread still ran at the bound")
		assert.Equal(t, bound, waited, "it waits exactly its bound")
		errs, fatals := test.reported()
		assert.Empty(t, fatals)
		require.Len(t, errs, 1, "and fails the test once")
		assert.Contains(t, errs[0], "process 42")
	})
}

// WaitStopped reads the threads /proc lists. A thread listed without a stat
// file ended after the listing, and is passed over. A task directory that
// lists no thread has none stopped, so the wait fails at its bound.
func TestWaitStoppedReadsTheThreadsProcLists(t *testing.T) {
	const bound = time.Second
	for _, c := range []struct {
		name    string
		threads func(t *testing.T, proc string)
		stopped bool
		waited  time.Duration
		errors  int
	}{
		{
			name: "a listed thread without a stat file is passed over",
			threads: func(t *testing.T, proc string) {
				writeStat(t, proc, 42, "T")
				require.NoError(t, os.MkdirAll(filepath.Join(proc, "42", "task", "44"), 0o700))
			},
			stopped: true,
		},
		{
			name: "an empty task directory has no thread stopped",
			threads: func(t *testing.T, proc string) {
				require.NoError(t, os.MkdirAll(filepath.Join(proc, "42", "task"), 0o700))
			},
			waited: bound,
			errors: 1,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				proc := t.TempDir()
				c.threads(t, proc)
				test := &fakeTB{}
				began := time.Now()

				assert.Equal(t, c.stopped, waitStopped(test, proc, 42, bound, noProcDelay))
				assert.Equal(t, c.waited, time.Since(began))
				errs, fatals := test.reported()
				assert.Empty(t, fatals)
				assert.Len(t, errs, c.errors)
			})
		})
	}
}

// A process whose threads cannot be read, as one that is gone, fails the test
// at once: no wait could see it stop.
func TestWaitStoppedFailsForAProcessItCannotRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		test := &fakeTB{}
		began := time.Now()

		assert.False(t, waitStopped(test, t.TempDir(), 42, time.Second, noProcDelay))
		assert.Zero(t, time.Since(began), "it fails at once")
		errs, _ := test.reported()
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0], "42")
	})
}

// Without /proc, as on macOS, WaitStopped cannot see a thread: it waits a
// fixed delay, well above the time a stop takes, and fails nothing. On such a
// host the exported helper does so too.
func TestWaitStoppedWithoutProcWaitsAFixedDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		test := &fakeTB{}
		began := time.Now()

		assert.True(t, waitStopped(test, filepath.Join(t.TempDir(), "no-proc"), 42, time.Second, noProcDelay))
		assert.Equal(t, noProcDelay, time.Since(began), "it waits the fixed delay")
		errs, fatals := test.reported()
		assert.Empty(t, errs)
		assert.Empty(t, fatals)
	})
	if hasProc() {
		return // this host has /proc: the exported helper reads it (TestWaitStoppedReturnsForAStoppedProcessAndFailsForARunningOne)
	}
	test := &fakeTB{}
	began := time.Now()
	assert.True(t, WaitStopped(test, os.Getpid(), time.Second))
	assert.GreaterOrEqual(t, time.Since(began), noProcDelay)
	errs, _ := test.reported()
	assert.Empty(t, errs)
}
