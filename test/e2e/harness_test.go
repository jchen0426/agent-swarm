//go:build e2e

package e2e

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// pgSSLRequest is the first message a Postgres client may send: its length, 8,
// and the code 80877103, which asks whether the server speaks TLS. A server
// answers one byte, S or N.
var pgSSLRequest = []byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}

// answers reports whether the Postgres at addr answers an SSLRequest.
func answers(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(pgSSLRequest); err != nil {
		return false
	}
	var reply [1]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return false
	}
	return reply[0] == 'S' || reply[0] == 'N'
}

// hostPort returns the address a database URL names.
func hostPort(t *testing.T, databaseURL string) string {
	t.Helper()
	u, err := url.Parse(databaseURL)
	require.NoError(t, err)
	require.NotEmpty(t, u.Host)
	return u.Host
}

// docker runs the docker command line and returns what it prints.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", args...).Output()
	require.NoError(t, err, "docker %s", strings.Join(args, " "))
	return string(out)
}

func containerLabels(t *testing.T, id string) map[string]string {
	t.Helper()
	require.NotEmpty(t, id)
	var labels map[string]string
	require.NoError(t, json.Unmarshal([]byte(docker(t, "container", "inspect", "--format", "{{json .Config.Labels}}", id)), &labels))
	return labels
}

// containerNetworks returns the names of the networks a container is on.
func containerNetworks(t *testing.T, id string) []string {
	t.Helper()
	var networks map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(docker(t, "container", "inspect", "--format", "{{json .NetworkSettings.Networks}}", id)), &networks))
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// projectContainers returns the ids of the running containers that carry the
// project label.
func projectContainers(t *testing.T) []string {
	t.Helper()
	ids := strings.Fields(docker(t, "ps", "--quiet", "--filter", "label=agentswarm.project=agent-swarm"))
	slices.Sort(ids)
	return ids
}

func requireGone(t *testing.T, id string) {
	t.Helper()
	require.NotEmpty(t, id)
	err := exec.CommandContext(t.Context(), "docker", "container", "inspect", id).Run()
	require.Error(t, err, "container %s is still there", id)
}

func TestTheHarnessContainersCarryTheProjectLabel(t *testing.T) {
	var postgresID, toxiproxyID string
	t.Run("harness", func(t *testing.T) {
		env := harness.New(t, harness.Options{Toxiproxy: true})
		postgresID, toxiproxyID = env.PostgresID(), env.ToxiproxyID()

		postgres := containerLabels(t, postgresID)
		assert.Equal(t, "agent-swarm", postgres["agentswarm.project"])
		assert.Equal(t, "/var/lib/postgresql/data", postgres["agentswarm.data"])
		proxy := containerLabels(t, toxiproxyID)
		assert.Equal(t, "agent-swarm", proxy["agentswarm.project"])
		assert.NotContains(t, proxy, "agentswarm.data", "a toxiproxy holds no data worth keeping")

		networks := containerNetworks(t, postgresID)
		require.Len(t, networks, 1)
		assert.Equal(t, networks, containerNetworks(t, toxiproxyID), "Postgres and the toxiproxy share one network")
	})

	// The subtest's cleanup removed both containers.
	require.NotEmpty(t, postgresID)
	requireGone(t, postgresID)
	requireGone(t, toxiproxyID)
}

func TestTheToxiproxyCutsAndRestoresAnInstancesDatabase(t *testing.T) {
	env := harness.New(t, harness.Options{Toxiproxy: true})
	a, b := hostPort(t, env.DatabaseURL("a")), hostPort(t, env.DatabaseURL("b"))
	require.NotEqual(t, a, b, "each instance has a proxy of its own")
	require.NotEqual(t, a, hostPort(t, env.DatabaseURL("")), "an instance does not reach the database directly")
	require.True(t, answers(a), "the SSLRequest through a's proxy")
	require.True(t, answers(b), "the SSLRequest through b's proxy")

	env.Cut(t, "a")

	assert.False(t, answers(a), "after Cut a's proxy answers nothing")
	assert.True(t, answers(b), "b's proxy is not cut")
	assert.True(t, answers(hostPort(t, env.DatabaseURL(""))), "the database itself is not cut")

	env.Restore(t, "a")

	assert.True(t, answers(a), "after Restore a's proxy answers again")
}

// messagesRequest is a Messages API request of the agent's, as the runtime
// sends one: the agent's skill is a system block.
func messagesRequest(agent string) string {
	return `{"model":"claude-sonnet-5-5","max_tokens":256,` +
		`"system":[{"type":"text","text":"# ` + agent + `\n\nYou are ` + agent + `.\n\nRead the board.","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":"Begin."}],` +
		`"tools":[{"name":"read_board","description":"Read the board.","input_schema":{"type":"object"}},` +
		`{"name":"end_turn","description":"End the turn.","input_schema":{"type":"object"}}]}`
}

// postMessages posts a request to the scripted vendor with an API key.
func postMessages(t *testing.T, client *http.Client, base, key, body string) (resp *http.Response, answer []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/v1/messages", strings.NewReader(body))
	require.NoError(t, err)
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err = client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	answer, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, answer
}

// vendorClient is an HTTP client that opens a connection for every request.
func vendorClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
}

