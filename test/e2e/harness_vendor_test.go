//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

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
