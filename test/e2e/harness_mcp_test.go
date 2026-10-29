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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

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
