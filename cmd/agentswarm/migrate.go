package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/secrets/files"
)

func newMigrateCommand() *cobra.Command {
	var (
		secretsDir, database string
		lockTimeout          time.Duration
		force                bool
	)
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply the database schema's migrations",
		Long: `Apply the migrations of this build that the database lacks, each in a
transaction of its own, and record for each the oldest build that can read the
schema it leaves, its min_reader. Print each migration applied, then the
schema's level.

The database is the one the libpq environment variables name (PGHOST, PGUSER
and the rest), or the one whose URL --database names, a secret reference that
resolves in the --secrets directory. Each wait for a lock lasts --lock-timeout
at most, so that a migration never holds the platform's queries up behind it
for long.

A migration that older builds cannot read is refused while an instance holds
the platform's lease, unless --force is given. Deploy such a migration in this
order: stop the older instances, migrate, then start this build.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if lockTimeout <= 0 {
				return failure{fmt.Errorf("--lock-timeout: %v is not above zero", lockTimeout)}
			}
			url, err := databaseURL(c.Context(), secretsDir, database)
			if err != nil {
				return failure{err}
			}
			pool, err := db.Open(c.Context(), url)
			if err != nil {
				return failure{err}
			}
			defer pool.Close()
			res, err := db.MigrateFS(c.Context(), pool, db.Migrations(), db.MigrateOptions{LockTimeout: lockTimeout, Force: force})
			var out strings.Builder
			for _, name := range res.Applied {
				out.WriteString("applied " + name + "\n")
			}
			if err == nil {
				fmt.Fprintf(&out, "schema at level %d (%s)\n", res.Schema.Level, res.Schema.Last)
			}
			if _, werr := io.WriteString(c.OutOrStdout(), out.String()); werr != nil && err == nil {
				err = fmt.Errorf("write output: %w", werr)
			}
			if err != nil {
				return failure{err}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&secretsDir, "secrets", "", "directory the --database reference resolves in")
	fl.StringVar(&database, "database", "", "secret reference of the database URL, such as secret://database/url; when empty, the libpq environment variables")
	fl.DurationVar(&lockTimeout, "lock-timeout", db.DefaultLockTimeout, "how long each wait for a lock lasts")
	fl.BoolVar(&force, "force", false, "apply a migration older builds cannot read while the platform's lease is held")
	cmd.MarkFlagsRequiredTogether("secrets", "database")
	return cmd
}

// databaseURL returns the database URL a secret reference names in the
// secrets directory, or "" for the libpq environment variables when there is
// no reference. Its errors never hold the URL.
func databaseURL(ctx context.Context, secretsDir, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	secrets, err := files.Open(secretsDir)
	if err != nil {
		return "", fmt.Errorf("--secrets: %w", err)
	}
	defer func() { _ = secrets.Close() }()
	url, err := secrets.Secret(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("--database: %w", err)
	}
	return url, nil
}
