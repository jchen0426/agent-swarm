package deploy_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
)

// image is the runtime image of Contract C1's example.
var image = "sha256:" + strings.Repeat("0", 64)

// minimal is a deployment of C1's required keys alone, as a document a test
// may change before it writes it.
func minimal() map[string]any {
	return map[string]any{
		"tenant":        "example",
		"holder":        "instance-a",
		"secrets":       "/etc/agentswarm/secrets",
		"database":      "secret://database/url",
		"configuration": map[string]any{"repository": "/srv/agentswarm/config", "ref": "refs/heads/main"},
		"listeners": map[string]any{
			"mcp": "127.0.0.1:7401", "llm": "127.0.0.1:7402", "admin": "127.0.0.1:7403", "addresses": "/run/agentswarm/addresses.json",
		},
		"admin":  map[string]any{"token": "secret://admin/token"},
		"vendor": map[string]any{"baseURL": "https://api.anthropic.com", "key": "secret://vendor/key", "apiVersion": "2023-06-01"},
		"prices": map[string]any{
			"reference": 3_000_000, "dailyCap": 20_000_000,
			"cards": map[string]any{"claude-sonnet-5-5": map[string]any{"base": map[string]any{
				"input": 1_000_000, "output": 5_000_000, "cacheWrite5m": 1_250_000, "cacheWrite1h": 2_000_000, "cacheRead": 100_000,
			}}},
		},
		"runner": map[string]any{
			"kind": "process", "unsafeNoSandbox": true, "output": "/var/lib/agentswarm/turns", "images": map[string]any{"thinloop": image},
		},
	}
}

// minimalDocker is a deployment of the Docker runner with C1's required keys
// alone, as a document a test may change before it writes it: minimal's,
// without the agents' listeners and unsafeNoSandbox, which the Docker runner
// refuses, and with a runner.docker block of its sockets and store.
func minimalDocker() map[string]any {
	doc := minimal()
	remove(doc, "listeners.mcp")
	remove(doc, "listeners.llm")
	remove(doc, "runner.unsafeNoSandbox")
	set(doc, "runner.kind", "docker")
	set(doc, "runner.docker", map[string]any{"sockets": "/run/agentswarm/a", "store": "/var/lib/agentswarm/images"})
	return doc
}

// write writes doc as a deployment file and returns its path.
func write(t *testing.T, doc map[string]any) string {
	t.Helper()
	raw, err := yaml.Marshal(doc)
	require.NoError(t, err)
	return writeText(t, string(raw))
}

func writeText(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deployment.yaml")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	return path
}

// set sets the key at a dotted path, making the mappings on the way.
func set(doc map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := doc[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			doc[p] = next
		}
		doc = next
	}
	doc[parts[len(parts)-1]] = value
}

// remove removes the key at a dotted path.
func remove(doc map[string]any, path string) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		doc = doc[p].(map[string]any)
	}
	delete(doc, parts[len(parts)-1])
}

// refused loads doc, which must be refused, and returns its problems.
func refused(t *testing.T, doc map[string]any) deploy.Problems {
	t.Helper()
	return problems(t, write(t, doc))
}

func problems(t *testing.T, path string) deploy.Problems {
	t.Helper()
	d, err := deploy.Load(path)
	require.Error(t, err)
	assert.Nil(t, d)
	var ps deploy.Problems
	require.ErrorAs(t, err, &ps)
	return ps
}

// keys are the keys the problems name, in their order.
func keys(ps deploy.Problems) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Key)
	}
	return out
}

