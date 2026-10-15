package check

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

// The board tools every agent has in a check.
const (
	postEvidence   = "post_evidence"
	postHypothesis = "post_hypothesis"
	endTurn        = "end_turn"
)

// readTool is a read-only tool of one of the case's servers.
type readTool struct {
	session *mcp.ClientSession
	server  string
}

// toolbox is one agent's tools in one run: the read-only tools of the case's
// MCP servers, each call recorded as a read call, and the board tools. The
// runner calls it from one goroutine.
type toolbox struct {
	agent        string
	run          board.RunID
	boards       *app.Boards
	calls        *memory.Calls
	maxOutput    int
	readTimeout  time.Duration
	sessions     []*mcp.ClientSession
	reads        map[string]readTool
	tools        []runner.Tool
	instructions []string
	n            int // read calls made
	report       *TurnReport
}

// openToolbox connects to the case's servers and lists their read-only tools.
// Tools that do not declare readOnlyHint are never offered.
func openToolbox(ctx context.Context, c *checker, cs *Case, run board.RunID, agent string, report *TurnReport, boards *app.Boards, calls *memory.Calls) (*toolbox, error) {
	tb := &toolbox{
		agent: agent, run: run, boards: boards, calls: calls, report: report,
		maxOutput: c.opts.MaxOutput, readTimeout: c.opts.ReadTimeout, reads: map[string]readTool{},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "agentswarm-check", Version: "1"}, nil)
	for _, url := range cs.Task.Servers {
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, MaxRetries: -1}, nil)
		if err != nil {
			tb.close()
			return nil, fmt.Errorf("connect to %s: %w", url, err)
		}
		tb.sessions = append(tb.sessions, session)
		if err := tb.addServer(ctx, session); err != nil {
			tb.close()
			return nil, fmt.Errorf("%s: %w", url, err)
		}
	}
	tb.tools = append(tb.tools, boardTools(c.set, cs)...)
	return tb, nil
}

// addServer lists a server's read-only tools and records the server.
func (tb *toolbox) addServer(ctx context.Context, session *mcp.ClientSession) error {
	info := session.InitializeResult()
	server := info.ServerInfo.Name
	if info.Instructions != "" {
		tb.instructions = append(tb.instructions, server+": "+info.Instructions)
	}
	digest := sha256.New()
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			continue
		}
		if _, dup := tb.reads[tool.Name]; dup || slices.Contains([]string{postEvidence, postHypothesis, endTurn}, tool.Name) {
			return fmt.Errorf("tool %s is offered twice", tool.Name)
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return fmt.Errorf("tool %s: input schema: %w", tool.Name, err)
		}
		tb.reads[tool.Name] = readTool{session: session, server: server}
		tb.tools = append(tb.tools, runner.Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
		_, _ = fmt.Fprintf(digest, "%q %q %s\n", tool.Name, tool.Description, schema)
	}
	tb.report.Servers = append(tb.report.Servers, Server{Name: server, Version: info.ServerInfo.Version, Tools: hex.EncodeToString(digest.Sum(nil))})
	return nil
}

func (tb *toolbox) close() {
	for _, s := range tb.sessions {
		_ = s.Close()
	}
}

// Tools lists the read-only tools and the board tools.
func (tb *toolbox) Tools(context.Context) ([]runner.Tool, error) { return tb.tools, nil }

// Call runs one tool call. A refused call goes back to the model as an error;
// err is for failures the turn cannot go on after, such as a lost server.
func (tb *toolbox) Call(ctx context.Context, name string, input json.RawMessage) (output string, isError bool, err error) {
	switch name {
	case postEvidence:
		return tb.postEvidence(ctx, input)
	case postHypothesis:
		return tb.postHypothesis(ctx, input)
	case endTurn:
		return "Your turn has ended.", false, nil
	}
	rt, ok := tb.reads[name]
	if !ok {
		return fmt.Sprintf("there is no tool named %q", name), true, nil
	}
	return tb.read(ctx, name, rt, input)
}

