package deploy_test

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
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

// lifecycleDefaults are the lifecycle block of C1's table.
var lifecycleDefaults = deploy.Lifecycle{Investigating: 3, ReopenWindow: 24 * time.Hour, ClosedRulings: 72 * time.Hour}

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
		MCP:       deploy.MCP{CABundle: "/etc/agentswarm/mcp-ca.pem"},
		Timing:    defaults,
		Lifecycle: lifecycleDefaults,
	}, d)
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

// The markers between which the example deployment holds its Docker variant,
// commented out.
const (
	variantBegins = "# >>> the Docker runner"
	variantEnds   = "# <<< the Docker runner"
)

// The example deployment's Docker variant loads once it is taken as the file
// says to take it: the runner block replaced by the variant's lines,
// uncommented, and listeners.mcp and listeners.llm, which the Docker runner
// refuses, dropped. It loads again with the optional keys of its docker
// block, commented out within the variant, uncommented too, so that a
// misspelt one is refused. So the variant, which serve never reads in place,
// cannot drift from what serve reads either.
func TestTheExampleDeploymentsDockerVariantLoads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "deployment.yaml"))
	require.NoError(t, err)

	for _, optional := range []bool{false, true} {
		d, err := deploy.Load(writeText(t, dockerVariant(t, string(raw), optional)))

		require.NoError(t, err, "the optional keys uncommented: %v", optional)
		assert.Equal(t, deploy.KindDocker, d.Runner.Kind, "the optional keys uncommented: %v", optional)
	}
}

// optionalKey is a line of the variant's docker block, once uncommented,
// that gives an optional key commented out: its indentation, "# ", a word, a
// colon, a space and a value. Without its "# ", it gives the key.
var optionalKey = regexp.MustCompile(`^( {4,})# ([A-Za-z][A-Za-z0-9]*: \S.*)$`)

// dockerVariant returns the example deployment, text, as an operator who
// takes its Docker variant edits it: its runner block replaced by the lines
// between the variant's markers, each uncommented, and the lines of its
// listeners block that give mcp and llm dropped. With optional, the lines of
// the variant's docker block that give an optional key, commented out, are
// uncommented too.
func dockerVariant(t *testing.T, text string, optional bool) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	begins, ends := slices.Index(lines, variantBegins), slices.Index(lines, variantEnds)
	require.GreaterOrEqual(t, begins, 0, "the example deployment holds the Docker variant after the line %q", variantBegins)
	require.Greater(t, ends, begins, "the example deployment ends the Docker variant with the line %q", variantEnds)
	variant := make([]string, 0, ends-begins-1)
	inDocker, optionals := false, 0
	for _, line := range lines[begins+1 : ends] {
		uncommented, ok := strings.CutPrefix(line, "# ")
		require.True(t, ok, "each line of the variant is commented out by a leading %q: %q", "# ", line)
		switch {
		case uncommented == "  docker:":
			inDocker = true
		case !strings.HasPrefix(uncommented, "    "):
			inDocker = false
		case inDocker && optional && optionalKey.MatchString(uncommented):
			uncommented = optionalKey.ReplaceAllString(uncommented, "$1$2")
			optionals++
		}
		variant = append(variant, uncommented)
	}
	if optional {
		require.Positive(t, optionals, "the variant's docker block gives its optional keys, commented out")
	}
	runner, listeners := blockOf(t, lines, "runner"), blockOf(t, lines, "listeners")
	out := make([]string, 0, len(lines))
	dropped := 0
	for i := 0; i < len(lines); i++ {
		switch line := strings.TrimSpace(lines[i]); {
		case i == runner.start:
			out = append(out, variant...)
			i = runner.end - 1
		case i > listeners.start && i < listeners.end && (strings.HasPrefix(line, "mcp:") || strings.HasPrefix(line, "llm:")):
			dropped++
		default:
			out = append(out, lines[i])
		}
	}
	require.Equal(t, 2, dropped, "the lines of listeners.mcp and listeners.llm")
	return strings.Join(out, "\n")
}

// block is the lines of a top-level key's block: from its key's line, start,
// to end, the next line that is neither empty nor indented.
type block struct{ start, end int }

func blockOf(t *testing.T, lines []string, key string) block {
	t.Helper()
	start := slices.Index(lines, key+":")
	require.GreaterOrEqual(t, start, 0, "the example deployment gives %s a block", key)
	end := start + 1
	for end < len(lines) && (lines[end] == "" || strings.HasPrefix(lines[end], " ")) {
		end++
	}
	return block{start: start, end: end}
}