type messageAnswer struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Text  string          `json:"text"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func TestTheScriptedVendorAnswersByScriptAndKeepsEveryRequest(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	vendor := env.Vendor()
	vendor.Script(func(r *harness.VendorRequest) harness.VendorReply {
		if r.Agent == "alpha" {
			return harness.ToolUse("read_board", map[string]any{"round": 1})
		}
		return harness.Text("Nothing to add.").WithUsage(harness.Usage(7, 3))
	})
	client := vendorClient()
	keyA, keyB := vendor.Key("a"), vendor.Key("b")
	require.NotEqual(t, keyA, keyB, "each instance has a key of its own")

	resp, body := postMessages(t, client, vendor.URL(), keyA, messagesRequest("alpha"))

	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	var answer messageAnswer
	require.NoError(t, json.Unmarshal(body, &answer))
	assert.Equal(t, "message", answer.Type)
	assert.Equal(t, "assistant", answer.Role)
	assert.Equal(t, "claude-sonnet-5-5", answer.Model, "the answer names the model the request asked for")
	assert.Equal(t, "tool_use", answer.StopReason)
	require.Len(t, answer.Content, 1)
	assert.Equal(t, "tool_use", answer.Content[0].Type)
	assert.Equal(t, "read_board", answer.Content[0].Name)
	assert.Regexp(t, `^toolu_[A-Za-z0-9_-]{1,100}$`, answer.Content[0].ID)
	assert.JSONEq(t, `{"round": 1}`, string(answer.Content[0].Input))
	assert.EqualValues(t, 100, answer.Usage.InputTokens, "the default usage")
	assert.EqualValues(t, 20, answer.Usage.OutputTokens)

	recorded := vendor.Requests("a")
	require.Len(t, recorded, 1)
	got := recorded[0]
	assert.Equal(t, "a", got.Instance)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v1/messages", got.Path)
	assert.Empty(t, got.ReadError)
	assert.Equal(t, keyA, got.Header.Get("X-Api-Key"))
	assert.Equal(t, "2023-06-01", got.Header.Get("Anthropic-Version"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.JSONEq(t, messagesRequest("alpha"), string(got.Body))
	assert.Equal(t, "alpha", got.Agent, "the agent is the one its skill names")
	assert.Equal(t, "claude-sonnet-5-5", got.Model)
	assert.EqualValues(t, 256, got.MaxTokens)
	assert.Contains(t, got.System, "Read the board.")
	assert.Equal(t, []string{"read_board", "end_turn"}, got.Tools)
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "user", got.Messages[0].Role)
	require.Len(t, got.Messages[0].Blocks, 1)
	assert.Equal(t, "text", got.Messages[0].Blocks[0].Type)
	assert.Equal(t, "Begin.", got.Messages[0].Blocks[0].Text)
	assert.WithinDuration(t, time.Now(), got.At, time.Minute)
	assert.Empty(t, vendor.Requests("b"))
	assert.Len(t, vendor.All(), 1)

	// Another instance, another agent, and a conversation with a tool result.
	followUp := strings.Replace(messagesRequest("beta"), `"messages":[{"role":"user","content":"Begin."}]`,
		`"messages":[{"role":"user","content":"Begin."},`+
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_board","input":{"round":1}}]},`+
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"The board is empty."}],"is_error":true}]}]`, 1)
	resp, body = postMessages(t, client, vendor.URL(), keyB, followUp)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.NoError(t, json.Unmarshal(body, &answer))
	assert.Equal(t, "end_turn", answer.StopReason)
	require.Len(t, answer.Content, 1)
	assert.Equal(t, "text", answer.Content[0].Type)
	assert.Equal(t, "Nothing to add.", answer.Content[0].Text)
	assert.EqualValues(t, 7, answer.Usage.InputTokens)
	assert.EqualValues(t, 3, answer.Usage.OutputTokens)

	recorded = vendor.Requests("b")
	require.Len(t, recorded, 1)
	assert.Equal(t, "beta", recorded[0].Agent)
	require.Len(t, recorded[0].Messages, 3)
	call := recorded[0].Messages[1].Blocks[0]
	assert.Equal(t, "tool_use", call.Type)
	assert.Equal(t, "toolu_1", call.ID)
	assert.Equal(t, "read_board", call.Name)
	assert.JSONEq(t, `{"round": 1}`, string(call.Input))
	result := recorded[0].Messages[2].Blocks[0]
	assert.Equal(t, "tool_result", result.Type)
	assert.Equal(t, "toolu_1", result.ToolUseID)
	assert.Equal(t, "The board is empty.", result.Text)
	assert.True(t, result.IsError)
	assert.Len(t, vendor.Requests("a"), 1, "a's requests are the same")

	assert.Len(t, vendor.All(), 2)
	assert.Equal(t, []string{"a", "b"}, []string{vendor.All()[0].Instance, vendor.All()[1].Instance}, "in arrival order")

	// Without a script the vendor ends every turn.
	vendor.Script(nil)
	resp, body = postMessages(t, client, vendor.URL(), keyA, messagesRequest("alpha"))
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var ended messageAnswer
	require.NoError(t, json.Unmarshal(body, &ended))
	require.Len(t, ended.Content, 1)
	assert.Equal(t, "tool_use", ended.Content[0].Type)
	assert.Equal(t, "end_turn", ended.Content[0].Name)
	assert.JSONEq(t, `{}`, string(ended.Content[0].Input))
}

func TestTheScriptedVendorRefusesAKeyNoInstanceOwns(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	vendor := env.Vendor()
	calls := 0
	vendor.Script(func(*harness.VendorRequest) harness.VendorReply {
		calls++
		return harness.Text("Done.")
	})
	client := vendorClient()

	resp, body := postMessages(t, client, vendor.URL(), "not-a-key", messagesRequest("alpha"))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a key that no instance owns")
	assert.JSONEq(t, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, string(body))
	resp, body = postMessages(t, client, vendor.URL(), "", messagesRequest("alpha"))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "no key")
	assert.JSONEq(t, `{"type":"error","error":{"type":"authentication_error","message":"x-api-key header is required"}}`, string(body))

	assert.Zero(t, calls, "the script answers no request that carries no instance's key")
	refused := vendor.Requests("")
	require.Len(t, refused, 2, "the refused requests are kept, under no instance")
	assert.Equal(t, "not-a-key", refused[0].Header.Get("X-Api-Key"))
	assert.Equal(t, "alpha", refused[0].Agent, "and read like the others")
	assert.Empty(t, refused[1].Header.Get("X-Api-Key"))
	assert.Len(t, vendor.All(), 2)

	// A stand-in accepts the keys its clients carry, which no deployment wrote.
	vendor.AcceptKey(t, "turn", "session-token-1")
	resp, body = postMessages(t, client, vendor.URL(), "session-token-1", messagesRequest("alpha"))
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Equal(t, 1, calls)
	require.Len(t, vendor.Requests("turn"), 1)
	assert.Equal(t, "session-token-1", vendor.Requests("turn")[0].Header.Get("X-Api-Key"))
	assert.Len(t, vendor.Requests(""), 2)
	resp, body = postMessages(t, client, vendor.URL(), vendor.Key("a"), messagesRequest("alpha"))
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Len(t, vendor.Requests("a"), 1, "the key Key made is still the instance's")
}

func TestTheScriptedVendorFailsHangsAndDelaysOnScript(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	vendor := env.Vendor()
	const overloaded = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	vendor.Script(func(r *harness.VendorRequest) harness.VendorReply {
		switch r.Agent {
		case "failing":
			return harness.Status(529, overloaded)
		case "slow":
			return harness.Text("Done.").After(time.Second)
		case "hanging":
			return harness.Hang()
		case "dropping":
			return harness.Disconnect()
		}
		return harness.EndTurn()
	})
	key := vendor.Key("a")

	resp, body := postMessages(t, vendorClient(), vendor.URL(), key, messagesRequest("failing"))
	assert.Equal(t, 529, resp.StatusCode)
	assert.JSONEq(t, overloaded, string(body))

	start := time.Now()
	resp, body = postMessages(t, vendorClient(), vendor.URL(), key, messagesRequest("slow"))
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "the answer comes after its delay")
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "Done.")

	hung := &http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, vendor.URL()+"/v1/messages", strings.NewReader(messagesRequest("hanging")))
	require.NoError(t, err)
	req.Header.Set("X-Api-Key", key)
	start = time.Now()
	_, err = hung.Do(req)
	require.Error(t, err)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout(), "a hang ends when the client gives up: %v", err)
	assert.Less(t, time.Since(start), 5*time.Second)

	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, vendor.URL()+"/v1/messages", strings.NewReader(messagesRequest("dropping")))
	require.NoError(t, err)
	req.Header.Set("X-Api-Key", key)
	_, err = vendorClient().Do(req)
	require.ErrorIs(t, err, io.EOF, "a dropped connection ends the client's request with an EOF")

	assert.Len(t, vendor.Requests("a"), 4, "failed, slow, hanging and dropped requests are kept too")
}

// mcpCall posts one JSON-RPC message to a scripted MCP server with a
// credential, and returns the response and its body.
func mcpCall(t *testing.T, client *http.Client, method, target, credential, body string) (*http.Response, []byte, error) {
	t.Helper()
	return mcpCallIn(t, client, method, target, credential, "", body)
}

// mcpCallIn is mcpCall in the session, if it names one.
func mcpCallIn(t *testing.T, client *http.Client, method, target, credential, session, body string) (*http.Response, []byte, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, answer, nil
}

// mcpClient returns an HTTP client for the scripted MCP servers of env: it
// trusts the test CA, and opens a connection for every request.
func mcpClient(t *testing.T, env *harness.Env) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(env.CA())
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pem), "the CA file holds a certificate")
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DisableKeepAlives: true,
	}}
}

// rpcAnswer is a JSON-RPC response, as far as these tests read one.
type rpcAnswer struct {
	ID     int `json:"id"`
	Result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeRPC(t *testing.T, body []byte) rpcAnswer {
	t.Helper()
	var a rpcAnswer
	require.NoError(t, json.Unmarshal(body, &a), string(body))
	return a
}

const (
	rpcInitialize  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	rpcInitialized = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	rpcPing        = `{"jsonrpc":"2.0","id":2,"method":"ping"}`
	rpcList        = `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`
)

func rpcQuery(mode string) string {
	return `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"query","arguments":{"mode":"` + mode + `"}}}`
}

// exchangeWithAnMCPServer runs the exchange of an MCP client, with the
// credential, against a scripted server whose tool query is scripted by the
// mode argument.
func exchangeWithAnMCPServer(t *testing.T, client *http.Client, srv *harness.MCPServer, credential string) {
	t.Helper()
	resp, body, err := mcpCall(t, client, http.MethodPost, srv.URL(), credential, rpcInitialize)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.NotEmpty(t, resp.Header.Get("Mcp-Session-Id"))
	init := decodeRPC(t, body)
	assert.Equal(t, 1, init.ID)
	assert.Equal(t, "2025-06-18", init.Result.ProtocolVersion)
	assert.Equal(t, "metrics", init.Result.ServerInfo.Name)

	resp, body, err = mcpCall(t, client, http.MethodPost, srv.URL(), credential, rpcInitialized)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Empty(t, body)

	resp, body, err = mcpCall(t, client, http.MethodPost, srv.URL(), credential, rpcPing)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":2,"result":{}}`, string(body))

	resp, body, err = mcpCall(t, client, http.MethodPost, srv.URL(), credential, rpcList)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	list := decodeRPC(t, body)
	require.Len(t, list.Result.Tools, 1)
	assert.Equal(t, "query", list.Result.Tools[0].Name)
	assert.Equal(t, "Run a query.", list.Result.Tools[0].Description)
	assert.JSONEq(t, `{"type":"object","properties":{"mode":{"type":"string"}}}`, string(list.Result.Tools[0].InputSchema))
}