func (tb *toolbox) read(ctx context.Context, name string, rt readTool, input json.RawMessage) (output string, isError bool, err error) {
	tb.n++
	id := fmt.Sprintf("%s-c%d", tb.agent, tb.n)
	args := input
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	readCtx, cancel := context.WithTimeout(ctx, tb.readTimeout)
	defer cancel()
	res, err := rt.session.CallTool(readCtx, &mcp.CallToolParams{Name: name, Arguments: args})
	switch {
	case err != nil && ctx.Err() == nil && readCtx.Err() != nil:
		return "", false, fmt.Errorf("%s gave no answer in %s", name, tb.readTimeout)
	case err != nil:
		return "", false, fmt.Errorf("read %s: %w", name, err)
	}
	out, note := cut(resultText(res), tb.maxOutput)
	tb.calls.Add(&board.ReadCall{ID: id, Run: tb.run, Agent: tb.agent, Server: rt.server, Tool: name, Failed: res.IsError, Output: []byte(out)})
	tb.report.Reads++
	header := "call_id: " + id
	if res.IsError {
		tb.report.FailedReads++
		header += " (failed)"
	}
	return header + "\n" + out + note, res.IsError, nil
}

// resultText is a tool result's text content, or its structured content as
// JSON when it has no text.
func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			return string(raw)
		}
	}
	return strings.Join(parts, "\n")
}

// cut shortens an output to at most limit bytes, on a character boundary, and
// returns a note for the agent when it did. The note is not part of the
// output, so it cannot be quoted.
func cut(out string, limit int) (kept, note string) {
	if limit <= 0 || len(out) <= limit {
		return out, ""
	}
	n := limit
	for n > 0 && !utf8.RuneStart(out[n]) {
		n--
	}
	return out[:n], fmt.Sprintf("\n[output cut at %d of %d bytes]", n, len(out))
}

type evidenceArgs struct {
	Claim      string `json:"claim"`
	CallID     string `json:"call_id"`
	Quote      string `json:"quote"`
	Hypothesis string `json:"hypothesis"`
	Stance     string `json:"stance"`
}

func (tb *toolbox) postEvidence(ctx context.Context, input json.RawMessage) (output string, isError bool, err error) {
	var in evidenceArgs
	if err := json.Unmarshal(input, &in); err != nil {
		return tb.refuse(postEvidence, fmt.Errorf("invalid input: %w", err))
	}
	if !strings.HasPrefix(in.CallID, tb.agent+"-c") {
		// Another agent's call id gets the answer an unknown one gets, so that
		// citing it says nothing of the other agent's reads.
		return tb.refuse(postEvidence, fmt.Errorf("%w: no read call %q", board.ErrProvenance, in.CallID))
	}
	p, err := tb.boards.PostEvidence(ctx, tb.run, 1, tb.author(), &board.EvidenceInput{
		Claim: in.Claim, CallID: in.CallID, Quote: in.Quote,
		Hypothesis: board.EntryID(in.Hypothesis), Stance: board.Stance(in.Stance),
	})
	if err != nil {
		return tb.refuseRule(postEvidence, err)
	}
	if !p.Repeat {
		tb.report.Entries++
	}
	return "posted evidence " + string(p.Event.ID), false, nil
}

type hypothesisArgs struct {
	Service     string   `json:"service"`
	FailureMode string   `json:"failure_mode"`
	Statement   string   `json:"statement"`
	Evidence    []string `json:"evidence"`
}

