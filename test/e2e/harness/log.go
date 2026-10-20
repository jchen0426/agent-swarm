//go:build e2e

package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

const (
	// logPoll is how often WaitFor reads serve's log again.
	logPoll = 25 * time.Millisecond
	// exitGrace is how long WaitFor reads on once serve's parent has exited:
	// serve outlives a parent that was killed by a moment.
	exitGrace = 2 * time.Second
)

// Log is serve's log: the JSON lines it writes on its standard error
// (Contract C3), which its parent keeps in a file. Every line has time, level
// and msg, and the attributes C3 lists for its msg.
type Log struct {
	path string
	// exited is closed once serve's parent has exited: serve writes no more.
	exited <-chan struct{}
}

// Lines returns serve's log lines so far, in order, each a JSON object. A line
// still being written is left out, and so is anything on serve's standard
// error that is not a JSON object, which a failed test's log shows (see
// Serve).
func (l *Log) Lines() []map[string]any {
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return nil
	}
	if end := bytes.LastIndexByte(raw, '\n'); end >= 0 {
		raw = raw[:end]
	} else {
		raw = nil
	}
	var lines []map[string]any
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		var m map[string]any
		if json.Unmarshal(line, &m) == nil && m != nil {
			lines = append(lines, m)
		}
	}
	return lines
}

// WaitFor waits for serve to log a line whose msg is msg and that match,
// when it is not nil, accepts, and returns the first such line. The test fails
// when none comes within timeout, or soon after serve's parent has exited
// without one.
func (l *Log) WaitFor(t *testing.T, msg string, match func(map[string]any) bool, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var exitedAt time.Time
	for {
		// Whether the parent had exited is read before the lines, so that
		// the lines read after the grace are the last serve wrote.
		if exitedAt.IsZero() && l.hasExited() {
			exitedAt = time.Now()
		}
		for _, line := range l.Lines() {
			if line["msg"] == msg && (match == nil || match(line)) {
				return line
			}
		}
		switch {
		case !exitedAt.IsZero() && time.Since(exitedAt) > exitGrace:
			t.Fatalf("harness: serve exited without logging a matching %q; its log:\n%s", msg, l.tail(logTail))
			return nil
		case time.Now().After(deadline):
			t.Fatalf("harness: serve logged no matching %q within %v; its log:\n%s", msg, timeout, l.tail(logTail))
			return nil
		}
		time.Sleep(logPoll)
	}
}

// hasExited reports whether serve's parent has exited, after serve.
func (l *Log) hasExited() bool {
	select {
	case <-l.exited:
		return true
	default:
		return false
	}
}

// tail returns at most the last n bytes of serve's standard error, whatever
// they hold.
func (l *Log) tail(n int) string {
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return "(the log could not be read: " + err.Error() + ")"
	}
	if len(raw) > n {
		raw = raw[len(raw)-n:]
	}
	return string(raw)
}
