//go:build e2e

package e2e

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// validated is the part of the output of registry validate that these tests
// read, for a configuration that keeps every rule.
type validated struct {
	Commit  string `json:"commit"`
	Objects []struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"objects"`
}

func TestTheHarnessConfigurationValidates(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true, Servers: []harness.ServerSpec{{
		Name: "metrics",
		Tools: []harness.ToolSpec{
			{Name: "query", Description: "Run a query.", InputSchema: `{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`},
			{Name: "alerts", Description: "List alerts."},
		},
	}}})

	repo := env.Config(t, harness.ConfigSpec{Agents: []harness.AgentSpec{
		{Name: "alpha", Skill: "Look at the metrics.", Servers: []string{"metrics"}},
		{Name: "beta", Model: "claude-haiku-5-5", Skill: "Look at the alerts.", Servers: []string{"metrics"}},
	}})

	pinned := env.Server("metrics").All()
	require.NotEmpty(t, pinned, "the harness lists the tools to pin them")
	for _, r := range pinned {
		assert.Equal(t, harness.PinInstance, r.Instance, "and the listing is no instance's request")
	}
	assert.Empty(t, env.Server("metrics").Requests(""))
	out, errOut, code := agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", repo.Ref())
	require.Equal(t, 0, code, errOut+out)
	var v validated
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	assert.Regexp(t, `^[0-9a-f]{40}$`, v.Commit)
	agents := map[string]string{}
	for _, o := range v.Objects {
		if o.Kind == "Agent" {
			agents[o.Name] = o.Version
		}
	}
	require.Len(t, agents, 2)
	assert.Contains(t, agents, "alpha")
	assert.Contains(t, agents, "beta")

	// The scripted server's tools are pinned by the hashes hash-tools prints,
	// and each agent's skill carries the line that tells the agents apart.
	hashes, errOut, code := agentswarm(t, "registry", "hash-tools", "--endpoint", env.Server("metrics").PinURL(), "--ca", env.CA())
	require.Equal(t, 0, code, errOut)
	var listed []struct {
		Name       string `json:"name"`
		SchemaHash string `json:"schemaHash"`
	}
	require.NoError(t, json.Unmarshal([]byte(hashes), &listed))
	require.Len(t, listed, 2)
	registration, err := os.ReadFile(filepath.Join(repo.Dir(), "servers", "metrics.yaml"))
	require.NoError(t, err)
	for _, tool := range listed {
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, tool.SchemaHash, tool.Name)
		assert.Contains(t, string(registration), tool.SchemaHash, "the registration pins %s", tool.Name)
	}
	assert.Contains(t, string(registration), "credentialRef: secret://mcp/metrics")
	for _, agent := range []string{"alpha", "beta"} {
		skill, err := os.ReadFile(filepath.Join(repo.Dir(), "skills", agent+"-skill", "1.0.0", "SKILL.md"))
		require.NoError(t, err)
		assert.Contains(t, "\n"+string(skill), "\nYou are "+agent+".\n")
	}

	// A commit changes what the repository holds, and a bad one is refused.
	good := repo.Commit(t, func(dir string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("A note.\n"), 0o600))
	})
	require.Regexp(t, `^[0-9a-f]{40}$`, good)
	require.NotEqual(t, v.Commit, good)
	out, errOut, code = agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", repo.Ref())
	require.Equal(t, 0, code, errOut)
	v = validated{}
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	assert.Equal(t, good, v.Commit)

	bad := repo.Commit(t, func(dir string) {
		path := filepath.Join(dir, "agents", "alpha.yaml")
		text, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(text, []byte("  unknownKey: 1\n")...), 0o600))
	})
	require.NotEqual(t, good, bad)
	out, errOut, code = agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", repo.Ref())
	assert.Equal(t, 1, code, out)
	assert.Contains(t, errOut, "the configuration is refused")
	out, _, code = agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", good)
	assert.Equal(t, 0, code, "the good commit is still there: %s", out)
}

// A tool whose spec has a write policy is registered as a write tool, its
// scope bound to the cited hypothesis's key and with its summary, and the
// registry takes it; a tool without one stays a read tool. An agent is
// granted a server with a write tool tool by tool, since the registry grants
// a write tool by its exact name only, and any other server whole.
func TestTheHarnessRegistersAWriteTool(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true, Servers: []harness.ServerSpec{logs, {
		Name: "releases",
		Tools: []harness.ToolSpec{
			{Name: "list", Description: "List the releases."},
			{
				Name: "rollback", Description: "Roll a service back.", InputSchema: `{"type":"object","properties":{"service":{"type":"string"}}}`,
				Write: &harness.WriteSpec{Scope: map[string]string{"service": "service"}, Summary: "Roll the service back"},
			},
		},
	}}})

	repo := env.Config(t, harness.ConfigSpec{Agents: []harness.AgentSpec{{Name: "alpha", Servers: []string{"logs", "releases"}}}})

	out, errOut, code := agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", repo.Ref())
	require.Equal(t, 0, code, errOut+out)
	registration, err := os.ReadFile(filepath.Join(repo.Dir(), "servers", "releases.yaml"))
	require.NoError(t, err)
	assert.Regexp(t, `"list": \{schemaHash: "sha256:[0-9a-f]{64}", effect: read\}`, string(registration))
	assert.Regexp(t, `"rollback": \{schemaHash: "sha256:[0-9a-f]{64}", effect: write, scope: \{"service": "hypothesis.service"\}, `+
		`summary: "Roll the service back"\}`, string(registration))
	agent, err := os.ReadFile(filepath.Join(repo.Dir(), "agents", "alpha.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(agent), `tools: ["logs.*", "releases.list", "releases.rollback"]`)
}

// flatten reads a deployment file as the harness writes it, a block mapping of
// plain keys, and returns each scalar by its dotted path. Strings are
// unquoted.
func flatten(t *testing.T, path string) map[string]string {
	t.Helper()
	text, err := os.ReadFile(path)
	require.NoError(t, err)
	keys := map[string]string{}
	var stack []string
	for _, line := range strings.Split(string(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		depth := (len(line) - len(strings.TrimLeft(line, " "))) / 2
		key, value, found := strings.Cut(trimmed, ":")
		require.True(t, found, "a line of the deployment is not key: value: %q", line)
		value = strings.TrimSpace(value)
		stack = append(stack[:depth], key)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, `"`) {
			require.NoError(t, json.Unmarshal([]byte(value), &value), line)
		}
		keys[strings.Join(stack, ".")] = value
	}
	return keys
}

