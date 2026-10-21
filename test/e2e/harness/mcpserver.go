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
	"testing"
	"time"
)

// maxMCPBody caps the request body a scripted MCP server reads.
const maxMCPBody = 16 << 20

// mcpVersions are the protocol versions a scripted server speaks, newest
// first. It answers a client's version when it knows it.
var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const (
	// PinInstance is the instance a server files the harness's own listing of
	// its tools under: Config lists the tools through PinURL to pin them, as a
	// person does with registry hash-tools, which sends no credential. The name
	// cannot be an instance's, so no instance's requests are mistaken for it.
	PinInstance = "(pinning)"
	// pinPath is the path of that door, which the platform's endpoint, /mcp,
	// is not.
	pinPath = "/pin"
	// sessionHeader carries a session's id.
	sessionHeader = "Mcp-Session-Id"
)

// serverName is what a server's name may be: a registration's name, a DNS
// label.
var serverName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ServerSpec describes a scripted MCP server.
type ServerSpec struct {
	Name  string
	Tools []ToolSpec
	// Plain serves HTTP without TLS: a stand-in for the MCP gateway in the
	// conformance tests of the runtime, whose clients carry credentials that
	// no deployment wrote (see AcceptCredential). A plain server is not
	// registered in the configuration, which takes https endpoints only.
	Plain bool
}

// ToolSpec is a tool a scripted server lists.
type ToolSpec struct {
	Name        string
	Description string
	// InputSchema is the tool's input schema as JSON; when empty it is
	// {"type":"object"}.
	InputSchema string
	// Write, when set, registers the tool as a write tool, whose calls the
	// MCP gateway records as proposals for a person and never sends to the
	// server (spec Section 6.3). A tool without it is registered as a read
	// tool.
	Write *WriteSpec
}

// WriteSpec is a write tool's policy, as Config registers it.
type WriteSpec struct {
	// Scope binds each argument it names to a field of the cited
	// hypothesis's key: a call's argument must equal that field's value.
	Scope map[string]string
	// Summary is the text of the tool's proposals, without holes.
	Summary string
}

// MCPServer is a scripted MCP server. It speaks MCP's streamable HTTP with
// the standard library alone, not the SDK the platform uses, so that a fault
// of that SDK cannot hide behind the same SDK on the other side: initialize,
// notifications, ping, tools/list and tools/call, each answered with
// application/json; a GET answers 405 and a DELETE 200. It serves HTTPS with
// a certificate of the test CA, or HTTP when its spec says Plain.
//
// It tells the instances apart by the credential a request carries as a bearer
// token (see Credential and AcceptCredential), and keeps every request it
// receives. A request whose credential no instance owns, or that carries none,
// is refused with a 401, as a real server refuses it, and kept under no
// instance. Only the listing of the tools at PinURL needs none.
//
// It keeps sessions as a server with sessions does: initialize starts one, a
// request that names a session the server does not know is answered 404, a
// DELETE ends one, and a restart (Down, then Up) ends them all. A request that
// names no session is served, as the MCP gateway's endpoint serves one, which
// has none.
type MCPServer struct {
	t    testing.TB
	name string
	spec ServerSpec
	ep   *endpoint

	// scriptMu lets one call at a time into a script, so that a script may
	// keep counters without a lock of its own.
	scriptMu sync.Mutex

	mu       sync.Mutex
	scripts  map[string]func(*ToolCall) ToolReply
	creds    map[string]string // credential to instance
	byName   map[string]string // instance to credential
	sessions map[string]struct{}
	requests []MCPRequest
}

