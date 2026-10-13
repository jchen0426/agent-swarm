// Package llmgw stands in for the platform's LLM gateway (ADR-007) in the
// runtime spike. It records every request a runtime sends, and answers either
// from a script or by forwarding to the real API with a key that the sandbox
// never holds.
package llmgw

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// DummyKey is the only credential a sandbox holds. In forward mode the
// gateway replaces it with its own key.
const DummyKey = "sandbox-dummy-key"

// Turn is one scripted assistant reply: optional text, then an optional tool call.
type Turn struct {
	Text    string   `json:"text,omitempty"`
	ToolUse *ToolUse `json:"tool_use,omitempty"`
}

// ToolUse names a tool by its bare name. The gateway matches it against the
// request's tools, so "end_turn" also matches "mcp__gw__end_turn".
type ToolUse struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Record is one request the gateway received.
type Record struct {
	Seq       int             `json:"seq"`
	Time      time.Time       `json:"time"` // when the request arrived
	Method    string          `json:"method"`
	Path      string          `json:"path"`
	KeyHeader string          `json:"key_header"` // "dummy", "other" or "none"
	AgentLoop bool            `json:"agent_loop"` // the request offered the end tool
	Body      json.RawMessage `json:"body,omitempty"`
}

// Options configure a Gateway.
type Options struct {
	// Script holds the replies to agent-loop requests, in order. Requests that
	// do not offer EndTool get a plain text reply and do not consume it.
	Script []Turn
	// EndTool marks agent-loop requests. The default is "end_turn".
	EndTool string
	// Upstream selects forward mode: Messages API requests go to the real API
	// with APIKey, and nothing else is forwarded.
	Upstream *url.URL
	APIKey   string `json:"-"`
	// Models, in forward mode, lists the models a request may name. Empty
	// allows any.
	Models []string
	// RetryBackoff, in forward mode, sets the waits between attempts when the
	// API is overloaded, rate-limited or failing. The default is 1 s, then 2 s.
	RetryBackoff []time.Duration
	// Log, when set, receives every record as one JSON line.
	Log io.Writer
}

// Gateway is an http.Handler.
type Gateway struct {
	opts    Options
	client  *http.Client
	mu      sync.Mutex
	records []Record
	next    int
}

// New returns a gateway in scripted mode, or in forward mode when
// opts.Upstream is set.
func New(opts *Options) *Gateway {
	g := &Gateway{opts: *opts}
	if g.opts.EndTool == "" {
		g.opts.EndTool = "end_turn"
	}
	if g.opts.RetryBackoff == nil {
		g.opts.RetryBackoff = []time.Duration{time.Second, 2 * time.Second}
	}
	if g.opts.Upstream != nil {
		g.client = &http.Client{}
	}
	return g
}

// Records returns a copy of the requests received so far.
func (g *Gateway) Records() []Record {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Record(nil), g.records...)
}

