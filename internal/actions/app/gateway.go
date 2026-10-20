package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/tools"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Options bound the gateway.
type Options struct {
	// MaxArgsBytes caps a call's arguments.
	MaxArgsBytes int
	// MaxOutputBytes caps a call's output. A longer output is a read error,
	// never cut, because a cut output is partial data.
	MaxOutputBytes int
	// CallTimeout caps one call, within the round.
	CallTimeout time.Duration
	// Retries is how often a connection failure is retried, after Backoff,
	// doubled each time.
	Retries int
	Backoff time.Duration
	// VerifyEvery is how old the last look at a server's definitions may be
	// when a call goes to it; an older look is taken again first.
	VerifyEvery time.Duration
	// LookTimeout caps one look at a server's definitions.
	LookTimeout time.Duration
	// SessionCalls caps one session's calls in flight, and ServerCalls its
	// calls in flight to one server.
	SessionCalls int
	ServerCalls  int
	// RecordTimeout bounds each attempt to record how a call ended.
	RecordTimeout time.Duration
	// Log receives each request to a server the holding refused ("holding
	// refused"). Nil logs nothing.
	Log *slog.Logger
}

// Defaults are the gateway's limits: arguments of 64 KiB, outputs of 256 KiB,
// 60 s for a call, 2 retries of a connection failure (spec Section 15) after
// 200 and 400 ms, definitions looked at again after 30 s and for at most
// 10 s, 4 calls in flight per session and 2 per session and server, and 5 s
// to record an outcome.
var Defaults = Options{
	MaxArgsBytes: 64 << 10, MaxOutputBytes: 256 << 10, CallTimeout: 60 * time.Second,
	Retries: 2, Backoff: 200 * time.Millisecond, VerifyEvery: 30 * time.Second, LookTimeout: 10 * time.Second,
	SessionCalls: 4, ServerCalls: 2, RecordTimeout: 5 * time.Second,
}

// Ports are what the gateway works through.
type Ports struct {
	Sessions   Sessions
	Revisions  Revisions
	Upstream   Upstream
	Audit      Audit
	Hides      Hides
	Conditions Conditions
	IDs        IDs
	Limits     *limits.Limits
	Now        func() time.Time
	Board      Board
	// Holding is asked, before every request to a server, whether the
	// holding the request acts for still holds (Plan 7d-1, Decision 4).
	Holding holding.Holding
}

// Gateway is the MCP gateway's read path: the only place tool policy is
// enforced for reads (spec Section 6). No request reaches a server once the
// holding it acts for has ended: the call is answered unavailable, and
// records no failure (Plan 7d-1, Decisions 4 and 20).
type Gateway struct {
	p    Ports
	opts Options
	log  *slog.Logger

	mu sync.Mutex
	// busy counts the calls in the gateway's hands. Once closing is set, it
	// takes no call, and drained is closed when busy falls to zero.
	busy    int
	closing bool
	drained chan struct{}
	// The hides are reloaded before a tool's visibility is told once a look,
	// or a session of a later epoch than any before, made them due
	// (Decision 5): due counts the times they were made due, loaded the count
	// the last reload caught up with, and epoch is the latest epoch seen.
	due, loaded uint64
	epoch       int64

	observed  map[toolKey]observation
	verified  map[registration]time.Time
	verifying map[registration]chan struct{}
	hidden    map[hideKey]bool
	lists     map[listKey]roundList
	schemas   map[schemaKey]*tools.Schema
	flights   *flights
	refused   map[listKey]refusals
	noticed   map[listKey]*noticed
}

// noticed holds the notices a session's calls left in its round.
type noticed struct {
	round int
	kinds map[noticeKind]bool
}

// noticeKind names one notice of a round.
type noticeKind struct {
	tool, code, rule string
}

// registration names a server's registration: its endpoint and the hash of
// its file. Each registration looks at the server itself, with its own
// credential, and keeps what it saw.
type registration struct{ endpoint, hash string }

// toolKey names a tool of a registration.
type toolKey struct {
	registration
	tool string
}

// observation is the gateway's last look at a tool's definition.
type observation struct {
	offered bool
	hash    string // empty when the definition could not be read
	def     config.ToolDefinition
}

// hideKey names one pinned definition of one registration.
type hideKey struct{ endpoint, tool, pin, registration string }

type schemaKey struct{ endpoint, tool, hash string }

type listKey struct {
	run   board.RunID
	agent string
}

type roundList struct {
	round int
	tools []Tool
}

// Tool is a tool as an agent sees it: its name for agents, and the fields of
// the definition the gateway verified, never the server's bytes. Board
// tools are the gateway's own.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema json.RawMessage
	Annotations json.RawMessage

	kind   toolKind
	ref    config.ToolRef
	schema *tools.Schema // a board tool's
}

