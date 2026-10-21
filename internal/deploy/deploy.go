// Package deploy reads a deployment file: what one instance of agentswarm
// serve runs with (Plan 7d-1, Contract C1). The file is strict YAML. A key
// the format does not have is refused, and so are a duration without its
// unit, a secret given in place of its reference, and an image given by a
// tag, each named by its key's path, such as timing.lease.ttl: a person who
// misspelt a key, or wrote 15 for 15s, sees serve refuse to start rather
// than run on a default (Review Focus 1). Load fills in the defaults of the
// keys a file leaves out, then refuses, through Check, what Decision 1
// refuses that needs no database.
package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jchen0426/agent-swarm/internal/llm/cost"
)

// MaxTokensCap is the LLM gateway's cap on a request's max_tokens [16,384]
// (spec Section 15). runner.maxTokens is at most it (R14), and serve gives
// the gateway its cap from it.
const MaxTokensCap = 16_384

// The defaults of runner's keys that a file may leave out.
const (
	defaultOutputCap = 1 << 20 // runner.outputCap, in bytes [1 MiB]
	defaultMaxTokens = 4096    // runner.maxTokens (R14)
)

// processRunner is the only kind of runner this build has: turns as host
// processes, for development and tests (Decision 12).
const processRunner = "process"

// defaultTiming is the timing of a file that sets none (Contract C1's
// table).
var defaultTiming = Timing{
	Lease:       LeaseTiming{TTL: 15 * time.Second, RenewEvery: 5 * time.Second, Margin: 5 * time.Second, TakeEvery: 5 * time.Second, TakeWait: 5 * time.Second},
	Database:    DatabaseTiming{TransactionTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second},
	Coordinator: CoordinatorTiming{Grace: 5 * time.Second, SettleWait: 30 * time.Second, SettlePoll: time.Second, Retry: 5 * time.Second},
	LLM:         LLMTiming{RecordTimeout: 5 * time.Second, LastCallWait: 15 * time.Second, VendorTimeout: 10 * time.Minute, CoolDown: 60 * time.Second},
	MCP: MCPTiming{
		RecordTimeout: 5 * time.Second, CallTimeout: 60 * time.Second, VerifyEvery: 30 * time.Second, LookTimeout: 10 * time.Second, CoolDown: 60 * time.Second,
	},
	Runtime:  RuntimeTiming{Slack: 2 * time.Second},
	Drain:    30 * time.Second,
	Registry: RegistryTiming{Refresh: time.Minute},
}

// Deployment is a deployment file (Contract C1), its defaults filled in.
type Deployment struct {
	// Tenant is the tenant the instance serves.
	Tenant string
	// Holder is the instance's name, as the lease records it.
	Holder string
	// Secrets is the secrets directory, under which a secret reference,
	// secret://<path>, names a file (internal/secrets/files).
	Secrets string
	// Database is the reference of the database's URL.
	Database      string
	Configuration Configuration
	Listeners     Listeners
	Admin         Admin
	Vendor        Vendor
	Prices        Prices
	Runner        Runner
	MCP           MCP
	Timing        Timing
}

// Configuration is the configuration repository the registry reads.
type Configuration struct {
	// Repository is a local Git repository; keeping it current is the
	// deployment's job.
	Repository string `yaml:"repository"`
	// Ref is a full reference, such as refs/heads/main: the registry's Git
	// source refuses a short name.
	Ref string `yaml:"ref"`
}

// Listeners are serve's addresses, as host:port; a port may be 0.
type Listeners struct {
	// MCP and LLM are the agents' listeners, which serve plain HTTP: on
	// loopback only while the runner is the process runner, whose turns run
	// on this host, until Plan 7d-2 brings TLS.
	MCP string `yaml:"mcp"`
	LLM string `yaml:"llm"`
	// Admin is on loopback only.
	Admin string `yaml:"admin"`
	// Addresses is the file serve writes its bound addresses to (Contract
	// C2).
	Addresses string `yaml:"addresses"`
}

