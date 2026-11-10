//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
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

// adminGet sends a GET to the admin API, with the admin token when it is not
// empty, and returns the answer's status and body.
func adminGet(t *testing.T, url, token string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// The admin API reads the schema's state, for /readyz, which needs no token,
// and for /v1/status, on the main pool, never on the lease's, which Decision
// 4 keeps for the takes, the renewals, the release, the clock's samples and
// the leader's checks: a local process that asks either in a loop competes
// with the stores, not with the renewals. A started instance whose leader
// does not lead yet uses the lease's pool for nothing else.
func TestTheAdminAPIReadsTheSchemaOnTheMainPool(t *testing.T) {
	u := freshDatabase(t, "serve_admin_schema")
	migrated(t, u)
	deployment, _ := servedDeployment(t, u.String(), nil)
	_, err := git.PlainInit(filepath.Join(filepath.Dir(deployment), "config"), false)
	require.NoError(t, err)
	s, err := start(t.Context(), slog.New(slog.DiscardHandler), deployment)
	require.NoError(t, err)
	t.Cleanup(func() { s.close(context.Background()) })
	lease, main := s.lease.Stat().AcquireCount(), s.main.Stat().AcquireCount()
	base := "http://" + s.listeners.admin.Addr().String()

	ready, why := adminGet(t, base+"/readyz", "")
	status, shown := adminGet(t, base+"/v1/status", "admin-token")

	assert.Equal(t, http.StatusOK, ready, why)
	assert.Equal(t, http.StatusOK, status, shown)
	assert.Contains(t, shown, `"schema":"compatible"`)
	assert.Equal(t, lease, s.lease.Stat().AcquireCount(), "the lease's pool served neither")
	assert.GreaterOrEqual(t, s.main.Stat().AcquireCount(), main+2, "the main pool served both")
	assert.Nil(t, s.web, "no third pool without a web block (R142)")
	assert.Nil(t, s.watch, "nor a board watch")
}

// The start's line names the build's schema level apart from the line's own
// level: a log pipeline keeps one value of a key a JSON line gives twice, or
// refuses the line.
func TestServeStartedNamesTheSchemaLevelApartFromTheLogLevel(t *testing.T) {
	u := freshDatabase(t, "serve_started_line")
	migrated(t, u)
	deployment, _ := servedDeployment(t, u.String(), nil)
	_, err := git.PlainInit(filepath.Join(filepath.Dir(deployment), "config"), false)
	require.NoError(t, err)
	var out lockedBuffer
	s, err := start(t.Context(), slog.New(slog.NewJSONHandler(&out, nil)), deployment)
	require.NoError(t, err)
	t.Cleanup(func() { s.close(context.Background()) })

	var line string
	for l := range strings.SplitSeq(out.String(), "\n") {
		if strings.Contains(l, `"msg":"serve started"`) {
			line = l
		}
	}
	require.NotEmpty(t, line, "the log:\n%s", out.String())
	keys, values := jsonKeys(t, line)
	levels := 0
	for _, k := range keys {
		if k == "level" {
			levels++
		}
	}
	assert.Equal(t, 1, levels, "each key once: %s", line)
	assert.Equal(t, "INFO", values["level"])
	assert.InDelta(t, db.Level(), values["schemaLevel"], 0, "the build's schema level")
}

// The start's line names the worst drain (R22), from the deployment's
// timings, as a Go duration (R21): 1m42s with the defaults (R142), for a
// supervisor's stop timeout to be set above it.
func TestServeStartedNamesTheWorstDrain(t *testing.T) {
	u := freshDatabase(t, "serve_worst_drain")
	migrated(t, u)
	deployment, _ := servedDeployment(t, u.String(), nil)
	_, err := git.PlainInit(filepath.Join(filepath.Dir(deployment), "config"), false)
	require.NoError(t, err)
	var out lockedBuffer

	s, err := start(t.Context(), slog.New(slog.NewJSONHandler(&out, nil)), deployment)

	require.NoError(t, err)
	t.Cleanup(func() { s.close(context.Background()) })
	assert.Equal(t, "1m42s", logLine(t, out.String(), "serve started")["worstDrain"])
}

// Under the process runner, whose turns run as host processes, serve says at
// its start that its turns run unsandboxed, at Warn, naming the runner
// (Plan 7d-2, Decision 7; Contract C3).
func TestServeWarnsThatTheProcessRunnersTurnsRunUnsandboxed(t *testing.T) {
	u := freshDatabase(t, "serve_unsandboxed")
	migrated(t, u)
	deployment, _ := servedDeployment(t, u.String(), nil)
	_, err := git.PlainInit(filepath.Join(filepath.Dir(deployment), "config"), false)
	require.NoError(t, err)
	var out lockedBuffer

	s, err := start(t.Context(), slog.New(slog.NewJSONHandler(&out, nil)), deployment)

	require.NoError(t, err)
	t.Cleanup(func() { s.close(context.Background()) })
	warning := logLine(t, out.String(), "turns run unsandboxed")
	assert.Equal(t, "WARN", warning["level"])
	assert.Equal(t, "process", warning["runner"])
}

// jsonKeys returns a JSON object's keys in order, a key it gives twice
// included, and its values, the last of a key given twice.
func jsonKeys(t *testing.T, line string) (keys []string, values map[string]any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	open, err := dec.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('{'), open)
	values = map[string]any{}
	for dec.More() {
		tok, err := dec.Token()
		require.NoError(t, err)
		key, ok := tok.(string)
		require.True(t, ok, "a key: %v", tok)
		var v any
		require.NoError(t, dec.Decode(&v))
		keys = append(keys, key)
		values[key] = v
	}
	return keys, values
}