// problem returns the problem of a key, failing the test when there is none.
func problem(t *testing.T, ps deploy.Problems, key string) deploy.Problem {
	t.Helper()
	for _, p := range ps {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("no problem names %s: %v", key, ps)
	return deploy.Problem{}
}

// defaults are the timing of C1's table.
var defaults = deploy.Timing{
	Lease:       deploy.LeaseTiming{TTL: 15 * time.Second, RenewEvery: 5 * time.Second, Margin: 5 * time.Second, TakeEvery: 5 * time.Second, TakeWait: 5 * time.Second},
	Database:    deploy.DatabaseTiming{TransactionTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second},
	Coordinator: deploy.CoordinatorTiming{Grace: 5 * time.Second, SettleWait: 30 * time.Second, SettlePoll: time.Second, Retry: 5 * time.Second},
	LLM:         deploy.LLMTiming{RecordTimeout: 5 * time.Second, LastCallWait: 15 * time.Second, VendorTimeout: 10 * time.Minute, CoolDown: 60 * time.Second},
	MCP: deploy.MCPTiming{
		RecordTimeout: 5 * time.Second, CallTimeout: 60 * time.Second, VerifyEvery: 30 * time.Second, LookTimeout: 10 * time.Second, CoolDown: 60 * time.Second,
	},
	Runtime:  deploy.RuntimeTiming{Slack: 2 * time.Second},
	Drain:    30 * time.Second,
	Registry: deploy.RegistryTiming{Refresh: time.Minute},
}

// C1's required keys alone give every default of C1's table: the timing,
// the turns' output cap and maxTokens, serve's own executable for a turn,
// and no CA bundle.
func TestADeploymentLoadsWithItsDefaults(t *testing.T) {
	d, err := deploy.Load(write(t, minimal()))

	require.NoError(t, err)
	assert.Equal(t, defaults, d.Timing)
	assert.Equal(t, deploy.Runner{
		Kind: "process", UnsafeNoSandbox: true, Binary: "", Output: "/var/lib/agentswarm/turns", OutputCap: 1 << 20, MaxTokens: 4096,
		Images: map[string]string{"thinloop": image},
	}, d.Runner)
	assert.Equal(t, deploy.MCP{}, d.MCP)
}

// A Docker runner's block that gives its sockets and store alone gives every
// default of C1: the engine's socket, serve's own group, no labels of the
// deployment's, and the sandbox's limits, its CPUs whole (Plan 7d-2, R9 and
// R10).
func TestADockerRunnerLoadsWithItsDefaults(t *testing.T) {
	d, err := deploy.Load(write(t, minimalDocker()))

	require.NoError(t, err)
	assert.Equal(t, deploy.Runner{
		Kind: deploy.KindDocker, Output: "/var/lib/agentswarm/turns", OutputCap: 1 << 20, MaxTokens: 4096,
		Images: map[string]string{"thinloop": image},
		Docker: &deploy.Docker{
			Endpoint: "unix:///var/run/docker.sock", Sockets: "/run/agentswarm/a", Store: "/var/lib/agentswarm/images", Group: -1,
			Memory: 268_435_456, CPUs: 1, Pids: 64, Files: 256, Tmp: 16_777_216,
		},
	}, d.Runner)
}

// example is Contract C1's example, as the plan gives it.
const example = `tenant: example                     # required
holder: instance-a                  # required: this instance's name, as the lease records it
secrets: /etc/agentswarm/secrets    # required: the secrets directory
database: secret://database/url     # required: the database's URL
configuration:
  repository: /srv/agentswarm/config  # required: a local Git repository
  ref: refs/heads/main                # required: a full reference; the registry's Git source refuses a short name
listeners:
  mcp: 127.0.0.1:7401               # required
  llm: 127.0.0.1:7402               # required
  admin: 127.0.0.1:7403             # required, loopback only
  addresses: /run/agentswarm/addresses.json  # required (C2)
admin:
  token: secret://admin/token       # required
vendor:
  baseURL: https://api.anthropic.com  # required
  key: secret://vendor/key          # required
  apiVersion: "2023-06-01"          # required
prices:
  reference: 3000000                # required: pico-USD per cost-weighted token
  dailyCap: 20000000                # required: cost-weighted tokens per UTC day
  cards:                            # required: one per model an agent uses (cost.Card)
    claude-sonnet-5-5:
      threshold: 0
      base: {input: 1000000, output: 5000000, cacheWrite5m: 1250000, cacheWrite1h: 2000000, cacheRead: 100000}
      # long: {...} when threshold > 0
runner:
  kind: process                     # required: "process" in this plan
  unsafeNoSandbox: true             # required with kind process
  binary: ""                        # optional (R5): default, serve's own executable
  output: /var/lib/agentswarm/turns # required: one file per turn
  outputCap: 1048576                # optional [1 MiB]
  maxTokens: 4096                   # optional [4,096] (R14): at most the LLM gateway's cap [16,384]
  images:                           # required: each runtime the agents name -> its image
    thinloop: sha256:0000000000000000000000000000000000000000000000000000000000000000
mcp:
  caBundle: /etc/agentswarm/mcp-ca.pem  # optional: PEM, trusted besides the system roots
timing:                             # optional; each key defaults as below
  lease: {ttl: 15s, renewEvery: 5s, margin: 5s, takeEvery: 5s, takeWait: 5s}
`

// C1's example loads key by key, into the platform's types: its durations as
// durations, and its card as the LLM gateway prices with it.
func TestTheContractsExampleLoads(t *testing.T) {
	d, err := deploy.Load(writeText(t, example))

	require.NoError(t, err)
	assert.Equal(t, &deploy.Deployment{
		Tenant: "example", Holder: "instance-a", Secrets: "/etc/agentswarm/secrets", Database: "secret://database/url",
		Configuration: deploy.Configuration{Repository: "/srv/agentswarm/config", Ref: "refs/heads/main"},
		Listeners:     deploy.Listeners{MCP: "127.0.0.1:7401", LLM: "127.0.0.1:7402", Admin: "127.0.0.1:7403", Addresses: "/run/agentswarm/addresses.json"},
		Admin:         deploy.Admin{Token: "secret://admin/token"},
		Vendor:        deploy.Vendor{BaseURL: "https://api.anthropic.com", Key: "secret://vendor/key", APIVersion: "2023-06-01"},
		Prices: deploy.Prices{Reference: 3_000_000, DailyCap: 20_000_000, Cards: map[string]cost.Card{"claude-sonnet-5-5": {
			Base: cost.Rates{Input: 1_000_000, Output: 5_000_000, CacheWrite5m: 1_250_000, CacheWrite1h: 2_000_000, CacheRead: 100_000},
		}}},
		Runner: deploy.Runner{
			Kind: "process", UnsafeNoSandbox: true, Output: "/var/lib/agentswarm/turns", OutputCap: 1_048_576, MaxTokens: 4096,
			Images: map[string]string{"thinloop": image},
		},
		MCP:    deploy.MCP{CABundle: "/etc/agentswarm/mcp-ca.pem"},
		Timing: defaults,
	}, d)
}

// dockerExample is the Docker variant of Contract C1's example: its listeners
// and its runner, as the plan gives them, the image's digest filled in. A
// deployment's other keys are as before.
const dockerExample = `listeners:
  admin: 127.0.0.1:7403             # required, loopback only
  addresses: /var/lib/agentswarm/a/addresses.json  # required (C2)
  # mcp and llm are refused: the sandboxes reach serve through runner.docker.sockets
runner:
  kind: docker
  output: /var/lib/agentswarm/a/turns   # as before
  outputCap: 1048576                    # as before [1 MiB]
  maxTokens: 4096                       # as before [4,096]
  images:
    thinloop: sha256:0000000000000000000000000000000000000000000000000000000000000000
  docker:
    endpoint: unix:///var/run/docker.sock  # optional, this default; unix:// only
    sockets: /run/agentswarm/a             # required: serve listens on mcp.sock and llm.sock here
    store: /var/lib/agentswarm/images      # required: serve keeps a copy of each runtime image here
    group: 990                             # optional: the gid the sockets and the sandboxes share; default serve's own
    labels: {}                             # optional: more labels on each sandbox
    memory: 268435456                      # optional [256 MiB]
    cpus: 1                                # optional [1], whole CPUs
    pids: 64                               # optional [64]
    files: 256                             # optional [256]
    tmp: 16777216                          # optional [16 MiB]
`

// The Docker variant of C1's example loads key by key: its listeners without
// the agents' two, and its runner with its runner.docker block, each key in
// its own field. Its other keys are minimal's.
func TestTheDockerVariantOfTheContractsExampleLoads(t *testing.T) {
	others := minimal()
	delete(others, "listeners")
	delete(others, "runner")
	raw, err := yaml.Marshal(others)
	require.NoError(t, err)

	d, err := deploy.Load(writeText(t, string(raw)+dockerExample))

	require.NoError(t, err)
	assert.Equal(t, deploy.Listeners{Admin: "127.0.0.1:7403", Addresses: "/var/lib/agentswarm/a/addresses.json"}, d.Listeners)
	assert.Equal(t, deploy.Runner{
		Kind: "docker", Output: "/var/lib/agentswarm/a/turns", OutputCap: 1_048_576, MaxTokens: 4096,
		Images: map[string]string{"thinloop": image},
		Docker: &deploy.Docker{
			Endpoint: "unix:///var/run/docker.sock", Sockets: "/run/agentswarm/a", Store: "/var/lib/agentswarm/images", Group: 990,
			Labels: map[string]string{}, Memory: 268_435_456, CPUs: 1, Pids: 64, Files: 256, Tmp: 16_777_216,
		},
	}, d.Runner)
}

// The repository's example deployment loads as it stands, so that it cannot
// drift from what serve reads: its timing keys, commented out, leave every
// default, and its card prices long prompts at their own rates.
func TestTheExampleDeploymentLoads(t *testing.T) {
	d, err := deploy.Load(filepath.Join("..", "..", "examples", "deployment.yaml"))

	require.NoError(t, err)
	assert.Equal(t, "process", d.Runner.Kind)
	assert.Equal(t, defaults, d.Timing)
	card, ok := d.Prices.Cards["claude-haiku-5-5"]
	require.True(t, ok, "the card of the model the examples' agents use")
	assert.Positive(t, card.Threshold)
	assert.Greater(t, card.Long.Input, card.Base.Input)
}

// Every timing key is read, each into its own field: none is left at its
// default, nor taken for another.
func TestEveryTimingKeyIsRead(t *testing.T) {
	doc := minimal()
	doc["timing"] = map[string]any{
		"lease":       map[string]any{"ttl": "20s", "renewEvery": "4s", "margin": "6s", "takeEvery": "3s", "takeWait": "2s"},
		"database":    map[string]any{"transactionTimeout": "7s", "idleTimeout": "8s"},
		"coordinator": map[string]any{"grace": "9s", "settleWait": "31s", "settlePoll": "2s", "retry": "6s"},
		"llm":         map[string]any{"recordTimeout": "3s", "lastCallWait": "16s", "vendorTimeout": "11m", "coolDown": "61s"},
		"mcp":         map[string]any{"recordTimeout": "4s", "callTimeout": "62s", "verifyEvery": "32s", "lookTimeout": "12s", "coolDown": "63s"},
		"runtime":     map[string]any{"slack": "1500ms"},
		"drain":       "45s",
		"registry":    map[string]any{"refresh": "2m"},
	}

	d, err := deploy.Load(write(t, doc))

	require.NoError(t, err)
	assert.Equal(t, deploy.Timing{
		Lease:       deploy.LeaseTiming{TTL: 20 * time.Second, RenewEvery: 4 * time.Second, Margin: 6 * time.Second, TakeEvery: 3 * time.Second, TakeWait: 2 * time.Second},
		Database:    deploy.DatabaseTiming{TransactionTimeout: 7 * time.Second, IdleTimeout: 8 * time.Second},
		Coordinator: deploy.CoordinatorTiming{Grace: 9 * time.Second, SettleWait: 31 * time.Second, SettlePoll: 2 * time.Second, Retry: 6 * time.Second},
		LLM:         deploy.LLMTiming{RecordTimeout: 3 * time.Second, LastCallWait: 16 * time.Second, VendorTimeout: 11 * time.Minute, CoolDown: 61 * time.Second},
		MCP: deploy.MCPTiming{
			RecordTimeout: 4 * time.Second, CallTimeout: 62 * time.Second, VerifyEvery: 32 * time.Second, LookTimeout: 12 * time.Second, CoolDown: 63 * time.Second,
		},
		Runtime:  deploy.RuntimeTiming{Slack: 1500 * time.Millisecond},
		Drain:    45 * time.Second,
		Registry: deploy.RegistryTiming{Refresh: 2 * time.Minute},
	}, d.Timing)
}

// A card with a threshold has long rates, which price a call whose prompt is
// longer than the threshold.
func TestAPriceCardsLongRatesAreRead(t *testing.T) {
	doc := minimal()
	set(doc, "prices.cards.claude-sonnet-5-5.threshold", 200_000)
	set(doc, "prices.cards.claude-sonnet-5-5.long", map[string]any{
		"input": 2_000_000, "output": 7_500_000, "cacheWrite5m": 2_500_000, "cacheWrite1h": 4_000_000, "cacheRead": 200_000,
	})

	d, err := deploy.Load(write(t, doc))

	require.NoError(t, err)
	assert.Equal(t, cost.Card{
		Threshold: 200_000,
		Base:      cost.Rates{Input: 1_000_000, Output: 5_000_000, CacheWrite5m: 1_250_000, CacheWrite1h: 2_000_000, CacheRead: 100_000},
		Long:      cost.Rates{Input: 2_000_000, Output: 7_500_000, CacheWrite5m: 2_500_000, CacheWrite1h: 4_000_000, CacheRead: 200_000},
	}, d.Prices.Cards["claude-sonnet-5-5"])
}

// A misspelt key is refused, by its path, rather than ignored for a default
// (Review Focus 1): in a runner.docker block too.
func TestADeploymentWithAnUnknownKeyIsRefused(t *testing.T) {
	for _, key := range []string{"timing.lease.tll", "tenantt", "runner.imagez", "prices.cards.claude-sonnet-5-5.base.inptu", "runner.docker.sokets"} {
		doc := minimal()
		set(doc, key, "15s")

		ps := refused(t, doc)

		assert.Equal(t, []string{key}, keys(ps), key)
		assert.ErrorContains(t, ps, key)
	}
}

// A duration without its unit is refused, naming its key, rather than read
// as nanoseconds or seconds (Review Focus 1): 15 and "15" alike, and a bare
// 0 too, where a duration may be zero.
func TestADurationWithoutAUnitIsRefused(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
	}{{"timing.lease.ttl", 15}, {"timing.lease.ttl", "15"}, {"timing.runtime.slack", 0}} {
		doc := minimal()
		set(doc, tc.key, tc.value)

		ps := refused(t, doc)

		assert.Equal(t, []string{tc.key}, keys(ps), "%v", tc.value)
		assert.Contains(t, problem(t, ps, tc.key).Message, "15s", "the problem shows the form a duration takes")
	}
}

