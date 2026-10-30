//go:build e2e

package harness

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// noProcDelay is how long WaitStopped waits where there is no /proc to read a
// process's threads in, as on macOS: well above the 5 ms or so that a SIGSTOP
// took to stop every thread of serve in scenario 9's runs.
const noProcDelay = 100 * time.Millisecond

// stopPoll is how often WaitStopped reads the states of the threads.
const stopPoll = 100 * time.Microsecond

// WaitStopped waits until every thread of the process pid has stopped, as a
// SIGSTOP stops it, for at most bound, and reports whether they all have. A
// SIGSTOP has not stopped a process when kill returns: the kernel stops each
// of its threads only as that thread next returns to user space, and until
// the last one has, the process runs on. It reads what reaches it, and sends
// what it was about to send. A scenario that means a stopped process to read
// something only once it runs again sends it after WaitStopped.
//
// On Linux it reads the state of each thread in /proc/<pid>/task until every
// one is T, stopped. It fails t, with Errorf, when bound runs out first, or
// when the threads cannot be read, as those of a process that is gone. It
// never calls Fatal, so a scripted server's handler may call it. Where there
// is no /proc, as on macOS, it cannot see the threads: it waits noProcDelay
// and reports true.
func WaitStopped(t testing.TB, pid int, bound time.Duration) bool {
	t.Helper()
	return waitStopped(t, "/proc", pid, bound, noProcDelay)
}

// waitStopped is WaitStopped over the /proc at proc, and waits fallback where
// there is none.
func waitStopped(t testing.TB, proc string, pid int, bound, fallback time.Duration) bool {
	t.Helper()
	if _, err := os.Stat(proc); errors.Is(err, fs.ErrNotExist) {
		time.Sleep(fallback)
		return true
	}
	task := filepath.Join(proc, strconv.Itoa(pid), "task")
	deadline := time.Now().Add(bound)
	for {
		stopped, err := threadsStopped(task)
		switch {
		case err != nil:
			t.Errorf("harness: the threads of process %d cannot be read, so its stop cannot be seen: %v", pid, err)
			return false
		case stopped:
			return true
		case !time.Now().Before(deadline):
			t.Errorf("harness: process %d did not stop within %s: a thread of it still runs", pid, bound)
			return false
		}
		time.Sleep(stopPoll)
	}
}

// threadsStopped reports whether every thread listed in a /proc task
// directory is in the state T. A thread that ended since the directory was
// read is passed over: its stat file is gone, or, opened just before the
// thread ended, reads as ESRCH. A directory whose threads all ended, or that
// lists none, has none stopped.
func threadsStopped(task string) (bool, error) {
	threads, err := os.ReadDir(task)
	if err != nil {
		return false, err
	}
	read := 0
	for _, thread := range threads {
		stat, err := os.ReadFile(filepath.Join(task, thread.Name(), "stat")) //nolint:gosec // G304: a thread's stat file, under /proc.
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return false, err
		}
		if threadState(stat) != 'T' {
			return false, nil
		}
		read++
	}
	return read > 0, nil
}

// threadState returns a thread's state from its stat line: the field after
// the command, which is in parentheses and may hold any byte, ")" among them,
// so the state follows the line's last ")" and a space.
func threadState(stat []byte) byte {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return 0
	}
	return stat[i+2]
}
