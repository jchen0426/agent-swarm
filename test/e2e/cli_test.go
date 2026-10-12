//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionPrintsBuildInfo(t *testing.T) {
	out, errOut, code := agentswarm(t, "version")

	require.Equal(t, 0, code, errOut)
	var info struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.NotEmpty(t, info.Version)
}

func TestUnknownCommandExitsWithUsage(t *testing.T) {
	_, errOut, code := agentswarm(t, "frobnicate")

	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, `unknown command "frobnicate"`)
}