// Each required key that is left out is refused, by its path.
func TestMissingRequiredKeysAreNamed(t *testing.T) {
	for _, key := range []string{
		"tenant", "holder", "secrets", "database",
		"configuration.repository", "configuration.ref",
		"listeners.mcp", "listeners.llm", "listeners.admin", "listeners.addresses",
		"admin.token",
		"vendor.baseURL", "vendor.key", "vendor.apiVersion",
		"prices.reference", "prices.dailyCap", "prices.cards",
		"runner.kind", "runner.unsafeNoSandbox", "runner.output", "runner.images",
	} {
		doc := minimal()
		remove(doc, key)

		ps := refused(t, doc)

		assert.Equal(t, []string{key}, keys(ps), key)
	}
}

// A secret is given by its reference, secret://<path>, never in place: a
// value of another form is refused, naming its key, and never repeated, as
// it may be the secret itself.
func TestAReferenceWithoutItsSchemeIsRefused(t *testing.T) {
	for key, value := range map[string]string{
		"database":    "database-url",
		"admin.token": "postgres://agentswarm:hunter2@db.internal/agentswarm",
		"vendor.key":  "sk-ant-api03-hunter2",
	} {
		for _, v := range []string{value, "secret://../outside", "secret://"} {
			doc := minimal()
			set(doc, key, v)

			ps := refused(t, doc)

			assert.Equal(t, []string{key}, keys(ps), "%s: %s", key, v)
			assert.NotContains(t, ps.Error(), "hunter2", "the value is never repeated")
		}
	}
}