// Admin is the admin API (Contract C4).
type Admin struct {
	// Token is the reference of the admin token.
	Token string `yaml:"token"`
}

// Vendor is the model vendor the LLM gateway forwards to.
type Vendor struct {
	BaseURL string `yaml:"baseURL"`
	// Key is the reference of the vendor's API key.
	Key string `yaml:"key"`
	// APIVersion is the anthropic-version every request carries.
	APIVersion string `yaml:"apiVersion"`
}

// Prices are what the LLM gateway prices model calls with.
type Prices struct {
	// Reference is the price of one cost-weighted token, in pico-USD.
	Reference int64
	// DailyCap is the cost-weighted tokens the tenant may spend in a UTC
	// day.
	DailyCap int64
	// Cards are the models' prices, by model: one for each model an agent
	// uses.
	Cards map[string]cost.Card
}

// Runner is how turns run (Decisions 12 and 13).
type Runner struct {
	// Kind is "process" in this plan, which needs UnsafeNoSandbox.
	Kind            string `yaml:"kind"`
	UnsafeNoSandbox bool   `yaml:"unsafeNoSandbox"`
	// Binary is the executable run as `<Binary> turn`; empty, serve's own
	// (R5).
	Binary string `yaml:"binary"`
	// Output is the directory of the turns' output files.
	Output string `yaml:"output"`
	// OutputCap caps a turn's output file, in bytes [1 MiB].
	OutputCap int64 `yaml:"outputCap"`
	// MaxTokens is each turn's max_tokens [4,096], at most MaxTokensCap
	// (R14).
	MaxTokens int64 `yaml:"maxTokens"`
	// Images maps each runtime the agents name to its image's digest.
	Images map[string]string `yaml:"images"`
}

// MCP is what the MCP gateway trusts.
type MCP struct {
	// CABundle is a PEM file of CAs trusted besides the system roots, or
	// empty.
	CABundle string `yaml:"caBundle"`
}

// Timing holds every duration a deployment may set (Contract C1's table).
type Timing struct {
	Lease       LeaseTiming       `yaml:"lease"`
	Database    DatabaseTiming    `yaml:"database"`
	Coordinator CoordinatorTiming `yaml:"coordinator"`
	LLM         LLMTiming         `yaml:"llm"`
	MCP         MCPTiming         `yaml:"mcp"`
	Runtime     RuntimeTiming     `yaml:"runtime"`
	// Drain bounds the drain (Decision 6).
	Drain    time.Duration  `yaml:"drain"`
	Registry RegistryTiming `yaml:"registry"`
}

// LeaseTiming is the lease's (spec Section 8.2), and its take's wait.
type LeaseTiming struct {
	TTL        time.Duration `yaml:"ttl"`
	RenewEvery time.Duration `yaml:"renewEvery"`
	Margin     time.Duration `yaml:"margin"`
	TakeEvery  time.Duration `yaml:"takeEvery"`
	// TakeWait is the take's lock_timeout.
	TakeWait time.Duration `yaml:"takeWait"`
}

// DatabaseTiming bounds the transactions of both pools: it converts to
// db.PoolOptions.
type DatabaseTiming struct {
	TransactionTimeout time.Duration `yaml:"transactionTimeout"`
	IdleTimeout        time.Duration `yaml:"idleTimeout"`
}

// CoordinatorTiming is the coordinator's (app.Options).
type CoordinatorTiming struct {
	Grace      time.Duration `yaml:"grace"`
	SettleWait time.Duration `yaml:"settleWait"`
	SettlePoll time.Duration `yaml:"settlePoll"`
	Retry      time.Duration `yaml:"retry"`
}