type request struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
	Tools  []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		_, _ = io.WriteString(w, "ok")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := decode(r.Header, raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req request
	_ = json.Unmarshal(body, &req)
	tools := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, t.Name)
	}
	agentLoop := resolve(g.opts.EndTool, tools) != ""
	seq := g.record(r, body, agentLoop)

	if g.client != nil {
		switch {
		case r.URL.Path == "/api/hello":
			w.WriteHeader(http.StatusOK)
		case !forwardable(r):
			http.NotFound(w, r)
		default:
			checked, err := g.allowed(body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			g.forward(w, r, checked)
		}
		return
	}
	switch r.URL.Path {
	case "/v1/messages":
		blocks, stop := g.reply(seq, agentLoop, tools)
		if req.Stream {
			writeStream(w, seq, req.Model, len(body)/4, blocks, stop)
			return
		}
		writeJSON(w, map[string]any{
			"id": fmt.Sprintf("msg_spike_%d", seq), "type": "message", "role": "assistant",
			"model": req.Model, "content": blocks, "stop_reason": stop, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": len(body) / 4, "output_tokens": 10},
		})
	case "/v1/messages/count_tokens":
		writeJSON(w, map[string]int{"input_tokens": len(body) / 4})
	case "/api/hello":
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (g *Gateway) record(r *http.Request, body []byte, agentLoop bool) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec := Record{
		Seq: len(g.records) + 1, Time: time.Now(), Method: r.Method, Path: r.URL.Path,
		KeyHeader: keyKind(r.Header), AgentLoop: agentLoop,
	}
	if json.Valid(body) {
		rec.Body = body
	}
	g.records = append(g.records, rec)
	if g.opts.Log != nil {
		line, err := json.Marshal(rec)
		if err == nil {
			_, _ = g.opts.Log.Write(append(line, '\n'))
		}
	}
	return rec.Seq
}

// forwardable reports whether forward mode may send a request upstream with
// the real key: only the Messages API.
func forwardable(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/messages/count_tokens")
}

// sensitive are the top-level fields the checks below read, plus one the vendor
// runs code in. A key that differs from one of them only in case is refused,
// since another parser might read it as the field itself.
var sensitive = []string{"model", "messages", "system", "tools", "mcp_servers", "container"}

// allowed refuses what would make the vendor run or fetch something for the
// sandbox: server tools, MCP connectors, code execution containers and content
// from any source that is not inline. It also holds requests to the allowed
// models. It returns the request re-encoded from the one reading it checked, so
// the vendor receives exactly that: duplicate keys keep their last value, and
// nothing that another JSON parser might read differently gets through.
//
// It is a minimal stand-in for the LLM gateway's allowlist (ADR-007, spec
// Section 9.3), which Plan 6 builds; it does not check beta headers,
// max_tokens or unknown fields.
func (g *Gateway) allowed(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var req map[string]any
	if err := dec.Decode(&req); err != nil {
		return nil, fmt.Errorf("unreadable request: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the request")
	}
	for key := range req {
		for _, name := range sensitive {
			if strings.EqualFold(key, name) && key != name {
				return nil, fmt.Errorf("field %q is ambiguous", key)
			}
		}
	}
	model, _ := req["model"].(string)
	if len(g.opts.Models) > 0 && !slices.Contains(g.opts.Models, model) {
		return nil, fmt.Errorf("model %q is not allowed", model)
	}
	for _, key := range []string{"mcp_servers", "container"} {
		if _, ok := req[key]; ok {
			return nil, fmt.Errorf("%s is not allowed", key)
		}
	}
	if err := customToolsOnly(req["tools"]); err != nil {
		return nil, err
	}
	for _, key := range []string{"messages", "system"} {
		if src := remoteSource(req[key]); src != "" {
			return nil, fmt.Errorf("content from a %s source is not allowed", src)
		}
	}
	return json.Marshal(req)
}

// customToolsOnly accepts no tools, or a list of tools that each have no type
// or the type "custom". Anything else is a tool the vendor would run.
func customToolsOnly(tools any) error {
	if tools == nil {
		return nil
	}
	list, ok := tools.([]any)
	if !ok {
		return errors.New("tools must be a list")
	}
	for _, tool := range list {
		obj, ok := tool.(map[string]any)
		if !ok {
			return errors.New("a tool must be an object")
		}
		if typ, ok := obj["type"]; ok && typ != "custom" {
			return fmt.Errorf("tool type %v is not allowed", typ)
		}
	}
	return nil
}

// remoteSource walks a JSON value and returns the kind of the first "source"
// that the vendor would fetch, or "". Only inline sources pass: base64, text,
// and content, whose own blocks are walked too.
func remoteSource(v any) string {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			if src := remoteSource(item); src != "" {
				return src
			}
		}
	case map[string]any:
		if src, ok := x["source"]; ok {
			obj, ok := src.(map[string]any)
			if !ok {
				return "non-object"
			}
			switch typ, _ := obj["type"].(string); typ {
			case "base64", "text", "content":
			case "":
				return "untyped"
			default:
				return typ
			}
		}
		for _, child := range x {
			if src := remoteSource(child); src != "" {
				return src
			}
		}
	}
	return ""
}

// retryable reports whether an upstream status is worth another attempt.
func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// forward sends a request upstream with the real key, retries overload and
// rate limits after the configured waits, and streams the reply back.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, raw []byte) {
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		var err error
		if resp, err = g.send(r, raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if !retryable(resp.StatusCode) || attempt == len(g.opts.RetryBackoff) {
			break
		}
		_ = resp.Body.Close()
		select {
		case <-time.After(g.opts.RetryBackoff[attempt]):
		case <-r.Context().Done():
			return
		}
	}
	defer func() { _ = resp.Body.Close() }()
	copyReply(w, resp)
}

