//go:build e2e

package harness

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	defaultDailyCap = 20_000_000
	// priceReference and the rates of priceCard are those of Contract C1's
	// example: $3 per million tokens of reference, and a card for the model.
	priceReference = 3_000_000
	apiVersion     = "2023-06-01"
)

// DeploymentSpec holds what a scenario changes in an instance's deployment
// file (Contract C1). The harness fills in the rest.
type DeploymentSpec struct {
	// Timing sets the timing keys. A key left zero is left out of the file,
	// and serve's default applies.
	Timing Timing
	// DailyCap is prices.dailyCap, in cost-weighted tokens a UTC day; when
	// zero, 20,000,000.
	DailyCap int64
	// Binary is runner.binary, the executable that runs a turn; when empty,
	// the key is left out and the runner uses serve's own executable.
	Binary string
	// MaxTokens is runner.maxTokens; when zero, the key is left out.
	MaxTokens int64
	// WithoutCA leaves mcp.caBundle out, so that the instance trusts the
	// system roots alone and refuses the scripted servers' certificates.
	WithoutCA bool
}

// Timing holds the keys of a deployment's timing section.
type Timing struct {
	Lease       LeaseTiming
	Database    DatabaseTiming
	Coordinator CoordinatorTiming
	LLM         LLMTiming
	MCP         MCPTiming
	// RuntimeSlack is timing.runtime.slack, Drain timing.drain, and
	// RegistryRefresh timing.registry.refresh.
	RuntimeSlack    time.Duration
	Drain           time.Duration
	RegistryRefresh time.Duration
}

// LeaseTiming holds timing.lease.
type LeaseTiming struct {
	TTL, RenewEvery, Margin, TakeEvery, TakeWait time.Duration
}

// DatabaseTiming holds timing.database.
type DatabaseTiming struct {
	TransactionTimeout, IdleTimeout time.Duration
}

// CoordinatorTiming holds timing.coordinator.
type CoordinatorTiming struct {
	Grace, SettleWait, SettlePoll, Retry time.Duration
}

// LLMTiming holds timing.llm.
type LLMTiming struct {
	RecordTimeout, LastCallWait, VendorTimeout, CoolDown time.Duration
}

// MCPTiming holds timing.mcp.
type MCPTiming struct {
	RecordTimeout, CallTimeout, VerifyEvery, LookTimeout, CoolDown time.Duration
}

// timingField is one key of the timing section, in a group of the section or
// at its top.
type timingField struct {
	group, key string
	value      time.Duration
}

func (t *Timing) fields() []timingField {
	return []timingField{
		{"lease", "ttl", t.Lease.TTL}, {"lease", "renewEvery", t.Lease.RenewEvery}, {"lease", "margin", t.Lease.Margin},
		{"lease", "takeEvery", t.Lease.TakeEvery}, {"lease", "takeWait", t.Lease.TakeWait},
		{"database", "transactionTimeout", t.Database.TransactionTimeout}, {"database", "idleTimeout", t.Database.IdleTimeout},
		{"coordinator", "grace", t.Coordinator.Grace}, {"coordinator", "settleWait", t.Coordinator.SettleWait},
		{"coordinator", "settlePoll", t.Coordinator.SettlePoll}, {"coordinator", "retry", t.Coordinator.Retry},
		{"llm", "recordTimeout", t.LLM.RecordTimeout}, {"llm", "lastCallWait", t.LLM.LastCallWait},
		{"llm", "vendorTimeout", t.LLM.VendorTimeout}, {"llm", "coolDown", t.LLM.CoolDown},
		{"mcp", "recordTimeout", t.MCP.RecordTimeout}, {"mcp", "callTimeout", t.MCP.CallTimeout},
		{"mcp", "verifyEvery", t.MCP.VerifyEvery}, {"mcp", "lookTimeout", t.MCP.LookTimeout}, {"mcp", "coolDown", t.MCP.CoolDown},
		{"runtime", "slack", t.RuntimeSlack},
		{"", "drain", t.Drain},
		{"registry", "refresh", t.RegistryRefresh},
	}
}

// yamlWriter writes the block mappings of a deployment: plain keys, and
// strings in the double quotes JSON and YAML share.
type yamlWriter struct{ b strings.Builder }

// parent writes a key that holds a mapping.
func (w *yamlWriter) parent(depth int, key string) {
	w.b.WriteString(strings.Repeat("  ", depth) + key + ":\n")
}

// str writes a string value, quoted.
func (w *yamlWriter) str(depth int, key, value string) { w.raw(depth, key, quote(value)) }

// raw writes a value as it is: a number or a boolean.
func (w *yamlWriter) raw(depth int, key, value string) {
	w.b.WriteString(strings.Repeat("  ", depth) + key + ": " + value + "\n")
}

// plainKey matches a key that needs no quotes.
var plainKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// timing writes the timing section: the keys that are set, and nothing when
// none is.
func (w *yamlWriter) timing(t *Timing) {
	var groups []string
	byGroup := map[string][]timingField{}
	for _, f := range t.fields() {
		if f.value == 0 {
			continue
		}
		if _, seen := byGroup[f.group]; !seen {
			groups = append(groups, f.group)
		}
		byGroup[f.group] = append(byGroup[f.group], f)
	}
	if len(groups) == 0 {
		return
	}
	w.parent(0, "timing")
	for _, group := range groups {
		depth := 1
		if group != "" {
			w.parent(1, group)
			depth = 2
		}
		for _, f := range byGroup[group] {
			w.str(depth, f.key, f.value.String())
		}
	}
}

