//go:build e2e

package harness

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
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

	// e2eDir is the directory that make test-linux and make test-docker make
	// on the engine's host, and mount at the same path in the test container
	// (Contract C15), under which each instance's sockets directory lies, in
	// its tenant's: serve and the sandboxes' bind mounts must share the path.
	e2eDir = "/srv/agentswarm-e2e"
	// stubbornTurn is the turn that Stubborn runs under the process runner,
	// the script that test/e2e/testdata/stubborn/stubborn holds and the
	// stubborn image runs: it reads nothing, ignores SIGTERM, and sleeps for
	// 120 s at most (R5), so that only a kill ends it before then.
	stubbornTurn = "#!/bin/sh\ntrap '' TERM\nexec sleep 120\n"
)

// DeploymentSpec holds what a scenario changes in an instance's deployment
// file (Contract C1). The harness fills in the rest.
type DeploymentSpec struct {
	// Timing sets the timing keys. A key left zero is left out of the file,
	// and serve's default applies.
	Timing Timing
	// Lifecycle sets the lifecycle block's keys. A key left zero is left out
	// of the file, and serve's default applies.
	Lifecycle Lifecycle
	// DailyCap is prices.dailyCap, in cost-weighted tokens a UTC day; when
	// zero, 20,000,000.
	DailyCap int64
	// Binary is runner.binary, the executable that runs a turn; when empty,
	// the key is left out and the runner uses serve's own executable. It is
	// the process runner's alone: a Docker deployment that sets it fails the
	// test.
	Binary string
	// Stubborn makes the turns ignore their input and SIGTERM, and sleep for
	// 120 s at most (R5): the process runner runs a script of the harness's
	// as runner.binary, and the Docker runner the image StubbornEnv names in
	// place of the runtime image. With Binary it fails the test.
	Stubborn bool
	// MaxTokens is runner.maxTokens; when zero, the key is left out.
	MaxTokens int64
	// WithoutCA leaves mcp.caBundle out, so that the instance trusts the
	// system roots alone and refuses the scripted servers' certificates.
	WithoutCA bool
	// PoolMaxConns, when above zero, holds each of serve's pools to that
	// many connections, as pgxpool's pool_max_conns in the instance's
	// database URL says, so that a scenario runs on the pools of a small
	// host; when zero, pgxpool's default applies, the greater of 4 and the
	// host's CPUs.
	PoolMaxConns int
	// Web gives the deployment the web block (Plan 9a-1, m18), and a web
	// listener on loopback: web.publicURL is the harness's TLS stub's
	// (Env.Web), web.oidc.issuer its fake IdP's, trusted through
	// web.oidc.caBundle, the test CA's; the client's secret and the session
	// key, in base64, are the harness's, which every instance shares.
	Web bool
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

// Lifecycle holds the keys of a deployment's lifecycle block: how many items
// investigate at once, lifecycle.investigating; how long after its close a
// person may reopen an item, lifecycle.reopenWindow, and a closed item takes
// late rulings, lifecycle.closedRulings; and the extension the interface
// offers, lifecycle.extension, nil to leave it out.
type Lifecycle struct {
	Investigating               int
	ReopenWindow, ClosedRulings time.Duration
	Extension                   *Amount
}

// Amount is an extension's amount, as lifecycle.extension holds it
// (Contract C1): its rounds and its wall clock. Both are written, a zero
// among them, so that serve's default of a key the block leaves out never
// applies.
type Amount struct {
	Rounds    int
	WallClock time.Duration
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

// web writes the web block (Plan 9a-1, R86) of the harness's web side: its
// stub's URL, its IdP and client, the test CA as the IdP's bundle (R94), its
// groups, and the session key's reference; serve's defaults apply to the
// rest.
func (w *yamlWriter) web(web *Web, caFile string) {
	w.parent(0, "web")
	w.str(1, "publicURL", web.url)
	w.parent(1, "oidc")
	w.str(2, "issuer", web.idp.URL)
	w.str(2, "clientID", WebClientID)
	w.str(2, "clientSecretRef", "secret://web/client")
	w.str(2, "caBundle", caFile)
	w.parent(1, "groups")
	w.str(2, "viewers", ViewersGroup)
	w.str(2, "responders", RespondersGroup)
	w.str(2, "admins", AdminsGroup)
	w.str(1, "sessionKeyRef", "secret://web/session")
}

// lifecycle writes the lifecycle block: the keys that are set, and nothing
// when none is.
func (w *yamlWriter) lifecycle(l *Lifecycle) {
	if *l == (Lifecycle{}) {
		return
	}
	w.parent(0, "lifecycle")
	if l.Investigating != 0 {
		w.raw(1, "investigating", strconv.Itoa(l.Investigating))
	}
	for _, window := range []struct {
		key   string
		value time.Duration
	}{{"reopenWindow", l.ReopenWindow}, {"closedRulings", l.ClosedRulings}} {
		if window.value != 0 {
			w.str(1, window.key, window.value.String())
		}
	}
	if e := l.Extension; e != nil {
		w.parent(1, "extension")
		w.raw(2, "rounds", strconv.Itoa(e.Rounds))
		w.str(2, "wallClock", e.WallClock.String())
	}
}

// instance returns the files of an instance, making its directories on first
// use. Under Docker it names the instance's sockets directory, one of its
// own, /srv/agentswarm-e2e/<tenant>/<instance>-<8 hex digits>, which Serve
// makes (Contract C9).
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
	if e.runner == dockerRunner {
		in.sockets = filepath.Join(e2eDir, e.tenant, name+"-"+randomHex(4))
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
// The file names the runner of Runner (Contract C9). Under Docker it is C1's
// Docker form: the instance's sockets directory, which Serve makes; the
// Env's store, <its directory>/images, which its instances share; the label
// agentswarm.project=agent-swarm on every sandbox; and the image ImageEnv
// names, or with Stubborn StubbornEnv's. A Docker deployment whose image's
// variable is unset, or that sets Binary, fails the test, and writes nothing.
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
	by := e.turnsOf(t, d)
	in := e.instance(t, instance)
	database := e.databaseURL(t, instance)
	if d.PoolMaxConns > 0 {
		database = withPoolMaxConns(t, database, d.PoolMaxConns)
	}
	secrets := map[string]string{
		"database/url": database,
		"admin/token":  in.adminToken,
		"vendor/key":   e.vendor.Key(instance),
	}
	for _, name := range e.order {
		if s := e.servers[name]; !s.spec.Plain {
			secrets["mcp/"+name] = s.Credential(instance)
		}
	}
	var web *Web
	if d.Web {
		web = e.Web()
		secrets["web/client"] = web.secret
		secrets["web/session"] = web.sessionKey
	}
	for name, value := range secrets {
		writeFile(t, filepath.Join(in.secrets, filepath.FromSlash(name)), value+"\n")
	}
	if e.runner == dockerRunner {
		if err := os.MkdirAll(e.store(), 0o750); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	writeFile(t, in.deployment, e.deploymentYAML(in, repo, d, &by, web))
	return in.deployment
}

// turnsBy says what runs a deployment's turns: the image runner.images
// names, and under the process runner the executable runner.binary names, ""
// for serve's own.
type turnsBy struct{ image, binary string }

// turnsOf returns what runs the turns of a deployment d. Under the process
// runner the image is a fixed digest, which no engine runs, and the
// executable Binary, or with Stubborn the harness's stubborn turn. Under
// Docker the image is ImageEnv's, or with Stubborn StubbornEnv's. It fails
// the test for a deployment its runner cannot run: Stubborn with Binary;
// under Docker, Binary, and an image whose variable is unset.
func (e *Env) turnsOf(t testing.TB, d *DeploymentSpec) turnsBy {
	t.Helper()
	if e.runner != dockerRunner {
		if d.Stubborn && d.Binary != "" {
			t.Fatal("harness: Stubborn names runner.binary, and so does Binary: set one")
		}
		sum := sha256.Sum256([]byte("harness runtime " + runtimeName))
		by := turnsBy{image: "sha256:" + hex.EncodeToString(sum[:]), binary: d.Binary}
		if d.Stubborn {
			by.binary = e.stubbornScript(t)
		}
		return by
	}
	if d.Binary != "" {
		t.Fatalf("harness: DeploymentSpec.Binary is the process runner's runner.binary, and this deployment's runner is %s, "+
			"whose sandboxes run their images", dockerRunner)
	}
	variable := ImageEnv
	if d.Stubborn {
		variable = StubbornEnv
	}
	image := os.Getenv(variable)
	if image == "" {
		t.Fatalf("harness: %s is unset: under the Docker runner it names the id of the image the turns run, "+
			"which make test-linux and make test-docker build and pass in", variable)
	}
	return turnsBy{image: image}
}

// stubbornScript returns the path of the harness's stubborn turn, which it
// writes in the Env's directory on first use.
func (e *Env) stubbornScript(t testing.TB) string {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stubborn == "" {
		path := filepath.Join(e.dir, "stubborn-turn")
		writeFile(t, path, stubbornTurn)
		if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // G302: a turn's executable, which runs; its owner alone has any access.
			t.Fatalf("harness: %v", err)
		}
		e.stubborn = path
	}
	return e.stubborn
}

// store is runner.docker.store, which the Env's instances share (R17): serve
// keeps a copy of each runtime image there, and its holder's lock.
func (e *Env) store() string { return filepath.Join(e.dir, "images") }

// withPoolMaxConns returns a database URL whose pools hold n connections at
// most: pgxpool's pool_max_conns.
func withPoolMaxConns(t testing.TB, raw string, n int) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("harness: the database URL does not parse")
	}
	query := u.Query()
	query.Set("pool_max_conns", strconv.Itoa(n))
	u.RawQuery = query.Encode()
	return u.String()
}

