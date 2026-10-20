//go:build e2e

package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// maxMCPBody caps the request body a scripted MCP server reads.
const maxMCPBody = 16 << 20

// mcpVersions are the protocol versions a scripted server speaks, newest
// first. It answers a client's version when it knows it.
var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// serverName is what a server's name may be: a registration's name, a DNS
// label.
var serverName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ServerSpec describes a scripted MCP server.
type ServerSpec struct {
	Name  string
	Tools []ToolSpec
	// Plain serves HTTP without TLS: a stand-in for the MCP gateway in the
	// conformance tests of the runtime. A plain server is not registered in
	// the configuration, which takes https endpoints only.
	Plain bool
}

// ToolSpec is a tool a scripted server lists.
type ToolSpec struct {
	Name        string
	Description string
	// InputSchema is the tool's input schema as JSON; when empty it is
	// {"type":"object"}.
	InputSchema string
}

// MCPServer is a scripted MCP server. It speaks MCP's streamable HTTP with
// the standard library alone, not the SDK the platform uses, so that a fault
// of that SDK cannot hide behind the same SDK on the other side: initialize,
// notifications, ping, tools/list and tools/call, each answered with
// application/json; a GET answers 405 and a DELETE 200. It serves HTTPS with
// a certificate of the test CA, or HTTP when its spec says Plain.
//
// It tells the instances apart by the credential a request carries as a bearer
// token (see Credential), and keeps every request it receives.
type MCPServer struct {
	t    *testing.T
	name string
	spec ServerSpec
	ep   *endpoint

	// pinning counts the harness's own listings of the tools, which carry no
	// credential and are not kept.
	pinning atomic.Int32
	// scriptMu lets one call at a time into a script, so that a script may
	// keep counters without a lock of its own.
	scriptMu sync.Mutex

	mu       sync.Mutex
	scripts  map[string]func(*ToolCall) ToolReply
	creds    map[string]string // credential to instance
	byName   map[string]string // instance to credential
	requests []MCPRequest
}

func newMCPServer(t *testing.T, spec *ServerSpec, ca *testCA) *MCPServer {
	t.Helper()
	for _, tool := range spec.Tools {
		if tool.InputSchema != "" && !json.Valid([]byte(tool.InputSchema)) {
			t.Fatalf("harness: the input schema of tool %s of server %s is not JSON", tool.Name, spec.Name)
		}
	}
	s := &MCPServer{
		t: t, name: spec.Name, spec: *spec,
		scripts: map[string]func(*ToolCall) ToolReply{}, creds: map[string]string{}, byName: map[string]string{},
	}
	if !spec.Plain {
		config, err := ca.serverConfig()
		if err != nil {
			t.Fatalf("harness: the certificate of server %s: %v", spec.Name, err)
		}
		s.ep = newEndpoint(t, s, config)
		return s
	}
	s.ep = newEndpoint(t, s, nil)
	return s
}

// URL returns the server's MCP endpoint.
func (s *MCPServer) URL() string { return s.ep.url("/mcp") }

// Credential returns the credential of an instance, which Deployment writes
// into the instance's secrets directory. A request that carries it as a
// bearer token is held under the instance.
func (s *MCPServer) Credential(instance string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if credential, ok := s.byName[instance]; ok {
		return credential
	}
	credential := "mcp-credential-" + s.name + "-" + instance + "-" + randomHex(16)
	s.creds[credential], s.byName[instance] = instance, credential
	return credential
}

// Script sets the function that answers the calls of a tool from now on,
// replacing the one before it; a nil fn takes the script away. A tool without
// a script answers "ok". fn runs for one call at a time, so it may keep
// counters; it should delay an answer by ToolReply.After and not by sleeping.
func (s *MCPServer) Script(tool string, fn func(c *ToolCall) ToolReply) {
	s.t.Helper()
	if !s.lists(tool) {
		s.t.Fatalf("harness: server %s lists no tool %q", s.name, tool)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts[tool] = fn
}

// Down makes the server refuse connections, and ends the ones it has. The
// requests that were in flight get no answer.
func (s *MCPServer) Down() { s.ep.down() }

// Up makes the server serve again at its address.
func (s *MCPServer) Up() { s.ep.up() }

// Requests returns every request received with the credential of the
// instance, in the order they arrived. The requests that carried a credential
// no instance owns, or none, are those of the instance "".
func (s *MCPServer) Requests(instance string) []MCPRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []MCPRequest
	for i := range s.requests {
		if s.requests[i].Instance == instance {
			out = append(out, s.requests[i])
		}
	}
	return out
}

