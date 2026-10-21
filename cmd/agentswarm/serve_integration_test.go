//go:build integration

package main

import (
	"bytes"
	"fmt"
	"io"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
)

// refuseWait bounds how long serve takes to refuse a start: one that has not
// returned by then has started instead.
const refuseWait = 30 * time.Second

// lockedBuffer is a buffer that goroutines may write while the test reads
// it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// serveOnce runs serve on a deployment, with its standard input held open
// under --exit-with-stdin, and returns its exit code and its log once it
// returns. A serve that has not returned within refuseWait did not refuse its
// start, as one that started does not: the row fails, and the end of its
// input stops it at once.
func serveOnce(t *testing.T, deployment string) (code int, log string) {
	t.Helper()
	stdin, input := io.Pipe()
	defer func() { _ = input.Close() }()
	var errOut lockedBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"serve", "--deployment", deployment, "--exit-with-stdin"}, stdin, io.Discard, &errOut)
	}()
	select {
	case code = <-done:
		return code, errOut.String()
	case <-time.After(refuseWait):
		_ = input.Close()
		select {
		case <-done:
		case <-time.After(refuseWait):
		}
		t.Fatalf("serve did not refuse its start within %v; its log:\n%s", refuseWait, errOut.String())
		return 0, ""
	}
}

// migrated migrates a fresh database.
func migrated(t *testing.T, u *neturl.URL) {
	t.Helper()
	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(t.Context(), pool))
}

// Serve refuses to start on what Decision 1 refuses that needs the database,
// the schema, and on what the deployment names that serve opens itself,
// before it binds a listener; it logs why, in its JSON log, and exits 1.
func TestServeRefusesWhatDecision1Refuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		// prepare readies the database.
		prepare func(t *testing.T, u *neturl.URL)
		// change edits the deployment's text, and setup the files of its
		// directory, which holds its turns' directory, turns.
		change func(text string) string
		setup  func(t *testing.T, dir string)
		// msg is the log line that says why, and names what it names.
		msg   string
		names []string
	}{
		{
			name: "a schema behind this build", prepare: func(*testing.T, *neturl.URL) {},
			msg: "schema incompatible", names: []string{"the schema is behind", "agentswarm migrate"},
		},
		{
			name: "a schema a later build needs",
			prepare: func(t *testing.T, u *neturl.URL) {
				migrated(t, u)
				pool, err := db.Open(t.Context(), u.String())
				require.NoError(t, err)
				defer pool.Close()
				_, err = pool.Exec(t.Context(), "INSERT INTO schema_migrations (name, min_reader) VALUES ('0099_later.sql', 99)")
				require.NoError(t, err)
			},
			msg: "schema incompatible", names: []string{"the schema is incompatible", "level 99", "min_reader 99", fmt.Sprintf("this build is at level %d", db.Level())},
		},
		{
			name: "an output directory that is not there", prepare: migrated,
			change: func(text string) string { return strings.Replace(text, `/turns"`, `/gone"`, 1) },
			msg:    "serve refused to start", names: []string{"runner.output"},
		},
		{
			name: "an output directory serve cannot write", prepare: migrated,
			setup: func(t *testing.T, dir string) {
				if os.Geteuid() == 0 {
					t.Skip("root writes in any directory")
				}
				turns := filepath.Join(dir, "turns")
				require.NoError(t, os.Chmod(turns, 0o500))
				t.Cleanup(func() { _ = os.Chmod(turns, 0o700) })
			},
			msg: "serve refused to start", names: []string{"runner.output", "cannot be written"},
		},
		{
			name: "a runtime binary that is not there", prepare: migrated,
			change: func(text string) string {
				return strings.Replace(text, "  kind: process\n", "  kind: process\n  binary: /nonexistent/agentswarm\n", 1)
			},
			msg: "serve refused to start", names: []string{"runner.binary"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := freshDatabase(t, "serve_"+strings.ReplaceAll(tc.name, " ", "_"))
			tc.prepare(t, u)
			deployment, addresses := servedDeployment(t, u.String(), tc.change)
			if tc.setup != nil {
				tc.setup(t, filepath.Dir(deployment))
			}

			code, errOut := serveOnce(t, deployment)

			assert.Equal(t, exitError, code, errOut)
			assert.Contains(t, errOut, `"msg":"`+tc.msg+`"`, "serve logs why, as JSON")
			for _, name := range tc.names {
				assert.Contains(t, errOut, name)
			}
			assert.Contains(t, errOut, `"msg":"serve stopped"`)
			assert.Contains(t, errOut, `"code":1`)
			assert.NoFileExists(t, addresses, "no listener was bound")
		})
	}
}