func newMCPEnv(t *testing.T, plain bool) *harness.Env {
	t.Helper()
	env := harness.New(t, harness.Options{NoDatabase: true, Servers: []harness.ServerSpec{{
		Name:  "metrics",
		Plain: plain,
		Tools: []harness.ToolSpec{{
			Name: "query", Description: "Run a query.",
			InputSchema: `{"type":"object","properties":{"mode":{"type":"string"}}}`,
		}},
	}}})
	env.Server("metrics").Script("query", func(c *harness.ToolCall) harness.ToolReply {
		var args struct {
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal(c.Arguments, &args)
		switch args.Mode {
		case "error":
			return harness.ErrorResult("The backend is down.")
		case "rpc":
			return harness.RPCFailure(-32602, "Bad arguments.")
		case "status":
			return harness.HTTPFailure(http.StatusInternalServerError)
		case "drop":
			return harness.Dropped()
		}
		return harness.Result("42")
	})
	return env
}

func TestTheScriptedMCPServerSpeaksMCPOverTLS(t *testing.T) {
	env := newMCPEnv(t, false)
	srv := env.Server("metrics")
	require.True(t, strings.HasPrefix(srv.URL(), "https://127.0.0.1:"), srv.URL())
	client := mcpClient(t, env)
	credA, credB := srv.Credential("a"), srv.Credential("b")
	require.NotEqual(t, credA, credB, "each instance has a credential of its own")

	exchangeWithAnMCPServer(t, client, srv, credA)

	resp, body, err := mcpCall(t, client, http.MethodPost, srv.URL(), credA, rpcQuery("text"))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	call := decodeRPC(t, body)
	assert.Equal(t, 4, call.ID)
	require.Len(t, call.Result.Content, 1)
	assert.Equal(t, "text", call.Result.Content[0].Type)
	assert.Equal(t, "42", call.Result.Content[0].Text)
	assert.False(t, call.Result.IsError)

	_, body, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, rpcQuery("error"))
	require.NoError(t, err)
	call = decodeRPC(t, body)
	assert.True(t, call.Result.IsError, "an isError result")
	require.Len(t, call.Result.Content, 1)
	assert.Equal(t, "The backend is down.", call.Result.Content[0].Text)

	resp, body, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, rpcQuery("rpc"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	call = decodeRPC(t, body)
	require.NotNil(t, call.Error)
	assert.Equal(t, -32602, call.Error.Code)
	assert.Equal(t, "Bad arguments.", call.Error.Message)

	resp, _, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, rpcQuery("status"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	_, _, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, rpcQuery("drop"))
	require.ErrorIs(t, err, io.EOF, "a dropped connection")

	resp, _, err = mcpCall(t, client, http.MethodGet, srv.URL(), credA, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "no stream the server starts")
	resp, _, err = mcpCall(t, client, http.MethodDelete, srv.URL(), credA, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a session ends")

	// An unknown tool and an unknown method are JSON-RPC errors.
	_, body, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	require.NoError(t, err)
	require.NotNil(t, decodeRPC(t, body).Error)
	_, body, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, `{"jsonrpc":"2.0","id":6,"method":"server/discover"}`)
	require.NoError(t, err)
	require.NotNil(t, decodeRPC(t, body).Error)
	assert.Equal(t, -32601, decodeRPC(t, body).Error.Code)
	resp, body, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, `{"jsonrpc":`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a message that is not JSON")
	require.NotNil(t, decodeRPC(t, body).Error)
	assert.Equal(t, -32700, decodeRPC(t, body).Error.Code)

	// Another instance, and a request with no credential, which is refused.
	exchangeWithAnMCPServer(t, client, srv, credB)
	resp, _, err = mcpCall(t, client, http.MethodPost, srv.URL(), "", rpcPing)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// A client that does not trust the CA is refused.
	_, _, err = mcpCall(t, &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}, http.MethodPost, srv.URL(), credA, rpcPing)
	var untrusted *tls.CertificateVerificationError
	require.ErrorAs(t, err, &untrusted, "a server's certificate is the test CA's, no public CA's")

	// Down: connections are refused. Up: the same address serves again.
	srv.Down()
	_, _, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, rpcPing)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
	srv.Up()
	resp, _, err = mcpCall(t, client, http.MethodPost, srv.URL(), credA, rpcPing)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Every request is held under the instance its credential names.
	rpcMethods := func(instance string) []string {
		var methods []string
		for _, r := range srv.Requests(instance) {
			assert.Equal(t, instance, r.Instance)
			methods = append(methods, r.Method+" "+r.RPC)
		}
		return methods
	}
	assert.Equal(t, []string{
		"POST initialize", "POST notifications/initialized", "POST ping", "POST tools/list",
		"POST tools/call", "POST tools/call", "POST tools/call", "POST tools/call", "POST tools/call",
		"GET ", "DELETE ", "POST tools/call", "POST server/discover", "POST ", "POST ping",
	}, rpcMethods("a"), "a's requests, in arrival order: those refused before they arrived are not among them")
	assert.Equal(t, []string{"POST initialize", "POST notifications/initialized", "POST ping", "POST tools/list"}, rpcMethods("b"))
	assert.Equal(t, []string{"POST ping"}, rpcMethods(""), "the request without a credential, refused and kept")
	assert.Len(t, srv.All(), len(srv.Requests("a"))+len(srv.Requests("b"))+len(srv.Requests("")))
	var tools []string
	for _, r := range srv.Requests("a") {
		if r.RPC == "tools/call" {
			tools = append(tools, r.Tool)
		}
	}
	assert.Equal(t, []string{"query", "query", "query", "query", "query", "nope"}, tools)
	first := srv.Requests("a")[0]
	assert.Equal(t, "Bearer "+credA, first.Header.Get("Authorization"))
	assert.JSONEq(t, rpcInitialize, string(first.Body))
	assert.Empty(t, first.ReadError)
	assert.WithinDuration(t, time.Now(), first.At, time.Minute)
}

