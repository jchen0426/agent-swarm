package db

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrIncompatible is returned when this build does not run on the schema:
// the schema is behind it, or needs a newer reader.
var ErrIncompatible = errors.New("db: this build does not run on the schema")

// undefinedTable is the SQLSTATE of a query of a table that does not exist.
const undefinedTable = "42P01"

// State is how a schema stands to a build.
type State string

// How a schema stands to a build.
const (
	// Compatible: the build runs on the schema.
	Compatible State = "compatible"
	// Behind: the schema lacks migrations of the build's.
	Behind State = "behind"
	// Incompatible: a migration of the schema needs a newer reader than the
	// build.
	Incompatible State = "incompatible"
)

// Schema is the database's schema as its migrations left it.
type Schema struct {
	Level     int    // its last applied migration's number
	Last      string // that migration's file name
	MinReader int    // the largest min_reader of its applied migrations
}

// State returns how the schema stands to a build of the level: a build runs
// on a schema whose level is at least its own, and whose min_reader is at
// most its own level.
func (s Schema) State(build int) State {
	switch {
	case s.Level < build:
		return Behind
	case s.MinReader > build:
		return Incompatible
	}
	return Compatible
}

// describe names the schema's level, its last migration and its min_reader.
func (s Schema) describe() string {
	if s.Last == "" {
		return fmt.Sprintf("level %d, min_reader %d", s.Level, s.MinReader)
	}
	return fmt.Sprintf("level %d (%s), min_reader %d", s.Level, s.Last, s.MinReader)
}

// Level returns this build's level: its last migration's number.
func Level() int { return buildLevel() }

// buildLevel reads this build's level once. The embedded migrations are part
// of the build, and the tests of this package read them all, so they always
// read.
var buildLevel = sync.OnceValue(func() int {
	ms, err := readMigrations(Migrations())
	if err != nil {
		panic("db: the embedded migrations: " + err.Error())
	}
	if len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].level
})

// ReadSchema reads, in one query, the schema the database's migrations left.
// A database no migration ever ran on is at level 0.
func ReadSchema(ctx context.Context, pool *pgxpool.Pool) (Schema, error) {
	_, s, err := readApplied(ctx, pool)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == undefinedTable {
		return Schema{}, nil
	}
	return s, err
}

// Checker checks the schema against this build, in one query, on the pool it
// is given: serve's start and its leader check it on the lease's pool, and
// its admin API reads it on the main pool.
type Checker struct {
	pool *pgxpool.Pool
}

// NewChecker returns a checker that reads the schema through the pool.
func NewChecker(pool *pgxpool.Pool) *Checker { return &Checker{pool: pool} }

// Check returns nil when this build runs on the schema, and otherwise an
// error that wraps ErrIncompatible and names the schema's state and the
// levels. A schema it cannot read gives an error of another kind.
func (c *Checker) Check(ctx context.Context) error {
	s, err := ReadSchema(ctx, c.pool)
	if err != nil {
		return err
	}
	build := Level()
	switch s.State(build) {
	case Behind:
		return fmt.Errorf("%w: the schema is behind: %s; this build is at level %d: run agentswarm migrate", ErrIncompatible, s.describe(), build)
	case Incompatible:
		return fmt.Errorf("%w: the schema is incompatible: %s; this build is at level %d", ErrIncompatible, s.describe(), build)
	}
	return nil
}

// State returns how the schema stands to this build, for /readyz and
// /v1/status.
func (c *Checker) State(ctx context.Context) (State, error) {
	s, err := ReadSchema(ctx, c.pool)
	if err != nil {
		return "", err
	}
	return s.State(Level()), nil
}