// webHost is the web block's public URL's host, which a page's request
// carries (R95).
const webHost = "agents.example.com"

// withWeb gives a served deployment's text a web block and its listener: a
// public URL, an identity provider that is never asked, the groups, and the
// session key's reference.
func withWeb(text string) string {
	text = strings.Replace(text, `admin: "127.0.0.1:0", `, `admin: "127.0.0.1:0", web: "127.0.0.1:0", `, 1)
	return text + `web:
  publicURL: https://` + webHost + `
  oidc: {issuer: "https://idp.example.com", clientID: agentswarm, clientSecretRef: secret://web/client}
  groups: {viewers: v, responders: r, admins: a}
  sessionKeyRef: secret://web/session
`
}

// committedConfiguration makes the configuration repository at dir: a work
// item type, committed on refs/heads/main, which the registry's first
// refresh makes current.
func committedConfiguration(t *testing.T, dir string) {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	path := filepath.Join(dir, "work-item-types", "incident.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("kind: WorkItemType\nmetadata: {name: incident, owner: sre-leads}\n"+
		"spec:\n  hypothesisKey:\n    - {name: service, values: [checkout]}\n"), 0o600))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddGlob("."))
	h, err := wt.Commit("a work item type", &git.CommitOptions{Author: &object.Signature{Name: "R", Email: "r@example.com", When: time.Now()}})
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), h)))
}

// holdAll takes every connection of pool, until the function it returns
// gives them back, which the test's end does too.
func holdAll(t *testing.T, pool *pgxpool.Pool) (release func()) {
	t.Helper()
	taking, cancel := context.WithTimeout(t.Context(), refuseWait)
	defer cancel()
	conns := make([]*pgxpool.Conn, 0, pool.Stat().MaxConns())
	for range pool.Stat().MaxConns() {
		c, err := pool.Acquire(taking)
		require.NoError(t, err)
		conns = append(conns, c)
	}
	release = sync.OnceFunc(func() {
		for _, c := range conns {
			c.Release()
		}
	})
	t.Cleanup(release)
	return release
}

// webPoolWait is the bound a board read waits under in these tests, the web
// pool's wait (R141).
const webPoolWait = 2 * time.Second