func TestAPlainScriptedMCPServerServesHTTP(t *testing.T) {
	env := newMCPEnv(t, true)
	srv := env.Server("metrics")
	require.True(t, strings.HasPrefix(srv.URL(), "http://127.0.0.1:"), srv.URL())
	client := vendorClient()
	cred := srv.Credential("a")

	exchangeWithAnMCPServer(t, client, srv, cred)

	_, body, err := mcpCall(t, client, http.MethodPost, srv.URL(), cred, rpcQuery("text"))
	require.NoError(t, err)
	call := decodeRPC(t, body)
	require.Len(t, call.Result.Content, 1)
	assert.Equal(t, "42", call.Result.Content[0].Text)
	assert.Len(t, srv.Requests("a"), 5)
	assert.Len(t, srv.All(), 5)

	// The server is not TLS: a TLS client's hello is not a request it can read.
	pem, err := os.ReadFile(env.CA())
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pem))
	secure := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	_, _, err = mcpCall(t, secure, http.MethodPost, strings.Replace(srv.URL(), "http://", "https://", 1), cred, rpcPing)
	require.Error(t, err)
}

func TestTheScriptedMCPServersRefuseACredentialNoInstanceOwns(t *testing.T) {
	for _, plain := range []bool{false, true} {
		name := "TLS"
		if plain {
			name = "plain"
		}
		t.Run(name, func(t *testing.T) {
			env := newMCPEnv(t, plain)
			srv := env.Server("metrics")
			client := mcpClient(t, env)
			calls := 0
			srv.Script("query", func(*harness.ToolCall) harness.ToolReply {
				calls++
				return harness.Result("42")
			})

			for _, credential := range []string{"", "not-a-credential"} {
				for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
					body := ""
					if method == http.MethodPost {
						body = rpcQuery("text")
					}
					resp, _, err := mcpCall(t, client, method, srv.URL(), credential, body)
					require.NoError(t, err)
					assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s with credential %q", method, credential)
					assert.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))
				}
			}

			assert.Zero(t, calls, "no script answers a request that carries no instance's credential")
			refused := srv.Requests("")
			require.Len(t, refused, 6, "the refused requests are kept, under no instance")
			assert.Equal(t, "tools/call", refused[0].RPC)
			assert.Equal(t, "query", refused[0].Tool)
			assert.Empty(t, refused[0].Header.Get("Authorization"))
			assert.Equal(t, http.MethodGet, refused[1].Method)
			assert.Equal(t, http.MethodDelete, refused[2].Method)
			assert.Equal(t, "Bearer not-a-credential", refused[3].Header.Get("Authorization"))
			assert.Len(t, srv.All(), 6)

			// A stand-in accepts the credentials its clients carry, which no
			// deployment wrote.
			srv.AcceptCredential(t, "turn", "session-token-1")
			resp, body, err := mcpCall(t, client, http.MethodPost, srv.URL(), "session-token-1", rpcQuery("text"))
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
			assert.Equal(t, 1, calls)
			require.Len(t, srv.Requests("turn"), 1)
			assert.Len(t, srv.Requests(""), 6)

			// The harness lists the tools, to pin them, with no credential: at
			// a door of its own, which calls no tool.
			resp, body, err = mcpCall(t, client, http.MethodPost, srv.PinURL(), "", rpcInitialize)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
			resp, body, err = mcpCall(t, client, http.MethodPost, srv.PinURL(), "", rpcList)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
			assert.Len(t, decodeRPC(t, body).Result.Tools, 1)
			resp, _, err = mcpCall(t, client, http.MethodPost, srv.PinURL(), "", rpcQuery("text"))
			require.NoError(t, err)
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a call is not a listing")
			assert.Equal(t, 1, calls)
			assert.Len(t, srv.Requests(harness.PinInstance), 2, "the listing is kept under a name of its own")
			assert.Len(t, srv.Requests(""), 7, "the refused call is kept like the others")
		})
	}
}