var (
	// requiredDeploymentKeys are the keys every deployment of the harness's
	// holds, and runnerKeys those its runner adds (Contract C1).
	requiredDeploymentKeys = []string{
		"tenant", "holder", "secrets", "database", "configuration.repository", "configuration.ref",
		"listeners.admin", "listeners.addresses", "admin.token",
		"vendor.baseURL", "vendor.key", "vendor.apiVersion", "prices.reference", "prices.dailyCap",
		"runner.kind", "runner.output", "runner.images.thinloop",
	}
	runnerKeys = map[string][]string{
		"process": {"listeners.mcp", "listeners.llm", "runner.unsafeNoSandbox"},
		"docker":  {"runner.docker.sockets", "runner.docker.store", "runner.docker.labels.agentswarm.project"},
	}
	optionalDeploymentKeys = regexp.MustCompile(`^(runner\.(outputCap|maxTokens)|mcp\.caBundle|` +
		`timing\.lease\.(ttl|renewEvery|margin|takeEvery|takeWait)|timing\.database\.(transactionTimeout|idleTimeout)|` +
		`timing\.coordinator\.(grace|settleWait|settlePoll|retry)|timing\.llm\.(recordTimeout|lastCallWait|vendorTimeout|coolDown)|` +
		`timing\.mcp\.(recordTimeout|callTimeout|verifyEvery|lookTimeout|coolDown)|timing\.runtime\.slack|timing\.drain|timing\.registry\.refresh|` +
		`prices\.cards\.[A-Za-z0-9._-]+\.(threshold|base\.(input|output|cacheWrite5m|cacheWrite1h|cacheRead)))$`)
	secretReference = regexp.MustCompile(`^secret://[A-Za-z0-9._/-]+$`)
)

func readSecret(t *testing.T, dir, reference string) string {
	t.Helper()
	require.Regexp(t, secretReference, reference)
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(reference, "secret://")))
	require.NoError(t, err, reference)
	return strings.TrimRight(string(data), "\r\n")
}