// LLMTiming is the LLM gateway's, and its vendor's cool-down.
type LLMTiming struct {
	RecordTimeout time.Duration `yaml:"recordTimeout"`
	LastCallWait  time.Duration `yaml:"lastCallWait"`
	VendorTimeout time.Duration `yaml:"vendorTimeout"`
	CoolDown      time.Duration `yaml:"coolDown"`
}

// MCPTiming is the MCP gateway's, and its servers' cool-down.
type MCPTiming struct {
	RecordTimeout time.Duration `yaml:"recordTimeout"`
	CallTimeout   time.Duration `yaml:"callTimeout"`
	VerifyEvery   time.Duration `yaml:"verifyEvery"`
	LookTimeout   time.Duration `yaml:"lookTimeout"`
	CoolDown      time.Duration `yaml:"coolDown"`
}

// RuntimeTiming is the runtime's.
type RuntimeTiming struct {
	// Slack is added to a turn's deadline in its spec; it may be zero.
	Slack time.Duration `yaml:"slack"`
}

// RegistryTiming is the registry's.
type RegistryTiming struct {
	Refresh time.Duration `yaml:"refresh"`
}

// Problem is one reason a deployment is refused: the key, by its path in the
// file, such as timing.lease.ttl, and what is wrong with it, said after the
// key.
type Problem struct {
	Key     string
	Message string
}

// Problems is the error of a refused deployment: every problem found, in the
// order found.
type Problems []Problem

func (ps Problems) Error() string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.Key+" "+p.Message)
	}
	return "deploy: the deployment is refused: " + strings.Join(parts, "; ")
}

// file is a deployment file as it is written. Its parts but the prices are
// the deployment's own; a card is read in the file's names, then converted
// to the LLM gateway's cost.Card. A key the file leaves out keeps the value
// the file held before it was decoded: its default, or nothing.
type file struct {
	Tenant        string        `yaml:"tenant"`
	Holder        string        `yaml:"holder"`
	Secrets       string        `yaml:"secrets"`
	Database      string        `yaml:"database"`
	Configuration Configuration `yaml:"configuration"`
	Listeners     Listeners     `yaml:"listeners"`
	Admin         Admin         `yaml:"admin"`
	Vendor        Vendor        `yaml:"vendor"`
	Prices        prices        `yaml:"prices"`
	Runner        Runner        `yaml:"runner"`
	MCP           MCP           `yaml:"mcp"`
	Timing        Timing        `yaml:"timing"`
}

type prices struct {
	Reference int64           `yaml:"reference"`
	DailyCap  int64           `yaml:"dailyCap"`
	Cards     map[string]card `yaml:"cards"`
}

// card is a cost.Card in the file's names.
type card struct {
	Threshold int64 `yaml:"threshold"`
	Base      rates `yaml:"base"`
	// Long is given when Threshold is above zero.
	Long rates `yaml:"long"`
}

// rates are cost.Rates in the file's names, field for field.
type rates struct {
	Input        int64 `yaml:"input"`
	Output       int64 `yaml:"output"`
	CacheWrite5m int64 `yaml:"cacheWrite5m"`
	CacheWrite1h int64 `yaml:"cacheWrite1h"`
	CacheRead    int64 `yaml:"cacheRead"`
}

// Load reads a deployment file: strictly, durations with their units, and
// defaults filled in; then Check. A file it refuses fails with Problems:
// every key whose form it refuses, or, when every form is right, every key
// Check refuses. Check waits for the forms, since a key whose value could
// not be read would read as one left out.
func Load(path string) (*Deployment, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("deploy: %w", err)
	}
	root, err := document(raw)
	if err != nil {
		return nil, fmt.Errorf("deploy: %s: %w", path, err)
	}
	f := file{Runner: Runner{OutputCap: defaultOutputCap, MaxTokens: defaultMaxTokens}, Timing: defaultTiming}
	var dec decoder
	dec.value(root, reflect.ValueOf(&f).Elem(), "")
	if len(dec.problems) > 0 {
		return nil, dec.problems
	}
	d := f.deployment()
	if err := d.Check(); err != nil {
		return nil, err
	}
	return d, nil
}

