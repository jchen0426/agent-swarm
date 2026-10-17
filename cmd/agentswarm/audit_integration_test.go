//go:build integration

package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

// verifyReport is what audit verify prints.
type verifyReport struct {
	Findings []struct {
		Kind   string `json:"kind"`
		Source string `json:"source"`
	} `json:"findings"`
}

func verifyKinds(t *testing.T, args ...string) []string {
	t.Helper()
	stdout, errOut, code := runCommand(append([]string{"audit", "verify"}, args...)...)
	require.Equal(t, exitFindings, code, "a directory store is never write-once: %s", errOut)
	var r verifyReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &r), stdout)
	var kinds []string
	for _, f := range r.Findings {
		kinds = append(kinds, f.Kind+" "+f.Source)
	}
	return kinds
}

// The commands anchor the chain and verify it against the database and the
// store; a row changed behind the platform's back shows.
func TestAuditAnchorThenVerify(t *testing.T) {
	url := pgtest.URL(t)
	pool := pgtest.Pool(t)
	const tenant = "cli-audit"
	_, err := postgres.NewEventStore(pool, tenant).Append(t.Context(), "run-cli", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{{ID: "cli-1", Run: "run-cli", Round: 1, Author: board.Author{Kind: board.AuthorAgent, Name: "a"},
			Body: board.Body{Task: &board.Task{Title: "check the pool"}}}}, nil
	})
	require.NoError(t, err)
	args := []string{"--tenant", tenant, "--anchors", "file://" + t.TempDir(), "--database-url", url}

	stdout, errOut, code := runCommand(append([]string{"audit", "anchor"}, args...)...)

	require.Equal(t, exitOK, code, errOut)
	var anchored struct {
		Key  string `json:"key"`
		Seq  int64  `json:"seq"`
		Runs int    `json:"runs"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &anchored))
	assert.Equal(t, int64(1), anchored.Seq)
	assert.Equal(t, 1, anchored.Runs)
	assert.Equal(t, []string{"store_unlocked "}, verifyKinds(t, args...))

	_, err = pool.Exec(t.Context(), `
BEGIN;
SET LOCAL session_replication_role = replica;
UPDATE board_events SET author_name = 'someone else' WHERE tenant_id = 'cli-audit' AND id = 'cli-1';
COMMIT;`)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"store_unlocked ", "changed_row board_events"}, verifyKinds(t, args...))
}
