//go:build integration

package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/registry/postgres"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

type anyCondition struct{}

func (anyCondition) Check(config.Environment, string) error { return nil }

func revision(t *testing.T, commit, pattern string) *config.Revision {
	t.Helper()
	files, err := memfs.New(map[string][]byte{
		"redaction-rules/default-secrets.yaml": []byte("kind: RedactionRule\nmetadata: {name: default-secrets, owner: security@example.com}\n" +
			"spec: {patterns: ['" + pattern + "']}\n"),
		"skills/runbook/skill.yaml": []byte("kind: Skill\nmetadata: {name: runbook, owner: alice@example.com}\n" +
			"spec: {versions: [{version: 1.0.0, source: {repository: https://git.example.com/runbooks, commit: 3f2a9c1}}]}\n"),
		"skills/runbook/1.0.0/SKILL.md": []byte("# Runbook\n"),
	})
	require.NoError(t, err)
	rev, err := config.Build(files, commit, anyCondition{})
	require.NoError(t, err)
	return rev
}

func TestARevisionIsRecordedWithItsObjects(t *testing.T) {
	store := postgres.NewRevisions(pgtest.Pool(t), "t1")
	rev := revision(t, "aaaa111", "AKIA[0-9A-Z]{16}")

	require.NoError(t, store.Record(t.Context(), rev))

	got, err := store.Objects(t.Context(), "aaaa111")
	require.NoError(t, err)
	assert.Equal(t, rev.Objects(), got)
}

func TestRecordingACommitAgainChangesNothing(t *testing.T) {
	store := postgres.NewRevisions(pgtest.Pool(t), "t2")
	rev := revision(t, "bbbb222", "AKIA[0-9A-Z]{16}")
	require.NoError(t, store.Record(t.Context(), rev))

	require.NoError(t, store.Record(t.Context(), rev))

	got, err := store.Objects(t.Context(), "bbbb222")
	require.NoError(t, err)
	assert.Len(t, got, len(rev.Objects()))
}

func TestOneCommitHasOneContent(t *testing.T) {
	store := postgres.NewRevisions(pgtest.Pool(t), "t3")
	require.NoError(t, store.Record(t.Context(), revision(t, "cccc333", "AKIA[0-9A-Z]{16}")))

	err := store.Record(t.Context(), revision(t, "cccc333", "ghp_[A-Za-z0-9]{36}"))

	require.ErrorIs(t, err, postgres.ErrConflict)
}

func TestTenantsDoNotSeeEachOthersRecords(t *testing.T) {
	pool := pgtest.Pool(t)
	require.NoError(t, postgres.NewRevisions(pool, "t4").Record(t.Context(), revision(t, "dddd444", "AKIA[0-9A-Z]{16}")))

	got, err := postgres.NewRevisions(pool, "t5").Objects(t.Context(), "dddd444")

	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, postgres.NewRevisions(pool, "t5").Record(t.Context(), revision(t, "dddd444", "ghp_[A-Za-z0-9]{36}")))
}

func TestRecordsAreAppendOnly(t *testing.T) {
	pool := pgtest.Pool(t)
	require.NoError(t, postgres.NewRevisions(pool, "t6").Record(t.Context(), revision(t, "eeee555", "AKIA[0-9A-Z]{16}")))

	_, err := pool.Exec(t.Context(), `UPDATE config_objects SET hash = 'x' WHERE tenant_id = 't6'`)
	require.ErrorContains(t, err, "config_objects is append-only")
	_, err = pool.Exec(t.Context(), `DELETE FROM config_revisions WHERE tenant_id = 't6'`)
	require.ErrorContains(t, err, "config_revisions is append-only")

	err = pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL agentswarm.retention = 'on'"); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `DELETE FROM config_objects WHERE tenant_id = 't6'`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM config_revisions WHERE tenant_id = 't6'`)
		return err
	})
	require.NoError(t, err, "the retention job may delete")
}
