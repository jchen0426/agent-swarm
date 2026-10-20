//go:build e2e

package e2e

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// applied matches a line of migrate's output that names a migration it
// applied, and level the line that ends it.
var (
	applied = regexp.MustCompile(`^applied ((\d+)_\w+\.sql)$`)
	level   = regexp.MustCompile(`^schema at level (\d+) \((\d+_\w+\.sql)\)$`)
)

func TestMigrateSetsUpTheHarnessDatabase(t *testing.T) {
	env := harness.New(t, harness.Options{})

	first := strings.Split(strings.TrimSuffix(env.Migrate(t), "\n"), "\n")

	require.Greater(t, len(first), 1, "a fresh database gains every migration")
	var name, number string
	for _, line := range first[:len(first)-1] {
		m := applied.FindStringSubmatch(line)
		require.NotNil(t, m, line)
		name, number = m[1], strings.TrimLeft(m[2], "0")
	}
	last := first[len(first)-1]
	assert.Equal(t, []string{last, number, name}, level.FindStringSubmatch(last), "the schema is at its last migration's level")

	assert.Equal(t, last+"\n", env.Migrate(t), "a second run applies nothing")
}

// A fresh schema's chain is empty and sound: the only finding is the file
// store's, which cannot keep anchors write-once (R22).
func TestAuditVerifyOfAFreshSchemaFindsOnlyTheUnlockedStore(t *testing.T) {
	env := harness.New(t, harness.Options{})
	env.Migrate(t)

	assert.Equal(t, []string{"store_unlocked"}, env.AuditVerify(t))
}