func TestTheHarnessDeploymentNamesEverySecretItUses(t *testing.T) {
	env := harness.New(t, harness.Options{Toxiproxy: true, Servers: []harness.ServerSpec{{
		Name:  "metrics",
		Tools: []harness.ToolSpec{{Name: "query", Description: "Run a query."}},
	}}})
	repo := env.Config(t, harness.ConfigSpec{Agents: []harness.AgentSpec{
		{Name: "alpha", Servers: []string{"metrics"}},
		{Name: "beta", Model: "claude-haiku-5-5", Servers: []string{"metrics"}},
	}})
	keys := map[string]map[string]string{}
	for _, instance := range []string{"a", "b"} {
		keys[instance] = flatten(t, env.Deployment(t, instance, harness.DeploymentSpec{}))
	}

	// The runner's keys follow the runner the harness writes deployments of.
	required := append(slices.Clone(requiredDeploymentKeys), runnerKeys[env.Runner()]...)
	for instance, deployment := range keys {
		for _, key := range required {
			assert.Contains(t, deployment, key, "instance %s", instance)
		}
		for key := range deployment {
			if !slices.Contains(required, key) {
				assert.Regexp(t, optionalDeploymentKeys, key, "a key C1 does not have, in instance %s", instance)
			}
		}
		assert.Equal(t, env.Tenant(), deployment["tenant"])
		assert.Equal(t, instance, deployment["holder"])
		assert.Equal(t, repo.Dir(), deployment["configuration.repository"])
		assert.Equal(t, repo.Ref(), deployment["configuration.ref"])
		assert.Equal(t, env.Runner(), deployment["runner.kind"])
		if env.Runner() == "docker" {
			assert.Equal(t, os.Getenv(harness.ImageEnv), deployment["runner.images.thinloop"])
		} else {
			assert.Equal(t, "true", deployment["runner.unsafeNoSandbox"])
			assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, deployment["runner.images.thinloop"])
			assert.Equal(t, "127.0.0.1:0", deployment["listeners.mcp"])
			assert.Equal(t, "127.0.0.1:0", deployment["listeners.llm"])
		}
		assert.Equal(t, env.CA(), deployment["mcp.caBundle"])
		assert.Equal(t, env.Vendor().URL(), deployment["vendor.baseURL"])
		assert.Equal(t, "127.0.0.1:0", deployment["listeners.admin"])
		assert.True(t, filepath.IsAbs(deployment["listeners.addresses"]))
		assert.True(t, filepath.IsAbs(deployment["runner.output"]))
		assert.NotEmpty(t, deployment["prices.cards.claude-sonnet-5-5.base.input"])
		assert.NotEmpty(t, deployment["prices.cards.claude-haiku-5-5.base.input"], "a card for each model the agents use")

		// Every reference names a secret in the instance's own directory.
		secrets := deployment["secrets"]
		require.True(t, filepath.IsAbs(secrets))
		for _, key := range []string{"database", "admin.token", "vendor.key"} {
			assert.NotEmpty(t, readSecret(t, secrets, deployment[key]), key)
		}
		assert.Equal(t, env.Vendor().Key(instance), readSecret(t, secrets, deployment["vendor.key"]), "the instance's own vendor key")
		assert.Equal(t, env.DatabaseURL(instance), readSecret(t, secrets, deployment["database"]), "the instance's own database")
		credential, err := os.ReadFile(filepath.Join(secrets, "mcp", "metrics"))
		require.NoError(t, err, "the credential the configuration's credentialRef names")
		assert.Equal(t, env.Server("metrics").Credential(instance), strings.TrimRight(string(credential), "\r\n"), "the instance's own server credential")
	}
	assert.NotEqual(t, keys["a"]["secrets"], keys["b"]["secrets"])
	assert.NotEqual(t, keys["a"]["listeners.addresses"], keys["b"]["listeners.addresses"])
	assert.NotEqual(t, keys["a"]["runner.output"], keys["b"]["runner.output"])
	assert.NotEqual(t, env.Vendor().Key("a"), env.Vendor().Key("b"))
	assert.NotEqual(t, env.Server("metrics").Credential("a"), env.Server("metrics").Credential("b"))
	assert.NotEqual(t, env.DatabaseURL("a"), env.DatabaseURL("b"))

	if env.Runner() == "docker" {
		assert.NotEqual(t, keys["a"]["runner.docker.sockets"], keys["b"]["runner.docker.sockets"])
		assert.Equal(t, keys["a"]["runner.docker.store"], keys["b"]["runner.docker.store"], "the Env's instances share one store")
	}

	// What a scenario changes. A binary is the process runner's alone.
	spec := harness.DeploymentSpec{
		Timing: harness.Timing{
			Lease: harness.LeaseTiming{TTL: 6 * time.Second, RenewEvery: 1500 * time.Millisecond},
			Drain: 10 * time.Second,
		},
		DailyCap:     1000,
		MaxTokens:    1024,
		WithoutCA:    true,
		PoolMaxConns: 4,
	}
	if env.Runner() != "docker" {
		spec.Binary = "/bin/sleep"
	}
	changed := flatten(t, env.Deployment(t, "c", spec))
	assert.Equal(t, "6s", changed["timing.lease.ttl"])
	assert.Equal(t, "1.5s", changed["timing.lease.renewEvery"])
	assert.Equal(t, "10s", changed["timing.drain"])
	assert.NotContains(t, changed, "timing.lease.margin", "a timing the scenario leaves out takes serve's default")
	assert.Equal(t, "1000", changed["prices.dailyCap"])
	if env.Runner() != "docker" {
		assert.Equal(t, "/bin/sleep", changed["runner.binary"])
	}
	assert.Equal(t, "1024", changed["runner.maxTokens"])
	assert.NotContains(t, changed, "mcp.caBundle")
	assert.NotContains(t, keys["a"], "runner.binary")
	assert.NotContains(t, keys["a"], "timing.lease.ttl")
	// The pools' size is pgxpool's pool_max_conns, in the instance's own
	// database URL.
	pooled, err := url.Parse(env.DatabaseURL("c"))
	require.NoError(t, err)
	query := pooled.Query()
	query.Set("pool_max_conns", "4")
	pooled.RawQuery = query.Encode()
	assert.Equal(t, pooled.String(), readSecret(t, changed["secrets"], changed["database"]), "serve's pools held to 4 connections")
	assert.NotContains(t, readSecret(t, keys["a"]["secrets"], keys["a"]["database"]), "pool_max_conns", "pgxpool's own pool size, by default")
}

