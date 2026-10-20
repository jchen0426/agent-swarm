//go:build integration

package pgtest_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

// make test-clean finds the containers a killed test run left behind by these
// labels, and backs up the data the second one names.
func TestMainLabelsItsContainer(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "docker", "inspect", "--format", "{{json .Config.Labels}}", pgtest.ContainerID()).Output()
	require.NoError(t, err)

	var labels map[string]string
	require.NoError(t, json.Unmarshal(out, &labels))
	assert.Equal(t, "agent-swarm", labels["agentswarm.project"])
	assert.Equal(t, "/var/lib/postgresql/data", labels["agentswarm.data"])
}