// A runtime's image is a digest, as a revision's lock takes it: a tag is
// refused, naming the runtime's key.
func TestAnImageThatIsNotADigestIsRefused(t *testing.T) {
	for _, value := range []string{"registry.example/runtime:1", "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("0", 63)} {
		doc := minimal()
		set(doc, "runner.images.thinloop", value)

		ps := refused(t, doc)

		assert.Equal(t, []string{"runner.images.thinloop"}, keys(ps), value)
	}
}

// runner.maxTokens is at most the LLM gateway's cap on a request's
// max_tokens (R14), and at least 1.
func TestAMaxTokensAboveTheGatewaysCapIsRefused(t *testing.T) {
	for _, value := range []int{deploy.MaxTokensCap + 1, 0} {
		doc := minimal()
		set(doc, "runner.maxTokens", value)

		ps := refused(t, doc)

		assert.Equal(t, []string{"runner.maxTokens"}, keys(ps), "%d", value)
	}
	doc := minimal()
	set(doc, "runner.maxTokens", deploy.MaxTokensCap)
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
	assert.Equal(t, int64(16_384), d.Runner.MaxTokens)
}

// The registry's Git source refuses a short name, so the deployment does
// first, naming the key and the full form.
func TestAShortConfigurationRefIsRefused(t *testing.T) {
	doc := minimal()
	set(doc, "configuration.ref", "main")

	ps := refused(t, doc)

	assert.Equal(t, []string{"configuration.ref"}, keys(ps))
	assert.Contains(t, problem(t, ps, "configuration.ref").Message, "refs/heads/main")

	set(doc, "configuration.ref", "refs/heads/main")
	_, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
}