func TestAScriptedMCPServerKeepsItsSessionsUntilTheyEndOrItRestarts(t *testing.T) {
	env := newMCPEnv(t, false)
	srv := env.Server("metrics")
	client := mcpClient(t, env)
	credential := srv.Credential("a")
	call := func(method, session, body string) (status int, header http.Header) {
		resp, _, err := mcpCallIn(t, client, method, srv.URL(), credential, session, body)
		require.NoError(t, err)
		return resp.StatusCode, resp.Header
	}
	start := func() string {
		status, header := call(http.MethodPost, "", rpcInitialize)
		require.Equal(t, http.StatusOK, status)
		require.NotEmpty(t, header.Get("Mcp-Session-Id"))
		return header.Get("Mcp-Session-Id")
	}

	session := start()

	status, _ := call(http.MethodPost, session, rpcPing)
	assert.Equal(t, http.StatusOK, status, "a request in the session")
	status, _ = call(http.MethodPost, session, rpcInitialized)
	assert.Equal(t, http.StatusAccepted, status)
	status, _ = call(http.MethodPost, "", rpcPing)
	assert.Equal(t, http.StatusOK, status, "a request that names no session, as a client of a server without sessions sends")
	status, _ = call(http.MethodPost, "not-a-session", rpcPing)
	assert.Equal(t, http.StatusNotFound, status, "a session the server does not know")
	status, _ = call(http.MethodPost, "not-a-session", rpcInitialized)
	assert.Equal(t, http.StatusNotFound, status, "a notification too")
	status, _ = call(http.MethodDelete, "not-a-session", "")
	assert.Equal(t, http.StatusNotFound, status)

	status, _ = call(http.MethodDelete, session, "")
	assert.Equal(t, http.StatusOK, status, "a DELETE ends the session")
	status, _ = call(http.MethodPost, session, rpcPing)
	assert.Equal(t, http.StatusNotFound, status, "an ended session is not found")
	status, _ = call(http.MethodDelete, session, "")
	assert.Equal(t, http.StatusNotFound, status)

	// A restart ends every session, as a real server's does.
	kept := start()
	require.NotEqual(t, session, kept, "a session has an id of its own")
	status, _ = call(http.MethodPost, kept, rpcPing)
	require.Equal(t, http.StatusOK, status)
	srv.Down()
	srv.Up()
	status, _ = call(http.MethodPost, kept, rpcPing)
	assert.Equal(t, http.StatusNotFound, status, "a session does not outlive a restart")
	again := start()
	status, _ = call(http.MethodPost, again, rpcPing)
	assert.Equal(t, http.StatusOK, status, "and a new one starts")
}

