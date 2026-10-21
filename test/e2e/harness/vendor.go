//go:build e2e

package harness

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// maxVendorBody caps the request body the scripted vendor reads.
const maxVendorBody = 16 << 20

// defaultModel names the model an answer says it came from when the request
// does not name one.
const defaultModel = "claude-sonnet-5-5"

// Vendor is the scripted model vendor: the Messages API on a loopback port,
// plain HTTP, without streaming. It tells the instances apart by the key a
// request carries in x-api-key (see Key and AcceptKey), and keeps every request
// it receives. A request whose key no instance owns, or that carries none, is
// refused as the vendor refuses one, with a 401, and is kept under no instance;
// any other the script answers.
type Vendor struct {
	ep *endpoint

	// scriptMu lets one request at a time into the script, so that a script
	// may keep counters without a lock of its own.
	scriptMu sync.Mutex

	mu       sync.Mutex
	script   func(*VendorRequest) VendorReply
	keys     map[string]string // key to instance
	byName   map[string]string // instance to key
	requests []VendorRequest
	answers  int
}

func newVendor(t testing.TB) *Vendor {
	t.Helper()
	v := &Vendor{keys: map[string]string{}, byName: map[string]string{}}
	v.ep = newEndpoint(t, v, nil)
	return v
}

// URL returns the vendor's address, which a deployment names as vendor.baseURL.
func (v *Vendor) URL() string { return v.ep.url("") }

// Key returns the vendor key of an instance, which Deployment writes into the
// instance's secrets directory. A request that carries it is held under the
// instance.
func (v *Vendor) Key(instance string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key, ok := v.byName[instance]; ok {
		return key
	}
	key := "vendor-key-" + instance + "-" + randomHex(16)
	v.keys[key], v.byName[instance] = instance, key
	return key
}

// AcceptKey makes key one of the instance's keys, besides the one Key made: a
// request that carries it is answered, and held under the instance. It is for a
// stand-in of the LLM gateway, whose clients carry keys that no deployment
// wrote, such as the session tokens that a test of the runtime sends.
func (v *Vendor) AcceptKey(t *testing.T, instance, key string) {
	t.Helper()
	if key == "" {
		t.Fatal("harness: the vendor cannot accept an empty key: it would accept a request that carries none")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys[key] = instance
}

// Script sets the function that answers every request from now on, replacing
// the one before it; a nil fn takes the script away. Without a script the
// vendor answers every request by calling the board's end_turn tool. fn runs
// for one request at a time, so it may keep counters; it must not modify the
// request, and it should delay an answer by VendorReply.After and not by
// sleeping.
func (v *Vendor) Script(fn func(r *VendorRequest) VendorReply) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.script = fn
}

// Requests returns every request received with a key of the instance, in the
// order they arrived. The requests that carried a key no instance owns, or
// none, are those of the instance "".
func (v *Vendor) Requests(instance string) []VendorRequest {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []VendorRequest
	for i := range v.requests {
		if v.requests[i].Instance == instance {
			out = append(out, v.requests[i])
		}
	}
	return out
}

// All returns every request received, whichever key it carried, in the order
// they arrived.
func (v *Vendor) All() []VendorRequest {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]VendorRequest(nil), v.requests...)
}

// A VendorRequest is one request the scripted vendor received. Header and Body
// are the request as it arrived; the other fields are what a Messages API
// body holds, read leniently: a body that is not one leaves them empty.
type VendorRequest struct {
	// Instance is the instance whose key the request carried, or "".
	Instance string
	// At is when the request arrived, by the clock of the test process.
	At     time.Time
	Header http.Header
	Body   []byte
	// Method and Path are the HTTP request's, and Query its query string, as
	// it came.
	Method string
	Path   string
	Query  string
	// ReadError is why the body could not be read to its end, if it could not;
	// Body then holds what arrived.
	ReadError string

	Model     string
	MaxTokens int64
	// System is the text of the system blocks, one per line.
	System string
	// Agent is the agent that the line "You are <name>." of the system text
	// names: ConfigSpec writes that line into each agent's skill.
	Agent string
	// Tools holds the names of the tools the request offers.
	Tools    []string
	Messages []VendorMessage
}

// A VendorMessage is one message of a conversation.
type VendorMessage struct {
	Role   string
	Blocks []VendorBlock
}

// A VendorBlock is one content block. A message whose content is text has one
// text block.
type VendorBlock struct {
	// Type is text, tool_use, tool_result, thinking or redacted_thinking.
	Type string
	// Text is the text of a text block, and the text of a tool result.
	Text string
	// ID and Name are those of a tool_use block, and Input its input.
	ID    string
	Name  string
	Input json.RawMessage
	// ToolUseID and IsError belong to a tool_result block.
	ToolUseID string
	IsError   bool
}

