package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/tools"
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
}

// Gateway is the MCP gateway's read path: the only place tool policy is
// enforced for reads (spec Section 6).
type Gateway struct {
	p    Ports
	opts Options

	mu        sync.Mutex
	observed  map[toolKey]observation
	verified  map[registration]time.Time
	verifying map[registration]chan struct{}
	hidden    map[hideKey]bool
	lists     map[listKey]roundList
	schemas   map[schemaKey]*tools.Schema
	flights   *flights
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
// the definition the gateway verified, never the server's bytes.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema json.RawMessage
	Annotations json.RawMessage

	ref config.ToolRef
}

// New returns a gateway that holds the hides already recorded.
func New(ctx context.Context, p *Ports, opts *Options) (*Gateway, error) {
	hides, err := p.Hides.Hidden(ctx)
	if err != nil {
		return nil, fmt.Errorf("app: read hidden tools: %w", err)
	}
	g := &Gateway{
		p: *p, opts: *opts,
		observed: map[toolKey]observation{}, verified: map[registration]time.Time{}, verifying: map[registration]chan struct{}{},
		hidden: map[hideKey]bool{}, lists: map[listKey]roundList{}, schemas: map[schemaKey]*tools.Schema{},
		flights: newFlights(),
	}
	for i := range hides {
		h := &hides[i]
		g.hidden[hideKey{h.Endpoint, h.Tool, h.PinnedHash, h.Registration}] = true
	}
	return g, nil
}

// Authenticate reports whether token names a live session.
func (g *Gateway) Authenticate(ctx context.Context, token string) error {
	_, err := g.p.Sessions.Session(ctx, token)
	return err
}

// Tools returns the tools the session's agent sees in its current round
// (spec Section 6.1): its granted read tools that are visible. The list stays
// the same until the round ends (ADR-006); a tool hidden meanwhile is
// refused when called. Listing ends as a call does: at the call timeout, the
// round's end or the run's stop.
func (g *Gateway) Tools(ctx context.Context, token string) ([]Tool, error) {
	s, err := g.p.Sessions.Session(ctx, token)
	if err != nil {
		return nil, err
	}
	ctx, cancel := g.callContext(ctx, s.Deadline, s.Done)
	defer cancel()
	return g.list(ctx, &s)
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
		return nil, fmt.Errorf("app: revision %s: %w", s.Commit, err)
	}
	agent := rev.Agent(s.Agent)
	if agent == nil {
		return nil, fmt.Errorf("app: revision %s has no agent %q", s.Commit, s.Agent)
	}
	var servers []string
	for _, gt := range agent.Tools() {
		if gt.Effect == config.EffectRead && !slices.Contains(servers, gt.Ref.Server) {
			servers = append(servers, gt.Ref.Server)
		}
	}
	looked := g.lookAll(ctx, rev, servers)
	var list []Tool
	for _, gt := range agent.Tools() {
		if gt.Effect != config.EffectRead {
			continue
		}
		v, ok := g.visible(ctx, rev, gt.Ref, looked[gt.Ref.Server])
		if !ok {
			continue
		}
		list = append(list, Tool{
			Name: gt.Ref.AgentName(), Title: v.def.Title, Description: v.def.Description,
			InputSchema: json.RawMessage(v.def.InputSchema), Annotations: json.RawMessage(v.def.Annotations), ref: gt.Ref,
		})
	}
	if err := ctx.Err(); err != nil {
		// A list made while the caller's time ran out is not the round's.
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

// lookAll looks at each server at once, and reports whose look succeeded,
// so that a server that hangs costs a list no more than one look.
func (g *Gateway) lookAll(ctx context.Context, rev *config.Revision, servers []string) map[string]bool {
	ok := make([]bool, len(servers))
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Go(func() { ok[i] = g.look(ctx, rev, server) == nil })
	}
	wg.Wait()
	looked := make(map[string]bool, len(servers))
	for i, server := range servers {
		looked[server] = ok[i]
	}
	return looked
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
// pinning): the gateway's last look at it found the pinned definition, no
// hide holds for that pin of the registration, a reviewer could see all of
// the definition, and the gateway can check its arguments. When the look is
// fresh, a mismatch hides the pin at once, for every run that holds it; an
// old look hides nothing, since the server may have changed since.
func (g *Gateway) visible(ctx context.Context, rev *config.Revision, ref config.ToolRef, fresh bool) (visibleTool, bool) {
	srv := rev.Server(ref.Server)
	policy := srv.Spec.Tools[ref.Tool]
	hk := hideKey{srv.Spec.Endpoint, ref.Tool, policy.SchemaHash, srv.Hash()}
	g.mu.Lock()
	hidden := g.hidden[hk]
	obs, looked := g.observed[toolKey{registration{srv.Spec.Endpoint, srv.Hash()}, ref.Tool}]
	g.mu.Unlock()
	hide := func(reason Reason) (visibleTool, bool) {
		if fresh {
			g.hide(ctx, hk, ref.Server, reason, obs.hash)
		}
		return visibleTool{}, false
	}
	switch {
	case hidden || !looked || !obs.offered:
		return visibleTool{}, false
	case obs.hash != policy.SchemaHash:
		return hide(ReasonChanged)
	case len(obs.def.HiddenFields()) > 0:
		return hide(ReasonHiddenCharacters)
	}
	schema, err := g.schema(schemaKey{srv.Spec.Endpoint, ref.Tool, obs.hash}, obs.def.InputSchema)
	if err != nil || len(schema.MissingBounds(policy.Bounds)) > 0 {
		return hide(ReasonSchema)
	}
	return visibleTool{def: obs.def, schema: schema, srv: srv, policy: policy}, true
}

// look takes a new look at a server's definitions for the registration
// rev holds, when its last one is missing or older than VerifyEvery. Looks
// of one registration run one at a time; a caller waits for another's until
// its own context ends, and a look ends after LookTimeout.
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
	offered, err := g.p.Upstream.Offered(ctx, &Server{Name: server, Endpoint: reg.endpoint, CredentialRef: srv.Spec.CredentialRef})
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