// document returns the root of a file's one document: a mapping, empty for
// a file that holds no document.
func document(raw []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	switch err := dec.Decode(&doc); {
	case errors.Is(err, io.EOF):
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	case err != nil:
		return nil, err
	}
	var next yaml.Node
	switch err := dec.Decode(&next); {
	case err == nil:
		return nil, errors.New("a deployment file holds one document, and this one holds more")
	case !errors.Is(err, io.EOF):
		return nil, err
	}
	if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0], nil
	}
	return nil, errors.New("a deployment file holds a mapping of keys")
}

// deployment returns the deployment the file holds.
func (f *file) deployment() *Deployment {
	cards := make(map[string]cost.Card, len(f.Prices.Cards))
	for model, c := range f.Prices.Cards {
		cards[model] = cost.Card{Threshold: c.Threshold, Base: cost.Rates(c.Base), Long: cost.Rates(c.Long)}
	}
	return &Deployment{
		Tenant: f.Tenant, Holder: f.Holder, Secrets: f.Secrets, Database: f.Database,
		Configuration: f.Configuration, Listeners: f.Listeners, Admin: f.Admin, Vendor: f.Vendor,
		Prices: Prices{Reference: f.Prices.Reference, DailyCap: f.Prices.DailyCap, Cards: cards},
		Runner: f.Runner, MCP: f.MCP, Timing: f.Timing,
	}
}

// durationType is the type of the durations a file holds, each with its
// unit.
var durationType = reflect.TypeFor[time.Duration]()

// decoder decodes the nodes of a deployment file into its types, by their
// YAML names, strictly: it refuses a key the types do not have, a key given
// twice or with no value, a duration without its unit, and a value of
// another kind than its key's. It keeps every problem it finds, each naming
// its key by its path.
type decoder struct{ problems Problems }

func (d *decoder) add(key, format string, args ...any) {
	d.problems = append(d.problems, Problem{Key: key, Message: fmt.Sprintf(format, args...)})
}

// value decodes n, the value of key, into v.
func (d *decoder) value(n *yaml.Node, v reflect.Value, key string) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		d.add(key, "has no value")
		return
	}
	switch {
	case v.Type() == durationType:
		d.duration(n, v, key)
	case v.Kind() == reflect.Struct:
		d.mapping(n, key, func(k string, value *yaml.Node) {
			i, ok := fieldOf(v.Type(), k)
			if !ok {
				d.add(join(key, k), "is not a key of a deployment")
				return
			}
			d.value(value, v.Field(i), join(key, k))
		})
	case v.Kind() == reflect.Map:
		m := reflect.MakeMap(v.Type())
		d.mapping(n, key, func(k string, value *yaml.Node) {
			e := reflect.New(v.Type().Elem()).Elem()
			d.value(value, e, join(key, k))
			m.SetMapIndex(reflect.ValueOf(k), e)
		})
		v.Set(m)
	default:
		d.scalar(n, v, key)
	}
}

// mapping calls each with every key of n, a mapping, and its value.
func (d *decoder) mapping(n *yaml.Node, key string, each func(k string, value *yaml.Node)) {
	if n.Kind != yaml.MappingNode {
		d.add(key, "must be a mapping of keys")
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		switch {
		case k.Kind != yaml.ScalarNode:
			d.add(key, "holds a key that is not a word")
		case seen[k.Value]:
			d.add(join(key, k.Value), "is given twice")
		default:
			seen[k.Value] = true
			each(k.Value, n.Content[i+1])
		}
	}
}