// agentLine is the line of a skill that names its agent.
var agentLine = regexp.MustCompile(`(?m)^You are ([a-z0-9][a-z0-9-]*)\.[ \t]*\r?$`)

// messagesBody is the part of a Messages API request that VendorRequest reads.
type messagesBody struct {
	Model     string          `json:"model"`
	MaxTokens int64           `json:"max_tokens"`
	System    json.RawMessage `json:"system"`
	Messages  []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

// contentBlock is a content block as a request holds one.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// parseVendorRequest reads what it can of a Messages API body.
func parseVendorRequest(body []byte) *VendorRequest {
	var b messagesBody
	req := &VendorRequest{}
	if json.Unmarshal(body, &b) != nil {
		return req
	}
	req.Model, req.MaxTokens = b.Model, b.MaxTokens
	req.System = joinText(b.System)
	if m := agentLine.FindStringSubmatch(req.System); m != nil {
		req.Agent = m[1]
	}
	for _, tool := range b.Tools {
		req.Tools = append(req.Tools, tool.Name)
	}
	for _, m := range b.Messages {
		req.Messages = append(req.Messages, VendorMessage{Role: m.Role, Blocks: parseBlocks(m.Content)})
	}
	return req
}

// joinText returns the text of a content that is a string or a list of blocks,
// the text blocks one per line.
func joinText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var lines []string
	for i := range blocks {
		if blocks[i].Type == "text" {
			lines = append(lines, blocks[i].Text)
		}
	}
	return strings.Join(lines, "\n")
}

func parseBlocks(raw json.RawMessage) []VendorBlock {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []VendorBlock{{Type: "text", Text: text}}
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	out := make([]VendorBlock, 0, len(blocks))
	for i := range blocks {
		b := &blocks[i]
		block := VendorBlock{Type: b.Type, Text: b.Text, ID: b.ID, Name: b.Name, Input: b.Input, ToolUseID: b.ToolUseID, IsError: b.IsError}
		if b.Type == "tool_result" {
			block.Text = joinText(b.Content)
		}
		out = append(out, block)
	}
	return out
}

// ServeHTTP implements http.Handler.
func (v *Vendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	at := time.Now()
	body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxVendorBody))
	req, known := v.keep(r, at, body, readErr)
	switch {
	case readErr != nil:
		writeVendorError(w, http.StatusBadRequest, "invalid_request_error", "the request body could not be read")
	case r.Method != http.MethodPost || r.URL.Path != "/v1/messages":
		writeVendorError(w, http.StatusNotFound, "not_found_error", "no such endpoint: "+r.Method+" "+r.URL.Path)
	case !known:
		message := "invalid x-api-key"
		if r.Header.Get("X-Api-Key") == "" {
			message = "x-api-key header is required"
		}
		writeVendorError(w, http.StatusUnauthorized, "authentication_error", message)
	default:
		v.answerWith(w, r, req)
	}
}

// answerWith answers a request as the script says.
func (v *Vendor) answerWith(w http.ResponseWriter, r *http.Request, req *VendorRequest) {
	reply := v.decide(req)
	if reply.Delay > 0 && !v.ep.wait(r, reply.Delay) {
		return
	}
	switch {
	case reply.Hang:
		v.ep.hang(r)
		dropConnection(w)
	case reply.Drop:
		dropConnection(w)
	default:
		v.write(w, req, &reply)
	}
}