// The admin API listens on loopback only (Decision 14).
func TestANonLoopbackAdminAddressIsRefused(t *testing.T) {
	for _, address := range []string{"0.0.0.0:7403", "192.0.2.1:7403", ":7403", "localhost:7403", "[2001:db8::1]:7403"} {
		doc := minimal()
		set(doc, "listeners.admin", address)

		ps := refused(t, doc)

		assert.Equal(t, []string{"listeners.admin"}, keys(ps), address)
	}
	for _, address := range []string{"127.0.0.1:0", "[::1]:0", "127.0.0.2:7403"} {
		doc := minimal()
		set(doc, "listeners.admin", address)

		_, err := deploy.Load(write(t, doc))

		require.NoError(t, err, address)
	}
}

// The agents' listeners serve plain HTTP, the sessions' tokens included, and
// the process runner's turns run on this host: while runner.kind is process,
// each of them is held to loopback, as the admin API's is. A name such as
// localhost is refused too. The refusal names the key. With the Docker
// runner, whose sandboxes reach serve through runner.docker.sockets, the key
// is refused whatever its address, as a key the Docker runner refuses, and
// runner.kind is not.
func TestTheAgentsListenersAreOnLoopbackWithTheProcessRunner(t *testing.T) {
	for _, key := range []string{"listeners.mcp", "listeners.llm"} {
		for _, address := range []string{"0.0.0.0:7401", "192.0.2.1:7401", "[2001:db8::1]:7401", ":7401", "localhost:7401"} {
			doc := minimal()
			set(doc, key, address)

			ps := refused(t, doc)

			assert.Equal(t, []string{key}, keys(ps), "%s: %s", key, address)
			assert.Contains(t, problem(t, ps, key).Message, "loopback", "%s: %s", key, address)
		}
		for _, address := range []string{"127.0.0.1:0", "[::1]:0", "127.0.0.2:7401"} {
			doc := minimal()
			set(doc, key, address)

			_, err := deploy.Load(write(t, doc))

			require.NoError(t, err, "%s: %s", key, address)
		}
		doc := minimal()
		set(doc, key, "0.0.0.0:7401")
		set(doc, "runner.kind", "docker")
		ps := refused(t, doc)
		assert.Contains(t, keys(ps), key, "%s: a key the Docker runner refuses", key)
		assert.NotContains(t, keys(ps), "runner.kind", "%s: the Docker runner", key)
		if slices.Contains(keys(ps), key) {
			assert.Contains(t, problem(t, ps, key).Message, "docker", "%s: refused as the Docker runner refuses it", key)
		}
	}
}

// The timings Decision 1 refuses, because they cannot hold, are refused,
// each naming its key and the key it is held against; the bounds themselves
// hold.
func TestTheTimingsThatCannotHoldAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timing  map[string]string
		key     string
		against string
	}{
		{
			name: "the grace below the LLM gateway's record timeout", timing: map[string]string{"coordinator.grace": "4s"},
			key: "timing.coordinator.grace", against: "timing.llm.recordTimeout",
		},
		{
			name: "the grace below the MCP gateway's record timeout", timing: map[string]string{"mcp.recordTimeout": "6s"},
			key: "timing.coordinator.grace", against: "timing.mcp.recordTimeout",
		},
		{
			name: "TTL less the margin not above the renewal's period", timing: map[string]string{"lease.ttl": "10s"},
			key: "timing.lease.renewEvery", against: "timing.lease.margin",
		},
		{
			name: "the take's wait not below TTL less the margin", timing: map[string]string{"lease.takeWait": "10s"},
			key: "timing.lease.takeWait", against: "timing.lease.margin",
		},
		{
			name: "the take's wait not below the database's transaction timeout", timing: map[string]string{"database.transactionTimeout": "5s"},
			key: "timing.lease.takeWait", against: "timing.database.transactionTimeout",
		},
		{
			name: "the transaction timeout above TTL less the margin", timing: map[string]string{"database.transactionTimeout": "11s"},
			key: "timing.database.transactionTimeout", against: "timing.lease.margin",
		},
		{
			name: "the idle timeout above TTL less the margin", timing: map[string]string{"database.idleTimeout": "11s"},
			key: "timing.database.idleTimeout", against: "timing.lease.margin",
		},
		{name: "a duration of zero", timing: map[string]string{"drain": "0s"}, key: "timing.drain"},
		{name: "a duration below zero", timing: map[string]string{"registry.refresh": "-1m"}, key: "timing.registry.refresh"},
		{name: "a slack below zero", timing: map[string]string{"runtime.slack": "-1s"}, key: "timing.runtime.slack"},
		{
			name: "a database timeout below a millisecond, which Postgres counts in", timing: map[string]string{"database.idleTimeout": "500us"},
			key: "timing.database.idleTimeout", against: "1ms",
		},
	} {
		doc := minimal()
		for key, value := range tc.timing {
			set(doc, "timing."+key, value)
		}

		ps := refused(t, doc)

		assert.True(t, slices.ContainsFunc(ps, func(p deploy.Problem) bool {
			return p.Key == tc.key && strings.Contains(p.Message, tc.against)
		}), "%s: %v", tc.name, ps)
	}

	// At their bounds the timings hold: the grace equal to the record
	// timeouts, the database's timeouts equal to TTL less the margin, the
	// take's wait just below both, and no slack.
	doc := minimal()
	doc["timing"] = map[string]any{
		"lease":       map[string]any{"ttl": "3s", "renewEvery": "1s", "margin": "1s", "takeEvery": "1s", "takeWait": "1999ms"},
		"database":    map[string]any{"transactionTimeout": "2s", "idleTimeout": "2s"},
		"coordinator": map[string]any{"grace": "5s"},
		"llm":         map[string]any{"recordTimeout": "5s"},
		"mcp":         map[string]any{"recordTimeout": "5s"},
		"runtime":     map[string]any{"slack": "0s"},
	}
	_, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
}