// postHypothesis posts a hypothesis. Its answer never says whether it merged
// into another agent's: agents in a check never see each other's entries.
func (tb *toolbox) postHypothesis(ctx context.Context, input json.RawMessage) (output string, isError bool, err error) {
	var in hypothesisArgs
	if err := json.Unmarshal(input, &in); err != nil {
		return tb.refuse(postHypothesis, fmt.Errorf("invalid input: %w", err))
	}
	evidence := make([]board.EntryID, 0, len(in.Evidence))
	for _, id := range in.Evidence {
		evidence = append(evidence, board.EntryID(id))
	}
	p, err := tb.boards.PostHypothesis(ctx, tb.run, 1, tb.author(), &board.HypothesisInput{
		Key:       key.Key{ServiceField: in.Service, FailureModeField: in.FailureMode},
		Statement: in.Statement,
		Evidence:  evidence,
	})
	if err != nil {
		return tb.refuseRule(postHypothesis, err)
	}
	if !p.Repeat {
		tb.report.Entries++
	}
	return "posted hypothesis " + string(p.Event.ID), false, nil
}

func (tb *toolbox) author() board.Author {
	return board.Author{Kind: board.AuthorAgent, Name: tb.agent}
}

// refuseRule hands a broken board rule back to the agent. Any other error
// ends the turn.
func (tb *toolbox) refuseRule(tool string, err error) (output string, isError bool, _ error) {
	if errors.Is(err, board.ErrNotFound) {
		return tb.refuse(tool, fmt.Errorf("%w; cite the ids that post_evidence and post_hypothesis returned, not call_ids", err))
	}
	for _, rule := range []error{board.ErrInvalid, board.ErrNotAllowed, board.ErrProvenance, board.ErrReasonRequired, board.ErrLimit} {
		if errors.Is(err, rule) {
			return tb.refuse(tool, err)
		}
	}
	return "", false, fmt.Errorf("%s: %w", tool, err)
}

func (tb *toolbox) refuse(tool string, err error) (output string, isError bool, _ error) {
	tb.report.Refused = append(tb.report.Refused, tool+": "+err.Error())
	return "refused: " + err.Error(), true, nil
}

// boardTools are the board's tools, with the case's key values as choices.
func boardTools(set *Set, cs *Case) []runner.Tool {
	enum := func(values []string) []string { return append(slices.Clone(values), key.Other) }
	hypothesis := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"service":      map[string]any{"type": "string", "enum": enum(cs.Task.Services), "description": "The service where the cause lies."},
			"failure_mode": map[string]any{"type": "string", "enum": enum(set.FailureModeNames()), "description": "The kind of failure."},
			"statement":    map[string]any{"type": "string", "description": "The hypothesis, in one sentence."},
			"evidence":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The ids that post_evidence returned for your evidence that supports it; not call_ids."},
		},
		"required": []string{"service", "failure_mode", "statement", "evidence"},
	}
	evidence := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"claim":      map[string]any{"type": "string", "description": "What the read shows, in a sentence or two."},
			"call_id":    map[string]any{"type": "string", "description": "The call_id of the read the claim comes from."},
			"quote":      map[string]any{"type": "string", "description": "Text copied exactly from that read's output."},
			"hypothesis": map[string]any{"type": "string", "description": "Optional: the id that post_hypothesis returned for one of your hypotheses, to support or refute it with this evidence. Leave it out otherwise; a hypothesis cites its evidence when you post it."},
			"stance":     map[string]any{"type": "string", "enum": []string{string(board.Supports), string(board.Refutes)}, "description": "Only with hypothesis."},
		},
		"required": []string{"claim", "call_id", "quote"},
	}
	return []runner.Tool{
		{Name: postEvidence, Description: "Post one piece of evidence: a claim, the call_id of the read it comes from, and a quote copied exactly from that read's output. Returns the evidence's id.", InputSchema: mustJSON(evidence)},
		{Name: postHypothesis, Description: "Post a hypothesis about the cause, keyed by service and failure mode, citing the ids of your evidence. Returns the hypothesis's id.", InputSchema: mustJSON(hypothesis)},
		{Name: endTurn, Description: "End your turn.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// randomIDs issues short entry ids, which a model copies more reliably than
// the platform's UUIDs, in no order, so that they tell an agent nothing of the
// others' entries.
type randomIDs struct{}

func (randomIDs) NewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "e" + hex.EncodeToString(b[:])
}
