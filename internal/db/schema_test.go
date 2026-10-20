package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/db"
)

// A build runs on a schema at its own level or later, unless a migration of
// the schema needs readers newer than the build.
func TestASchemasStateAgainstABuild(t *testing.T) {
	for _, c := range []struct {
		level, reader, build int
		want                 db.State
	}{
		{level: 8, reader: 8, build: 8, want: db.Compatible},
		{level: 7, reader: 7, build: 8, want: db.Behind},
		{level: 9, reader: 8, build: 8, want: db.Compatible},
		{level: 9, reader: 9, build: 8, want: db.Incompatible},
	} {
		s := db.Schema{Level: c.level, MinReader: c.reader}
		assert.Equal(t, c.want, s.State(c.build), "level %d, reader %d, build %d", c.level, c.reader, c.build)
	}
}