// alone is a configuration of one agent, granted no server.
var alone = harness.ConfigSpec{Agents: []harness.AgentSpec{{Name: "alpha"}}}

// Asked for the Docker runner, the harness writes C1's Docker form (C9) on
// any system, as only Serve needs Linux: the sockets in a directory of the
// instance's own under /srv/agentswarm-e2e/<tenant>/, which Serve makes; one
// store, which the Env's instances share; the project's label on every
// sandbox; and the runtime image AGENTSWARM_E2E_IMAGE names. It writes none of
// the process runner's keys: no listeners.mcp or listeners.llm, as the
// sandboxes reach serve through the sockets, no runner.binary and no
// runner.unsafeNoSandbox. The build's own loader takes the file.
func TestTheHarnessWritesADockerDeploymentWhenAsked(t *testing.T) {
	const image = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv(harness.ImageEnv, image)
	env := harness.New(t, harness.Options{Runner: "docker"})
	env.Migrate(t)
	env.Config(t, alone)
	paths, keys := map[string]string{}, map[string]map[string]string{}
	for _, instance := range []string{"a", "b"} {
		paths[instance] = env.Deployment(t, instance, harness.DeploymentSpec{})
		keys[instance] = flatten(t, paths[instance])
	}

	required := append(slices.Clone(requiredDeploymentKeys), runnerKeys["docker"]...)
	for instance, deployment := range keys {
		for _, key := range required {
			assert.Contains(t, deployment, key, "instance %s", instance)
		}
		for key := range deployment {
			if !slices.Contains(required, key) {
				assert.Regexp(t, optionalDeploymentKeys, key, "a key C1 does not have, in instance %s", instance)
			}
		}
		for _, key := range append(slices.Clone(runnerKeys["process"]), "runner.binary") {
			assert.NotContains(t, deployment, key, "a key of the process runner's, in instance %s", instance)
		}
		assert.Equal(t, "docker", deployment["runner.kind"])
		assert.Equal(t, image, deployment["runner.images.thinloop"])
		assert.Regexp(t, `^/srv/agentswarm-e2e/`+regexp.QuoteMeta(env.Tenant())+`/`+instance+`-[0-9a-f]{8}$`, deployment["runner.docker.sockets"])
		assert.Equal(t, "agent-swarm", deployment["runner.docker.labels.agentswarm.project"])
		assert.True(t, filepath.IsAbs(deployment["runner.docker.store"]))
		assert.DirExists(t, deployment["runner.docker.store"], "the directory serve keeps its copies of the images in")
		_, errOut, code := agentswarm(t, "inspect", "--deployment", paths[instance], "item", "none")
		assert.Equal(t, 0, code, "the build's loader takes instance %s's deployment: %s", instance, errOut)
	}
	assert.Equal(t, "docker", env.Runner())
	assert.NotEqual(t, keys["a"]["runner.docker.sockets"], keys["b"]["runner.docker.sockets"])
	assert.Equal(t, keys["a"]["runner.docker.store"], keys["b"]["runner.docker.store"], "the Env's instances share one store")
}

