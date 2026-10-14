//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type validation struct {
	Digest  string `json:"digest"`
	Objects []struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Hash    string `json:"hash"`
		Version string `json:"version"`
	} `json:"objects"`
	Problems []struct {
		File    string `json:"file"`
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"problems"`
}

func TestTheExampleConfigurationKeepsEveryRule(t *testing.T) {
	out, errOut, code := agentswarm(t, "registry", "validate", "--dir", "../../examples/config")

	require.Equal(t, 0, code, errOut)
	var v validation
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, v.Digest)
	agents := 0
	for _, o := range v.Objects {
		if o.Kind == "Agent" {
			agents++
			assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, o.Version, o.Name)
		}
	}
	assert.Equal(t, 3, agents)

	again, _, _ := agentswarm(t, "registry", "validate", "--dir", "../../examples/config")
	assert.Equal(t, out, again, "the same configuration validates to the same output")
}

func TestABrokenConfigurationIsRefusedWithEveryProblem(t *testing.T) {
	out, errOut, code := agentswarm(t, "registry", "validate", "--dir", "testdata/config-broken")

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "the configuration is refused: 2 problems")
	var v validation
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	require.Len(t, v.Problems, 2)
	assert.Equal(t, "agents/investigator.yaml", v.Problems[0].File)
	assert.Contains(t, v.Problems[0].Message, "grant write tools by exact name")
	assert.Equal(t, "servers/release-manager.yaml", v.Problems[1].File)
	assert.Contains(t, v.Problems[1].Message, "required for a write tool")
}