// toolKind says what a call to a tool does.
type toolKind int

const (
	readTool  toolKind = iota // read through the read path
	boardTool                 // post to the run's board
	writeTool                 // record a proposal
)

// New returns a gateway that holds the hides already recorded.
func New(ctx context.Context, p *Ports, opts *Options) (*Gateway, error) {
	if p.Holding == nil {
		return nil, errors.New("app: the gateway needs the holding, to ask before each request to a server")
	}
	hides, err := p.Hides.Hidden(ctx)
	if err != nil {
		return nil, fmt.Errorf("app: read hidden tools: %w", err)
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	g := &Gateway{
		p: *p, opts: *opts, log: log, drained: make(chan struct{}),
		observed: map[toolKey]observation{}, verified: map[registration]time.Time{}, verifying: map[registration]chan struct{}{},
		hidden: map[hideKey]bool{}, lists: map[listKey]roundList{}, schemas: map[schemaKey]*tools.Schema{},
		flights: newFlights(), refused: map[listKey]refusals{}, noticed: map[listKey]*noticed{},
	}
	g.hideAll(hides)
	return g, nil
}

// hideAll holds the hides given. The caller holds g.mu, or is New.
func (g *Gateway) hideAll(hides []Hide) {
	for i := range hides {
		h := &hides[i]
		g.hidden[hideKey{h.Endpoint, h.Tool, h.PinnedHash, h.Registration}] = true
	}
}

// see notes the epoch of a session, or of a platform read: the first of a
// later epoch than any before makes the hides due, as after a takeover
// (Decision 5).
func (g *Gateway) see(epoch int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if epoch > g.epoch {
		g.epoch = epoch
		g.due++
	}
}

// freshHides reloads the hides when a look, or a session of a later epoch,
// made them due since the last reload: another instance may have hidden a
// tool meanwhile (Decision 5). The reload runs apart from the call's context,
// within the record timeout, as the gateway's other reads and writes of its
// records do, so that a call cut short meanwhile is not the platform's
// failure. A reload that fails is returned: no tool's visibility can be told
// then.
func (g *Gateway) freshHides(ctx context.Context) error {
	g.mu.Lock()
	due, loaded := g.due, g.loaded
	g.mu.Unlock()
	if due == loaded {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.opts.RecordTimeout)
	defer cancel()
	hides, err := g.p.Hides.Hidden(ctx)
	if err != nil {
		return fmt.Errorf("app: reload hidden tools: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hideAll(hides)
	g.loaded = max(g.loaded, due)
	return nil
}

// Authenticate reports whether token names a live session.
func (g *Gateway) Authenticate(ctx context.Context, token string) error {
	_, err := g.p.Sessions.Session(ctx, token)
	return err
}

// Tools returns the tools the session's agent sees in its current round
// (spec Section 6.1): its granted read tools that are visible. The list stays
// the same until the round ends (ADR-006); a tool hidden meanwhile is
// refused when called. A server that cannot be reached when the list is
// made has its tools left out, and is recorded as a failed call of the
// round (Decision 20). Listing ends as a call does: at the call timeout, the
// round's end or the run's stop. It acts for the holding that issued the
// session, as a call does, and fails wrapping ErrUnavailable when the
// session's revision, its board or the hides cannot be read, a failed look
// cannot be recorded, or the holding refuses a look; a gateway that drains
// takes no listing either. A listing that its round's end or its run's stop
// cuts short fails wrapping ErrNoSession: the session is gone (Plan 7d-1,
// Decision 13).
func (g *Gateway) Tools(ctx context.Context, token string) ([]Tool, error) {
	if err := g.enter(); err != nil {
		return nil, err
	}
	defer g.leave()
	s, err := g.p.Sessions.Session(ctx, token)
	if err != nil {
		return nil, err
	}
	g.see(s.Epoch)
	ctx, cancel := g.callContext(holding.WithEpoch(ctx, s.Epoch), s.Deadline, s.Done)
	defer cancel()
	list, err := g.list(ctx, &s)
	if err != nil && !unheld(err) && cutShort(ctx) {
		return nil, fmt.Errorf("%w: its round ended or its run stopped while its tools were listed", ErrNoSession)
	}
	return list, err
}

func (g *Gateway) list(ctx context.Context, s *Session) ([]Tool, error) {
	key := listKey{s.Run, s.Agent}
	g.mu.Lock()
	l, ok := g.lists[key]
	g.mu.Unlock()
	if ok && l.round == s.Round {
		return l.tools, nil
	}
	rev, err := g.p.Revisions.At(ctx, s.Commit)
	if err != nil {
		return nil, fmt.Errorf("%w: revision %s: %w", ErrUnavailable, s.Commit, err)
	}
	agent := rev.Agent(s.Agent)
	if agent == nil {
		return nil, fmt.Errorf("app: revision %s has no agent %q", s.Commit, s.Agent)
	}
	var servers []string
	for _, gt := range agent.Tools() {
		if listable(s, &gt) && !slices.Contains(servers, gt.Ref.Server) {
			servers = append(servers, gt.Ref.Server)
		}
	}
	looks := g.lookAll(ctx, rev, servers)
	if err := refusedLook(looks); err != nil {
		return nil, err
	}
	bc, err := g.p.Board.Context(ctx, s.Run)
	if err != nil {
		return nil, fmt.Errorf("%w: the board of run %s: %w", ErrUnavailable, s.Run, err)
	}
	list, err := boardTools(&bc)
	if err != nil {
		return nil, err
	}
	granted, err := g.granted(ctx, s, rev, agent, looks)
	if err != nil {
		return nil, err
	}
	list = append(list, granted...)
	if err := ctx.Err(); err != nil {
		// A list made while the caller's time ran out is not the round's.
		return nil, err
	}
	if err := g.unreachable(ctx, s, rev, looks); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.lists) >= maxLists {
		clear(g.lists)
	}
	g.lists[key] = roundList{round: s.Round, tools: list}
	return list, nil
}

// listTool is the tool a listing is recorded as, for a server it could not
// reach.
const listTool = "tools/list"

// unreachable records, as a failed call of the session's round, each server
// whose look failed when the session's list was made, on the server's side
// (Decision 20): its tools are left out of the list, which holds for the
// round, so nothing in the round recovers it. A failure that cannot be
// recorded makes the listing unavailable, and the list is made again.
func (g *Gateway) unreachable(ctx context.Context, s *Session, rev *config.Revision, looks map[string]error) error {
	for _, server := range slices.Sorted(maps.Keys(looks)) {
		failed := looks[server]
		if failed == nil {
			continue
		}
		rec := &CallRecord{
			ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Server: server, Tool: listTool,
			Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now(),
		}
		if err := g.begin(ctx, rec); err != nil {
			return unrecorded(err)
		}
		g.record(ctx, &Outcome{ID: rec.ID, Status: StatusReadError, Failure: FailureServer, Output: describe(server, failed, always(rev)), At: g.p.Now()})
	}
	return nil
}

// lookAll looks at each server at once, and returns each one's error, nil
// for a look that succeeded, so that a server that hangs costs a list no
// more than one look.
func (g *Gateway) lookAll(ctx context.Context, rev *config.Revision, servers []string) map[string]error {
	errs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Go(func() { errs[i] = g.look(ctx, rev, server) })
	}
	wg.Wait()
	looks := make(map[string]error, len(servers))
	for i, server := range servers {
		looks[server] = errs[i]
	}
	return looks
}

// refusedLook returns the holding's refusal of a look, if one of looks met
// one: the listing is then answered unavailable, and records no failed look.
func refusedLook(looks map[string]error) error {
	for _, err := range looks {
		if unheld(err) {
			return err
		}
	}
	return nil
}

// granted returns the agent's granted tools that the session may see now:
// its visible read tools, and its visible write tools, extended with
// _hypothesis, unless the item is diagnosis-only. The tools of a server
// whose look failed are left out (Decision 20). It fails wrapping
// ErrUnavailable when the hides cannot be read.
func (g *Gateway) granted(ctx context.Context, s *Session, rev *config.Revision, agent *config.Agent, looks map[string]error) ([]Tool, error) {
	var list []Tool
	for _, gt := range agent.Tools() {
		if !listable(s, &gt) || looks[gt.Ref.Server] != nil {
			continue
		}
		v, ok, err := g.visible(ctx, rev, gt.Ref)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if !ok {
			continue
		}
		if gt.Effect == config.EffectWrite {
			if t, ok := writeListed(gt.Ref.AgentName(), &v, gt.Ref); ok {
				list = append(list, t)
			}
			continue
		}
		list = append(list, Tool{
			Name: gt.Ref.AgentName(), Title: v.def.Title, Description: v.def.Description,
			InputSchema: json.RawMessage(v.def.InputSchema), Annotations: json.RawMessage(v.def.Annotations), ref: gt.Ref,
		})
	}
	return list, nil
}

// listable reports whether a granted tool may be listed to the session:
// every read tool, and write tools, which are recorded as proposals, unless
// the item is diagnosis-only (spec Sections 3.6 and 6.1).
func listable(s *Session, gt *config.GrantedTool) bool {
	return gt.Effect == config.EffectRead || gt.Effect == config.EffectWrite && !s.DiagnosisOnly
}

// maxLists bounds the round lists kept; when it is reached, they are all
// computed again.
const maxLists = 4096

// visibleTool is a tool a call may use: its verified definition and its
// compiled input schema.
type visibleTool struct {
	def    config.ToolDefinition
	schema *tools.Schema
	srv    *config.McpServer
	policy *config.ToolPolicy
}

// visible returns a tool if it may be used now (spec Section 3.3, server
// pinning): the gateway's last look at it, which the caller has just taken
// or found fresh, found the pinned definition, no hide holds for that pin of
// the registration, a reviewer could see all of the definition, and the
// gateway can check its arguments. A mismatch hides the pin at once, for
// every run that holds it. It reloads the hides first when they are due,
// and fails when they cannot be read: no tool can be told visible then, a
// failure on the platform's side (Decision 20).
func (g *Gateway) visible(ctx context.Context, rev *config.Revision, ref config.ToolRef) (visibleTool, bool, error) {
	if err := g.freshHides(ctx); err != nil {
		return visibleTool{}, false, err
	}
	srv := rev.Server(ref.Server)
	policy := srv.Spec.Tools[ref.Tool]
	hk := hideKey{srv.Spec.Endpoint, ref.Tool, policy.SchemaHash, srv.Hash()}
	g.mu.Lock()
	hidden := g.hidden[hk]
	obs, looked := g.observed[toolKey{registration{srv.Spec.Endpoint, srv.Hash()}, ref.Tool}]
	g.mu.Unlock()
	hide := func(reason Reason) (visibleTool, bool, error) {
		g.hide(ctx, hk, ref.Server, reason, obs.hash)
		return visibleTool{}, false, nil
	}
	switch {
	case hidden || !looked || !obs.offered:
		return visibleTool{}, false, nil
	case obs.hash != policy.SchemaHash:
		return hide(ReasonChanged)
	case len(obs.def.HiddenFields()) > 0:
		return hide(ReasonHiddenCharacters)
	}
	schema, err := g.schema(schemaKey{srv.Spec.Endpoint, ref.Tool, obs.hash}, obs.def.InputSchema)
	if err != nil || len(schema.MissingBounds(policy.Bounds)) > 0 || policy.Effect == config.EffectWrite && !extensible(obs.def.InputSchema, schema) {
		return hide(ReasonSchema)
	}
	return visibleTool{def: obs.def, schema: schema, srv: srv, policy: policy}, true, nil
}

// look takes a new look at a server's definitions for the registration
// rev holds, when its last one is missing or older than VerifyEvery. Looks
// of one registration run one at a time; a caller waits for another's until
// its own context ends, and a look ends after LookTimeout. A new look asks
// the holding first, and makes the hides due (Decision 5).
func (g *Gateway) look(ctx context.Context, rev *config.Revision, server string) error {
	srv := rev.Server(server)
	reg := registration{srv.Spec.Endpoint, srv.Hash()}
	g.mu.Lock()
	lock := g.verifying[reg]
	if lock == nil {
		lock = make(chan struct{}, 1)
		g.verifying[reg] = lock
	}
	g.mu.Unlock()
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-lock }()
	g.mu.Lock()
	at, ok := g.verified[reg]
	g.mu.Unlock()
	if ok && g.p.Now().Sub(at) <= g.opts.VerifyEvery {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, g.opts.LookTimeout)
	defer cancel()
	if err := g.holds(ctx); err != nil {
		return err
	}
	offered, err := g.p.Upstream.Offered(ctx, &Server{Name: server, Endpoint: reg.endpoint, CredentialRef: srv.Spec.CredentialRef})
	if errors.Is(err, ErrUnavailable) {
		return g.holdingRefused(ctx)
	}
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, name := range slices.Sorted(maps.Keys(srv.Spec.Tools)) {
		o, ok := offered[name]
		obs := observation{offered: ok}
		if ok && o.Err == nil {
			if h, err := config.ToolSchemaHash(&o.Def); err == nil {
				obs.hash, obs.def = h, o.Def
			}
		}
		g.observed[toolKey{reg, name}] = obs
	}
	g.verified[reg] = g.p.Now()
	g.due++
	return nil
}

// hide hides a pinned definition of a registration at once, and records it.
// A hide that cannot be recorded still holds until the gateway restarts.
func (g *Gateway) hide(ctx context.Context, k hideKey, server string, reason Reason, observed string) {
	g.mu.Lock()
	g.hidden[k] = true
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.opts.RecordTimeout)
	defer cancel()
	_ = g.p.Hides.Hide(ctx, &Hide{
		Endpoint: k.endpoint, Server: server, Tool: k.tool, PinnedHash: k.pin, Registration: k.registration,
		Reason: reason, Observed: observed, At: g.p.Now(),
	})
}

// schema returns a definition's compiled input schema.
func (g *Gateway) schema(k schemaKey, raw []byte) (*tools.Schema, error) {
	g.mu.Lock()
	s, ok := g.schemas[k]
	g.mu.Unlock()
	if ok {
		return s, nil
	}
	s, err := tools.CompileSchema(raw)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.schemas[k] = s
	return s, nil
}
