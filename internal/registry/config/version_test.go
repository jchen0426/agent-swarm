package config_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

func agentVersion(t *testing.T, repo fstest.MapFS) string {
	t.Helper()
	return build(t, repo).Agent("release-investigator").Version()
}

func TestAnAgentsVersionCoversItsFileSkillsAndTools(t *testing.T) {
	base := agentVersion(t, exampleRepo())
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, base)
	assert.Equal(t, base, agentVersion(t, exampleRepo()))

	newTool := edit(t, exampleRepo(), "servers/metrics.yaml", "    query_instant:",
		"    query_series: {schemaHash: \"sha256:9999999999999999999999999999999999999999999999999999999999999999\", effect: read}\n    query_instant:")
	assert.NotEqual(t, base, agentVersion(t, newTool), "a newly listed tool that a pattern matches is an agent change")

	rehashed := edit(t, exampleRepo(), "servers/metrics.yaml", "query_instant: {schemaHash: \"sha256:8888", "query_instant: {schemaHash: \"sha256:9999")
	assert.NotEqual(t, base, agentVersion(t, rehashed))

	skillChanged := edit(t, exampleRepo(), "skills/release-diagnosis/1.2.0/SKILL.md", "first", "before anything else")
	assert.NotEqual(t, base, agentVersion(t, skillChanged))

	fileChanged := edit(t, exampleRepo(), investigator, "taskTags: [release]", "taskTags: [release, flags]")
	assert.NotEqual(t, base, agentVersion(t, fileChanged))

	unrelated := edit(t, exampleRepo(), "servers/catalog.yaml", "endpoint: https://catalog-mcp.internal", "endpoint: https://catalog.internal")
	assert.Equal(t, base, agentVersion(t, unrelated), "a server the agent is not granted is not part of its version")
}

func TestTheDigestCoversEveryFileAndBundle(t *testing.T) {
	base := build(t, exampleRepo()).Digest()

	assert.Equal(t, base, build(t, exampleRepo()).Digest())
	assert.NotEqual(t, base, build(t, edit(t, exampleRepo(), "yanks.yaml", "ignored flag changes", "ignored flags")).Digest())
	assert.NotEqual(t, base, build(t, edit(t, exampleRepo(), "skills/release-diagnosis/1.1.0/SKILL.md", "first", "early")).Digest())
	assert.Equal(t, base, build(t, edit(t, exampleRepo(), "README.md", "checkout", "cart")).Digest(), "files outside the configuration are not part of it")
}

func TestALockPinsTheCommitTheDigestAndEveryRuntimeImage(t *testing.T) {
	rev := build(t, exampleRepo())
	img := "sha256:abababababababababababababababababababababababababababababababab"

	lock, err := rev.Lock(map[string]string{"thinloop": img, "unused": img})

	require.NoError(t, err)
	assert.Equal(t, config.Lock{Commit: "c0ffee0", Digest: rev.Digest(), RuntimeImages: map[string]string{"thinloop": img}}, lock)

	_, err = rev.Lock(map[string]string{"thinloop": "latest"})
	require.ErrorContains(t, err, `runtime "thinloop" of agent release-investigator has no image`)
}

func TestALockCoversShadowRunRuntimes(t *testing.T) {
	repo := edit(t, exampleRepo(), incident, "override: {budget: {rounds: 1}, round: {timeLimit: 15m}}",
		"override: {budget: {rounds: 1}, round: {timeLimit: 15m}, runtime: vendor-agent}")
	img := "sha256:abababababababababababababababababababababababababababababababab"

	_, err := build(t, repo).Lock(map[string]string{"thinloop": img})

	require.ErrorContains(t, err, `runtime "vendor-agent" of shadow run incident/independent`)
}

func TestObjectsRecordEveryObjectWithItsHash(t *testing.T) {
	rev := build(t, exampleRepo())

	byName := map[string]config.Object{}
	for _, o := range rev.Objects() {
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, o.Hash, o.Name)
		byName[string(o.Kind)+"/"+o.Name] = o
	}
	assert.Len(t, byName, 14)
	agent := byName["Agent/release-investigator"]
	assert.Equal(t, "alice@example.com", agent.Owner)
	assert.Equal(t, investigator, agent.File)
	assert.Equal(t, rev.Agent("release-investigator").Version(), agent.Version)
	assert.Equal(t, rev.Agent("release-investigator").Hash(), agent.Hash)
	version := byName["SkillVersion/release-diagnosis@1.2.0"]
	assert.Equal(t, "skills/release-diagnosis/1.2.0", version.File)
	assert.Equal(t, rev.SkillVersion(config.SkillRef{Skill: "release-diagnosis", Version: "1.2.0"}).Digest(), version.Hash)
	assert.Equal(t, "yanks.yaml", byName["YankList/yanks"].File)
}