// A take ends within the take's wait and the renewal period, which together
// must stay below the lease's time: serve's drain waits for the leader's stop
// no longer than the lease's time, and a take that outlasted it would hold the
// relead past Decision 4's bound. A sum at the lease's time, or above it, is
// refused, naming both keys; one just below it holds, as the defaults do. The
// rows keep every other check of the lease's timing.
func TestATakesBoundMustEndWithinTheLeasesTime(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		renewEvery, takeWait string
		refused              bool
	}{
		{name: "the sum at the lease's time", renewEvery: "7s", takeWait: "8s", refused: true},
		{name: "the sum above the lease's time", renewEvery: "7s", takeWait: "9s", refused: true},
		{name: "the sum just below the lease's time", renewEvery: "7s", takeWait: "7999ms"},
	} {
		doc := minimal()
		doc["timing"] = map[string]any{"lease": map[string]any{
			"ttl": "15s", "margin": "1s", "renewEvery": tc.renewEvery, "takeWait": tc.takeWait,
		}}

		d, err := deploy.Load(write(t, doc))

		if !tc.refused {
			require.NoError(t, err, tc.name)
			assert.Equal(t, 15*time.Second, d.Timing.Lease.TTL, tc.name)
			continue
		}
		var ps deploy.Problems
		require.ErrorAs(t, err, &ps, tc.name)
		assert.Equal(t, []string{"timing.lease.takeWait"}, keys(ps), tc.name)
		for _, other := range []string{"timing.lease.renewEvery", "timing.lease.ttl"} {
			assert.Contains(t, ps[0].Message, other, tc.name)
		}
	}
	_, err := deploy.Load(write(t, minimal()))
	require.NoError(t, err, "the defaults: 5s and 5s, below 15s")
}

// The process runner runs turns as host processes, unsandboxed: serve runs
// it only when the deployment says so (Decision 12). With the Docker runner,
// unsafeNoSandbox is a key of the process runner's, which the Docker runner
// refuses, and runner.kind is not refused.
func TestAProcessRunnerNeedsUnsafeNoSandbox(t *testing.T) {
	doc := minimal()
	set(doc, "runner.unsafeNoSandbox", false)

	ps := refused(t, doc)

	assert.Equal(t, []string{"runner.unsafeNoSandbox"}, keys(ps))

	doc = minimal()
	set(doc, "runner.kind", "docker")
	ps = refused(t, doc)
	assert.Contains(t, keys(ps), "runner.unsafeNoSandbox", "a key the Docker runner refuses")
	assert.NotContains(t, keys(ps), "runner.kind", "the Docker runner")
	if slices.Contains(keys(ps), "runner.unsafeNoSandbox") {
		assert.Contains(t, problem(t, ps, "runner.unsafeNoSandbox").Message, "docker", "refused as the Docker runner refuses it")
	}
}

// A runner of a kind this build does not have is refused, naming runner.kind
// alone. A guard: Check refused every kind but process before it took
// docker, and must go on refusing the kinds it does not know.
func TestAnUnknownRunnerKindIsRefused(t *testing.T) {
	doc := minimal()
	set(doc, "runner.kind", "kubernetes")

	ps := refused(t, doc)

	assert.Equal(t, []string{"runner.kind"}, keys(ps))
}

// A runner.docker block is the Docker runner's: with the process runner it is
// refused, naming runner.docker, rather than ignored. A guard: the decoder
// refused the block as no key of a deployment before runner.docker was one,
// and Check must go on refusing it with the process runner once it is.
func TestADockerBlockIsRefusedWithTheProcessRunner(t *testing.T) {
	doc := minimal()
	set(doc, "runner.docker", map[string]any{"sockets": "/run/agentswarm/a", "store": "/var/lib/agentswarm/images"})

	assert.Equal(t, []string{"runner.docker"}, keys(refused(t, doc)))
}

// The process runner's keys are refused with the Docker runner, each naming
// its key: the agents' listeners, as the sandboxes reach serve through
// runner.docker.sockets, and the process runner's binary and
// unsafeNoSandbox, as each turn runs its runtime's image in a sandbox.
func TestKeysOfTheProcessRunnerAreRefusedWithTheDockerRunner(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
	}{
		{key: "listeners.mcp", value: "127.0.0.1:7401"},
		{key: "listeners.llm", value: "127.0.0.1:7402"},
		{key: "runner.binary", value: "/usr/local/bin/agentswarm"},
		{key: "runner.unsafeNoSandbox", value: true},
	} {
		doc := minimalDocker()
		set(doc, tc.key, tc.value)

		ps := refused(t, doc)

		assert.Equal(t, []string{tc.key}, keys(ps), tc.key)
		if len(ps) == 1 {
			assert.Contains(t, ps[0].Message, "docker", tc.key)
		}
	}
}