// The two instances' deployment files, which a first deployment of two
// instances on one host copies (Plan 7d-2, Decision 13), load as they stand,
// both of the Docker runner: holders a and b of one tenant, which share the
// database, the secrets, the image store, the images and the configuration
// repository, and each have their own sockets directory, output directory,
// addresses file and admin port. Each file's sockets directory is
// /run/agentswarm/ and the file's stem: agentswarm@.service runs the instance
// %i with --deployment /etc/agentswarm/%i.yaml, and makes
// RuntimeDirectory=agentswarm/%i, which serve refuses to start without.
func TestTheTwoInstancesDeploymentsLoad(t *testing.T) {
	load := func(name string) *deploy.Deployment {
		d, err := deploy.Load(filepath.Join("..", "..", "examples", "two-instances", name))
		require.NoError(t, err, name)
		require.Equal(t, deploy.KindDocker, d.Runner.Kind, name)
		require.NotNil(t, d.Runner.Docker, name)
		assert.Equal(t, "/run/agentswarm/"+strings.TrimSuffix(name, ".yaml"), d.Runner.Docker.Sockets,
			"%s: the sockets directory its unit's RuntimeDirectory makes", name)
		return d
	}
	a, b := load("a.yaml"), load("b.yaml")

	assert.Equal(t, []string{"a", "b"}, []string{a.Holder, b.Holder}, "the holders")
	assert.Equal(t, a.Tenant, b.Tenant, "the tenant, whose lease one instance holds while the other stands by")
	assert.Equal(t, a.Database, b.Database, "the database")
	assert.Equal(t, a.Secrets, b.Secrets, "the secrets")
	assert.Equal(t, a.Runner.Docker.Store, b.Runner.Docker.Store, "the image store")
	assert.Equal(t, a.Runner.Images, b.Runner.Images, "the images")
	assert.Equal(t, a.Configuration, b.Configuration, "the configuration repository")
	assert.NotEqual(t, a.Runner.Docker.Sockets, b.Runner.Docker.Sockets, "the sockets directories")
	assert.NotEqual(t, a.Runner.Output, b.Runner.Output, "the output directories")
	assert.NotEqual(t, a.Listeners.Addresses, b.Listeners.Addresses, "the addresses files")
	assert.NotEqual(t, port(t, a.Listeners.Admin), port(t, b.Listeners.Admin), "the admin ports")
}

// port is the port of a listener's address, host:port.
func port(t *testing.T, address string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(address)
	require.NoError(t, err, address)
	return p
}

// The markers between which each example deployment holds its lifecycle
// block, commented out.
const (
	lifecycleBegins = "# >>> the lifecycle"
	lifecycleEnds   = "# <<< the lifecycle"
)

