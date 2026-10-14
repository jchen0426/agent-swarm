package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

const incident = "work-item-types/incident.yaml"

func TestUnsetSettingsTakeTheSpecsDefaults(t *testing.T) {
	s := build(t, exampleRepo()).WorkItemType("incident").Settings()

	assert.Equal(t, config.Budget{WallClock: 15 * time.Minute, Tokens: 2_000_000, Rounds: 6}, s.Budget)
	assert.Equal(t, 150*time.Second, s.RoundLimit)
	assert.Equal(t, 4, s.MaxAgents)
	assert.Equal(t, config.Limits{EntriesPerRound: 5, OpenClaims: 2, LeaseRounds: 2}, s.Limits)
	assert.Equal(t, config.Convergence{MinContributors: 2, MinSources: 2, RefutationMargin: 2}, s.Convergence)
	assert.Equal(t, 30*time.Minute, s.AutoClose)
	assert.InDelta(t, 0.5, s.InvisibleShare, 0)
	assert.Equal(t, []string{"acknowledged", "cause_identified", "mitigation_started", "recovered"}, s.Milestones)
	require.Len(t, s.Key, 2)
	assert.Equal(t, []string{"bad-release", "flag-change", "config-change"}, s.Key[1].Values.List)
	require.NotNil(t, s.Key[0].Values.Read)
	assert.Equal(t, "catalog.list_services", s.Key[0].Values.Read.Tool)
}

func TestZeroIsASettingWhereZeroMeansSomething(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, "  milestones:", "  measurement: {invisibleShare: 0}\n  convergence: {refutationMargin: 0}\n  milestones:")

	s := build(t, repo).WorkItemType("incident").Settings()

	assert.Zero(t, s.InvisibleShare)
	assert.Zero(t, s.Convergence.RefutationMargin)
}

func TestSettingsAreChecked(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, "  milestones:", `  budget: {wallClock: 0s, tokens: -5, rounds: 0}
  round: {timeLimit: 20m}
  team: {maxAgents: 17}
  limits: {entriesPerRound: 0, openClaims: -1, leaseRounds: 0}
  convergence: {minContributors: 0, minSources: 0, refutationMargin: -1}
  autoClose: {after: -1m}
  measurement: {invisibleShare: 1.5}
  milestones:`)

	ps := refused(t, repo)

	for _, field := range []string{
		"spec.budget.wallClock", "spec.budget.tokens", "spec.budget.rounds", "spec.round.timeLimit",
		"spec.team.maxAgents", "spec.limits.entriesPerRound", "spec.limits.openClaims", "spec.limits.leaseRounds",
		"spec.convergence.minContributors", "spec.convergence.minSources", "spec.convergence.refutationMargin",
		"spec.autoClose.after", "spec.measurement.invisibleShare",
	} {
		assertProblem(t, ps, incident, field, "")
	}
}

func TestARoundFitsInTheWallClockBudget(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, "  milestones:", "  round: {timeLimit: 16m}\n  milestones:")

	ps := refused(t, repo)

	assertProblem(t, ps, incident, "spec.round.timeLimit", "longer than the wall-clock budget of 15m0s")
}

func TestAClosedListHoldsDistinctValuesOtherThanOther(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, "values: [bad-release, flag-change, config-change]", "values: [bad-release, other, bad-release, '']")

	ps := refused(t, repo)

	assertProblem(t, ps, incident, "spec.hypothesisKey[1].values[1]", `accepts "other" already`)
	assertProblem(t, ps, incident, "spec.hypothesisKey[1].values[2]", `repeats "bad-release"`)
	assertProblem(t, ps, incident, "spec.hypothesisKey[1].values[3]", "is empty")
}

func TestAReadOfKeyValuesIsDecodedAsStrictlyAsTheRest(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, `values: {tool: catalog.list_services, field: "$[*].name"}`,
		`values: {tool: catalog.list_services, filter: x, field: "$[*].name"}`)

	ps := refused(t, repo)

	assertProblem(t, ps, incident, "", "field filter not found in a read")
}

func TestAKeyFieldNeedsItsValuesAndAUniqueName(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, "    - {name: failureMode, values: [bad-release, flag-change, config-change]}\n",
		"    - {name: failureMode, values: [bad-release]}\n    - {name: region}\n"+
			"    - {name: team, values: {tool: catalog.list_services, args: {owner: '{labels.team}'}, field: $.team}}\n"+
			"    - {name: service, values: [a]}\n    - {name: 'bad name', values: [a]}\n")

	ps := refused(t, repo)

	assertProblem(t, ps, incident, "spec.hypothesisKey[2].values", "is required: a list, or a read")
	assertProblem(t, ps, incident, "spec.hypothesisKey[3].values.args.owner", "uses {labels.team}; key values are read with fixed arguments")
	assertProblem(t, ps, incident, "spec.hypothesisKey[4].name", `repeats field "service"`)
	assertProblem(t, ps, incident, "spec.hypothesisKey[5].name", "a field name such as failureMode")
}

func TestShadowRunsOverrideThePrimaryRunsSettings(t *testing.T) {
	s := build(t, exampleRepo()).WorkItemType("incident").Settings()

	require.Len(t, s.ShadowRuns, 1)
	run := s.ShadowRuns[0]
	assert.Equal(t, "independent", run.Name)
	assert.Equal(t, []string{"invisible"}, run.Arms)
	assert.Equal(t, config.Budget{WallClock: 15 * time.Minute, Tokens: 2_000_000, Rounds: 1}, run.Budget)
	assert.Equal(t, 15*time.Minute, run.RoundLimit)
	assert.Empty(t, run.Agents)
}

func TestShadowRunsAreChecked(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, "      override: {budget: {rounds: 1}, round: {timeLimit: 15m}}\n",
		"      override: {budget: {rounds: 1}, round: {timeLimit: 15m}}\n"+
			"    - {name: independent, arms: [visible, visible, both]}\n"+
			"    - {name: Comparator, arms: [], override: {agents: [release-investigator, ghost], runtime: 'Other Runtime'}}\n")

	ps := refused(t, repo)

	assertProblem(t, ps, incident, "spec.shadowRuns[1].name", `repeats shadow run "independent"`)
	assertProblem(t, ps, incident, "spec.shadowRuns[1].arms[1]", `got "visible"`)
	assertProblem(t, ps, incident, "spec.shadowRuns[1].arms[2]", `got "both"`)
	assertProblem(t, ps, incident, "spec.shadowRuns[2].name", "lowercase DNS label")
	assertProblem(t, ps, incident, "spec.shadowRuns[2].arms", "names no arm")
	assertProblem(t, ps, incident, "spec.shadowRuns[2].override.agents[1]", `no agent "ghost"`)
	assertProblem(t, ps, incident, "spec.shadowRuns[2].override.runtime", "runtime name")
}

func TestMilestonesAreWordsEachOnce(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, "milestones: [acknowledged, cause_identified, mitigation_started, recovered]",
		"milestones: [acknowledged, acknowledged, Cause Identified]")

	ps := refused(t, repo)

	assertProblem(t, ps, incident, "spec.milestones[1]", "each once")
	assertProblem(t, ps, incident, "spec.milestones[2]", "lowercase word")
}