func TestARequestWhoseBodyBreaksOffIsKeptWithItsHeaders(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true, Servers: []harness.ServerSpec{{
		Name: "gateway", Plain: true, Tools: []harness.ToolSpec{{Name: "end_turn"}},
	}}})
	vendor, gateway := env.Vendor(), env.Server("gateway")
	vendorKey, credential := vendor.Key("a"), gateway.Credential("a")

	// breakOff sends a request that promises a hundred bytes of body and
	// delivers a few of them after a pause, then hangs up. It returns the time
	// at which the body began.
	breakOff := func(target, path, header string) time.Time {
		u, err := url.Parse(target)
		require.NoError(t, err)
		conn, err := net.Dial("tcp", u.Host)
		require.NoError(t, err)
		defer conn.Close()
		_, err = io.WriteString(conn, "POST "+path+" HTTP/1.1\r\nHost: "+u.Host+"\r\n"+header+
			"Content-Type: application/json\r\nContent-Length: 100\r\n\r\n")
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)
		began := time.Now()
		_, err = io.WriteString(conn, `{"jsonrpc"`)
		require.NoError(t, err)
		return began
	}
	vendorBody := breakOff(vendor.URL(), "/v1/messages?trace=on", "X-Api-Key: "+vendorKey+"\r\n")
	gatewayBody := breakOff(gateway.URL(), "/mcp?trace=on", "Authorization: Bearer "+credential+"\r\n")
	breakOff(vendor.URL(), "/v1/messages", "X-Api-Key: not-a-key\r\n")

	require.Eventually(t, func() bool {
		return len(vendor.Requests("a")) == 1 && len(gateway.Requests("a")) == 1 && len(vendor.Requests("")) == 1
	}, 5*time.Second, 10*time.Millisecond)
	got := vendor.Requests("a")[0]
	assert.NotEmpty(t, got.ReadError, "the body did not arrive in full")
	assert.Equal(t, `{"jsonrpc"`, string(got.Body), "what arrived is kept")
	assert.Equal(t, vendorKey, got.Header.Get("X-Api-Key"), "and the headers")
	assert.Equal(t, "/v1/messages", got.Path)
	assert.Equal(t, "trace=on", got.Query, "and the query")
	assert.True(t, got.At.Before(vendorBody), "a request is stamped when it begins, not when its body has been read")
	refused := vendor.Requests("")[0]
	assert.NotEmpty(t, refused.ReadError, "a request that is refused is kept the same")
	assert.Equal(t, "not-a-key", refused.Header.Get("X-Api-Key"))

	call := gateway.Requests("a")[0]
	assert.NotEmpty(t, call.ReadError)
	assert.Equal(t, `{"jsonrpc"`, string(call.Body))
	assert.Equal(t, "Bearer "+credential, call.Header.Get("Authorization"))
	assert.Equal(t, "/mcp", call.Path)
	assert.Equal(t, "trace=on", call.Query)
	assert.True(t, call.At.Before(gatewayBody))
}

// parentScript is the script of a child that waits for its standard input to
// end and then leaves a marker.
func parentScript(marker string) []string {
	return []string{"sh", "-c", `cat >/dev/null; echo eof > "$1"`, "sh", marker}
}

func markerHolds(marker, text string) func() bool {
	return func() bool {
		data, err := os.ReadFile(marker)
		return err == nil && string(data) == text
	}
}

func TestKillingTheParentClosesItsChildsStandardInput(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	marker := filepath.Join(t.TempDir(), "MARKER")
	parent := env.StartParent(t, parentScript(marker), nil)
	assert.Never(t, markerHolds(marker, "eof\n"), 300*time.Millisecond, 20*time.Millisecond,
		"the parent holds its child's standard input open")

	parent.Kill(t)

	assert.Eventually(t, markerHolds(marker, "eof\n"), 2*time.Second, 10*time.Millisecond,
		"the child's standard input closed with the parent")
}

func TestClosingTheParentsOwnInputEndsItAndItsChild(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	marker := filepath.Join(t.TempDir(), "MARKER")
	parent := env.StartParent(t, parentScript(marker), nil)
	assert.Never(t, markerHolds(marker, "eof\n"), 300*time.Millisecond, 20*time.Millisecond,
		"the parent holds its child's standard input open")

	parent.Close(t)

	assert.Equal(t, 0, parent.Wait(t, 2*time.Second), "the parent ends with its child's exit code")
	assert.Eventually(t, markerHolds(marker, "eof\n"), 2*time.Second, 10*time.Millisecond,
		"the child's standard input closed when the parent's did")
}

func TestAParentKillsAChildThatDoesNotEndWhenItsInputCloses(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	pidFile := filepath.Join(t.TempDir(), "pid")
	parent := env.StartParent(t, []string{"sh", "-c", `echo $$ > "$1"; exec sleep 300`, "sh", pidFile}, nil)
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, syscall.Kill(pid, 0), "the child runs")

	parent.Close(t)

	assert.Equal(t, 128+int(syscall.SIGKILL), parent.Wait(t, 20*time.Second), "the parent kills a child that outlasts its grace")
	assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "the child is gone")
}

func TestTheParentKeepsItsChildsOutputInFilesAndExitsWithItsCode(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	parent := env.StartParent(t, []string{"sh", "-c", `echo "out $TOKEN"; echo err >&2; exit 3`}, []string{"TOKEN=seven"})

	assert.Equal(t, 3, parent.Wait(t, 5*time.Second), "the child's exit code")

	out, err := os.ReadFile(parent.Stdout())
	require.NoError(t, err)
	assert.Equal(t, "out seven\n", string(out), "the child ran with the environment it was given")
	errOut, err := os.ReadFile(parent.Stderr())
	require.NoError(t, err)
	assert.Equal(t, "err\n", string(errOut))
}

