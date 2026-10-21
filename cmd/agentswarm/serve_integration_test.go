//go:build integration

package main

import (
	"fmt"
	neturl "net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
)

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
		change  func(text string) string
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

			_, errOut, code := runCommand("serve", "--deployment", deployment)

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
