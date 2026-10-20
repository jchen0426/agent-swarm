// Package sessions keeps the live sessions of the runs one instance
// coordinates, in memory. The coordinator issues, moves and ends them; the
// gateways look a request's session up by its token, two or three times per
// tool call (spec Section 2). Only the tokens' hashes are kept, and a
// session lives no longer than the instance that issued it.
package sessions

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// Registry holds the live sessions. It implements app.Sessions for the
// coordinator, and each gateway's Sessions port.
type Registry struct {
	mu       sync.RWMutex
	sessions map[key]*live
	byToken  map[[32]byte]named
}

type key struct {
	run   board.RunID
	agent string
}

// live is a session as the registry holds it. Its round is 0 until the
// coordinator moves it into one.
type live struct {
	app.Session
	mcp, llm [32]byte
	round    int
	deadline time.Time
	budget   int64
	done     chan struct{}
}

// named is the session a token's hash names, and the token's gateway.
type named struct {
	s       *live
	gateway tokens.Gateway
}

// New returns a registry with no session.
func New() *Registry {
	return &Registry{sessions: map[key]*live{}, byToken: map[[32]byte]named{}}
}

// Issue gives an agent's session in a run two new tokens, which the
// gateways know once Move puts the session in a round. The session's earlier
// tokens, if it has any, are forgotten, so that a turn of an earlier round
// still running cannot act in the next; the session keeps its channel, which
// ends the calls of either tokens when the session ends. The gateways see the
// epoch s was issued under.
func (r *Registry) Issue(s *app.Session) app.Tokens {
	t := app.Tokens{MCP: tokens.New(tokens.MCP), LLM: tokens.New(tokens.LLM)}
	l := &live{Session: *s, mcp: t.MCP.Hash(), llm: t.LLM.Hash(), done: make(chan struct{})}
	l.Item = maps.Clone(s.Item)
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key{s.Run, s.Agent}
	if old := r.sessions[k]; old != nil {
		delete(r.byToken, old.mcp)
		delete(r.byToken, old.llm)
		l.done = old.done
	}
	r.sessions[k] = l
	r.byToken[l.mcp] = named{l, tokens.MCP}
	r.byToken[l.llm] = named{l, tokens.LLM}
	return t
}

// Move puts a session in a round, which counts from 1, until deadline, in
// the process's clock, with the run's token budget as the LLM gateway holds
// it in that round. The coordinator moves a session once the round's board
// entry is committed, so that no session holds a round the board has not
// opened.
func (r *Registry) Move(run board.RunID, agent string, round int, deadline time.Time, budget int64) error {
	if round < 1 || deadline.IsZero() {
		return fmt.Errorf("sessions: a session moves into a round from 1, with a deadline, not round %d", round)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.sessions[key{run, agent}]
	if l == nil {
		return fmt.Errorf("sessions: %s has no session in run %s", agent, run)
	}
	l.round, l.deadline, l.budget = round, deadline, budget
	return nil
}

// End ends an agent's session in a run: its tokens are forgotten and its
// channel is closed, which ends its calls at once.
func (r *Registry) End(run board.RunID, agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.end(key{run, agent})
}

// EndRun ends every session of a run.
func (r *Registry) EndRun(run board.RunID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.sessions {
		if k.run == run {
			r.end(k)
		}
	}
}

// EndAll ends every session, as an instance that lost its lease does.
func (r *Registry) EndAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.sessions {
		r.end(k)
	}
}

func (r *Registry) end(k key) {
	l := r.sessions[k]
	if l == nil {
		return
	}
	delete(r.sessions, k)
	delete(r.byToken, l.mcp)
	delete(r.byToken, l.llm)
	close(l.done)
}

// lookup returns a copy of the session a token of gateway g names, once the
// session is in a round.
func (r *Registry) lookup(tok string, g tokens.Gateway) (live, bool) {
	h := tokens.Hash(tok)
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.byToken[h]
	if !ok || n.gateway != g || n.s.round == 0 {
		return live{}, false
	}
	return *n.s, true
}

// MCP returns the sessions as the MCP gateway looks them up.
func (r *Registry) MCP() actionsapp.Sessions { return mcpSessions{r} }

// LLM returns the sessions as the LLM gateway looks them up.
func (r *Registry) LLM() llmapp.Sessions { return llmSessions{r} }

type mcpSessions struct{ r *Registry }

func (m mcpSessions) Session(_ context.Context, tok string) (actionsapp.Session, error) {
	l, ok := m.r.lookup(tok, tokens.MCP)
	if !ok {
		return actionsapp.Session{}, actionsapp.ErrNoSession
	}
	return actionsapp.Session{
		Run: l.Run, Agent: l.Agent, Commit: l.Commit, Round: l.round, Deadline: l.deadline, Class: limits.Primary,
		Done: l.done, Item: maps.Clone(l.Item), Epoch: l.Epoch,
	}, nil
}

type llmSessions struct{ r *Registry }

func (m llmSessions) Session(_ context.Context, tok string) (llmapp.Session, error) {
	l, ok := m.r.lookup(tok, tokens.LLM)
	if !ok {
		return llmapp.Session{}, llmapp.ErrNoSession
	}
	return llmapp.Session{
		Run: string(l.Run), Agent: l.Agent, Commit: l.Commit, Round: l.round, Deadline: l.deadline, Class: cost.Primary,
		Budget: l.budget, Done: l.done, Epoch: l.Epoch,
	}, nil
}