// duration decodes a Go duration with its unit, such as 15s or 1m. A bare
// number is refused, quoted or not: 15 could mean seconds or nanoseconds.
func (d *decoder) duration(n *yaml.Node, v reflect.Value, key string) {
	dur, err := time.ParseDuration(n.Value)
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" || err != nil {
		d.add(key, "must be a duration with its unit, such as 15s or 1m")
		return
	}
	v.SetInt(int64(dur))
}

// scalar decodes a word, a flag or a whole number. A flag is true or false,
// and a number has the form of a whole number: neither is read from a word.
func (d *decoder) scalar(n *yaml.Node, v reflect.Value, key string) {
	if n.Kind != yaml.ScalarNode {
		d.add(key, "must be a single value, not a mapping or a list")
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(n.Value)
	case reflect.Bool:
		var b bool
		if n.ShortTag() != "!!bool" || n.Decode(&b) != nil {
			d.add(key, "must be true or false")
			return
		}
		v.SetBool(b)
	default:
		var i int64
		if n.ShortTag() != "!!int" || n.Decode(&i) != nil {
			d.add(key, "must be a whole number")
			return
		}
		v.SetInt(i)
	}
}

// fieldOf returns the index of the field of t whose YAML name is key.
func fieldOf(t reflect.Type, key string) (int, bool) {
	for i := range t.NumField() {
		if nameOf(t.Field(i).Tag) == key {
			return i, true
		}
	}
	return 0, false
}

// nameOf is the YAML name a field's tag gives it.
func nameOf(tag reflect.StructTag) string {
	name, _, _ := strings.Cut(tag.Get("yaml"), ",")
	return name
}

// join is the path of k within key.
func join(key, k string) string {
	if key == "" {
		return k
	}
	return key + "." + k
}

// imageDigest is an image's digest, as config.Revision.Lock takes it.
var imageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Check refuses what Decision 1 refuses that needs no database, and what
// C1 says has the wrong form: a required key left out; a secret given in any
// form but its reference; a configuration ref that is not in full; an admin
// address that is not on loopback, and, while the runner is the process
// runner, an agents' listener that is not; a price card the LLM gateway
// cannot price with; a runner other than the process runner, or the process
// runner without unsafeNoSandbox; a maxTokens above the LLM gateway's cap; an
// image that is not a digest; a duration that is not above zero; and the
// timings that cannot hold together. It fails with Problems.
func (d *Deployment) Check() error {
	var c checker
	c.required("tenant", d.Tenant)
	c.required("holder", d.Holder)
	c.required("secrets", d.Secrets)
	c.reference("database", d.Database)
	c.required("configuration.repository", d.Configuration.Repository)
	c.fullRef("configuration.ref", d.Configuration.Ref)
	c.agentsListener("listeners.mcp", d.Listeners.MCP, d.Runner.Kind)
	c.agentsListener("listeners.llm", d.Listeners.LLM, d.Runner.Kind)
	c.loopback("listeners.admin", d.Listeners.Admin, "such as 127.0.0.1:7403 or [::1]:7403: the admin API listens on loopback only")
	c.required("listeners.addresses", d.Listeners.Addresses)
	c.reference("admin.token", d.Admin.Token)
	c.httpURL("vendor.baseURL", d.Vendor.BaseURL)
	c.reference("vendor.key", d.Vendor.Key)
	c.required("vendor.apiVersion", d.Vendor.APIVersion)
	c.prices(&d.Prices)
	c.runner(&d.Runner)
	c.timing(&d.Timing)
	if len(c.problems) > 0 {
		return c.problems
	}
	return nil
}

// checker keeps the problems Check finds.
type checker struct{ problems Problems }

func (c *checker) add(key, format string, args ...any) {
	c.problems = append(c.problems, Problem{Key: key, Message: fmt.Sprintf(format, args...)})
}

// required refuses an empty value, and reports whether there is one.
func (c *checker) required(key, value string) bool {
	if value == "" {
		c.add(key, "is required")
		return false
	}
	return true
}