// deploymentYAML writes Contract C1, of the Env's runner. A Docker
// deployment binds no mcp or llm listener: the sandboxes reach serve through
// its sockets.
func (e *Env) deploymentYAML(in *instance, repo *Repo, d *DeploymentSpec, by *turnsBy, web *Web) string {
	var w yamlWriter
	w.str(0, "tenant", e.tenant)
	w.str(0, "holder", in.name)
	w.str(0, "secrets", in.secrets)
	w.str(0, "database", "secret://database/url")
	w.parent(0, "configuration")
	w.str(1, "repository", repo.dir)
	w.str(1, "ref", repo.Ref())
	w.parent(0, "listeners")
	listeners := []string{"mcp", "llm", "admin"}
	if e.runner == dockerRunner {
		listeners = []string{"admin"}
	}
	if web != nil {
		listeners = append(listeners, "web")
	}
	for _, name := range listeners {
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
	if e.runner == dockerRunner {
		w.str(1, "kind", dockerRunner)
	} else {
		w.str(1, "kind", processRunner)
		w.raw(1, "unsafeNoSandbox", "true")
		if by.binary != "" {
			w.str(1, "binary", by.binary)
		}
	}
	w.str(1, "output", in.output)
	if d.MaxTokens > 0 {
		w.raw(1, "maxTokens", strconv.FormatInt(d.MaxTokens, 10))
	}
	w.parent(1, "images")
	w.str(2, runtimeName, by.image)
	if e.runner == dockerRunner {
		w.parent(1, "docker")
		w.str(2, "sockets", in.sockets)
		w.str(2, "store", e.store())
		w.parent(2, "labels")
		w.str(3, projectLabel, projectName)
	}
	if !d.WithoutCA {
		w.parent(0, "mcp")
		w.str(1, "caBundle", e.caFile)
	}
	w.timing(&d.Timing)
	w.lifecycle(&d.Lifecycle)
	if web != nil {
		w.web(web, e.caFile)
	}
	return w.b.String()
}
