package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func runCommand(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

const testSet = `benchmark: test
source: https://huggingface.co/datasets/org/bench
revision: d0916b08ba421ce5e672e9ad68aa947d938dfef0
prefix: snapshots
namespace: otel-demo
flagStore: flagd-config
failureModes: [bad-release, flag-change, config-change, other]
scenarios:
  - {id: S1, dir: Scenario-1, change: true, keys: [{service: shipping, failureMode: config-change}], why: test}
  - {id: S2, dir: Scenario-2, change: false, keys: [{service: cart, failureMode: bad-release}], why: test}
`

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestUsageErrors(t *testing.T) {
	_, errOut, code := runCommand()
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "a command is required")

	_, errOut, code = runCommand("score")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `required flag(s) "answers" not set`)
}

func TestScorePrintsEachScenarioAndTheTotals(t *testing.T) {
	dir := t.TempDir()
	set := writeTestFile(t, dir, "set.yaml", testSet)
	answers := writeTestFile(t, dir, "answers.json", `{"S1": {"service": "shipping", "failureMode": "config-change"}, "S2": {"service": "cart", "failureMode": "config-change"}}`)
	baseline := writeTestFile(t, dir, "baseline.json", `{"S1": {"service": "shipping", "failureMode": "config-change"}}`)

	out, errOut, code := runCommand("score", "--set", set, "--answers", answers, "--baseline", baseline)

	require.Equal(t, exitOK, code, errOut)
	assert.Contains(t, out, "S1")
	assert.Contains(t, out, "shipping/config-change")
	assert.Contains(t, out, "hits 1/2 (change scenarios 1/1); baseline 1/2 (change scenarios 1/1); only the answers right: 0; only the baseline right: 0")
}

func TestScoreRejectsAnAnswerOutsideTheSet(t *testing.T) {
	dir := t.TempDir()
	set := writeTestFile(t, dir, "set.yaml", testSet)
	answers := writeTestFile(t, dir, "answers.json", `{"S9": {"service": "x", "failureMode": "config-change"}}`)

	_, errOut, code := runCommand("score", "--set", set, "--answers", answers)

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "S9")
}

func TestBaselineWritesAnswersForEveryScenarioWithAChange(t *testing.T) {
	dir := t.TempDir()
	set := writeTestFile(t, dir, "set.yaml", testSet)
	datasets := filepath.Join(dir, "datasets")
	alert := time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)
	to := "quote:0000"
	for id, changes := range map[string][]dataset.Change{
		"S1": {{Time: alert.Add(-5 * time.Minute), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated",
			Fields: []dataset.Field{{Path: "spec.template.spec.containers[shipping].env[QUOTE_ADDR].value", To: &to}}}},
		"S2": nil,
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(datasets, id), 0o750))
		d := &dataset.Dataset{Meta: dataset.Meta{AlertTime: alert, Trigger: []dataset.Alert{{Service: "frontend"}}}, Changes: changes}
		require.NoError(t, d.Save(filepath.Join(datasets, id)))
	}

	out, errOut, code := runCommand("baseline", "--set", set, "--datasets", datasets)
	require.Equal(t, exitOK, code, errOut)
	var answers map[string]map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &answers))
	assert.Equal(t, map[string]map[string]string{"S1": {"service": "shipping", "failureMode": "config-change"}}, answers)

	out, errOut, code = runCommand("baseline", "--set", set, "--datasets", datasets, "--scope", "service")
	require.Equal(t, exitOK, code, errOut)
	assert.JSONEq(t, `{}`, out)
}

func TestServeNeedsADataset(t *testing.T) {
	_, errOut, code := runCommand("serve", "--dataset", filepath.Join(t.TempDir(), "missing"))

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "missing: no such file or directory")
}
