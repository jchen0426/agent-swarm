//go:build e2e

package harness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// serveWait bounds the wait for serve to write its addresses, or to exit
	// before it does: its start reads the database and the configuration.
	serveWait = 2 * time.Minute
	// logTail is how much of serve's log a failed test shows.
	logTail = 64 << 10
)

// Addresses are what serve writes once its listeners are bound (Contract
// C2): its process id, and the address of each listener.
type Addresses struct {
	PID   int    `json:"pid"`
	MCP   string `json:"mcp"`
	LLM   string `json:"llm"`
	Admin string `json:"admin"`
}

// Instance is a serve the harness runs, under a parent (R20): agentswarm
// serve --exit-with-stdin, whose standard input the parent holds. Closing it,
// as the test's end does, makes serve stop every turn and exit at once (R6).
type Instance struct {
	deployment string
	// turns is the deployment's runner.output, the turns' output files.
	turns  string
	parent *Parent
	addrs  Addresses
	log    *Log
	admin  *Admin
}

// Serve runs agentswarm serve on a deployment of the instance, with
// --exit-with-stdin, under StartParent, and returns once serve has written its
// addresses (Contract C2), or has exited before it did, as a serve that
// refuses to start does: Wait then gives its exit code, and Log says why. An
// addresses file an earlier serve of the instance left is set aside first. A
// serve that does neither within serveWait fails the test. When the test
// fails, its log shows the end of serve's. Serve's temporary directory, under
// which the runner makes each turn's, is the instance's own, which the test's
// end removes: a serve that exits at once leaves its turns' directories.
func (e *Env) Serve(t *testing.T, instance, deployment string) *Instance {
	t.Helper()
	in := e.instance(t, instance)
	setAside(t, in.addresses)
	tmp := filepath.Join(in.dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatalf("harness: %v", err)
	}
	p := e.StartParent(t, []string{binary(t), "serve", "--deployment", deployment, "--exit-with-stdin"}, []string{"TMPDIR=" + tmp})
	i := &Instance{deployment: deployment, turns: in.output, parent: p, log: &Log{path: p.Stderr(), exited: p.exited}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("harness: the end of the log of serve %s:\n%s", instance, i.log.tail(logTail))
		}
	})
	deadline := time.Now().Add(serveWait)
	for {
		if a, ok := readAddresses(in.addresses); ok {
			i.addrs = a
			i.admin = newAdmin(a.Admin, in.adminToken)
			return i
		}
		select {
		case <-p.exited:
			return i
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("harness: serve %s wrote no addresses within %v; its log:\n%s", instance, serveWait, i.log.tail(logTail))
		}
	}
}

// setAside renames a file an earlier serve left, so that the file the next
// one writes is told from it; nothing is removed.
func setAside(t testing.TB, path string) {
	t.Helper()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := os.Rename(path, path+"."+strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
		t.Fatalf("harness: set aside %s: %v", path, err)
	}
}

// readAddresses reads an addresses file, which serve writes whole, by a
// rename: it reports false while there is none.
func readAddresses(path string) (Addresses, bool) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the instance's own addresses file.
	if err != nil {
		return Addresses{}, false
	}
	var a Addresses
	if json.Unmarshal(raw, &a) != nil || a.PID <= 0 {
		return Addresses{}, false
	}
	return a, true
}

// Addresses returns what serve wrote once its listeners were bound (C2): the
// zero Addresses when serve exited before it bound them.
func (i *Instance) Addresses() Addresses { return i.addrs }

// Log returns serve's log, the JSON lines on its standard error (C3).
func (i *Instance) Log() *Log { return i.log }

// Admin returns serve's admin API (C4), called with the instance's admin
// token; nil when serve exited before it bound its listeners.
func (i *Instance) Admin() *Admin { return i.admin }

// Signal sends sig to serve itself, by the process id it wrote (C2); its
// parent and its turns get nothing.
func (i *Instance) Signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if i.addrs.PID <= 0 {
		t.Fatal("harness: serve wrote no process id to signal")
	}
	if err := syscall.Kill(i.addrs.PID, sig); err != nil {
		t.Fatalf("harness: send %v to serve: %v", sig, err)
	}
}

// KillParent sends SIGKILL to serve's parent, and waits until it is gone:
// serve's standard input closes with it.
func (i *Instance) KillParent(t *testing.T) {
	t.Helper()
	i.parent.Kill(t)
}

// Wait waits until serve has exited, and returns its exit code, which its
// parent exits with: 128 and the signal's number when a signal ended it, or
// -1 when the parent itself was killed.
func (i *Instance) Wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	return i.parent.Wait(t, timeout)
}

// Output returns all serve has written so far: its standard output, then its
// standard error.
func (i *Instance) Output(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, path := range []string{i.parent.Stdout(), i.parent.Stderr()} {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a file of the parent's, which the harness made.
		if err != nil {
			t.Fatalf("harness: read what serve wrote: %v", err)
		}
		b.Write(raw)
	}
	return b.String()
}

// TurnOutputs returns the turns' output files, <runner.output>/<turn id>.out,
// what each holds by its turn's id.
func (i *Instance) TurnOutputs(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(i.turns, "*.out"))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a turn's output file, in the instance's directory.
		if err != nil {
			t.Fatalf("harness: read a turn's output: %v", err)
		}
		out[strings.TrimSuffix(filepath.Base(path), ".out")] = string(raw)
	}
	return out
}
