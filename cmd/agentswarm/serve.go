package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	actionsendpoint "github.com/jchen0426/agent-swarm/internal/actions/endpoint"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/upstream"
	"github.com/jchen0426/agent-swarm/internal/admin"
	auditpg "github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/clock"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/ids"
	"github.com/jchen0426/agent-swarm/internal/llm/anthropic"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	llmendpoint "github.com/jchen0426/agent-swarm/internal/llm/endpoint"
	registryapp "github.com/jchen0426/agent-swarm/internal/registry/app"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/current"
	"github.com/jchen0426/agent-swarm/internal/registry/gitsource"
	registrypg "github.com/jchen0426/agent-swarm/internal/registry/postgres"
	"github.com/jchen0426/agent-swarm/internal/runner/process"
	"github.com/jchen0426/agent-swarm/internal/secrets/files"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	workpg "github.com/jchen0426/agent-swarm/internal/work/postgres"
	"github.com/jchen0426/agent-swarm/internal/work/sessions"
)

// The listeners' read bounds (Decision 1), fixed in code: a client that sends
// a request's header or body slowly cannot hold a connection for long.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
)

// llmMaxBody caps a request to the LLM gateway [512 KiB] (spec Section 15).
// It is the gateway's MaxBodyBytes and the most the LLM listener reads of a
// body, but for the one byte more by which the gateway tells a larger one:
// one setting for both.
const llmMaxBody = 512 << 10

// The LLM gateway's settings that a deployment does not set (spec Section 15;
// Contract C1): the share of the daily cap from which shadow runs are cut, the
// refused requests a session's round records, and the vendor's calls at once
// and the failures in a row that degrade it.
const (
	shadowCut      = 80
	maxRefusals    = 10
	vendorSlots    = 10
	vendorFailures = 3
)

// vendorBackoff are the waits between attempts at the vendor [1 s, 2 s, 4 s].
var vendorBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// leaseName names the platform's lease, which the leader holds.
const leaseName = "platform"

// startWait bounds each step of the start that reaches the database: the
// server's check, the pools' opening, the schema's check and the hides' read.
const startWait = 30 * time.Second

// stopWait bounds each step of the stop that has no bound of its own: a
// listener's shutdown, the upstream sessions' close, which the drain bound
// caps too, the registry's last refresh and a pool's close.
const stopWait = 5 * time.Second