// validated is the part of the output of registry validate that these tests
// read, for a configuration that keeps every rule.
type validated struct {
	Commit  string `json:"commit"`
	Objects []struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"objects"`
}

func TestTheHarnessConfigurationValidates(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true, Servers: []harness.ServerSpec{{
		Name: "metrics",
		Tools: []harness.ToolSpec{
			{Name: "query", Description: "Run a query.", InputSchema: `{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`},
			{Name: "alerts", Description: "List alerts."},
		},
	}}})

	repo := env.Config(t, harness.ConfigSpec{Agents: []harness.AgentSpec{
		{Name: "alpha", Skill: "Look at the metrics.", Servers: []string{"metrics"}},
		{Name: "beta", Model: "claude-haiku-5-5", Skill: "Look at the alerts.", Servers: []string{"metrics"}},
	}})

	pinned := env.Server("metrics").All()
	require.NotEmpty(t, pinned, "the harness lists the tools to pin them")
	for _, r := range pinned {
		assert.Equal(t, harness.PinInstance, r.Instance, "and the listing is no instance's request")
	}
	assert.Empty(t, env.Server("metrics").Requests(""))
	out, errOut, code := agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", repo.Ref())
	require.Equal(t, 0, code, errOut+out)
	var v validated
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	assert.Regexp(t, `^[0-9a-f]{40}$`, v.Commit)
	agents := map[string]string{}
	for _, o := range v.Objects {
		if o.Kind == "Agent" {
			agents[o.Name] = o.Version
		}
	}
	require.Len(t, agents, 2)
	assert.Contains(t, agents, "alpha")
	assert.Contains(t, agents, "beta")

	// The scripted server's tools are pinned by the hashes hash-tools prints,
	// and each agent's skill carries the line that tells the agents apart.
	hashes, errOut, code := agentswarm(t, "registry", "hash-tools", "--endpoint", env.Server("metrics").PinURL(), "--ca", env.CA())
	require.Equal(t, 0, code, errOut)
	var listed []struct {
		Name       string `json:"name"`
		SchemaHash string `json:"schemaHash"`
	}
	require.NoError(t, json.Unmarshal([]byte(hashes), &listed))
	require.Len(t, listed, 2)
	registration, err := os.ReadFile(filepath.Join(repo.Dir(), "servers", "metrics.yaml"))
	require.NoError(t, err)
	for _, tool := range listed {
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, tool.SchemaHash, tool.Name)
		assert.Contains(t, string(registration), tool.SchemaHash, "the registration pins %s", tool.Name)
	}
	assert.Contains(t, string(registration), "credentialRef: secret://mcp/metrics")
	for _, agent := range []string{"alpha", "beta"} {
		skill, err := os.ReadFile(filepath.Join(repo.Dir(), "skills", agent+"-skill", "1.0.0", "SKILL.md"))
		require.NoError(t, err)
		assert.Contains(t, "\n"+string(skill), "\nYou are "+agent+".\n")
	}

	// A commit changes what the repository holds, and a bad one is refused.
	good := repo.Commit(t, func(dir string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("A note.\n"), 0o600))
	})
	require.Regexp(t, `^[0-9a-f]{40}$`, good)
	require.NotEqual(t, v.Commit, good)
	out, errOut, code = agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", repo.Ref())
	require.Equal(t, 0, code, errOut)
	v = validated{}
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	assert.Equal(t, good, v.Commit)

	bad := repo.Commit(t, func(dir string) {
		path := filepath.Join(dir, "agents", "alpha.yaml")
		text, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(text, []byte("  unknownKey: 1\n")...), 0o600))
	})
	require.NotEqual(t, good, bad)
	out, errOut, code = agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", repo.Ref())
	assert.Equal(t, 1, code, out)
	assert.Contains(t, errOut, "the configuration is refused")
	out, _, code = agentswarm(t, "registry", "validate", "--repo", repo.Dir(), "--ref", good)
	assert.Equal(t, 0, code, "the good commit is still there: %s", out)
}

// flatten reads a deployment file as the harness writes it, a block mapping of
// plain keys, and returns each scalar by its dotted path. Strings are
// unquoted.
func flatten(t *testing.T, path string) map[string]string {
	t.Helper()
	text, err := os.ReadFile(path)
	require.NoError(t, err)
	keys := map[string]string{}
	var stack []string
	for _, line := range strings.Split(string(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		depth := (len(line) - len(strings.TrimLeft(line, " "))) / 2
		key, value, found := strings.Cut(trimmed, ":")
		require.True(t, found, "a line of the deployment is not key: value: %q", line)
		value = strings.TrimSpace(value)
		stack = append(stack[:depth], key)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, `"`) {
			require.NoError(t, json.Unmarshal([]byte(value), &value), line)
		}
		keys[strings.Join(stack, ".")] = value
	}
	return keys
}

var (
	requiredDeploymentKeys = []string{
		"tenant", "holder", "secrets", "database", "configuration.repository", "configuration.ref",
		"listeners.mcp", "listeners.llm", "listeners.admin", "listeners.addresses", "admin.token",
		"vendor.baseURL", "vendor.key", "vendor.apiVersion", "prices.reference", "prices.dailyCap",
		"runner.kind", "runner.unsafeNoSandbox", "runner.output", "runner.images.thinloop",
	}
	optionalDeploymentKeys = regexp.MustCompile(`^(runner\.(binary|outputCap|maxTokens)|mcp\.caBundle|` +
		`timing\.lease\.(ttl|renewEvery|margin|takeEvery|takeWait)|timing\.database\.(transactionTimeout|idleTimeout)|` +
		`timing\.coordinator\.(grace|settleWait|settlePoll|retry)|timing\.llm\.(recordTimeout|lastCallWait|vendorTimeout|coolDown)|` +
		`timing\.mcp\.(recordTimeout|callTimeout|verifyEvery|lookTimeout|coolDown)|timing\.runtime\.slack|timing\.drain|timing\.registry\.refresh|` +
		`prices\.cards\.[A-Za-z0-9._-]+\.(threshold|base\.(input|output|cacheWrite5m|cacheWrite1h|cacheRead)))$`)
	secretReference = regexp.MustCompile(`^secret://[A-Za-z0-9._/-]+$`)
)

func readSecret(t *testing.T, dir, reference string) string {
	t.Helper()
	require.Regexp(t, secretReference, reference)
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(reference, "secret://")))
	require.NoError(t, err, reference)
	return strings.TrimRight(string(data), "\r\n")
}

