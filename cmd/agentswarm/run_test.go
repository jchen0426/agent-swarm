package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCommand(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestVersion(t *testing.T) {
	out, _, code := runCommand("version")

	require.Equal(t, exitOK, code)
	var info map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.Equal(t, "dev", info["version"])
}

func TestUsageErrors(t *testing.T) {
	_, errOut, code := runCommand()
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "Usage: agentswarm")

	_, errOut, code = runCommand("frobnicate")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `unknown command "frobnicate"`)
}
