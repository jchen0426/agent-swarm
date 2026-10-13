package check_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/measurement/check"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

const validSet = `{
  "failureModes": [
    {"name": "bad-release", "meaning": "A new version of the service broke it."},
    {"name": "config-change", "meaning": "A change to the service's configuration broke it."}
  ],
  "cases": [
    {
      "id": "S1",
      "task": {"servers": ["http://127.0.0.1:1/S1/mcp"], "alert": "Errors on checkout", "services": ["cart", "checkout"]},
      "change": true,
      "labels": [{"service": "checkout", "failureMode": "config-change"}],
      "baseline": {"service": "cart", "failureMode": "bad-release"}
    },
    {
      "id": "S2",
      "task": {"servers": ["http://127.0.0.1:1/S2/mcp"], "alert": "Latency on cart", "services": ["cart", "checkout"]},
      "labels": [{"service": "cart", "failureMode": "bad-release"}, {"service": "cart", "failureMode": "config-change"}]
    }
  ]
}`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadSetReadsTasksAndAnswers(t *testing.T) {
	set, err := check.LoadSet(writeFile(t, t.TempDir(), "set.json", validSet))

	require.NoError(t, err)
	require.Len(t, set.Cases, 2)
	assert.Equal(t, []string{"bad-release", "config-change"}, set.FailureModeNames())
	c := set.Cases[0]
	assert.Equal(t, "S1", c.ID)
	assert.Equal(t, "Errors on checkout", c.Task.Alert)
	assert.True(t, c.Change)
	assert.Equal(t, []key.Key{{"service": "checkout", "failureMode": "config-change"}}, c.Labels)
	assert.Equal(t, key.Key{"service": "cart", "failureMode": "bad-release"}, c.Baseline)
	assert.Nil(t, set.Cases[1].Baseline, "a case without a baseline answer")
}

func TestABaselineMayAnswerOther(t *testing.T) {
	raw := strings.Replace(validSet, `"baseline": {"service": "cart", "failureMode": "bad-release"}`,
		`"baseline": {"service": "other", "failureMode": "config-change"}`, 1)

	set, err := check.LoadSet(writeFile(t, t.TempDir(), "set.json", raw))

	require.NoError(t, err)
	assert.Equal(t, key.Key{"service": "other", "failureMode": "config-change"}, set.Cases[0].Baseline)
}

func TestLoadSetRefusesASetThatCannotBeScored(t *testing.T) {
	tests := map[string]struct{ from, to, want string }{
		"unknown field":            {`"change": true,`, `"change": true, "why": "x",`, "unknown field"},
		"repeated case":            {`"id": "S2"`, `"id": "S1"`, "case S1 appears twice"},
		"case id unfit for a path": {`"id": "S2"`, `"id": "../S2"`, "case id"},
		"no server":                {`"servers": ["http://127.0.0.1:1/S2/mcp"]`, `"servers": []`, "case S2 has no server"},
		"no alert":                 {`"alert": "Latency on cart"`, `"alert": " "`, "case S2 has no alert"},
		"no services":              {`"services": ["cart", "checkout"]},` + "\n      \"labels\": [{\"service\": \"cart\"", `"services": []},` + "\n      \"labels\": [{\"service\": \"cart\"", "case S2 has no services"},
		"other as a service":       {`"services": ["cart", "checkout"]},` + "\n      \"change\"", `"services": ["cart", "other"]},` + "\n      \"change\"", `case S1: "other" is not a service`},
		"no label":                 {`"labels": [{"service": "checkout", "failureMode": "config-change"}]`, `"labels": []`, "case S1 has no labels"},
		"label of another service": {`"labels": [{"service": "checkout"`, `"labels": [{"service": "payment"`, "case S1: label"},
		"label with other":         {`"labels": [{"service": "checkout"`, `"labels": [{"service": "other"`, "case S1: label"},
		"label without a mode":     {`{"service": "checkout", "failureMode": "config-change"}`, `{"service": "checkout"}`, "case S1: label"},
		"baseline of no mode":      {`"failureMode": "bad-release"}`, `"failureMode": "outage"}`, "case S1: baseline"},
		"repeated failure mode":    {`{"name": "config-change"`, `{"name": "bad-release"`, "failure mode bad-release appears twice"},
		"other as a failure mode":  {`{"name": "config-change"`, `{"name": "other"`, `"other" is not a failure mode`},
		"failure mode unexplained": {`"meaning": "A new version of the service broke it."`, `"meaning": ""`, "failure mode bad-release has no meaning"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, validSet, tt.from)
			raw := strings.Replace(validSet, tt.from, tt.to, 1)

			_, err := check.LoadSet(writeFile(t, t.TempDir(), "set.json", raw))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoadSetRefusesAnEmptySetAndTrailingData(t *testing.T) {
	_, err := check.LoadSet(writeFile(t, t.TempDir(), "set.json", `{"failureModes": [{"name": "a", "meaning": "b"}], "cases": []}`))
	require.ErrorContains(t, err, "no cases")

	_, err = check.LoadSet(writeFile(t, t.TempDir(), "set.json", validSet+"{}"))
	require.ErrorContains(t, err, "data after the JSON value")
}

func TestLoadAgentsReadsEachSkillBesideTheFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes.md", "Look at the changes.")
	writeFile(t, dir, "runtime.md", "Look at the runtime.")
	path := writeFile(t, dir, "agents.json", `[{"name": "changes", "skill": "changes.md"}, {"name": "runtime", "skill": "runtime.md"}]`)

	agents, err := check.LoadAgents(path)

	require.NoError(t, err)
	assert.Equal(t, []check.Agent{{Name: "changes", Skill: "Look at the changes."}, {Name: "runtime", Skill: "Look at the runtime."}}, agents)
}

func TestLoadAgentsRefusesBadAgents(t *testing.T) {
	tests := map[string]struct{ agents, want string }{
		"none":            {`[]`, "no agents"},
		"repeated name":   {`[{"name": "a", "skill": "a.md"}, {"name": "a", "skill": "a.md"}]`, "agent a appears twice"},
		"name unfit":      {`[{"name": "A/b", "skill": "a.md"}]`, "agent name"},
		"missing skill":   {`[{"name": "a", "skill": "missing.md"}]`, "missing.md"},
		"empty skill":     {`[{"name": "a", "skill": "empty.md"}]`, "skill of agent a is empty"},
		"unknown field":   {`[{"name": "a", "skill": "a.md", "model": "x"}]`, "unknown field"},
		"skill elsewhere": {`[{"name": "a", "skill": "../a.md"}]`, "../a.md"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.md", "Skill.")
			writeFile(t, dir, "empty.md", "  \n")

			_, err := check.LoadAgents(writeFile(t, dir, "agents.json", tt.agents))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
