package score_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

const setYAML = `benchmark: ITBench-Lite SRE v0.2
source: https://huggingface.co/datasets/ibm-research/ITBench-Lite
revision: d0916b08ba421ce5e672e9ad68aa947d938dfef0
prefix: snapshots/sre/v0.2-B96DF826-4BB2-4B62-97AB-6D84254C53D7
namespace: otel-demo
flagStore: flagd-config
failureModes: [bad-release, flag-change, config-change, other]
scenarios:
  - id: S16
    dir: Scenario-16
    change: true
    keys:
      - {service: shipping, failureMode: config-change}
      - {service: shipping, failureMode: bad-release}
    why: QUOTE_ADDR set to a bad port.
  - id: S21
    dir: Scenario-21
    change: false
    keys:
      - {service: valkey-cart, failureMode: bad-release}
    why: test.
excluded:
  - {id: S12, why: its ground truth contradicts itself}
`

func writeSet(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenarios.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadReadsTheSet(t *testing.T) {
	set, err := score.Load(writeSet(t, setYAML))

	require.NoError(t, err)
	assert.Equal(t, "otel-demo", set.Namespace)
	assert.Equal(t, "flagd-config", set.FlagStore)
	require.Len(t, set.Scenarios, 2)
	assert.Equal(t, "Scenario-16", set.Scenarios[0].Dir)
	assert.True(t, set.Scenarios[0].Change)
	assert.Equal(t, score.Key{Service: "shipping", FailureMode: "bad-release"}, set.Scenarios[0].Keys[1])
	assert.Equal(t, "S12", set.Excluded[0].ID)
}

func TestLoadRejectsBadSets(t *testing.T) {
	for name, bad := range map[string]string{
		"unknown field":        setYAML + "extra: 1\n",
		"mode outside the set": strings.Replace(setYAML, "failureMode: bad-release}\n    why: test.", "failureMode: traffic-surge}\n    why: test.", 1),
		"other as an answer":   strings.Replace(setYAML, "failureMode: bad-release}\n    why: test.", "failureMode: other}\n    why: test.", 1),
		"no keys":              strings.Replace(setYAML, "    keys:\n      - {service: valkey-cart, failureMode: bad-release}\n", "    keys: []\n", 1),
		"repeated id":          strings.Replace(setYAML, "id: S21", "id: S16", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := score.Load(writeSet(t, bad))
			assert.Error(t, err)
		})
	}
}

func TestHitNeedsAnAcceptedKeyAndNeverOther(t *testing.T) {
	set, err := score.Load(writeSet(t, setYAML))
	require.NoError(t, err)
	s16 := set.Scenarios[0]

	assert.True(t, s16.Hit(&score.Key{Service: "shipping", FailureMode: "bad-release"}))
	assert.False(t, s16.Hit(&score.Key{Service: "quote", FailureMode: "config-change"}))
	assert.False(t, s16.Hit(nil), "no conclusion is not a hit")
}

func TestCompareCountsHitsAndDiscordance(t *testing.T) {
	set, err := score.Load(writeSet(t, setYAML))
	require.NoError(t, err)
	agents := score.Answers{"S16": {Service: "quote", FailureMode: "config-change"}, "S21": {Service: "valkey-cart", FailureMode: "bad-release"}}
	baseline := score.Answers{"S16": {Service: "shipping", FailureMode: "config-change"}}

	r := score.Compare(set, agents, baseline)

	assert.Equal(t, 1, r.Hits)
	assert.Equal(t, 1, r.BaselineHits)
	assert.Equal(t, 1, r.OnlyAnswers)
	assert.Equal(t, 1, r.OnlyBaseline)
	assert.Equal(t, 0, r.ChangeHits)
	assert.Equal(t, 1, r.ChangeBaselineHits)
	require.Len(t, r.Rows, 2)
	assert.Equal(t, score.Row{ID: "S16", Change: true, Answer: "quote/config-change", Hit: false, Baseline: "shipping/config-change", BaselineHit: true}, r.Rows[0])
	assert.Equal(t, score.Row{ID: "S21", Answer: "valkey-cart/bad-release", Hit: true, Baseline: "none"}, r.Rows[1])
}

func TestTheFrozenSetIsValid(t *testing.T) {
	set, err := score.Load("../scenarios.yaml")

	require.NoError(t, err)
	assert.Equal(t, "d0916b08ba421ce5e672e9ad68aa947d938dfef0", set.Revision)
	assert.Equal(t, "flagd-config", set.FlagStore)
	var ids, changes []string
	for _, sc := range set.Scenarios {
		ids = append(ids, sc.ID)
		if sc.Change {
			changes = append(changes, sc.ID)
		}
	}
	assert.Equal(t, []string{"S4", "S5", "S16", "S20", "S21", "S23", "S24", "S33", "S34", "S80", "S81", "S102", "S105"}, ids)
	assert.Equal(t, []string{"S4", "S5", "S16", "S20", "S23", "S24", "S33", "S102", "S105"}, changes)
	assert.Len(t, set.Excluded, 2)
}
