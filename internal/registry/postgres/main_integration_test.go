//go:build integration

package postgres_test

import (
	"os"
	"testing"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}