// keep records a request, whatever becomes of it: with its headers, the part of
// its body that arrived and why the rest did not, under the instance its key
// names. It returns the copy that the script sees, and whether the key is one
// that an instance owns.
func (v *Vendor) keep(r *http.Request, at time.Time, body []byte, readErr error) (req *VendorRequest, known bool) {
	req = parseVendorRequest(body)
	req.At, req.Header, req.Body, req.Method, req.Path, req.Query = at, r.Header.Clone(), body, r.Method, r.URL.Path, r.URL.RawQuery
	if readErr != nil {
		req.ReadError = readErr.Error()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	req.Instance, known = v.keys[r.Header.Get("X-Api-Key")]
	v.requests = append(v.requests, *req)
	return req, known
}

func (v *Vendor) decide(req *VendorRequest) VendorReply {
	v.mu.Lock()
	script := v.script
	v.mu.Unlock()
	if script == nil {
		return EndTurn()
	}
	v.scriptMu.Lock()
	defer v.scriptMu.Unlock()
	return script(req)
}

func writeVendorError(w http.ResponseWriter, status int, kind, message string) {
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// write sends a reply that answers: a status and a body as they are, or a
// Messages API answer built from the reply's blocks.
func (v *Vendor) write(w http.ResponseWriter, req *VendorRequest, reply *VendorReply) {
	status, body := reply.Status, []byte(reply.Body)
	if status == 0 && reply.Body == "" {
		var err error
		if body, err = v.answer(req, reply); err != nil {
			// A script's value that cannot be written is the test's bug, not the
			// vendor's failure: the test must not read it as one.
			v.ep.report("harness: the vendor could not write a scripted answer: %v", err)
			writeVendorError(w, http.StatusInternalServerError, "api_error", "the harness could not write its scripted answer")
			return
		}
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body) //nolint:gosec // G705: a scripted JSON answer to an API client, never a page.
}

// answer builds the body of a Messages API answer.
func (v *Vendor) answer(req *VendorRequest, reply *VendorReply) ([]byte, error) {
	v.mu.Lock()
	v.answers++
	n := v.answers
	v.mu.Unlock()

	stop := reply.Stop
	content := make([]map[string]any, 0, len(reply.Blocks))
	for i, block := range reply.Blocks {
		out := make(map[string]any, len(block)+1)
		for k, val := range block {
			out[k] = val
		}
		if out["type"] == "tool_use" {
			if _, ok := out["id"]; !ok {
				out["id"] = "toolu_" + strconv.Itoa(n) + "_" + strconv.Itoa(i)
			}
			if out["input"] == nil {
				out["input"] = map[string]any{}
			}
			if stop == "" {
				stop = "tool_use"
			}
		}
		content = append(content, out)
	}
	if stop == "" {
		stop = "end_turn"
	}
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	usage := Usage(100, 20)
	if reply.Tokens != nil {
		usage = *reply.Tokens
	}
	return json.Marshal(map[string]any{
		"id": "msg_harness_" + strconv.Itoa(n), "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stop, "stop_sequence": nil, "usage": usage.wire(),
	})
}

// A VendorReply is how the scripted vendor answers one request. Text, ToolUse
// and EndTurn make a Messages API answer; Status an answer that is not one;
// Hang and Disconnect no answer at all.
type VendorReply struct {
	// Status is the HTTP status. With Status or Body set, the vendor sends
	// Body as the body as it is, with status 200 when Status is zero. With
	// neither, it sends a Messages API answer with status 200.
	Status int
	Body   string
	// Blocks are the content of a Messages API answer. Text and ToolUse
	// make them.
	Blocks []map[string]any
	// Stop is the answer's stop_reason. When empty it is tool_use if an
	// answer's block calls a tool, and end_turn if none does.
	Stop string
	// Tokens is the usage the answer reports; when nil it is Usage(100, 20).
	Tokens *TokenUsage
	// Delay holds the answer back, whatever it is.
	Delay time.Duration
	// Hang never answers: the request is held until the client gives up or
	// the test ends. Drop closes the connection without an answer.
	Hang bool
	Drop bool
}

// Text answers with one text block: a model that answers without calling a
// tool.
func Text(text string) VendorReply {
	return VendorReply{Blocks: []map[string]any{{"type": "text", "text": text}}}
}

// ToolUse answers with a call of the named tool. input is marshalled to JSON,
// as it is when it is a json.RawMessage; nil is an empty object.
func ToolUse(name string, input any) VendorReply {
	return VendorReply{Blocks: []map[string]any{{"type": "tool_use", "name": name, "input": input}}}
}

// EndTurn answers with a call of the board's end_turn tool, by which an agent
// ends its turn.
func EndTurn() VendorReply { return ToolUse("end_turn", nil) }

// Status answers with the status and the body as they are: a failure of the
// vendor, or an answer the runtime cannot read.
func Status(code int, body string) VendorReply { return VendorReply{Status: code, Body: body} }

// Hang never answers.
func Hang() VendorReply { return VendorReply{Hang: true} }

// Disconnect closes the connection without an answer.
func Disconnect() VendorReply { return VendorReply{Drop: true} }

// After returns the reply held back for d.
//
//nolint:gocritic // hugeParam: a reply is a value that scripts build and return.
func (r VendorReply) After(d time.Duration) VendorReply {
	r.Delay = d
	return r
}

// WithUsage returns the reply reporting the usage.
//
//nolint:gocritic // hugeParam: a reply is a value that scripts build and return.
func (r VendorReply) WithUsage(u TokenUsage) VendorReply {
	r.Tokens = &u
	return r
}

// TokenUsage is the tokens an answer reports, as the vendor counts them.
type TokenUsage struct {
	Input, Output int64
}

// Usage returns the usage of input and output tokens.
func Usage(input, output int64) TokenUsage { return TokenUsage{Input: input, Output: output} }

// wire returns the usage object of a Messages API answer.
func (u TokenUsage) wire() map[string]any {
	return map[string]any{"input_tokens": u.Input, "output_tokens": u.Output}
}
