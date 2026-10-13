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
	assert.Contains(t, out, "raters")
	assert.Contains(t, out, "hits 1/2 (change scenarios 1/1); baseline 1/2 (change scenarios 1/1); only the answers right: 0; only the baseline right: 0; "+
		"answers that raters would judge, counted as misses: 0")
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

const testModes = `- {name: bad-release, meaning: A new version broke the service.}
- {name: flag-change, meaning: A feature flag was turned on or off.}
- {name: config-change, meaning: A configuration change broke the service.}
`

// saveDatasets writes S1, where shipping changed before the alert, and S2,
// where cart only logged.
func saveDatasets(t *testing.T, dir string) string {
	t.Helper()
	datasets := filepath.Join(dir, "datasets")
	alert := time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)
	for id, d := range map[string]*dataset.Dataset{
		"S1": {Meta: dataset.Meta{AlertTime: alert, Trigger: []dataset.Alert{{Name: "RequestErrorRate", Service: "shipping", ActiveAt: alert}}},
			Changes: []dataset.Change{{Time: alert.Add(-5 * time.Minute), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated"}}},
		"S2": {Meta: dataset.Meta{AlertTime: alert, Trigger: []dataset.Alert{{Name: "RequestLatency", Service: "cart", ActiveAt: alert}}},
			Logs: []dataset.Log{{Time: alert, Service: "cart", Body: "slow"}}},
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(datasets, id), 0o750))
		require.NoError(t, d.Save(filepath.Join(datasets, id)))
	}
	return datasets
}

func TestCheckSetWritesTheRetrospectiveSet(t *testing.T) {
	dir := t.TempDir()
	set := writeTestFile(t, dir, "set.yaml", testSet)
	modes := writeTestFile(t, dir, "modes.yaml", testModes)
	datasets := saveDatasets(t, dir)

	out, errOut, code := runCommand("checkset", "--set", set, "--datasets", datasets, "--modes", modes, "--server", "http://127.0.0.1:9090")

	require.Equal(t, exitOK, code, errOut)
	var got struct {
		FailureModes []map[string]string `json:"failureModes"`
		Cases        []struct {
			ID   string `json:"id"`
			Task struct {
				Servers  []string `json:"servers"`
				Services []string `json:"services"`
			} `json:"task"`
			Baseline map[string]string `json:"baseline"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Len(t, got.FailureModes, 3)
	require.Len(t, got.Cases, 2)
	assert.Equal(t, []string{"http://127.0.0.1:9090/S1/mcp"}, got.Cases[0].Task.Servers)
	assert.Equal(t, map[string]string{"service": "shipping", "failureMode": "config-change"}, got.Cases[0].Baseline)
	assert.Equal(t, []string{"cart"}, got.Cases[1].Task.Services)
	assert.Nil(t, got.Cases[1].Baseline)
}

func TestServeTakesOneDatasetOrADirectoryOfThem(t *testing.T) {
	dir := t.TempDir()
	datasets := saveDatasets(t, dir)

	_, errOut, code := runCommand("serve")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "give --dataset or --datasets")

	_, errOut, code = runCommand("serve", "--dataset", filepath.Join(datasets, "S1"), "--datasets", datasets)
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "give --dataset or --datasets")

	loaded, err := loadDatasets(datasets)
	require.NoError(t, err)
	assert.Len(t, loaded, 2)
	assert.Contains(t, loaded, "S2")

	_, err = loadDatasets(t.TempDir())
	require.ErrorContains(t, err, "no datasets")
}

func TestServeRefusesADatasetDirectoryNameUnfitForAPath(t *testing.T) {
	dir := t.TempDir()
	datasets := saveDatasets(t, dir)
	require.NoError(t, os.Rename(filepath.Join(datasets, "S2"), filepath.Join(datasets, "S 2 {x}")))

	_, err := loadDatasets(datasets)

	require.ErrorContains(t, err, `dataset directory "S 2 {x}"`)
}
