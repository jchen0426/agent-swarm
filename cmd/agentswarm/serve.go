package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
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
	"github.com/jchen0426/agent-swarm/internal/secrets/files"
	"github.com/jchen0426/agent-swarm/internal/work/app"
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
the runner, which runs the agents' turns, and the admin API, as the deployment
file says. Of several instances, one leads at a time; the others stand by and
take the lease over when it is free.

Serve refuses to start on a database server older than PostgreSQL 17, on a
schema behind this build (run agentswarm migrate) or one only a later build
reads, on a deployment whose timings cannot hold together, and while the
environment gives MCPGODEBUG a value: the MCP SDK reads that variable once, and
its flags change the behaviour of both gateways. Once its three listeners are
bound, it writes their addresses and its process id to the deployment's
addresses file. It logs JSON lines on standard error.

The Docker runner (runner.kind docker), on Linux alone, runs each turn in a
sandbox: a container of the turn's runtime image, with no network, which
reaches the gateways through two Unix sockets in runner.docker.sockets, over
TLS 1.3, with a certificate serve makes at its start. Serve refuses to start
on another system; when the engine does not answer; when an image of
runner.images is missing from the engine and the store, or breaks the image
contract; when another serve holds the deployment's holder or its sockets
directory; and when that directory, or runner.docker.group, cannot serve the
sandboxes.

The process runner (runner.kind process), for development and tests, runs each
turn as this executable, or the deployment's runner.binary, unsandboxed, as it
finds that file when the turn starts: it ignores the runtime image an item
locked, so an executable rebuilt in place while items run runs their later
turns. Serve says at its start that the turns run unsandboxed.