// send makes one upstream attempt with the checked, decoded body: the client's
// headers, less its credentials, its body encoding and hop-by-hop headers, plus
// the real key.
func (g *Gateway) send(r *http.Request, raw []byte) (*http.Response, error) {
	target := g.opts.Upstream.JoinPath(r.URL.Path)
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	for name, values := range r.Header {
		switch http.CanonicalHeaderKey(name) {
		case "Authorization", "X-Api-Key", "Content-Length", "Content-Encoding", "Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade":
		default:
			req.Header[name] = values
		}
	}
	req.Header.Set("x-api-key", g.opts.APIKey)
	return g.client.Do(req) //nolint:gosec // G704: only the Messages API of the configured upstream
}

// copyReply streams an upstream reply to the client, flushing as it goes so
// that server-sent events arrive as they are produced.
func copyReply(w http.ResponseWriter, resp *http.Response) {
	for name, values := range resp.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// decode undoes a gzip Content-Encoding, so that compressed bodies are
// recorded and searched like any other.
func decode(h http.Header, raw []byte) ([]byte, error) {
	if !strings.EqualFold(h.Get("Content-Encoding"), "gzip") {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gzip body: %w", err)
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(io.LimitReader(zr, 64<<20))
}

func keyKind(h http.Header) string {
	key := h.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
	}
	switch key {
	case "":
		return "none"
	case DummyKey:
		return "dummy"
	default:
		return "other"
	}
}

// resolve returns the request's tool that a scripted bare name refers to, or "".
func resolve(name string, tools []string) string {
	for _, t := range tools {
		if t == name || strings.HasSuffix(t, "__"+name) {
			return t
		}
	}
	return ""
}

type block = map[string]any

func (g *Gateway) reply(seq int, agentLoop bool, tools []string) (blocks []block, stop string) {
	if !agentLoop {
		return []block{{"type": "text", "text": "ok"}}, "end_turn"
	}
	g.mu.Lock()
	if g.next >= len(g.opts.Script) {
		g.mu.Unlock()
		return []block{{"type": "text", "text": "script exhausted"}}, "end_turn"
	}
	turn := g.opts.Script[g.next]
	g.next++
	g.mu.Unlock()

	if turn.Text != "" {
		blocks = append(blocks, block{"type": "text", "text": turn.Text})
	}
	if turn.ToolUse == nil {
		return blocks, "end_turn"
	}
	name := resolve(turn.ToolUse.Name, tools)
	if name == "" {
		name = turn.ToolUse.Name
	}
	input := turn.ToolUse.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	blocks = append(blocks, block{
		"type": "tool_use", "id": fmt.Sprintf("toolu_spike_%d", seq), "name": name, "input": input,
	})
	return blocks, "tool_use"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeStream sends the reply as Messages API server-sent events.
func writeStream(w http.ResponseWriter, seq int, model string, inputTokens int, blocks []block, stop string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	send := func(event string, data any) {
		payload, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send("message_start", block{"type": "message_start", "message": block{
		"id": fmt.Sprintf("msg_spike_%d", seq), "type": "message", "role": "assistant", "model": model,
		"content": []block{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": inputTokens, "output_tokens": 1},
	}})
	for i, b := range blocks {
		switch b["type"] {
		case "text":
			send("content_block_start", block{"type": "content_block_start", "index": i, "content_block": block{"type": "text", "text": ""}})
			send("content_block_delta", block{"type": "content_block_delta", "index": i, "delta": block{"type": "text_delta", "text": b["text"]}})
		case "tool_use":
			send("content_block_start", block{"type": "content_block_start", "index": i, "content_block": block{
				"type": "tool_use", "id": b["id"], "name": b["name"], "input": block{},
			}})
			send("content_block_delta", block{"type": "content_block_delta", "index": i, "delta": block{
				"type": "input_json_delta", "partial_json": string(b["input"].(json.RawMessage)),
			}})
		}
		send("content_block_stop", block{"type": "content_block_stop", "index": i})
	}
	send("message_delta", block{"type": "message_delta", "delta": block{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 10}})
	send("message_stop", block{"type": "message_stop"})
}