// The board reads on the web's own pool, and every page 9a-1 shipped on the
// main pool (design D15; R143; plan review I5, M7). The wired web server's
// board reads are the watch's. With every connection of the main pool held,
// the watch reads an item's board, its probe, its records, board, titles,
// strip and kill switch, and a raw output's read call is looked up, each on
// the web's pool. With the web pool's two held, the board of a second item,
// which no tick covers, and the read call fail within the pool's wait, while
// the kill switch's page and the items list render and the admin API's status
// answers, on the main pool; released, the board reads again. (The plan's.)
func TestTheBoardReadsOnTheWebsPoolAndEveryOtherPageOnTheMain(t *testing.T) {
	u := freshDatabase(t, "serve_web_pool")
	migrated(t, u)
	deployment, _ := servedDeployment(t, u.String(), withWeb)
	dir := filepath.Dir(deployment)
	sessionKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	for name, value := range map[string]string{"web/session": sessionKey, "web/client": "client-secret"} {
		path := filepath.Join(dir, "secrets", filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(value+"\n"), 0o600))
	}
	committedConfiguration(t, filepath.Join(dir, "config"))
	var out lockedBuffer
	s, err := start(t.Context(), slog.New(slog.NewJSONHandler(&out, nil)), deployment)
	require.NoError(t, err)
	t.Cleanup(func() {
		s.leader.Stop(context.Background())
		waitFor(s.leader.Done(), refuseWait)
		s.close(context.Background())
	})
	require.NotNil(t, s.web, "the web's pool, with a web block")
	assert.Equal(t, int32(2), s.web.Stat().MaxConns())
	require.Eventually(t, func() bool { return strings.Contains(out.String(), `"msg":"revision current"`) }, refuseWait, 50*time.Millisecond)
	require.NoError(t, s.leader.Lead(context.Background()))
	require.NotNil(t, s.ports)
	assert.Same(t, s.watch, s.ports.Boards, "the wired web server's board reads are the watch's")
	admin := "http://" + s.listeners.admin.Addr().String()
	open := func(key string) item.ID {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, admin+"/v1/items", strings.NewReader(`{"type":"incident","title":"checkout fails"}`))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer admin-token")
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var opened struct{ ID item.ID }
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&opened))
		_ = resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		return opened.ID
	}
	first, second := open("board-pool-1"), open("board-pool-2")
	keys, err := auth.NewKeys(sessionKey, "https://idp.example.com", "agentswarm")
	require.NoError(t, err)
	cookie := webtest.SignedIn(t, keys, []auth.Role{auth.Viewer, auth.Responder, auth.Admin}, "sso:rita")
	page := func(path string) (int, error) {
		ctx, cancel := context.WithTimeout(t.Context(), refuseWait)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.listeners.web.Addr().String()+path, http.NoBody)
		require.NoError(t, err)
		req.Host = webHost
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	board := func(id item.ID) (app.BoardRead, error) {
		ctx, cancel := context.WithTimeout(t.Context(), webPoolWait)
		defer cancel()
		return s.watch.Board(ctx, id)
	}
	readCall := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), webPoolWait)
		defer cancel()
		_, err := s.reads.calls.ReadCall(ctx, "no-such-call")
		return err
	}

	releaseMain := holdAll(t, s.main)
	read, boardErr := board(first)
	callErr := readCall()
	releaseMain()

	require.NoError(t, boardErr, "every port the board reads is on the web's pool")
	assert.Equal(t, first, read.Board.View.ID)
	assert.True(t, read.Facts.KillSwitchRead, "the tick's kill switch read too")
	assert.False(t, read.Strip.Unread, "and the strip's listing")
	require.ErrorIs(t, callErr, app.ErrCallNotFound, "a raw output's read call is looked up there")

	// The second item was never read, so that no tick covers it, and its
	// read needs the web's pool at once (review 1's m6).
	releaseWeb := holdAll(t, s.web)
	began := time.Now()
	_, boardErr = board(second)
	boardTook := time.Since(began)
	began = time.Now()
	callErr = readCall()
	callTook := time.Since(began)
	switchPage, switchErr := page("/kill-switch")
	itemsPage, itemsErr := page("/items")
	status, shown := adminGet(t, admin+"/v1/status", "admin-token")
	releaseWeb()

	require.Error(t, boardErr, "the board's read waits on the web's pool alone")
	require.Error(t, callErr)
	assert.Less(t, boardTook, webPoolWait+500*time.Millisecond, "the board's read within the pool's wait")
	assert.Less(t, callTook, webPoolWait+500*time.Millisecond, "and the read call's")
	require.NoError(t, switchErr)
	assert.Equal(t, http.StatusOK, switchPage, "the kill switch's page keeps the main pool")
	require.NoError(t, itemsErr)
	assert.Equal(t, http.StatusOK, itemsPage, "and the items list")
	assert.Equal(t, http.StatusOK, status, shown)

	again, err := board(second)
	require.NoError(t, err, "released, the board reads again")
	assert.Equal(t, second, again.Board.View.ID)
}