// positive refuses a number that is not above zero: one left out, when it
// is required.
func (c *checker) positive(key string, value int64, required bool) {
	switch {
	case value <= 0 && required:
		c.add(key, "is required: a whole number above zero")
	case value <= 0:
		c.add(key, "must be a whole number above zero")
	}
}

// reference refuses a secret given in any form but its reference,
// secret://<path>, a path under the secrets directory, as the secrets' store
// reads it. The problem never repeats the value, which may be the secret
// itself, as a database's URL with its password is.
func (c *checker) reference(key, value string) {
	if !c.required(key, value) {
		return
	}
	if name, ok := strings.CutPrefix(value, "secret://"); !ok || !fs.ValidPath(name) || name == "." {
		c.add(key, "must be a secret's reference, secret://<path>, which names a file under the secrets directory")
	}
}

// fullRef refuses a reference that is not given in full: the registry's Git
// source refuses a short name, which a branch and a tag can share.
func (c *checker) fullRef(key, value string) {
	if !c.required(key, value) {
		return
	}
	if !strings.HasPrefix(value, "refs/") || value == "refs/" {
		c.add(key, "must be a full reference, such as refs/heads/%s for a branch or refs/tags/%s for a tag", value, value)
	}
}

// address refuses a listener's address that is not host:port, and returns
// its host.
func (c *checker) address(key, value string) (host string, ok bool) {
	if !c.required(key, value) {
		return "", false
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		c.add(key, "must be an address, host:port, whose port may be 0")
		return "", false
	}
	return host, true
}

// loopback refuses an address whose host is not a loopback address, saying
// why after an example: the admin API listens on loopback only (Decision 14).
// A name such as localhost is refused too, since what it resolves to is the
// host's to say.
func (c *checker) loopback(key, value, why string) {
	host, ok := c.address(key, value)
	if !ok {
		return
	}
	if ip, err := netip.ParseAddr(host); err != nil || !ip.Unmap().IsLoopback() {
		c.add(key, "must be a loopback address, %s", why)
	}
}

// agentsListener refuses an agents' listener whose address is not host:port,
// and, while the runner is the process runner, one that is not on loopback:
// the process runner's turns run on this host, and the listener serves plain
// HTTP, the sessions' tokens included, until Plan 7d-2 brings TLS.
func (c *checker) agentsListener(key, value, runner string) {
	if runner != processRunner {
		c.address(key, value)
		return
	}
	c.loopback(key, value, "such as 127.0.0.1:0 or [::1]:0, while runner.kind is process: its turns run on this host, and the listener serves plain HTTP")
}

// httpURL refuses a value that is not an HTTP or HTTPS URL with a host. The
// problem never repeats the value, which may hold credentials.
func (c *checker) httpURL(key, value string) {
	if !c.required(key, value) {
		return
	}
	if u, err := url.Parse(value); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		c.add(key, "must be an http or https URL, such as https://api.anthropic.com")
	}
}

// prices refuses prices the LLM gateway cannot price with.
func (c *checker) prices(p *Prices) {
	c.positive("prices.reference", p.Reference, true)
	c.positive("prices.dailyCap", p.DailyCap, true)
	if len(p.Cards) == 0 {
		c.add("prices.cards", "is required: a price card for each model the agents use")
	}
	for _, model := range slices.Sorted(maps.Keys(p.Cards)) {
		card := p.Cards[model]
		if err := card.Check(); err != nil {
			c.add("prices.cards."+model, "cannot price a call: %v", err)
		}
	}
}