SIGINT or SIGTERM drains it under the lease it holds: the coordinator stops
acting, so that the admin API opens no item and its /readyz answers draining,
the gateways finish their calls in flight, the lease is released, and the
listeners close. It then exits 0. A second signal stops every turn and exits 1
at once, and so does the end of standard input under --exit-with-stdin. On
every exit, serve kills every turn first.`,
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
// signals do not end: SIGINT and SIGTERM start the drain (Decision 6). A
// started instance ends through exit, whatever ends its run, so that every
// exit kills every turn first (Plan 7d-2, R19). Before it has started, a
// signal, or the end of stdin, ends it at once: nothing is held then, and no
// turn runs. stdin is nil without --exit-with-stdin.
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
		return r.s.exit(ctx, r.s.run(ctx, signals, eof))
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

// server is a started instance: what its drain, and its exit, act on.
type server struct {
	d       *deploy.Deployment
	log     *slog.Logger
	secrets *files.Store
	// main serves the stores, and lease the lease, its clock's samples and
	// the schema's checks (Decision 4).
	main, lease *pgxpool.Pool
	// hold is the holding, which the runner and both gateways ask before
	// each external request, and the leader publishes into (R4).
	hold      *app.Holding
	listeners listeners
	servers   servers
	// refreshing ends the registry's refreshes, and refreshed is closed
	// once they have ended.
	refreshing context.CancelFunc
	refreshed  chan struct{}
	upstream   *upstream.Pool
	mcp        *actionsapp.Gateway
	llm        *llmapp.Gateway
	runner     turnRunner
	// closer is the Docker runner, which exit closes once it has killed
	// every turn; nil under the process runner, which has no Close.
	closer io.Closer
	leader *app.Leader
}

// start starts an instance in Decision 1's order, up to its lead: it loads
// the deployment and opens its secrets; refuses the Docker runner on a
// system other than Linux, and an environment that gives MCPGODEBUG a value
// (Plan 7b-1, Decision 15); checks the database server and opens the pools;
// refuses a schema this build does not run on; makes the holding; under the
// Docker runner, makes the gateways' certificate and opens the runner, which
// checks its engine, its store, its holder's lock and its images, and sweeps
// (Plan 7d-2, R20); binds the listeners; wires the parts; writes the
// addresses; and serves. It refuses what it cannot run on before it binds a
// listener, and closes what it opened when it fails: the Docker runner among
// them, which has no turn yet.
func start(ctx context.Context, log *slog.Logger, path string) (_ *server, err error) {
	s := &server{log: log}
	defer func() {
		if err != nil {
			s.close(ctx)
			s.closeRunner(ctx)
		}
	}()
	if s.d, s.secrets, err = load(path); err != nil {
		return nil, err
	}
	if err := checkSystem(runtime.GOOS, s.d.Runner.Kind); err != nil {
		return nil, err
	}
	if err := checkEnvironment(os.Getenv); err != nil {
		return nil, err
	}
	if s.main, s.lease, err = openDatabase(ctx, s.d, s.secrets); err != nil {
		return nil, err
	}
	if err := checkSchema(ctx, s.lease); err != nil {
		return nil, err
	}
	s.hold = app.NewHolding(s.d.Timing.Lease.TTL, s.d.Timing.Lease.Margin, nil)
	var parts later
	var cert *tls.Certificate
	if s.d.Runner.Kind == deploy.KindDocker {
		if cert, err = s.openDocker(ctx, &parts, os.Getgid()); err != nil {
			return nil, err
		}
	}
	if s.listeners, err = listen(s.d, cert); err != nil {
		return nil, err
	}
	if err := s.wire(ctx, &parts); err != nil {
		return nil, err
	}
	if err := writeAddresses(s.d.Listeners.Addresses, s.listeners.addresses()); err != nil {
		return nil, err
	}
	log.InfoContext(ctx, "serve started", "holder", s.d.Holder, "schemaLevel", db.Level(), "worstDrain", worstDrain(s.d).String())
	s.serve(ctx)
	return s, nil
}

// load reads the deployment and opens its secrets, and checks, by the
// runner's kind, what the deployment names that serve opens itself (Task 14
// leaves them to it): the turns' output directory, which serve must be able
// to write in; under the process runner, the runtime's binary, when the
// deployment names one (R5); and under the Docker runner, the store, which
// serve must be able to write in too (Plan 7d-2, Decision 8). It refuses a
// kind this build does not have, which the deployment's check refuses
// first, before it opens anything.
func load(path string) (*deploy.Deployment, *files.Store, error) {
	d, err := deploy.Load(path)
	if err != nil {
		return nil, nil, err
	}
	switch d.Runner.Kind {
	case deploy.KindProcess:
		if err := writableDir(d.Runner.Output); err != nil {
			return nil, nil, fmt.Errorf("runner.output: %w", err)
		}
		if d.Runner.Binary != "" {
			if err := executable(d.Runner.Binary); err != nil {
				return nil, nil, fmt.Errorf("runner.binary: %w", err)
			}
		}
	case deploy.KindDocker:
		// The deployment's check takes a Docker runner's deployment only
		// with its runner.docker block.
		for _, dir := range []struct{ key, path string }{{"runner.output", d.Runner.Output}, {"runner.docker.store", d.Runner.Docker.Store}} {
			if err := writableDir(dir.path); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", dir.key, err)
			}
		}
	default:
		return nil, nil, fmt.Errorf("runner.kind: this build's serve does not run kind %q", d.Runner.Kind)
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
	info, err := os.Stat(dir) //nolint:gosec // G703: a directory the deployment names for serve to write in.
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
	return errors.Join(f.Close(), os.Remove(f.Name())) //nolint:gosec // G703: the file serve just made, in a directory the deployment names.
}

// executable checks that path is a file serve may run.
func executable(path string) error {
	info, err := os.Stat(path) //nolint:gosec // G703: the deployment's own runtime binary (runner.binary).
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	return nil
}

// openDatabase reads the database's URL, refuses one that does not parse,
// saying only that, refuses a server older than PostgreSQL 17 on a connection
// that sets no timeout, as one that sets one would be refused there, and
// opens the main pool and the lease's, each bounded as poolOptions says
// (Decision 4). Its errors never hold the URL.
func openDatabase(ctx context.Context, d *deploy.Deployment, secrets *files.Store) (main, lease *pgxpool.Pool, err error) {
	url, err := secrets.Secret(ctx, d.Database)
	if err != nil {
		return nil, nil, fmt.Errorf("database: %w", err)
	}
	if err := db.CheckURL(url); err != nil {
		return nil, nil, fmt.Errorf("database: %w", err)
	}
	opening, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	if err := db.CheckServer(opening, url); err != nil {
		return nil, nil, err
	}
	o := poolOptions(d)
	if main, err = db.OpenWith(opening, url, o); err != nil {
		return nil, nil, err
	}
	if lease, err = db.OpenWith(opening, url, o); err != nil {
		main.Close()
		return nil, nil, err
	}
	return main, lease, nil
}

// poolOptions are the bounds of serve's pools (Decision 4): every transaction,
// and every transaction left idle, as the deployment's database timing says,
// and each connect, unless the database's URL sets a connect_timeout, within
// the lease's renewal period, as each of the leader's calls to the lease ends.
// That is below a take's bound, the take's wait and the renewal period: a
// connect to a database whose address drops its packets frees its slot of
// the pool before the take that waits for it gives up, so that the take after
// it finds a slot once the database is back.
func poolOptions(d *deploy.Deployment) db.PoolOptions {
	return db.PoolOptions{
		TransactionTimeout: d.Timing.Database.TransactionTimeout,
		IdleTimeout:        d.Timing.Database.IdleTimeout,
		ConnectTimeout:     d.Timing.Lease.RenewEvery,
	}
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

// wire makes the instance's parts in the order Decision 1 gives, after the
// holding, which start makes before the runner: the database's clock,
// sampled on the lease's pool, for the coordinator, and the process's
// monotonic clock for the leader, each with its timers (Decision 2); the
// stores; the registry; the sessions; the MCP gateway; the LLM gateway; the
// process runner, under its kind, which needs the gateways' bound addresses
// (start opened the Docker runner already, before the listeners); the
// coordinator; the leader; and the admin API. It sets parts as it makes them,
// for the Docker runner's ports. Every part logs to the one logger.
func (s *server) wire(ctx context.Context, parts *later) error {
	d := s.d
	lease := workpg.NewLease(s.lease, d.Tenant, leaseName, d.Holder, d.Timing.Lease.TTL, d.Timing.Lease.TakeWait)
	dbClock, monotonic := clock.NewDB(lease, nil), clock.Monotonic{}
	dbTimers, monotonicTimers := clock.NewTimers(dbClock), clock.NewTimers(monotonic)
	st := newStores(s.main, d.Tenant, lease)
	parts.boards = st.boards
	cc, err := celeval.New()
	if err != nil {
		return err
	}
	revs, err := s.registry(ctx, st, cc)
	if err != nil {
		return err
	}
	parts.revs = revs
	live := sessions.New()
	if s.mcp, err = s.mcpGateway(ctx, s.hold, revs, live, st, cc); err != nil {
		return err
	}
	if s.llm, err = s.llmGateway(s.hold, revs, live, st); err != nil {
		return err
	}
	if d.Runner.Kind == deploy.KindProcess {
		// The runner reports turns' ends to the coordinator, which is made
		// after it: no turn starts before the coordinator takes over, under
		// the lead.
		r, err := s.processRunner(revs, st, func(e app.Exit) { parts.coord.Exited(ctx, e) })
		if err != nil {
			return err
		}
		s.runner = r
		s.log.WarnContext(ctx, "turns run unsandboxed", "runner", deploy.KindProcess)
	}
	if parts.coord, err = app.NewCoordinator(&app.Ports{
		Records: st.records, Boards: st.boards, Spend: st.models, ToolCalls: st.calls, Sweeper: s.llm, Revisions: revs,
		Sessions: live, Turns: s.runner, UpstreamSessions: s.upstream, Controls: workpg.NewControls(s.main, d.Tenant).Guarded(lease),
		Clock: dbClock, Timers: dbTimers, IDs: ids.UUIDv7{},
	}, &app.Options{
		Holder: d.Holder, DailyCap: d.Prices.DailyCap, Grace: d.Timing.Coordinator.Grace, SettleWait: d.Timing.Coordinator.SettleWait,
		SettlePoll: d.Timing.Coordinator.SettlePoll, Retry: d.Timing.Coordinator.Retry, ReopenWindow: d.Lifecycle.ReopenWindow,
		Limit: d.Lifecycle.Investigating, Log: s.log,
		// The kill switch's on during a drain is written within the database's
		// transaction timeout (Decision 5).
		WriteTimeout: d.Timing.Database.TransactionTimeout,
	}); err != nil {
		return err
	}
	// The leader's renewal check reads the kill switch's latest row on the
	// lease's pool, as its sample and its schema check (Decisions 4 and 5).
	s.leader, err = app.NewLeader(lease, parts.coord, monotonic, monotonicTimers, &app.LeaderOptions{
		RenewEvery: d.Timing.Lease.RenewEvery, Margin: d.Timing.Lease.Margin, TakeEvery: d.Timing.Lease.TakeEvery,
		TakeWait: d.Timing.Lease.TakeWait, Log: s.log, Holding: s.hold, Sampler: dbClock, Schema: schemaCheck{db.NewChecker(s.lease)},
		KillSwitch: workpg.NewControls(s.lease, d.Tenant),
	})
	if err != nil {
		return err
	}
	// The admin API reads the schema's state on the main pool: /readyz needs
	// no token, and a loop of probes must not compete with the renewals for
	// the lease's pool (Decision 4).
	api := admin.New(admin.Ports{
		Coordinator: parts.coord, Leader: s.leader, Schema: db.NewChecker(s.main), Items: app.NewViews(st.records, st.boards), Secrets: s.secrets,
		Runner: runnerStatus(s.runner),
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
// serve to serve's log. The agents' two allow HTTP/1 alone, under either
// runner (Plan 7d-2, Decision 2): Go's HTTP/2 server has no per-request
// header timeout, which the bound on a request's header needs. The process
// runner's plain HTTP never negotiated HTTP/2 anyway.
func newServers(mcp actionsendpoint.Gateway, llm llmendpoint.Gateway, api http.Handler, log *slog.Logger) servers {
	errs := slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	server := func(h http.Handler) *http.Server {
		return &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, ErrorLog: errs}
	}
	agents := func(h http.Handler) *http.Server {
		srv := server(h)
		srv.Protocols = new(http.Protocols)
		srv.Protocols.SetHTTP1(true)
		return srv
	}
	return servers{mcp: agents(actionsendpoint.New(mcp)), llm: agents(llmendpoint.New(llm, llmMaxBody)), admin: server(api)}
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
// drained, the leader has stopped for good, or serve is to exit at once, and
// returns serve's exit code, with which serve then exits (exit). The first
// signal starts the drain (Decision 6), after which serve exits 0. A second
// signal, or the end of standard input under --exit-with-stdin (R6), exits 1
// at once. A leader that stops for good, over a schema this build cannot run
// on (Decision 18), has fenced its coordinator and released the lease: serve
// exits 1, without draining; so does one whose Lead fails.
func (s *server) run(ctx context.Context, signals <-chan os.Signal, eof <-chan struct{}) int {
	led := make(chan error, 1)
	go func() { led <- s.leader.Lead(ctx) }()
	done := s.leader.Done()
	var drained chan struct{}
	for {
		select {
		case err := <-led:
			led = nil
			if err != nil && drained == nil {
				return exitError
			}
		case <-done:
			return exitError
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
			return exitOK
		}
	}
}

// exitAtOnce logs why serve exits at once, and returns its exit code, 1:
// exit then kills every turn, and serve waits for nothing else; the holding
// expires, and the calls it leaves without an outcome are swept at the next
// takeover (Decision 6).
func (s *server) exitAtOnce(ctx context.Context, why string) int {
	s.log.WarnContext(ctx, "serve exits at once", "why", why)
	return exitError
}

// exit ends a started instance with code, on each of its exits (Plan 7d-2,
// R19): it kills every turn (KillAll), then closes the Docker runner, the
// server's closer, when there is one, never before KillAll, whose removals
// need the engine's client; then it logs serve stopped with the code
// (Contract C3), and ends serve with it. A drain's close leaves the runner to
// it.
func (s *server) exit(ctx context.Context, code int) error {
	s.runner.KillAll(ctx)
	s.closeRunner(ctx)
	return stopped(ctx, s.log, code)
}

// closeRunner closes the Docker runner, the server's closer, when there is
// one, and logs a Close that fails, at Warn: a holder's lock left held then
// shows. exit calls it once it has killed every turn, and so does a start
// that fails once the runner is open, which has no turn yet.
func (s *server) closeRunner(ctx context.Context) {
	if s.closer == nil {
		return
	}
	if err := s.closer.Close(); err != nil {
		s.log.WarnContext(ctx, "the Docker runner did not close", "err", err)
	}
}

// drain shuts the instance down under the lease it holds (Decision 6): it
// drains up to the lease's release (drainer), then closes the listeners, the
// admin API's among them, and the pools. Until then the admin API takes no
// new work, since an opening of the fenced coordinator answers 409, while its
// reads, /healthz and /readyz, which says the instance drains, answer. Each
// step is bounded.
func (s *server) drain(ctx context.Context) {
	s.drainer().drain(ctx)
	s.close(ctx)
}

// drainer makes the instance's drainer: its leader, its gateways and its MCP
// servers' sessions, with the drain bound and, for the leader's stop, the
// leader's busy bound (leaderBusy).
func (s *server) drainer() *drainer {
	return &drainer{
		leader: s.leader, mcp: s.mcp, llm: s.llm, closeUpstream: s.upstream.Close,
		bound: s.d.Timing.Drain, busy: leaderBusy(s.d), log: s.log,
	}
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
	// bound is the drain bound, and busy the leader's busy bound
	// (leaderBusy), the most the leader's stop is waited for: what the
	// leader has in flight at the stop ends within it, a take that the drain
	// gives up included (Decision 14).
	bound, busy time.Duration
	log         *slog.Logger
}

// drain drains under the held lease, up to its release (Decision 6): the
// leader drains, fencing its coordinator, which stops every turn and ends
// every session, so that the gateways' calls in flight are cut, while it
// keeps the holding; the MCP gateway takes no call and finishes its detached
// writes, within 3 × RecordTimeout; the LLM gateway drains within the drain
// bound, and beside it the MCP servers' sessions close, while the holding
// still holds, within stopWait and the drain bound, so that the release waits
// for the slower of the two and never past the drain bound, which Decision 6's
// failover wait counts; then the leader releases the lease and stops, which
// the drain waits for up to the leader's busy bound (Decision 14).
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
	if !waitFor(d.leader.Done(), d.busy) {
		d.log.WarnContext(ctx, "the leader did not stop within its busy bound")
	}
}

// leaderBusy is the leader's busy bound (Decision 14), the most a drain waits
// for the leader's stop: a take's sample, the take, and a given-up take's
// release, takeWait + 3 × renewEvery. Those are the calls of a take in flight
// that the drain gives up, which the leader bounds by RenewEvery, TakeWait +
// RenewEvery and RenewEvery (internal/work/app/leader.go: take, sample and
// giveUp). A take that succeeded before the drain, and then samples the
// database's clock and checks the schema, runs takeWait + 4 × renewEvery from
// its start, above the bound; but by the stop only its sample and its check
// can be left, which worstDrain counts.
func leaderBusy(d *deploy.Deployment) time.Duration {
	l := d.Timing.Lease
	// The take's sample, the RenewEvery of the take's own bound, and the
	// release: internal/work/app/leader.go.
	return l.TakeWait + 3*l.RenewEvery
}

// worstDrain is the longest a drain can take before serve exits (R22, as Task
// 13 amends it), which serve logs at its start, for a supervisor's stop
// timeout to be set above it: the greater of the leader's path and the
// standby's, then the close's bounds and the exit's. It does not depend on the
// runner's kind, so it bounds the process runner's drain as well.
//
// The leader's path, when the instance holds the lease, is the MCP gateway's
// drain, the LLM gateway's with the MCP servers' sessions closing beside it,
// and the leader's stop, 2 × renewEvery. Stop releases the lease within
// RenewEvery, but Done closes only once no take, renewal or release of the
// leader's is in flight (internal/work/app/leader.go: settle), and a renewal,
// or a take, that succeeded just before the stop still samples the
// database's clock and then checks the schema, each within RenewEvery, under
// the leader's context, which the stop does not end (leader.go: renew, take,
// sample and check). A renewal's last step, its read of the kill switch's
// latest row, is not counted (R26): the leader skips it once it drains
// (leader.go: renew and checkKillSwitch), so the stop waits for no read a
// renewal begins during the drain, only for one in flight when the drain
// began, which ends within RenewEvery of its start. The standby's path, when
// it does not hold the lease, is the leader's busy bound, for a take in
// flight: it began before the drain, since a draining leader takes no more, so
// it runs beside the gateways' drains, not after them.
func worstDrain(d *deploy.Deployment) time.Duration {
	// The bounds that live in other packages, by their names there: the MCP
	// gateway's drain waits drainRecords record timeouts
	// (internal/actions/app); KillAll waits at most killAllWait, and the
	// Docker runner's Close at most closeWait (internal/runner/docker).
	const (
		drainRecords = 3
		killAllWait  = time.Second
		closeWait    = time.Second
	)
	t := d.Timing
	// The leader's stop waits for a sample and a check, each within
	// RenewEvery (internal/work/app/leader.go).
	leading := drainRecords*t.MCP.RecordTimeout + t.Drain + 2*t.Lease.RenewEvery
	// The close shuts three servers down, ends the registry's refresh and
	// closes two pools, each within stopWait.
	return max(leading, leaderBusy(d)) + 6*stopWait + killAllWait + closeWait
}

// close closes what the instance opened, each within its bound: the
// listeners, which removes the Docker runner's sockets and then releases
// their directory's flock, the registry's refreshes, the pools and the
// secrets. It also closes what a start that failed had opened. The upstream
// sessions are the drain's to close, under the holding; a start that failed
// made none. It leaves the runner to exit, which kills every turn before it
// closes the Docker runner, and to a start that failed, whose runner has no
// turn.
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
