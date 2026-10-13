// Package llmgw stands in for the platform's LLM gateway (ADR-007) in the
// runtime spike. It records every request a runtime sends, and answers either
// from a script or by forwarding to the real API with a key that the sandbox
// never holds.
package llmgw

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
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
	// Log, when set, receives every record as one JSON line.
	Log io.Writer
}

// Gateway is an http.Handler.
type Gateway struct {
	opts    Options
	proxy   *httputil.ReverseProxy
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
	if g.opts.Upstream != nil {
		g.proxy = &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(g.opts.Upstream)
				r.Out.Header.Del("Authorization")
				r.Out.Header.Set("x-api-key", g.opts.APIKey)
			},
			FlushInterval: -1,
		}
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

	if g.proxy != nil {
		switch {
		case r.URL.Path == "/api/hello":
			w.WriteHeader(http.StatusOK)
		case forwardable(r):
			r.Body = io.NopCloser(bytes.NewReader(raw))
			r.ContentLength = int64(len(raw))
			g.proxy.ServeHTTP(w, r) //nolint:gosec // G704: only the Messages API of the configured upstream
		default:
			http.NotFound(w, r)
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
		Seq: len(g.records) + 1, Method: r.Method, Path: r.URL.Path,
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
// the real key: only the Messages API, as the LLM gateway's allowlist does
// (ADR-007).
func forwardable(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/messages/count_tokens")
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