// runner refuses a runner this build cannot run turns with.
func (c *checker) runner(r *Runner) {
	if c.required("runner.kind", r.Kind) {
		switch {
		case r.Kind != processRunner:
			c.add("runner.kind", "must be %q, the only runner this build has", processRunner)
		case !r.UnsafeNoSandbox:
			c.add("runner.unsafeNoSandbox", "must be true with kind %s, whose turns run as host processes, unsandboxed", processRunner)
		}
	}
	c.required("runner.output", r.Output)
	c.positive("runner.outputCap", r.OutputCap, false)
	if r.MaxTokens < 1 || r.MaxTokens > MaxTokensCap {
		c.add("runner.maxTokens", "must be from 1 to %d, the LLM gateway's cap on max_tokens", MaxTokensCap)
	}
	if len(r.Images) == 0 {
		c.add("runner.images", "is required: the image of each runtime the agents name")
	}
	for _, runtime := range slices.Sorted(maps.Keys(r.Images)) {
		if !imageDigest.MatchString(r.Images[runtime]) {
			c.add("runner.images."+runtime, "must be an image's digest: sha256: and 64 lowercase hexadecimal digits")
		}
	}
}

// slack is the one duration that may be zero.
const slack = "timing.runtime.slack"

// databaseTimings is the path of the pools' timeouts, which Postgres counts in
// whole milliseconds, and reads 0 of as none.
const databaseTimings = "timing.database."

// timing refuses a duration that is not above zero, a database timeout
// below a millisecond, and, once every duration is right, the timings
// Decision 1 refuses because they cannot hold together.
func (c *checker) timing(t *Timing) {
	before := len(c.problems)
	eachDuration(reflect.ValueOf(*t), "timing", func(key string, d time.Duration) {
		switch {
		case key == slack && d < 0:
			c.add(key, "cannot be below zero")
		case key != slack && d <= 0:
			c.add(key, "must be above zero")
		case strings.HasPrefix(key, databaseTimings) && d < time.Millisecond:
			c.add(key, "must be at least 1ms: Postgres counts it in whole milliseconds")
		}
	})
	if len(c.problems) > before {
		return
	}
	l, db := &t.Lease, &t.Database
	// held is how long a holding is acted on after its last renewal: the
	// leader fences itself at the lease's time less the margin.
	held := l.TTL - l.Margin
	if held <= l.RenewEvery {
		c.add("timing.lease.renewEvery", "must be below timing.lease.ttl less timing.lease.margin, %v: the next renewal must come before the leader fences itself", held)
	}
	if l.TakeWait >= held {
		c.add("timing.lease.takeWait", "must be below timing.lease.ttl less timing.lease.margin, %v", held)
	}
	if l.TakeWait >= db.TransactionTimeout {
		c.add("timing.lease.takeWait", "must be below timing.database.transactionTimeout, %v: a take that waits behind writers must end by its own wait, not by the transaction's timeout", db.TransactionTimeout)
	}
	if db.TransactionTimeout > held {
		c.add("timing.database.transactionTimeout", "must be at most timing.lease.ttl less timing.lease.margin, %v: a transaction must not outlast its holding", held)
	}
	if db.IdleTimeout > held {
		c.add("timing.database.idleTimeout", "must be at most timing.lease.ttl less timing.lease.margin, %v: a transaction must not outlast its holding", held)
	}
	for _, gateway := range []struct {
		key     string
		timeout time.Duration
	}{{"timing.llm.recordTimeout", t.LLM.RecordTimeout}, {"timing.mcp.recordTimeout", t.MCP.RecordTimeout}} {
		if t.Coordinator.Grace < gateway.timeout {
			c.add("timing.coordinator.grace", "must be at least %s, %v: a round's end waits for its write calls to be recorded", gateway.key, gateway.timeout)
		}
	}
}

// eachDuration calls f with every duration of v, a struct of the file, and
// its key, by the YAML names on the way.
func eachDuration(v reflect.Value, key string, f func(key string, d time.Duration)) {
	for i := range v.NumField() {
		k := join(key, nameOf(v.Type().Field(i).Tag))
		switch field := v.Field(i); {
		case field.Type() == durationType:
			f(k, time.Duration(field.Int()))
		case field.Kind() == reflect.Struct:
			eachDuration(field, k, f)
		}
	}
}