// instance returns the files of an instance, making its directories on first
// use.
func (e *Env) instance(t testing.TB, name string) *instance {
	t.Helper()
	checkInstance(t, name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if in := e.instances[name]; in != nil {
		return in
	}
	dir := filepath.Join(e.dir, "instances", name)
	in := &instance{
		name: name, dir: dir, deployment: filepath.Join(dir, "deployment.yaml"), secrets: filepath.Join(dir, "secrets"),
		output: filepath.Join(dir, "turns"), addresses: filepath.Join(dir, "addresses.json"),
		adminToken: "admin-token-" + randomHex(16),
	}
	for _, d := range []string{in.secrets, in.output} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	e.instances[name] = in
	return in
}

// Deployment writes the deployment file of an instance (Contract C1) and its
// secrets directory, and returns the path of the file. The secrets directory
// holds the instance's own database URL, through its proxy when there is a
// toxiproxy, its own vendor key and its own credential for each scripted
// server (R3), so that the vendor and the servers tell the instances apart by
// them. The file names the repository and the models of the last Config, so
// Config comes first. Calling Deployment again for an instance writes its file
// anew and keeps its secrets.
//
//nolint:gocritic // hugeParam: the contract passes a scenario's spec by value.
func (e *Env) Deployment(t *testing.T, instance string, d DeploymentSpec) string {
	t.Helper()
	return e.deployment(t, instance, &d)
}

func (e *Env) deployment(t testing.TB, instance string, d *DeploymentSpec) string {
	t.Helper()
	if e.db == nil {
		t.Fatal("harness: there is no database for a deployment to name: Options.NoDatabase is set")
	}
	e.mu.Lock()
	repo := e.repo
	e.mu.Unlock()
	if repo == nil {
		t.Fatal("harness: call Config before Deployment: a deployment names the configuration repository")
	}
	in := e.instance(t, instance)
	secrets := map[string]string{
		"database/url": e.databaseURL(t, instance),
		"admin/token":  in.adminToken,
		"vendor/key":   e.vendor.Key(instance),
	}
	for _, name := range e.order {
		if s := e.servers[name]; !s.spec.Plain {
			secrets["mcp/"+name] = s.Credential(instance)
		}
	}
	for name, value := range secrets {
		writeFile(t, filepath.Join(in.secrets, filepath.FromSlash(name)), value+"\n")
	}
	writeFile(t, in.deployment, e.deploymentYAML(in, repo, d))
	return in.deployment
}

// deploymentYAML writes Contract C1.
func (e *Env) deploymentYAML(in *instance, repo *Repo, d *DeploymentSpec) string {
	var w yamlWriter
	w.str(0, "tenant", "harness")
	w.str(0, "holder", in.name)
	w.str(0, "secrets", in.secrets)
	w.str(0, "database", "secret://database/url")
	w.parent(0, "configuration")
	w.str(1, "repository", repo.dir)
	w.str(1, "ref", repo.Ref())
	w.parent(0, "listeners")
	for _, name := range []string{"mcp", "llm", "admin"} {
		w.str(1, name, "127.0.0.1:0")
	}
	w.str(1, "addresses", in.addresses)
	w.parent(0, "admin")
	w.str(1, "token", "secret://admin/token")
	w.parent(0, "vendor")
	w.str(1, "baseURL", e.vendor.URL())
	w.str(1, "key", "secret://vendor/key")
	w.str(1, "apiVersion", apiVersion)
	w.parent(0, "prices")
	w.raw(1, "reference", strconv.Itoa(priceReference))
	w.raw(1, "dailyCap", strconv.FormatInt(cmp.Or(d.DailyCap, defaultDailyCap), 10))
	w.parent(1, "cards")
	for _, model := range slices.Sorted(slices.Values(repo.models)) {
		key := model
		if !plainKey.MatchString(key) {
			key = quote(key)
		}
		w.parent(2, key)
		w.raw(3, "threshold", "0")
		w.parent(3, "base")
		for _, rate := range []struct {
			key   string
			value int
		}{{"input", 1_000_000}, {"output", 5_000_000}, {"cacheWrite5m", 1_250_000}, {"cacheWrite1h", 2_000_000}, {"cacheRead", 100_000}} {
			w.raw(4, rate.key, strconv.Itoa(rate.value))
		}
	}
	w.parent(0, "runner")
	w.str(1, "kind", "process")
	w.raw(1, "unsafeNoSandbox", "true")
	if d.Binary != "" {
		w.str(1, "binary", d.Binary)
	}
	w.str(1, "output", in.output)
	if d.MaxTokens > 0 {
		w.raw(1, "maxTokens", strconv.FormatInt(d.MaxTokens, 10))
	}
	w.parent(1, "images")
	sum := sha256.Sum256([]byte("harness runtime " + runtimeName))
	w.str(2, runtimeName, "sha256:"+hex.EncodeToString(sum[:]))
	if !d.WithoutCA {
		w.parent(0, "mcp")
		w.str(1, "caBundle", e.caFile)
	}
	w.timing(&d.Timing)
	return w.b.String()
}