// The Docker runner needs its sockets directory and its store: without them
// the deployment is refused, naming each, whether its runner.docker block
// leaves them out or the file leaves out the whole block, which reads as one
// given empty. Its store is an absolute path.
func TestTheDockerRunnerNeedsItsSocketsAndStore(t *testing.T) {
	doc := minimalDocker()
	remove(doc, "runner.docker.sockets")
	remove(doc, "runner.docker.store")
	set(doc, "runner.docker.group", 990)
	assert.Equal(t, []string{"runner.docker.sockets", "runner.docker.store"}, keys(refused(t, doc)), "a block without them")

	doc = minimalDocker()
	remove(doc, "runner.docker")
	assert.Equal(t, []string{"runner.docker.sockets", "runner.docker.store"}, keys(refused(t, doc)), "no block")

	doc = minimalDocker()
	set(doc, "runner.docker.store", "var/lib/agentswarm/images")
	ps := refused(t, doc)
	assert.Equal(t, []string{"runner.docker.store"}, keys(ps), "a store that is not an absolute path")
	if len(ps) == 1 {
		assert.Contains(t, ps[0].Message, "absolute")
	}
}

// Serve reaches the engine on its Unix socket alone: an endpoint of another
// scheme, or a unix:// endpoint whose path is not absolute, is refused,
// naming runner.docker.endpoint. One at another absolute path is taken.
func TestAnEndpointThatIsNotAUnixSocketIsRefused(t *testing.T) {
	for _, endpoint := range []string{"tcp://127.0.0.1:2375", "ssh://host", "unix://relative.sock"} {
		doc := minimalDocker()
		set(doc, "runner.docker.endpoint", endpoint)

		assert.Equal(t, []string{"runner.docker.endpoint"}, keys(refused(t, doc)), endpoint)
	}
	doc := minimalDocker()
	set(doc, "runner.docker.endpoint", "unix:///run/user/1000/docker.sock")
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
	assert.Equal(t, "unix:///run/user/1000/docker.sock", d.Runner.Docker.Endpoint)
}

// The runner's own labels, which it puts on every sandbox and its sweep
// reads, and agentswarm.data, which make test-clean reads, are refused in
// runner.docker.labels, each naming its key; so is an empty key, naming the
// labels. Other keys, agentswarm.project among them, are the deployment's.
func TestTheRunnersOwnLabelsAreRefused(t *testing.T) {
	for _, key := range []string{
		"agentswarm.tenant", "agentswarm.holder", "agentswarm.turn", "agentswarm.run",
		"agentswarm.agent", "agentswarm.round", "agentswarm.deadline", "agentswarm.data",
	} {
		doc := minimalDocker()
		set(doc, "runner.docker.labels", map[string]any{key: "x", "agentswarm.project": "agent-swarm"})

		assert.Equal(t, []string{"runner.docker.labels." + key}, keys(refused(t, doc)), key)
	}
	doc := minimalDocker()
	set(doc, "runner.docker.labels", map[string]any{"": "x", "agentswarm.project": "agent-swarm"})
	assert.Equal(t, []string{"runner.docker.labels"}, keys(refused(t, doc)), "an empty key")

	doc = minimalDocker()
	set(doc, "runner.docker.labels", map[string]any{"agentswarm.project": "agent-swarm"})
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"agentswarm.project": "agent-swarm"}, d.Runner.Docker.Labels)
}

// runner.docker.group is the gid the sockets and the sandboxes share, by
// default -1, serve's own: 0, root's, is refused, and so is a value below
// -1, naming the key. A gid above 0 is taken.
func TestAGroupOf0IsRefused(t *testing.T) {
	for _, group := range []int{0, -2} {
		doc := minimalDocker()
		set(doc, "runner.docker.group", group)

		assert.Equal(t, []string{"runner.docker.group"}, keys(refused(t, doc)), "%d", group)
	}
	doc := minimalDocker()
	set(doc, "runner.docker.group", 990)
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
	assert.Equal(t, 990, d.Runner.Docker.Group)
}

// A sockets directory that no socket could be made in, or that shares a path
// with the deployment's other files, is refused, naming runner.docker.sockets
// and what it meets (Plan 7d-2, Review Focus 5): a relative path; a path
// whose mcp.sock passes a Unix socket's 107 bytes; and a path that lies
// inside, or holds, the secrets, the output, the store, the addresses file or
// the configuration repository, by their cleaned paths. A path whose mcp.sock
// fits is taken.
func TestTheSocketsDirectoryMustStandApartAndFitASocketsPath(t *testing.T) {
	// sized is a sockets path of n bytes, under /run/agentswarm.
	sized := func(n int) string {
		const dir = "/run/agentswarm/"
		return dir + strings.Repeat("s", n-len(dir))
	}
	for _, tc := range []struct {
		name string
		set  map[string]any
		// meets is what the problem's message names: the rule, or the key
		// of the path the sockets directory meets.
		meets string
	}{
		{name: "a relative path", set: map[string]any{"runner.docker.sockets": "run/agentswarm/a"}, meets: "absolute"},
		{name: "a path of 99 bytes, its mcp.sock at 108", set: map[string]any{"runner.docker.sockets": sized(99)}, meets: "107"},
		{
			// The secrets' path does not hold its key's name, which the
			// message must name.
			name: "a path inside the secrets", meets: "secrets",
			set: map[string]any{"secrets": "/etc/agentswarm/keys", "runner.docker.sockets": "/etc/agentswarm/keys/sockets"},
		},
		{
			name: "a path that holds the output, written uncleaned", meets: "runner.output",
			set: map[string]any{"runner.docker.sockets": "/var/lib/agentswarm/a", "runner.output": "/var/lib/agentswarm//a/turns"},
		},
		{name: "the store's path, written uncleaned", set: map[string]any{"runner.docker.sockets": "/var/lib/agentswarm//images"}, meets: "runner.docker.store"},
		{name: "a path that holds the addresses file", set: map[string]any{"runner.docker.sockets": "/run/agentswarm"}, meets: "listeners.addresses"},
		{name: "a path inside the repository", set: map[string]any{"runner.docker.sockets": "/srv/agentswarm/config/sockets"}, meets: "configuration.repository"},
	} {
		doc := minimalDocker()
		for key, value := range tc.set {
			set(doc, key, value)
		}

		ps := refused(t, doc)

		assert.Equal(t, []string{"runner.docker.sockets"}, keys(ps), tc.name)
		if len(ps) == 1 {
			assert.Contains(t, ps[0].Message, tc.meets, tc.name)
		}
	}
	doc := minimalDocker()
	set(doc, "runner.docker.sockets", sized(98))
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err, "a path of 98 bytes, its mcp.sock at 107")
	assert.Len(t, d.Runner.Docker.Sockets, 98)
}