// The harness's tenant is AGENTSWARM_E2E_TENANT's, which make test-docker and
// make test-linux set for each go test invocation, so that two gates on one
// engine never sweep each other's sandboxes, and harness when it is unset, as
// test/compat leaves it (C9). AuditVerify verifies that tenant's chain.
func TestTheHarnessTakesItsTenantFromTheEnvironment(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		// A guard: the tenant is harness today, so this half passes before
		// the harness reads the variable.
		t.Setenv(harness.TenantEnv, "")
		require.NoError(t, os.Unsetenv(harness.TenantEnv))
		env := harness.New(t, harness.Options{})
		env.Config(t, alone)

		assert.Equal(t, "harness", flatten(t, env.Deployment(t, "a", harness.DeploymentSpec{}))["tenant"])
	})

	t.Run("set", func(t *testing.T) {
		t.Setenv(harness.TenantEnv, "e2e-x")
		env := harness.New(t, harness.Options{})
		env.Migrate(t)
		env.Config(t, alone)
		// A record of e2e-x's chain whose hash does not recompute: verifying
		// e2e-x's chain finds it, and verifying harness's would not.
		env.Psql(t, `INSERT INTO audit_chain (tenant_id, seq, source, key, run_id, form, digest, prev, hash, chained_at)
VALUES ('e2e-x', 1, 'work_records', 'none', '', 1, sha256(''::bytea), decode(repeat('00', 32), 'hex'),
        sha256('not the hash'::bytea), clock_timestamp())`)

		assert.Equal(t, "e2e-x", env.Tenant())
		assert.Equal(t, "e2e-x", flatten(t, env.Deployment(t, "a", harness.DeploymentSpec{}))["tenant"])
		assert.Contains(t, env.AuditVerify(t), "broken_link", "AuditVerify verifies e2e-x's chain")
	})
}

// With Stubborn the turns ignore their input and SIGTERM, and sleep for 120 s
// at most (R5): a Docker deployment locks the stubborn image that
// AGENTSWARM_E2E_STUBBORN names, in place of the runtime image, and a process
// deployment names the harness's script as runner.binary, the script that
// image runs.
func TestTheHarnessLocksTheStubbornImageUnderDocker(t *testing.T) {
	const (
		runtimeImage  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		stubbornImage = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	)
	t.Setenv(harness.ImageEnv, runtimeImage)
	t.Setenv(harness.StubbornEnv, stubbornImage)
	docker := harness.New(t, harness.Options{Runner: "docker"})
	docker.Config(t, alone)
	process := harness.New(t, harness.Options{Runner: "process"})
	process.Config(t, alone)

	sandboxed := flatten(t, docker.Deployment(t, "a", harness.DeploymentSpec{Stubborn: true}))
	unsandboxed := flatten(t, process.Deployment(t, "a", harness.DeploymentSpec{Stubborn: true}))

	assert.Equal(t, stubbornImage, sandboxed["runner.images.thinloop"], "the stubborn image, in place of the runtime image")
	assert.NotContains(t, sandboxed, "runner.binary")
	assert.Equal(t, runtimeImage, flatten(t, docker.Deployment(t, "b", harness.DeploymentSpec{}))["runner.images.thinloop"],
		"without Stubborn, the runtime image")
	script := unsandboxed["runner.binary"]
	require.NotEmpty(t, script, "the harness's script runs the turns")
	info, err := os.Stat(script)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode().Perm()&0o100, "the script can be run")
	text, err := os.ReadFile(script)
	require.NoError(t, err)
	image, err := os.ReadFile(filepath.Join("testdata", "stubborn", "stubborn"))
	require.NoError(t, err)
	assert.Equal(t, string(image), string(text), "the script the stubborn image runs")
	assert.Contains(t, string(text), "trap '' TERM")
	assert.NotContains(t, flatten(t, process.Deployment(t, "b", harness.DeploymentSpec{})), "runner.binary",
		"without Stubborn, serve's own executable runs the turns")
}
