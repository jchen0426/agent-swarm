package app

import (
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// Session is an agent's session in a run, as the coordinator issues it.
type Session struct {
	Run   board.RunID
	Agent string
	// Commit is the configuration revision the item locked.
	Commit string
	// Item holds the item's trusted routing values (spec Section 3.3).
	Item map[string]string
}

// Tokens are a session's two tokens, one for each gateway.
type Tokens struct {
	MCP, LLM tokens.Secret
}

// Sessions holds the live sessions, which the gateways look up by token
// (spec Section 2).
type Sessions interface {
	// Issue gives an agent's session in a run two new tokens, which the
	// gateways know once Move puts the session in a round, and forgets the
	// session's earlier tokens. The session keeps its channel until it
	// ends.
	Issue(s *Session) Tokens
	// Move puts a session in a round, which counts from 1, until deadline,
	// in the process's clock, with the token budget the LLM gateway holds
	// the run to in that round.
	Move(run board.RunID, agent string, round int, deadline time.Time, budget int64) error
	// End ends an agent's session in a run, EndRun every session of a run,
	// and EndAll every session: their tokens are forgotten and their calls
	// end at once.
	End(run board.RunID, agent string)
	EndRun(run board.RunID)
	EndAll()
}