// All returns every request received, whichever credential it carried, in the
// order they arrived.
func (s *MCPServer) All() []MCPRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MCPRequest(nil), s.requests...)
}

// An MCPRequest is one HTTP request a scripted MCP server received.
type MCPRequest struct {
	// Instance is the instance whose credential the request carried, or "".
	Instance string
	// At is when the request arrived, by the clock of the test process.
	At time.Time
	// Method and Path are the HTTP request's.
	Method string
	Path   string
	Header http.Header
	Body   []byte
	// Session is the Mcp-Session-Id the request carried.
	Session string
	// RPC is the JSON-RPC method of a POST, such as tools/list, and Tool the
	// name of the tool a tools/call names.
	RPC  string
	Tool string
}

// A ToolCall is a tools/call a script answers.
type ToolCall struct {
	// Instance is the instance whose credential the call carried, or "".
	Instance  string
	Tool      string
	Arguments json.RawMessage
	Header    http.Header
	At        time.Time
}

// A ToolReply is how a scripted server answers one call: a result, an error
// result, a JSON-RPC error, an HTTP status, or a dropped connection, after a
// delay if it has one.
type ToolReply struct {
	// Text is the text of the result, or the message of the JSON-RPC error.
	Text string
	// IsError marks the result as an error result, which the model reads.
	IsError bool
	// Code, when it is not zero, makes the answer a JSON-RPC error with this
	// code and Text as its message.
	Code int
	// Status, when it is not zero, makes the answer this HTTP status with
	// no JSON-RPC message.
	Status int
	// Delay holds the answer back, whatever it is.
	Delay time.Duration
	// Drop closes the connection without an answer.
	Drop bool
}

// Result answers with a result of one text block.
func Result(text string) ToolReply { return ToolReply{Text: text} }

// ErrorResult answers with a result that is an error, isError, which the
// model reads: a tool that failed on the server's side.
func ErrorResult(text string) ToolReply { return ToolReply{Text: text, IsError: true} }

// RPCFailure answers with a JSON-RPC error.
func RPCFailure(code int, message string) ToolReply { return ToolReply{Code: code, Text: message} }

// HTTPFailure answers with an HTTP status and no JSON-RPC message.
func HTTPFailure(status int) ToolReply { return ToolReply{Status: status} }

// Dropped closes the connection without an answer.
func Dropped() ToolReply { return ToolReply{Drop: true} }

// After returns the reply held back for d.
func (r ToolReply) After(d time.Duration) ToolReply {
	r.Delay = d
	return r
}

func (s *MCPServer) lists(tool string) bool {
	return slices.ContainsFunc(s.spec.Tools, func(t ToolSpec) bool { return t.Name == tool })
}

// rpcMessage is a JSON-RPC message of a client.
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// rpcResponse is a JSON-RPC response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP implements http.Handler.
func (s *MCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBody))
	if err != nil {
		http.Error(w, "the request body could not be read", http.StatusBadRequest)
		return
	}
	var msg rpcMessage
	parsed := json.Unmarshal(body, &msg)
	s.keep(r, body, &msg)
	switch r.Method {
	case http.MethodPost:
		s.post(w, r, &msg, parsed)
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "this server starts no stream", http.StatusMethodNotAllowed)
	}
}