func newMCPServer(t testing.TB, spec *ServerSpec, ca *testCA) *MCPServer {
	t.Helper()
	for _, tool := range spec.Tools {
		if tool.InputSchema != "" && !json.Valid([]byte(tool.InputSchema)) {
			t.Fatalf("harness: the input schema of tool %s of server %s is not JSON", tool.Name, spec.Name)
		}
	}
	s := &MCPServer{
		t: t, name: spec.Name, spec: *spec,
		scripts: map[string]func(*ToolCall) ToolReply{}, creds: map[string]string{}, byName: map[string]string{},
		sessions: map[string]struct{}{},
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

// PinURL returns the address at which the tools can be listed with no
// credential, as registry hash-tools lists them. It answers initialize, ping,
// tools/list and the like, and refuses a tools/call; its requests are held
// under PinInstance.
func (s *MCPServer) PinURL() string { return s.ep.url(pinPath) }

// AcceptCredential makes credential one of the instance's credentials, besides
// the one Credential made: a request that carries it as a bearer token is
// served, and held under the instance. It is for a stand-in of the MCP
// gateway, whose clients carry credentials that no deployment wrote, such as
// the session tokens that a test of the runtime sends.
func (s *MCPServer) AcceptCredential(t *testing.T, instance, credential string) {
	t.Helper()
	if credential == "" {
		t.Fatal("harness: a server cannot accept an empty credential: it would accept a request that carries none")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[credential] = instance
}

// Credential returns the credential of an instance, which Deployment writes
// into the instance's secrets directory. A request that carries it as a
// bearer token is served, and held under the instance.
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
// A tool the server does not list fails the test passed to New.
func (s *MCPServer) Script(tool string, fn func(c *ToolCall) ToolReply) {
	s.t.Helper()
	if !s.lists(tool) {
		s.t.Fatalf("harness: server %s lists no tool %q", s.name, tool)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts[tool] = fn
}

// Down makes the server refuse connections, and ends the ones it has, and its
// sessions. The requests that were in flight get no answer.
func (s *MCPServer) Down() {
	s.ep.down()
	s.endSessions()
}

// Up makes the server serve again at its address, with no session: a client
// that kept one is told that it is not found. An address that cannot be bound
// again fails the test passed to New.
func (s *MCPServer) Up() {
	if s.ep.up() {
		s.endSessions()
	}
}

// Requests returns every request received with a credential of the instance,
// in the order they arrived. The requests that carried a credential no
// instance owns, or none, are those of the instance "".
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
	// Instance is the instance whose credential the request carried, or "",
	// or PinInstance.
	Instance string
	// At is when the request began, by the clock of the test process.
	At time.Time
	// Method and Path are the HTTP request's, and Query its query string, as
	// it came.
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
	// Session is the Mcp-Session-Id the request carried.
	Session string
	// ReadError is why the body could not be read to its end, if it could not;
	// Body then holds what arrived.
	ReadError string
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
	at := time.Now()
	body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBody))
	var msg rpcMessage
	parsed := json.Unmarshal(body, &msg)
	known := s.keep(r, at, body, readErr, &msg)
	switch {
	case readErr != nil:
		http.Error(w, "the request body could not be read", http.StatusBadRequest)
	case !known:
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "a valid credential is required", http.StatusUnauthorized)
	case !s.inSession(r):
		http.Error(w, "session not found", http.StatusNotFound)
	case r.Method == http.MethodPost:
		s.post(w, r, &msg, parsed)
	case r.Method == http.MethodDelete:
		s.endSession(r.Header.Get(sessionHeader))
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "this server starts no stream", http.StatusMethodNotAllowed)
	}
}

// instanceOf returns the instance whose credential the request carries, and
// whether there is one. A listing of the tools at the pin door has the
// instance PinInstance.
func (s *MCPServer) instanceOf(r *http.Request, method string) (instance string, known bool) {
	credential, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if instance, known = s.creds[credential]; known {
		return instance, true
	}
	if r.URL.Path == pinPath && method != "tools/call" {
		return PinInstance, true
	}
	return "", false
}

// keep records a request, whatever becomes of it: with its headers, the part of
// its body that arrived and why the rest did not, under the instance its
// credential names. It reports whether the credential is one that an instance
// owns.
func (s *MCPServer) keep(r *http.Request, at time.Time, body []byte, readErr error, msg *rpcMessage) (known bool) {
	rec := MCPRequest{
		At: at, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body,
		Session: r.Header.Get(sessionHeader), RPC: msg.Method,
	}
	if readErr != nil {
		rec.ReadError = readErr.Error()
	}
	if msg.Method == "tools/call" {
		rec.Tool = callParams(msg.Params).Name
	}
	rec.Instance, known = s.instanceOf(r, msg.Method)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, rec)
	return known
}

// inSession reports whether the request's session, if it names one, is one the
// server has.
func (s *MCPServer) inSession(r *http.Request) bool {
	id := r.Header.Get(sessionHeader)
	if id == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	return ok
}

func (s *MCPServer) startSession() string {
	id := randomHex(16)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = struct{}{}
	return id
}

func (s *MCPServer) endSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *MCPServer) endSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.sessions)
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
	w.Header().Set(sessionHeader, s.startSession())
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
	instance, _ := s.instanceOf(r, msg.Method)
	s.mu.Lock()
	script := s.scripts[params.Name]
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