func newServeCommand() *cobra.Command {
	var (
		deployment    string
		exitWithStdin bool
	)
	cmd := &cobra.Command{
		Use:   "serve --deployment FILE [--exit-with-stdin]",
		Short: "Run the coordinator, the leader, both gateways and the AI agents' turns in one process",
		Long: `Run one instance of the platform (Plan 7d-1, Decision 1): the coordinator, the
leader, which holds the platform's lease, the MCP gateway and the LLM gateway,
the process runner, which runs each turn as a host process, and the admin API,
as the deployment file says. Of several instances, one leads at a time; the
others stand by and take the lease over when it is free.

Serve refuses to start on a database server older than PostgreSQL 17, on a
schema behind this build (run agentswarm migrate) or one only a later build
reads, and on a deployment whose timings cannot hold together. Once its three
listeners are bound, it writes their addresses and its process id to the
deployment's addresses file. It logs JSON lines on standard error.

SIGINT or SIGTERM drains it under the lease it holds: the coordinator stops
acting, so that the admin API opens no item and its /readyz answers draining,
the gateways finish their calls in flight, the lease is released, and the
listeners close. It then exits 0. A second signal stops every turn and exits 1
at once, and so does the end of standard input under --exit-with-stdin.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var stdin io.Reader
			if exitWithStdin {
				stdin = c.InOrStdin()
			}
			return serve(c.Context(), deployment, stdin, c.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&deployment, "deployment", "", "the deployment file")
	cmd.Flags().BoolVar(&exitWithStdin, "exit-with-stdin", false,
		"stop every turn and exit 1 at once when standard input ends, as on a second signal")
	_ = cmd.MarkFlagRequired("deployment")
	return cmd
}

// served ends agentswarm serve with its exit code once its log has said why:
// run prints nothing of its own, so that serve's standard error holds its JSON
// lines alone (Contract C3).
type served struct{ code int }

func (s served) Error() string { return "serve exits " + strconv.Itoa(s.code) }

// serve runs an instance until it has drained, exiting 0, or until it is
// refused or stopped at once, exiting 1. It leads under a context that the
// signals do not end: SIGINT and SIGTERM start the drain (Decision 6).
// Before it has started, a signal, or the end of stdin, ends it at once:
// nothing is held then. stdin is nil without --exit-with-stdin.
func serve(ctx context.Context, path string, stdin io.Reader, stderr io.Writer) error {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	ctx = context.WithoutCancel(ctx)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	eof := endOf(stdin)

	type result struct {
		s   *server
		err error
	}
	started := make(chan result, 1)
	go func() {
		s, err := start(ctx, log, path)
		started <- result{s, err}
	}()
	select {
	case r := <-started:
		if r.err != nil {
			refused(ctx, log, r.err)
			return stopped(ctx, log, exitError)
		}
		return r.s.run(ctx, signals, eof)
	case sig := <-signals:
		log.WarnContext(ctx, "serve stopped before it started", "signal", sig.String())
	case <-eof:
		log.WarnContext(ctx, "serve stopped before it started: standard input ended")
	}
	return stopped(ctx, log, exitError)
}

// endOf returns a channel closed once r ends, or nil, which never closes,
// when r is nil.
func endOf(r io.Reader) <-chan struct{} {
	if r == nil {
		return nil
	}
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(ended)
	}()
	return ended
}

// refused logs why serve refused to start: a schema this build does not run
// on as "schema incompatible", with the checker's words, which name the
// levels (Contract C3).
func refused(ctx context.Context, log *slog.Logger, err error) {
	if errors.Is(err, db.ErrIncompatible) {
		log.ErrorContext(ctx, "schema incompatible", "reason", err.Error())
		return
	}
	log.ErrorContext(ctx, "serve refused to start", "err", err)
}

// stopped logs serve's end with its exit code (Contract C3), and ends serve
// with it.
func stopped(ctx context.Context, log *slog.Logger, code int) error {
	if code == exitOK {
		log.InfoContext(ctx, "serve stopped", "code", code)
		return nil
	}
	log.ErrorContext(ctx, "serve stopped", "code", code)
	return served{code}
}

// server is a started instance: what its drain, and its exit at once, act on.
type server struct {
	d       *deploy.Deployment
	log     *slog.Logger
	secrets *files.Store
	// main serves the stores, and lease the lease, its clock's samples and
	// the schema's checks (Decision 4).
	main, lease *pgxpool.Pool
	listeners   listeners
	servers     servers
	// refreshing ends the registry's refreshes, and refreshed is closed
	// once they have ended.
	refreshing context.CancelFunc
	refreshed  chan struct{}
	upstream   *upstream.Pool
	mcp        *actionsapp.Gateway
	llm        *llmapp.Gateway
	runner     *process.Runner
	leader     *app.Leader
}

// start starts an instance in Decision 1's order, up to its lead: it loads
// the deployment and opens its secrets; checks the database server and opens
// the pools; refuses a schema this build does not run on; binds the
// listeners; wires the parts; writes the addresses; and serves. It refuses
// what it cannot run on before it binds a listener, and closes what it opened
// when it fails.
func start(ctx context.Context, log *slog.Logger, path string) (_ *server, err error) {
	s := &server{log: log}
	defer func() {
		if err != nil {
			s.close(ctx)
		}
	}()
	if s.d, s.secrets, err = load(path); err != nil {
		return nil, err
	}
	if s.main, s.lease, err = openDatabase(ctx, s.d, s.secrets); err != nil {
		return nil, err
	}
	if err := checkSchema(ctx, s.lease); err != nil {
		return nil, err
	}
	if s.listeners, err = listen(&s.d.Listeners); err != nil {
		return nil, err
	}
	if err := s.wire(ctx); err != nil {
		return nil, err
	}
	if err := writeAddresses(s.d.Listeners.Addresses, s.listeners.addresses()); err != nil {
		return nil, err
	}
	log.InfoContext(ctx, "serve started", "holder", s.d.Holder, "level", db.Level())
	s.serve(ctx)
	return s, nil
}

// load reads the deployment and opens its secrets, and checks what the
// deployment names that serve opens itself (Task 14 leaves them to it): the
// turns' output directory, which serve must be able to write in, and the
// runtime's binary, when the deployment names one (R5).
func load(path string) (*deploy.Deployment, *files.Store, error) {
	d, err := deploy.Load(path)
	if err != nil {
		return nil, nil, err
	}
	if err := writableDir(d.Runner.Output); err != nil {
		return nil, nil, fmt.Errorf("runner.output: %w", err)
	}
	if d.Runner.Binary != "" {
		if err := executable(d.Runner.Binary); err != nil {
			return nil, nil, fmt.Errorf("runner.binary: %w", err)
		}
	}
	secrets, err := files.Open(d.Secrets)
	if err != nil {
		return nil, nil, fmt.Errorf("secrets: %w", err)
	}
	return d, secrets, nil
}

// writableDir checks that dir is a directory in which serve can make a file:
// it makes one, and removes it.
func writableDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	f, err := os.CreateTemp(dir, ".agentswarm-*")
	if err != nil {
		return fmt.Errorf("%s cannot be written: %w", dir, err)
	}
	return errors.Join(f.Close(), os.Remove(f.Name())) //nolint:gosec // G703: the file serve just made, in the deployment's output directory.
}

// executable checks that path is a file serve may run.
func executable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	return nil
}

// openDatabase reads the database's URL, refuses a server older than
// PostgreSQL 17 on a connection that sets no timeout, as one that sets one
// would be refused there, and opens the main pool and the lease's, each
// bounding its transactions as the deployment says (Decision 4). Its errors
// never hold the URL.
func openDatabase(ctx context.Context, d *deploy.Deployment, secrets *files.Store) (main, lease *pgxpool.Pool, err error) {
	url, err := secrets.Secret(ctx, d.Database)
	if err != nil {
		return nil, nil, fmt.Errorf("database: %w", err)
	}
	opening, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	if err := db.CheckServer(opening, url); err != nil {
		return nil, nil, err
	}
	o := db.PoolOptions(d.Timing.Database)
	if main, err = db.OpenWith(opening, url, o); err != nil {
		return nil, nil, err
	}
	if lease, err = db.OpenWith(opening, url, o); err != nil {
		main.Close()
		return nil, nil, err
	}
	return main, lease, nil
}

// checkSchema refuses a schema this build does not run on (Decision 18): one
// behind it, which agentswarm migrate brings up, or one only a later build
// reads. Its error, which wraps db.ErrIncompatible and names the levels, is
// the checker's own.
func checkSchema(ctx context.Context, lease *pgxpool.Pool) error {
	checking, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	return db.NewChecker(lease).Check(checking)
}

// listeners are serve's three: MCP and LLM for the agents' runtimes, and the
// admin API's, on loopback.
type listeners struct{ mcp, llm, admin net.Listener }

// listen binds the three listeners, so that their addresses are known before
// anything that needs them is made, and refuses an admin address that is not
// on loopback (Decision 1), as the deployment's check does first.
func listen(l *deploy.Listeners) (_ listeners, err error) {
	if err := onLoopback(l.Admin); err != nil {
		return listeners{}, fmt.Errorf("listeners.admin: %w", err)
	}
	var lns listeners
	defer func() {
		if err != nil {
			lns.close()
		}
	}()
	for _, b := range []struct {
		ln        *net.Listener
		key, addr string
	}{{&lns.mcp, "listeners.mcp", l.MCP}, {&lns.llm, "listeners.llm", l.LLM}, {&lns.admin, "listeners.admin", l.Admin}} {
		if *b.ln, err = net.Listen("tcp", b.addr); err != nil {
			return listeners{}, fmt.Errorf("%s: %w", b.key, err)
		}
	}
	return lns, nil
}

// onLoopback refuses an address whose host is not a loopback IP.
func onLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(host); err != nil || !ip.Unmap().IsLoopback() {
		return fmt.Errorf("%s is not on loopback", addr)
	}
	return nil
}

func (l *listeners) close() {
	for _, ln := range []net.Listener{l.mcp, l.llm, l.admin} {
		if ln != nil {
			_ = ln.Close()
		}
	}
}

// addresses are what serve writes once its listeners are bound (Contract
// C2).
type addresses struct {
	PID   int    `json:"pid"`
	MCP   string `json:"mcp"`
	LLM   string `json:"llm"`
	Admin string `json:"admin"`
}

func (l *listeners) addresses() addresses {
	return addresses{PID: os.Getpid(), MCP: l.mcp.Addr().String(), LLM: l.llm.Addr().String(), Admin: l.admin.Addr().String()}
}

// writeAddresses writes the addresses file whole: a file of its own in the
// same directory, renamed over it, so that no reader sees part of one.
func writeAddresses(path string, a addresses) (err error) {
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".addresses-*")
	if err != nil {
		return fmt.Errorf("listeners.addresses: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name()) //nolint:gosec // G703: the file serve just made, beside the deployment's addresses file.
		}
	}()
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("listeners.addresses: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("listeners.addresses: %w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil { //nolint:gosec // G703: the deployment's own addresses file (Contract C2).
		return fmt.Errorf("listeners.addresses: %w", err)
	}
	return nil
}

// stores are the database's stores. Work records, board events, tool calls
// and model calls are guarded by the lease: each write checks that the
// holding it acts for still holds the lease. Hides and the configuration's
// revisions are not (Decision 4).
type stores struct {
	records   *workpg.Records
	events    *workpg.EventStore
	runs      *workpg.Runs
	calls     *auditpg.Calls
	models    *auditpg.ModelCalls
	hides     *auditpg.Hides
	revisions *registrypg.Revisions
	boards    *app.Boards
}

func newStores(pool *pgxpool.Pool, tenant string, lease *workpg.Lease) *stores {
	st := &stores{
		records:   workpg.NewRecords(pool, tenant).Guarded(lease),
		events:    workpg.NewEventStore(pool, tenant).Guarded(lease),
		runs:      workpg.NewRuns(pool, tenant),
		calls:     auditpg.NewCalls(pool, tenant).Guarded(lease),
		models:    auditpg.NewModelCalls(pool, tenant).Guarded(lease),
		hides:     auditpg.NewHides(pool, tenant),
		revisions: registrypg.NewRevisions(pool, tenant),
	}
	st.boards = app.NewBoards(st.events, st.calls, st.runs, ids.UUIDv7{})
	return st
}

// wire makes the instance's parts in the order Decision 1 gives: the holding
// first, which the runner and both gateways ask before each external request
// and the leader publishes into (R4); the database's clock, sampled on the
// lease's pool, for the coordinator, and the process's monotonic clock for
// the leader, each with its timers (Decision 2); the stores; the registry;
// the sessions; the MCP gateway; the LLM gateway; the runner; the
// coordinator; the leader; and the admin API. Every part logs to the one
// logger.
func (s *server) wire(ctx context.Context) error {
	d := s.d
	hold := app.NewHolding(d.Timing.Lease.TTL, d.Timing.Lease.Margin, nil)
	lease := workpg.NewLease(s.lease, d.Tenant, leaseName, d.Holder, d.Timing.Lease.TTL, d.Timing.Lease.TakeWait)
	dbClock, monotonic := clock.NewDB(lease, nil), clock.Monotonic{}
	dbTimers, monotonicTimers := clock.NewTimers(dbClock), clock.NewTimers(monotonic)
	st := newStores(s.main, d.Tenant, lease)
	cc, err := celeval.New()
	if err != nil {
		return err
	}
	revs, err := s.registry(ctx, st, cc)
	if err != nil {
		return err
	}
	live := sessions.New()
	if s.mcp, err = s.mcpGateway(ctx, hold, revs, live, st, cc); err != nil {
		return err
	}
	if s.llm, err = s.llmGateway(hold, revs, live, st); err != nil {
		return err
	}
	// The runner reports turns' ends to the coordinator, which is made after
	// it: no turn starts before the coordinator takes over, under the lead.
	var coord *app.Coordinator
	if s.runner, err = s.processRunner(hold, revs, st, func(e app.Exit) { coord.Exited(ctx, e) }); err != nil {
		return err
	}
	coord, err = app.NewCoordinator(&app.Ports{
		Records: st.records, Boards: st.boards, Spend: st.models, ToolCalls: st.calls, Sweeper: s.llm, Revisions: revs,
		Sessions: live, Turns: s.runner, Clock: dbClock, Timers: dbTimers, IDs: ids.UUIDv7{},
	}, &app.Options{
		Holder: d.Holder, DailyCap: d.Prices.DailyCap, Grace: d.Timing.Coordinator.Grace, SettleWait: d.Timing.Coordinator.SettleWait,
		SettlePoll: d.Timing.Coordinator.SettlePoll, Retry: d.Timing.Coordinator.Retry, Log: s.log,
	})
	if err != nil {
		return err
	}
	s.leader, err = app.NewLeader(lease, coord, monotonic, monotonicTimers, &app.LeaderOptions{
		RenewEvery: d.Timing.Lease.RenewEvery, Margin: d.Timing.Lease.Margin, TakeEvery: d.Timing.Lease.TakeEvery,
		TakeWait: d.Timing.Lease.TakeWait, Log: s.log, Holding: hold, Sampler: dbClock, Schema: schemaCheck{db.NewChecker(s.lease)},
	})
	if err != nil {
		return err
	}
	api := admin.New(admin.Ports{
		Coordinator: coord, Leader: s.leader, Schema: db.NewChecker(s.lease), Items: app.NewViews(st.records, st.boards), Secrets: s.secrets,
	}, admin.Options{Tenant: d.Tenant, TokenRef: d.Admin.Token, Log: s.log})
	s.servers = newServers(s.mcp, s.llm, api, s.log)
	return nil
}

// registry makes the registry at runtime over the configuration repository,
// and starts its refreshes: a revision is current once every agent's model
// has a price card and every skill passes the LLM gateway's checks of a
// system prompt (Decision 11). Until the first refresh makes one current, an
// opening answers 503.
func (s *server) registry(ctx context.Context, st *stores, cc *celeval.Conditions) (*current.Revisions, error) {
	d := s.d
	src, err := gitsource.Open(d.Configuration.Repository)
	if err != nil {
		return nil, fmt.Errorf("configuration.repository: %w", err)
	}
	revs := current.New(registryapp.New(src, cc, st.revisions, d.Configuration.Ref, d.Runner.Images), current.Options{
		Refresh: d.Timing.Registry.Refresh, Images: d.Runner.Images, Priced: priced(d.Prices.Cards), CheckSkill: llmapp.CheckSkill, Log: s.log,
	})
	refreshing, cancel := context.WithCancel(ctx)
	s.refreshing, s.refreshed = cancel, make(chan struct{})
	go func() {
		defer close(s.refreshed)
		revs.Run(refreshing)
	}()
	return revs, nil
}

// priced reports whether the deployment prices a model.
func priced(cards map[string]cost.Card) func(model string) bool {
	return func(model string) bool {
		_, ok := cards[model]
		return ok
	}
}

// mcpGateway makes the MCP gateway, over an upstream pool that asks the
// holding before each request and trusts the deployment's CA bundle besides
// the system roots.
func (s *server) mcpGateway(ctx context.Context, hold *app.Holding, revs *current.Revisions, live *sessions.Registry, st *stores,
	cc *celeval.Conditions,
) (*actionsapp.Gateway, error) {
	d := s.d
	pool := upstream.Defaults
	pool.Holding = hold
	if d.MCP.CABundle != "" {
		roots, err := caBundle(d.MCP.CABundle)
		if err != nil {
			return nil, err
		}
		pool.RootCAs = roots
	}
	s.upstream = upstream.New(s.secrets, pool, time.Now)
	settings := limits.Defaults
	settings.CoolDown = d.Timing.MCP.CoolDown
	reading, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	return actionsapp.New(reading, &actionsapp.Ports{
		Sessions: live.MCP(), Revisions: revs, Upstream: s.upstream, Audit: st.calls, Hides: st.hides, Conditions: cc, IDs: ids.UUIDv7{},
		Limits: limits.New(settings, nil, time.Now), Now: time.Now, Board: st.boards, Holding: hold,
	}, mcpOptions(d, s.log))
}

// caBundle reads the deployment's CA bundle into a pool that also holds the
// system roots, since a pool given to the upstream replaces them (Task 1).
func caBundle(path string) (*x509.CertPool, error) {
	roots, err := readCA(path)
	if err != nil {
		return nil, fmt.Errorf("mcp.caBundle: %w", err)
	}
	return roots, nil
}

// mcpOptions are the MCP gateway's options: its package's defaults, with the
// deployment's timings.
func mcpOptions(d *deploy.Deployment, log *slog.Logger) *actionsapp.Options {
	o := actionsapp.Defaults
	o.CallTimeout, o.VerifyEvery, o.LookTimeout = d.Timing.MCP.CallTimeout, d.Timing.MCP.VerifyEvery, d.Timing.MCP.LookTimeout
	o.RecordTimeout, o.Log = d.Timing.MCP.RecordTimeout, log
	return &o
}

// llmGateway makes the LLM gateway, which reaches the vendor with the
// deployment's key and asks the holding before each attempt.
func (s *server) llmGateway(hold *app.Holding, revs *current.Revisions, live *sessions.Registry, st *stores) (*llmapp.Gateway, error) {
	d := s.d
	vendor, err := anthropic.New(&anthropic.Config{BaseURL: d.Vendor.BaseURL, KeyRef: d.Vendor.Key, Version: d.Vendor.APIVersion}, s.secrets)
	if err != nil {
		return nil, fmt.Errorf("vendor: %w", err)
	}
	return llmapp.New(&llmapp.Ports{
		Sessions: live.LLM(), Revisions: revs, Vendor: vendor, Calls: st.models, IDs: ids.UUIDv7{}, Now: time.Now, Holding: hold,
	}, llmOptions(d, s.log))
}

// llmOptions are the LLM gateway's options, which have no defaults: spec
// Section 15's values, the deployment's prices and timings, and the cap on a
// request's max_tokens that runner.maxTokens is checked against (R14).
func llmOptions(d *deploy.Deployment, log *slog.Logger) *llmapp.Options {
	return &llmapp.Options{
		MaxBodyBytes: llmMaxBody, MaxTokens: deploy.MaxTokensCap, Version: d.Vendor.APIVersion,
		Limits: cost.Limits{DailyCap: d.Prices.DailyCap, ShadowCut: shadowCut}, Cards: d.Prices.Cards, Reference: d.Prices.Reference,
		Vendor:  limits.Settings{Slots: vendorSlots, Failures: vendorFailures, CoolDown: d.Timing.LLM.CoolDown},
		Backoff: slices.Clone(vendorBackoff), VendorTimeout: d.Timing.LLM.VendorTimeout, RecordTimeout: d.Timing.LLM.RecordTimeout,
		MaxRefusals: maxRefusals, LastCallWait: d.Timing.LLM.LastCallWait, Log: log,
	}
}

// processRunner makes the process runner, which runs each turn as serve's
// own executable, or the deployment's (R5), and gives it the gateways' bound
// addresses. A turn's end goes to exited.
func (s *server) processRunner(hold *app.Holding, revs *current.Revisions, st *stores, exited func(app.Exit)) (*process.Runner, error) {
	d := s.d
	return process.New(process.Ports{
		Holding: hold, Skills: revs.Skills, Ended: turnEnded(st.boards), Exited: exited, Log: s.log,
	}, process.Options{
		Binary: d.Runner.Binary, Output: d.Runner.Output, OutputCap: d.Runner.OutputCap, Slack: d.Timing.Runtime.Slack,
		MCP: "http://" + s.listeners.mcp.Addr().String(), LLM: "http://" + s.listeners.llm.Addr().String(), MaxTokens: d.Runner.MaxTokens,
	})
}

// turnEnded reports whether the board holds an agent's own turn end in a
// round, as the coordinator reads it.
func turnEnded(boards *app.Boards) func(ctx context.Context, run board.RunID, round int, agent string) (bool, error) {
	return func(ctx context.Context, run board.RunID, round int, agent string) (bool, error) {
		report, err := boards.Report(ctx, run, round)
		if err != nil {
			return false, err
		}
		return slices.Contains(report.Facts.Ended, agent), nil
	}
}

// schemaCheck is the leader's check of the schema at each take and renewal
// (Decision 18): the database's checker, whose refusal stops the leader for
// good, as app.ErrSchema; a read that failed stays a plain error, which the
// leader tries again.
type schemaCheck struct{ checker *db.Checker }

func (c schemaCheck) Check(ctx context.Context) error {
	err := c.checker.Check(ctx)
	if errors.Is(err, db.ErrIncompatible) {
		return schemaRefusal{err}
	}
	return err
}

// schemaRefusal is the checker's refusal as the leader reads it: ErrSchema,
// in the checker's words, which name the levels and which "schema
// incompatible" logs.
type schemaRefusal struct{ err error }

func (r schemaRefusal) Error() string { return r.err.Error() }

func (r schemaRefusal) Unwrap() []error { return []error{app.ErrSchema, r.err} }

// servers are the three listeners' servers.
type servers struct{ mcp, llm, admin *http.Server }

// newServers makes the listeners' servers: the MCP gateway's endpoint, the
// LLM gateway's, which reads at most the request size the gateway takes, and
// the admin API. Each bounds its reads (Decision 1), and logs what it cannot
// serve to serve's log.
func newServers(mcp actionsendpoint.Gateway, llm llmendpoint.Gateway, api http.Handler, log *slog.Logger) servers {
	errs := slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	server := func(h http.Handler) *http.Server {
		return &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, ErrorLog: errs}
	}
	return servers{mcp: server(actionsendpoint.New(mcp)), llm: server(llmendpoint.New(llm, llmMaxBody)), admin: server(api)}
}

// serve serves the three listeners.
func (s *server) serve(ctx context.Context) {
	for _, l := range []struct {
		srv *http.Server
		ln  net.Listener
	}{{s.servers.mcp, s.listeners.mcp}, {s.servers.llm, s.listeners.llm}, {s.servers.admin, s.listeners.admin}} {
		go func() {
			if err := l.srv.Serve(l.ln); !errors.Is(err, http.ErrServerClosed) {
				s.log.ErrorContext(ctx, "a listener stopped serving", "addr", l.ln.Addr().String(), "err", err)
			}
		}()
	}
}

// run leads, under ctx, which the signals do not end, until the instance has
// drained, the leader has stopped for good, or serve is to exit at once. The
// first signal starts the drain (Decision 6). A second signal, or the end of
// standard input under --exit-with-stdin (R6), stops every turn and exits 1
// at once. A leader that stops for good, over a schema this build cannot run
// on (Decision 18), has fenced its coordinator and released the lease: serve
// exits 1, without draining.
func (s *server) run(ctx context.Context, signals <-chan os.Signal, eof <-chan struct{}) error {
	led := make(chan error, 1)
	go func() { led <- s.leader.Lead(ctx) }()
	done := s.leader.Done()
	var drained chan struct{}
	for {
		select {
		case err := <-led:
			led = nil
			if err != nil && drained == nil {
				return stopped(ctx, s.log, exitError)
			}
		case <-done:
			return stopped(ctx, s.log, exitError)
		case sig := <-signals:
			if drained != nil {
				return s.exitAtOnce(ctx, "a second signal: "+sig.String())
			}
			s.log.InfoContext(ctx, "serve drains", "signal", sig.String())
			done, drained = nil, make(chan struct{})
			go func() {
				defer close(drained)
				s.drain(ctx)
			}()
		case <-eof:
			return s.exitAtOnce(ctx, "standard input ended")
		case <-drained:
			return stopped(ctx, s.log, exitOK)
		}
	}
}

// exitAtOnce stops every turn's process group and ends serve at once,
// waiting for nothing else: the holding expires, and the calls it leaves
// without an outcome are swept at the next takeover (Decision 6).
func (s *server) exitAtOnce(ctx context.Context, why string) error {
	s.runner.StopAll()
	s.log.WarnContext(ctx, "serve exits at once", "why", why)
	return stopped(ctx, s.log, exitError)
}

// drain shuts the instance down under the lease it holds (Decision 6): it
// drains up to the lease's release (drainer), then closes the listeners, the
// admin API's among them, and the pools. Until then the admin API takes no
// new work, since an opening of the fenced coordinator answers 409, while its
// reads, /healthz and /readyz, which says the instance drains, answer. Each
// step is bounded.
func (s *server) drain(ctx context.Context) {
	d := drainer{
		leader: s.leader, mcp: s.mcp, llm: s.llm, closeUpstream: s.upstream.Close,
		bound: s.d.Timing.Drain, ttl: s.d.Timing.Lease.TTL, log: s.log,
	}
	d.drain(ctx)
	s.close(ctx)
}

// drainer is what a drain acts on up to the lease's release: the leader, the
// two gateways and the MCP servers' sessions, each of which a test can stand
// in for.
type drainer struct {
	leader interface {
		Drain()
		Stop(ctx context.Context)
		Done() <-chan struct{}
	}
	mcp, llm interface {
		Drain(ctx context.Context) error
	}
	// closeUpstream closes the MCP servers' sessions, which needs the
	// holding.
	closeUpstream func()
	// bound is the drain bound, and ttl the lease's, the most the leader's
	// stop is waited for: a take in flight ends within the take's wait and
	// the renewal's period, below the lease's time by default, and once the
	// lease's time has passed, its holding has expired anyway.
	bound, ttl time.Duration
	log        *slog.Logger
}

// drain drains under the held lease, up to its release (Decision 6): the
// leader drains, fencing its coordinator, which stops every turn and ends
// every session, so that the gateways' calls in flight are cut, while it
// keeps the holding; the MCP gateway takes no call and finishes its detached
// writes, within 3 × RecordTimeout; the LLM gateway drains within the drain
// bound, and beside it the MCP servers' sessions close, while the holding
// still holds, within stopWait and the drain bound, so that the release waits
// for the slower of the two and never past the drain bound, which Decision 6's
// failover wait counts; then the leader releases the lease and stops.
func (d *drainer) drain(ctx context.Context) {
	d.leader.Drain()
	if err := d.mcp.Drain(ctx); err != nil {
		d.log.WarnContext(ctx, "the MCP gateway did not drain", "err", err)
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		d.closeUpstream()
	}()
	closing, stopClosing := context.WithTimeout(ctx, min(stopWait, d.bound))
	defer stopClosing()
	draining, stopDraining := context.WithTimeout(ctx, d.bound)
	defer stopDraining()
	if err := d.llm.Drain(draining); err != nil {
		d.log.WarnContext(ctx, "the LLM gateway did not drain", "err", err)
	}
	select {
	case <-closed:
	case <-closing.Done():
		d.log.WarnContext(ctx, "the MCP servers' sessions were not all closed", "wait", min(stopWait, d.bound).String())
	}
	d.leader.Stop(ctx)
	if !waitFor(d.leader.Done(), d.ttl) {
		d.log.WarnContext(ctx, "the leader did not stop in the lease's time")
	}
}

// close closes what the instance opened, each within its bound: the
// listeners, the registry's refreshes, the pools and the secrets. It also
// closes what a start that failed had opened. The upstream sessions are the
// drain's to close, under the holding; a start that failed made none.
func (s *server) close(ctx context.Context) {
	for _, srv := range []*http.Server{s.servers.mcp, s.servers.llm, s.servers.admin} {
		s.shutdown(ctx, srv)
	}
	s.listeners.close()
	if s.refreshing != nil {
		s.refreshing()
		if !waitFor(s.refreshed, stopWait) {
			s.log.WarnContext(ctx, "the registry's refresh did not end", "wait", stopWait.String())
		}
	}
	for _, pool := range []*pgxpool.Pool{s.main, s.lease} {
		if pool != nil && !within(stopWait, pool.Close) {
			s.log.WarnContext(ctx, "a database pool did not close", "wait", stopWait.String())
		}
	}
	if s.secrets != nil {
		_ = s.secrets.Close()
	}
}

// shutdown stops a server taking requests, and lets those it has finish
// within stopWait, after which it closes them.
func (s *server) shutdown(ctx context.Context, srv *http.Server) {
	if srv == nil {
		return
	}
	stopping, cancel := context.WithTimeout(ctx, stopWait)
	defer cancel()
	if err := srv.Shutdown(stopping); err != nil {
		_ = srv.Close()
	}
}

// within runs f, and reports whether it returned within d; one that did not
// goes on alone.
func within(d time.Duration, f func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	return waitFor(done, d)
}

// waitFor reports whether done closed within d.
func waitFor(done <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