// The sandbox's limits are each above zero: a limit of 0 is refused, naming
// its key. CPUs are whole (Plan 7d-2, R10), so half a CPU is refused, as the
// decoder reads whole numbers only.
func TestTheSandboxsLimitsMustBeAboveZero(t *testing.T) {
	for _, key := range []string{"memory", "cpus", "pids", "files", "tmp"} {
		doc := minimalDocker()
		set(doc, "runner.docker."+key, 0)

		ps := refused(t, doc)

		assert.Equal(t, []string{"runner.docker." + key}, keys(ps), key)
		if len(ps) == 1 {
			assert.Contains(t, ps[0].Message, "above zero", key)
		}
	}
	doc := minimalDocker()
	set(doc, "runner.docker.cpus", 0.5)
	ps := refused(t, doc)
	assert.Equal(t, []string{"runner.docker.cpus"}, keys(ps), "half a CPU")
	if len(ps) == 1 {
		assert.Contains(t, ps[0].Message, "whole number", "half a CPU")
	}
}

// A value of the wrong form is refused, naming its key: a key given no
// value, or twice; a number for a word or a word for a number; a flag that
// is not true or false; a size that is not a whole number above zero; a
// card the LLM gateway could not price with; a vendor that is not an HTTP
// URL; a listener without its port.
func TestValuesOfTheWrongFormAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name, key, raw string
		// named is the key the problem names, when it is not key, and says
		// words of its message.
		named, says string
	}{
		{name: "no value", key: "timing.lease.ttl", raw: "", says: "has no value"},
		{name: "a null value", key: "timing", raw: "~", says: "has no value"},
		{name: "a key given twice", key: "runner.maxTokens", raw: "100\nmaxTokens: 200", says: "given twice"},
		{name: "a mapping for a value", key: "holder", raw: "{name: a}", says: "single value"},
		{name: "a value for a mapping", key: "timing", raw: "15s", says: "mapping"},
		{name: "a flag that is not true or false", key: "runner.unsafeNoSandbox", raw: "yes", says: "true or false"},
		{name: "a size with a unit", key: "runner.outputCap", raw: "1MiB", says: "whole number"},
		{name: "a size that is not whole", key: "runner.outputCap", raw: "1e6", says: "whole number"},
		{name: "a size of zero", key: "runner.outputCap", raw: "0", says: "above zero"},
		{name: "a number as a word", key: "prices.reference", raw: `"3000000"`, says: "whole number"},
		{name: "a daily cap below zero", key: "prices.dailyCap", raw: "-1", says: "above zero"},
		{
			name: "a card the gateway cannot price with", key: "prices.cards.claude-sonnet-5-5.base.cacheRead", raw: "0",
			named: "prices.cards.claude-sonnet-5-5", says: "cannot price",
		},
		{name: "a vendor that is not an HTTP URL", key: "vendor.baseURL", raw: "api.anthropic.com", says: "http or https URL"},
		{name: "a listener without its port", key: "listeners.mcp", raw: "127.0.0.1", says: "host:port"},
	} {
		ps := problems(t, writeText(t, rawAt(t, minimal(), tc.key, tc.raw)))

		named := tc.key
		if tc.named != "" {
			named = tc.named
		}
		assert.Equal(t, []string{named}, keys(ps), tc.name)
		if len(ps) == 1 {
			assert.Contains(t, ps[0].Message, tc.says, tc.name)
		}
	}
}

// rawAt writes doc with the value at key replaced by raw, as written: each
// line raw holds after its first is indented as the key is.
func rawAt(t *testing.T, doc map[string]any, key, raw string) string {
	t.Helper()
	const sentinel = "SENTINEL-VALUE"
	set(doc, key, sentinel)
	out, err := yaml.Marshal(doc)
	require.NoError(t, err)
	text := string(out)
	at := strings.Index(text, sentinel)
	require.GreaterOrEqual(t, at, 0)
	line := text[strings.LastIndex(text[:at], "\n")+1 : at]
	indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
	return strings.Replace(text, sentinel, strings.ReplaceAll(raw, "\n", "\n"+indent), 1)
}

// A file holds one deployment: a second document is refused rather than
// ignored.
func TestAFileOfMoreThanOneDocumentIsRefused(t *testing.T) {
	raw, err := yaml.Marshal(minimal())
	require.NoError(t, err)

	_, err = deploy.Load(writeText(t, string(raw)+"---\ntenant: other\n"))

	require.Error(t, err)
	assert.ErrorContains(t, err, "one document")
}

// A file that cannot be read, or is not YAML, says so, with the file's
// path.
func TestAFileThatCannotBeReadIsRefused(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := deploy.Load(missing)
	require.ErrorContains(t, err, missing)

	broken := writeText(t, "tenant: [example\n")
	_, err = deploy.Load(broken)
	require.ErrorContains(t, err, broken)
}
