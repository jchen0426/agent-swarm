package config_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

const investigator = "agents/release-investigator.yaml"

func TestGrantsResolveToListedTools(t *testing.T) {
	a := build(t, exampleRepo()).Agent("release-investigator")
	require.NotNil(t, a)

	var refs []string
	for _, tool := range a.Tools() {
		refs = append(refs, tool.Ref.String()+" "+string(tool.Effect))
	}
	assert.Equal(t, []string{
		"metrics.query_instant read",
		"metrics.query_range read",
		"release-manager.get_release read",
		"release-manager.list_recent_changes read",
		"release-manager.rollback_release write",
	}, refs)
	assert.Equal(t, []config.SkillRef{{Skill: "release-diagnosis", Version: "1.2.0"}}, a.Skills())
}

func TestAPatternGrantsReadToolsOnly(t *testing.T) {
	repo := edit(t, exampleRepo(), investigator, `"release-manager.get_*"`, `"release-manager.*"`)

	ps := refused(t, repo)

	assertProblem(t, ps, investigator, "spec.tools[0]", "resolves to write tool release-manager.rollback_release; grant write tools by exact name")
}

func TestNoGrantReachesAPlatformTool(t *testing.T) {
	byPattern := edit(t, exampleRepo(), investigator, `"metrics.query_*"`, `"catalog.*"`)
	byName := edit(t, exampleRepo(), investigator, `"metrics.query_*"`, `catalog.responders_for`)

	assertProblem(t, refused(t, byPattern), investigator, "spec.tools[3]", "catalog.responsible_for, a platform tool")
	assertProblem(t, refused(t, byName), investigator, "spec.tools[3]", "catalog.responders_for, a platform tool")
}

func TestAGrantMustResolveToAListedTool(t *testing.T) {
	repo := edit(t, exampleRepo(), investigator, `"metrics.query_*"`, `"metrics.delete_*", logs.search, "metrics", "metrics.query_[a]"`)

	ps := refused(t, repo)

	assertProblem(t, ps, investigator, "spec.tools[3]", `resolves to no tool that server "metrics" lists`)
	assertProblem(t, ps, investigator, "spec.tools[4]", `no registered server "logs"`)
	assertProblem(t, ps, investigator, "spec.tools[5]", "must be server.tool or server.pattern")
	assertProblem(t, ps, investigator, "spec.tools[6]", "must be server.tool or server.pattern")
}

func TestStarMatchesWithinAToolName(t *testing.T) {
	repo := edit(t, exampleRepo(), investigator, `"metrics.query_*"`, `"metrics.*_range", "release-manager.*_recent_*"`)

	a := build(t, repo).Agent("release-investigator")

	var refs []string
	for _, tool := range a.Tools() {
		refs = append(refs, tool.Ref.String())
	}
	assert.Contains(t, refs, "metrics.query_range")
	assert.NotContains(t, refs, "metrics.query_instant")
	assert.Contains(t, refs, "release-manager.list_recent_changes")
}

func TestAnAgentPinsOneReleasedVersionOfEachSkill(t *testing.T) {
	repo := edit(t, exampleRepo(), investigator, "skills: [release-diagnosis@1.2.0]",
		"skills: [release-diagnosis@1.2.0, release-diagnosis@1.1.0, flags@2.0.0, release-diagnosis]")

	ps := refused(t, repo)

	assertProblem(t, ps, investigator, "spec.skills[1]", `pins skill "release-diagnosis" again`)
	assertProblem(t, ps, investigator, "spec.skills[2]", "no released version flags@2.0.0")
	assertProblem(t, ps, investigator, "spec.skills[3]", "must be skill@version")
}

func TestAnAgentNamesItsRuntimeModelAndSubscriptions(t *testing.T) {
	repo := edit(t, exampleRepo(), investigator, "model: claude-haiku-5-5", "model: ''")
	repo = edit(t, repo, investigator, "subscribe: {workItemTypes: [incident], taskTags: [release]}",
		"subscribe: {workItemTypes: [incident, change], taskTags: [Release Tag]}")

	ps := refused(t, repo)

	assertProblem(t, ps, investigator, "spec.model", "model id")
	assertProblem(t, ps, investigator, "spec.subscribe.workItemTypes[1]", `got "change"`)
	assertProblem(t, ps, investigator, "spec.subscribe.taskTags[0]", "lowercase word")
}

func TestAgentsComeInConfigurationOrder(t *testing.T) {
	second := edit(t, exampleRepo(), investigator, "name: release-investigator,", "name: a-investigator,")
	repo := with(exampleRepo(), map[string]*fstest.MapFile{"agents/a-investigator.yaml": second[investigator]})

	agents := build(t, repo).Agents()

	require.Len(t, agents, 2)
	assert.Equal(t, "a-investigator", agents[0].Metadata.Name)
	assert.Equal(t, "release-investigator", agents[1].Metadata.Name)
}

func TestYankedPinsAreTheAgentsWithdrawnVersions(t *testing.T) {
	assert.Empty(t, build(t, exampleRepo()).YankedPins("release-investigator"))

	pinsOld := edit(t, exampleRepo(), investigator, "release-diagnosis@1.2.0", "release-diagnosis@1.1.0")
	rev := build(t, pinsOld)

	assert.Equal(t, []config.SkillRef{{Skill: "release-diagnosis", Version: "1.1.0"}}, rev.YankedPins("release-investigator"))
	assert.Nil(t, rev.YankedPins("nobody"))
}
