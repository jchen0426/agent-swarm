package checkset_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/checkset"
	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

func at(minute int) time.Time { return time.Date(2025, 12, 15, 17, minute, 0, 0, time.UTC) }

// saveDataset writes a small dataset: shipping alerts after a change to its
// Deployment and a later flag change.
func saveDataset(t *testing.T, dir string) {
	t.Helper()
	d := &dataset.Dataset{
		Meta: dataset.Meta{Namespace: "otel-demo", AlertTime: at(59), WindowStart: at(29), Trigger: []dataset.Alert{
			{Name: "RequestErrorRate", Service: "shipping", Severity: "warning", State: "firing", ActiveAt: at(55), Description: "Error rate of shipping is above 0"},
		}},
		Changes: []dataset.Change{
			{Time: at(40), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated"},
			{Time: at(50), Kind: "ConfigMap", Name: "flagd-config", Service: "flagd", Action: "updated"},
		},
		Logs: []dataset.Log{{Time: at(56), Service: "quote", Body: "ok"}},
	}
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, d.Save(dir))
}

func testSet() *score.Set {
	return &score.Set{
		Namespace: "otel-demo", FlagStore: "flagd-config",
		FailureModes: []string{"config-change", "flag-change", "other"},
		Scenarios:    []score.Scenario{{ID: "S16", Dir: "Scenario-16", Change: true, Keys: []score.Key{{Service: "shipping", FailureMode: "config-change"}}}},
	}
}

var modes = []checkset.Mode{
	{Name: "config-change", Meaning: "A configuration change broke the service."},
	{Name: "flag-change", Meaning: "A feature flag was turned on or off."},
}

func TestBuildWritesWhatTheAgentsSeeAndWhatScoresThem(t *testing.T) {
	dir := t.TempDir()
	saveDataset(t, filepath.Join(dir, "S16"))
	set := testSet()

	out, err := checkset.Build(set, set.Scenarios, dir, modes, "http://127.0.0.1:8080")

	require.NoError(t, err)
	assert.Equal(t, modes, out.FailureModes, "other is never listed: every key field accepts it")
	require.Len(t, out.Cases, 1)
	c := out.Cases[0]
	assert.Equal(t, "S16", c.ID)
	assert.Equal(t, []string{"http://127.0.0.1:8080/S16/mcp"}, c.Task.Servers)
	assert.Equal(t, []string{"flagd", "quote", "shipping"}, c.Task.Services)
	assert.Equal(t, "Alerts firing at 2025-12-15T17:59:00Z:\n"+
		"- RequestErrorRate (warning) on service shipping, active since 2025-12-15T17:55:00Z: Error rate of shipping is above 0", c.Task.Alert)
	assert.True(t, c.Change)
	assert.Equal(t, []score.Key{{Service: "shipping", FailureMode: "config-change"}}, c.Labels)
	assert.Equal(t, &score.Key{Service: "flagd", FailureMode: "flag-change"}, c.Baseline, "the latest change in the namespace")
}

func TestBuildRefusesAKeyOfAServiceTheDataDoesNotName(t *testing.T) {
	dir := t.TempDir()
	saveDataset(t, filepath.Join(dir, "S16"))
	set := testSet()
	set.Scenarios[0].Keys = append(set.Scenarios[0].Keys, score.Key{Service: "payment", FailureMode: "config-change"})

	_, err := checkset.Build(set, set.Scenarios, dir, modes, "http://127.0.0.1:8080")

	require.ErrorContains(t, err, "S16: key payment/config-change names a service the data does not")
}

func TestLoadModesNeedsAMeaningForEveryFailureModeButOther(t *testing.T) {
	write := func(content string) string {
		path := filepath.Join(t.TempDir(), "modes.yaml")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}
	set := testSet()
	good := "- {name: config-change, meaning: A configuration change broke the service.}\n- {name: flag-change, meaning: A feature flag was turned on or off.}\n"

	got, err := checkset.LoadModes(write(good), set)
	require.NoError(t, err)
	assert.Equal(t, modes, got)

	for name, content := range map[string]string{
		"missing mode":  "- {name: config-change, meaning: x}\n",
		"unknown mode":  good + "- {name: outage, meaning: x}\n",
		"repeated mode": good + "- {name: flag-change, meaning: x}\n",
		"no meaning":    strings.Replace(good, "A feature flag was turned on or off.", "''", 1),
		"other":         good + "- {name: other, meaning: x}\n",
	} {
		_, err := checkset.LoadModes(write(content), set)
		assert.Error(t, err, name)
	}
}