// keep records a request under the instance its credential names.
func (s *MCPServer) keep(r *http.Request, body []byte, msg *rpcMessage) {
	rec := MCPRequest{
		At: time.Now(), Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body,
		Session: r.Header.Get("Mcp-Session-Id"), RPC: msg.Method,
	}
	if msg.Method == "tools/call" {
		rec.Tool = callParams(msg.Params).Name
	}
	credential, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.Instance = s.creds[credential]
	if rec.Instance == "" && s.pinning.Load() > 0 {
		return
	}
	s.requests = append(s.requests, rec)
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func callParams(raw json.RawMessage) toolCallParams {
	var p toolCallParams
	_ = json.Unmarshal(raw, &p)
	return p
}

func (s *MCPServer) post(w http.ResponseWriter, r *http.Request, msg *rpcMessage, parsed error) {
	switch {
	case parsed != nil:
		writeRPCStatus(w, http.StatusBadRequest, nil, nil, &rpcError{Code: -32700, Message: "parse error"})
	case len(msg.ID) == 0:
		// A notification: accepted, with no body.
		w.WriteHeader(http.StatusAccepted)
	case msg.Method == "initialize":
		s.initialize(w, msg)
	case msg.Method == "ping":
		writeRPC(w, msg.ID, map[string]any{}, nil)
	case msg.Method == "tools/list":
		s.list(w, msg)
	case msg.Method == "tools/call":
		s.call(w, r, msg)
	default:
		writeRPC(w, msg.ID, nil, &rpcError{Code: -32601, Message: "method not found"})
	}
}

func (s *MCPServer) initialize(w http.ResponseWriter, msg *rpcMessage) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(msg.Params, &params)
	version := mcpVersions[0]
	if slices.Contains(mcpVersions, params.ProtocolVersion) {
		version = params.ProtocolVersion
	}
	w.Header().Set("Mcp-Session-Id", randomHex(16))
	writeRPC(w, msg.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": s.name, "version": "1"},
	}, nil)
}

func (s *MCPServer) list(w http.ResponseWriter, msg *rpcMessage) {
	tools := make([]map[string]any, 0, len(s.spec.Tools))
	for _, t := range s.spec.Tools {
		schema := t.InputSchema
		if schema == "" {
			schema = `{"type":"object"}`
		}
		tool := map[string]any{"name": t.Name, "inputSchema": json.RawMessage(schema)}
		if t.Description != "" {
			tool["description"] = t.Description
		}
		tools = append(tools, tool)
	}
	writeRPC(w, msg.ID, map[string]any{"tools": tools}, nil)
}

func (s *MCPServer) call(w http.ResponseWriter, r *http.Request, msg *rpcMessage) {
	params := callParams(msg.Params)
	if !s.lists(params.Name) {
		writeRPC(w, msg.ID, nil, &rpcError{Code: -32602, Message: "unknown tool: " + params.Name})
		return
	}
	credential, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	instance, script := s.creds[credential], s.scripts[params.Name]
	s.mu.Unlock()
	reply := Result("ok")
	if script != nil {
		reply = s.run(script, &ToolCall{Instance: instance, Tool: params.Name, Arguments: params.Arguments, Header: r.Header.Clone(), At: time.Now()})
	}
	if reply.Delay > 0 && !s.ep.wait(r, reply.Delay) {
		return
	}
	switch {
	case reply.Drop:
		dropConnection(w)
	case reply.Status != 0:
		http.Error(w, "scripted status", reply.Status)
	case reply.Code != 0:
		writeRPC(w, msg.ID, nil, &rpcError{Code: reply.Code, Message: reply.Text})
	default:
		result := map[string]any{"content": []map[string]any{{"type": "text", "text": reply.Text}}}
		if reply.IsError {
			result["isError"] = true
		}
		writeRPC(w, msg.ID, result, nil)
	}
}

// run calls a script, one call at a time.
func (s *MCPServer) run(script func(*ToolCall) ToolReply, call *ToolCall) ToolReply {
	s.scriptMu.Lock()
	defer s.scriptMu.Unlock()
	return script(call)
}

// writeRPC answers a JSON-RPC request with application/json and status 200.
func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, failure *rpcError) {
	writeRPCStatus(w, http.StatusOK, id, result, failure)
}

func writeRPCStatus(w http.ResponseWriter, status int, id json.RawMessage, result any, failure *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: failure})
	if err != nil {
		// A script's value could not be marshalled: say so in the answer.
		body = []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":%q}}`, id, "the harness could not marshal its answer: "+err.Error()))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