func TestTheHarnessDeploymentNamesEverySecretItUses(t *testing.T) {
	env := harness.New(t, harness.Options{Toxiproxy: true, Servers: []harness.ServerSpec{{
		Name:  "metrics",
		Tools: []harness.ToolSpec{{Name: "query", Description: "Run a query."}},
	}}})
	repo := env.Config(t, harness.ConfigSpec{Agents: []harness.AgentSpec{
		{Name: "alpha", Servers: []string{"metrics"}},
		{Name: "beta", Model: "claude-haiku-5-5", Servers: []string{"metrics"}},
	}})
	keys := map[string]map[string]string{}
	for _, instance := range []string{"a", "b"} {
		keys[instance] = flatten(t, env.Deployment(t, instance, harness.DeploymentSpec{}))
	}

	for instance, deployment := range keys {
		for _, key := range requiredDeploymentKeys {
			assert.Contains(t, deployment, key, "instance %s", instance)
		}
		for key := range deployment {
			if !slices.Contains(requiredDeploymentKeys, key) {
				assert.Regexp(t, optionalDeploymentKeys, key, "a key C1 does not have, in instance %s", instance)
			}
		}
		assert.Equal(t, instance, deployment["holder"])
		assert.Equal(t, repo.Dir(), deployment["configuration.repository"])
		assert.Equal(t, repo.Ref(), deployment["configuration.ref"])
		assert.Equal(t, "process", deployment["runner.kind"])
		assert.Equal(t, "true", deployment["runner.unsafeNoSandbox"])
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, deployment["runner.images.thinloop"])
		assert.Equal(t, env.CA(), deployment["mcp.caBundle"])
		assert.Equal(t, env.Vendor().URL(), deployment["vendor.baseURL"])
		assert.Equal(t, "127.0.0.1:0", deployment["listeners.mcp"])
		assert.Equal(t, "127.0.0.1:0", deployment["listeners.llm"])
		assert.Equal(t, "127.0.0.1:0", deployment["listeners.admin"])
		assert.True(t, filepath.IsAbs(deployment["listeners.addresses"]))
		assert.True(t, filepath.IsAbs(deployment["runner.output"]))
		assert.NotEmpty(t, deployment["prices.cards.claude-sonnet-5-5.base.input"])
		assert.NotEmpty(t, deployment["prices.cards.claude-haiku-5-5.base.input"], "a card for each model the agents use")

		// Every reference names a secret in the instance's own directory.
		secrets := deployment["secrets"]
		require.True(t, filepath.IsAbs(secrets))
		for _, key := range []string{"database", "admin.token", "vendor.key"} {
			assert.NotEmpty(t, readSecret(t, secrets, deployment[key]), key)
		}
		assert.Equal(t, env.Vendor().Key(instance), readSecret(t, secrets, deployment["vendor.key"]), "the instance's own vendor key")
		assert.Equal(t, env.DatabaseURL(instance), readSecret(t, secrets, deployment["database"]), "the instance's own database")
		credential, err := os.ReadFile(filepath.Join(secrets, "mcp", "metrics"))
		require.NoError(t, err, "the credential the configuration's credentialRef names")
		assert.Equal(t, env.Server("metrics").Credential(instance), strings.TrimRight(string(credential), "\r\n"), "the instance's own server credential")
	}
	assert.NotEqual(t, keys["a"]["secrets"], keys["b"]["secrets"])
	assert.NotEqual(t, keys["a"]["listeners.addresses"], keys["b"]["listeners.addresses"])
	assert.NotEqual(t, keys["a"]["runner.output"], keys["b"]["runner.output"])
	assert.NotEqual(t, env.Vendor().Key("a"), env.Vendor().Key("b"))
	assert.NotEqual(t, env.Server("metrics").Credential("a"), env.Server("metrics").Credential("b"))
	assert.NotEqual(t, env.DatabaseURL("a"), env.DatabaseURL("b"))

	// What a scenario changes.
	changed := flatten(t, env.Deployment(t, "c", harness.DeploymentSpec{
		Timing: harness.Timing{
			Lease: harness.LeaseTiming{TTL: 6 * time.Second, RenewEvery: 1500 * time.Millisecond},
			Drain: 10 * time.Second,
		},
		DailyCap:  1000,
		Binary:    "/bin/sleep",
		MaxTokens: 1024,
		WithoutCA: true,
	}))
	assert.Equal(t, "6s", changed["timing.lease.ttl"])
	assert.Equal(t, "1.5s", changed["timing.lease.renewEvery"])
	assert.Equal(t, "10s", changed["timing.drain"])
	assert.NotContains(t, changed, "timing.lease.margin", "a timing the scenario leaves out takes serve's default")
	assert.Equal(t, "1000", changed["prices.dailyCap"])
	assert.Equal(t, "/bin/sleep", changed["runner.binary"])
	assert.Equal(t, "1024", changed["runner.maxTokens"])
	assert.NotContains(t, changed, "mcp.caBundle")
	assert.NotContains(t, keys["a"], "runner.binary")
	assert.NotContains(t, keys["a"], "timing.lease.ttl")
}

func TestTheHarnessUsesTheDatabaseItIsGiven(t *testing.T) {
	given := harness.New(t, harness.Options{})
	require.NotEmpty(t, given.PostgresID())
	t.Setenv("AGENTSWARM_E2E_DATABASE", given.DatabaseURL(""))
	t.Setenv("AGENTSWARM_E2E_DATABASE_CONTAINER", given.PostgresID())
	before := projectContainers(t)

	env := harness.New(t, harness.Options{Toxiproxy: true})

	assert.Empty(t, env.PostgresID(), "the harness started no Postgres")
	assert.Empty(t, env.ToxiproxyID(), "nor a toxiproxy")
	assert.Equal(t, before, projectContainers(t), "no container was started")
	assert.Equal(t, given.DatabaseURL(""), env.DatabaseURL("a"), "every instance gets the given database")
	assert.Equal(t, given.DatabaseURL(""), env.DatabaseURL("b"))
	env.Psql(t, "create table harness_given (n int); insert into harness_given values (7)")
	assert.Equal(t, "7", given.Psql(t, "select n from harness_given"), "Psql reaches the given database")
	assert.Equal(t, "7", env.Psql(t, "select n from harness_given"))
}

func TestPsqlReturnsWhatItPrints(t *testing.T) {
	env := harness.New(t, harness.Options{})

	assert.Equal(t, "3", env.Psql(t, "select 1 + 2"))
	assert.Empty(t, env.Psql(t, "create table harness_rows (a int, b text)"), "a statement prints nothing")
	env.Psql(t, "insert into harness_rows values (1, 'x'), (2, 'y')")
	assert.Equal(t, "1|x\n2|y", env.Psql(t, "select a, b from harness_rows order by a"), "rows are unaligned and without a header")
	assert.Equal(t, "2", env.Psql(t, "select count(*) from harness_rows; "), "a trailing semicolon and blank are fine")
	assert.Equal(t, "2\n5", env.Psql(t, "select count(*) from harness_rows; select 5"), "each statement prints its rows")
}