// Each example deployment holds the lifecycle block (Decision 15), commented
// out between its markers, with the defaults of C1's table. Taken as the
// files say to take it, with its lines uncommented, each loads, its block
// giving each key its value; the three files hold the same block, since two
// instances of one tenant set it alike; and the tenant of each is of the form
// that can name an anchor. So the block, which serve never reads in place,
// cannot drift from what serve reads.
func TestTheExamplesLifecycleBlocksLoad(t *testing.T) {
	for _, name := range []string{"deployment.yaml", filepath.Join("two-instances", "a.yaml"), filepath.Join("two-instances", "b.yaml")} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "examples", name))
		require.NoError(t, err, name)

		text, block := lifecycleVariant(t, name, string(raw))
		var given struct {
			Lifecycle map[string]any `yaml:"lifecycle"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(strings.Join(block, "\n")), &given), name)
		assert.Equal(t, map[string]any{"investigating": 3, "reopenWindow": "24h", "closedRulings": "72h"}, given.Lifecycle,
			"%s: the block gives each key its value", name)

		d, err := deploy.Load(writeText(t, text))

		require.NoError(t, err, "%s: its lifecycle block, uncommented", name)
		assert.Equal(t, lifecycleDefaults, d.Lifecycle, name)
		assert.True(t, chain.ValidTenant(d.Tenant), "%s: a tenant of the anchors' form", name)
	}
}

// lifecycleVariant returns an example deployment, text, as an operator who
// takes its lifecycle block edits it: the lines between the markers
// uncommented, in place. It returns those lines, uncommented, too: the block.
func lifecycleVariant(t *testing.T, name, text string) (variant string, block []string) {
	t.Helper()
	lines := strings.Split(text, "\n")
	begins, ends := slices.Index(lines, lifecycleBegins), slices.Index(lines, lifecycleEnds)
	require.GreaterOrEqual(t, begins, 0, "%s holds the lifecycle block after the line %q", name, lifecycleBegins)
	require.Greater(t, ends, begins, "%s ends the lifecycle block with the line %q", name, lifecycleEnds)
	for _, line := range lines[begins+1 : ends] {
		uncommented, ok := strings.CutPrefix(line, "# ")
		require.True(t, ok, "%s: each line of the lifecycle block is commented out by a leading %q: %q", name, "# ", line)
		block = append(block, uncommented)
	}
	return strings.Join(slices.Concat(lines[:begins], block, lines[ends+1:]), "\n"), block
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

// The lifecycle block is optional (Contract C1): a file that leaves it out
// gives every default of its table, and a block that gives some of its keys
// keeps the defaults of the others. A block that gives them all is read key
// by key, each into its own field.
func TestTheLifecycleBlockTakesItsDefaults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block map[string]any
		want  deploy.Lifecycle
	}{
		{name: "no block", want: lifecycleDefaults},
		{
			name: "a limit alone", block: map[string]any{"investigating": 1},
			want: deploy.Lifecycle{Investigating: 1, ReopenWindow: 24 * time.Hour, ClosedRulings: 72 * time.Hour},
		},
		{
			name: "every key", block: map[string]any{"investigating": 5, "reopenWindow": "2h", "closedRulings": "30m"},
			want: deploy.Lifecycle{Investigating: 5, ReopenWindow: 2 * time.Hour, ClosedRulings: 30 * time.Minute},
		},
	} {
		doc := minimal()
		if tc.block != nil {
			doc["lifecycle"] = tc.block
		}

		d, err := deploy.Load(write(t, doc))

		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, d.Lifecycle, tc.name)
	}
}

// The limit of items investigating at once is at least 1: a limit of 0 or
// below would leave every item queued for ever. The refusal names the key.
func TestALifecycleLimitBelowOneIsRefused(t *testing.T) {
	for _, value := range []int{0, -1} {
		doc := minimal()
		set(doc, "lifecycle.investigating", value)

		ps := refused(t, doc)

		assert.Equal(t, []string{"lifecycle.investigating"}, keys(ps), "%d", value)
		assert.Contains(t, problem(t, ps, "lifecycle.investigating").Message, "above zero", "%d", value)
	}
}

// A lifecycle window is a duration with its unit, above zero. Without its
// unit, 24 or "72", it is refused as every duration is, showing the form a
// duration takes; of 0s or below it is refused as not above zero. Each
// refusal names the window's key.
func TestALifecycleWindowWithoutAUnitIsRefused(t *testing.T) {
	for _, key := range []string{"lifecycle.reopenWindow", "lifecycle.closedRulings"} {
		for _, tc := range []struct {
			value any
			says  string
		}{
			{value: 24, says: "15s"},
			{value: "72", says: "15s"},
			{value: "0s", says: "above zero"},
			{value: "-1h", says: "above zero"},
		} {
			doc := minimal()
			set(doc, key, tc.value)

			ps := refused(t, doc)

			assert.Equal(t, []string{key}, keys(ps), "%s: %v", key, tc.value)
			assert.Contains(t, problem(t, ps, key).Message, tc.says, "%s: %v", key, tc.value)
		}
	}
}

// The tenant names the audit chain's anchors, so Check refuses one that
// chain.ValidTenant refuses, naming tenant: serve never runs for a tenant whose
// chain can never be anchored. Of the form, 1 to 63 lowercase letters, digits
// and dashes, the first not a dash, the longest and a generated one load.
func TestADeploymentWhoseTenantCannotNameAnAnchorIsRefused(t *testing.T) {
	for _, tenant := range []string{"Bad/Tenant", "UPPER", "-lead", strings.Repeat("a", 64)} {
		doc := minimal()
		set(doc, "tenant", tenant)

		ps := refused(t, doc)

		assert.Equal(t, []string{"tenant"}, keys(ps), tenant)
		assert.Contains(t, problem(t, ps, "tenant").Message, "lowercase letters, digits and dashes", tenant)
	}
	for _, tenant := range []string{"example", "it-0a1b2c3d", strings.Repeat("a", 63)} {
		doc := minimal()
		set(doc, "tenant", tenant)

		d, err := deploy.Load(write(t, doc))

		require.NoError(t, err, tenant)
		assert.Equal(t, tenant, d.Tenant)
	}
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
// must stay below the lease's time: a take that outlasted it would hold the
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
