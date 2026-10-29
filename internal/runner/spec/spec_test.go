package spec_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/runner/spec"
)

// plan7d1 is Plan 7d-1's example of Contract C6, which has no "v".
const plan7d1 = `{
  "turn": "0193...", "run": "0193...", "agent": "alpha", "round": 2,
  "mcp": {"url": "http://127.0.0.1:53001/", "token": "aswm_..."},
  "llm": {"url": "http://127.0.0.1:53002", "token": "aswl_..."},
  "model": "claude-sonnet-5-5",
  "system": ["<skill text>", "..."],
  "prompt": "<the round's prompt>",
  "deadline": "2026-10-20T10:00:02.5+11:00",
  "maxTokens": 4096
}`

// The tokens of the tests' specs.
const (
	mcpToken = "aswm_k3Jt9QxLmV2pR7sW"
	llmToken = "aswl_Z8yHc4NbF1dE6gTq"
)

// valid returns a spec the runtime takes, as the process runner writes one:
// version 2, with HTTP URLs, and neither a socket nor a cert.
func valid() map[string]any {
	return map[string]any{
		"v": 2, "turn": "0193a5c8-0000-7000-8000-000000000001", "run": "0193a5c8-0000-7000-8000-000000000002", "agent": "alpha", "round": 2,
		"mcp":   map[string]any{"url": "http://127.0.0.1:53001/", "token": mcpToken},
		"llm":   map[string]any{"url": "http://127.0.0.1:53002", "token": llmToken},
		"model": "claude-sonnet-5-5", "system": []string{"You are alpha."}, "prompt": "Find the cause.",
		"deadline": "2026-10-28T10:00:02.5+11:00", "maxTokens": 4096,
	}
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

// certificate returns the PEM of a self-signed ECDSA P-256 certificate for
// the gateways' names, as serve makes one (Plan 7d-2, Decision 2).
func certificate(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		Subject:   pkix.Name{CommonName: "mcp.agentswarm.internal"},
		DNSNames:  []string{"mcp.agentswarm.internal", "llm.agentswarm.internal"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// A spec without "v", as Plan 7d-1's runners wrote it, reads as version 1,
// whole.
func TestASpecWithoutAVersionIsVersion1(t *testing.T) {
	s, err := spec.Read(strings.NewReader(plan7d1))

	require.NoError(t, err)
	assert.Equal(t, spec.V1, s.Version())
	assert.Equal(t, "2026-10-20T10:00:02.5+11:00", s.Deadline.Format(time.RFC3339Nano))
	s.Deadline = time.Time{}
	assert.Equal(t, spec.Spec{
		Turn: "0193...", Run: "0193...", Agent: "alpha", Round: 2,
		MCP:   spec.Gateway{URL: "http://127.0.0.1:53001/", Token: "aswm_..."},
		LLM:   spec.Gateway{URL: "http://127.0.0.1:53002", Token: "aswl_..."},
		Model: "claude-sonnet-5-5", System: []string{"<skill text>", "..."}, Prompt: "<the round's prompt>", MaxTokens: 4096,
	}, *s)
}

// The runtime reads versions 1 and 2, and refuses a spec of any other,
// naming its version (R1).
func TestTheRuntimeReadsVersions1And2Only(t *testing.T) {
	assert.Equal(t, []int{spec.V1, spec.V2}, spec.Reads)
	for _, c := range []struct {
		v    int
		says string // the refusal's words; "" for a spec that reads
	}{
		{v: 1},
		{v: 2},
		{v: 3, says: "version 3"},
	} {
		t.Run(fmt.Sprintf("version %d", c.v), func(t *testing.T) {
			raw := valid()
			raw["v"] = c.v

			s, err := spec.Read(bytes.NewReader(encode(t, raw)))

			if c.says != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), c.says)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.v, s.Version())
		})
	}
}

// A gateway's socket, when the spec names one, is an absolute path, and the
// spec's cert, when it has one, holds a PEM certificate (C6, version 2).
func TestASpecsSocketAndCertAreChecked(t *testing.T) {
	good := certificate(t)
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a certificate")}))
	junk := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a certificate")}))
	for _, c := range []struct {
		name                 string
		mcpSocket, llmSocket string
		cert                 string
		says                 string // the refusal's words; "" for a spec that reads
	}{
		{name: "neither a socket nor a cert"},
		{name: "absolute sockets and a real certificate", mcpSocket: "/run/agentswarm/mcp.sock", llmSocket: "/run/agentswarm/llm.sock", cert: good},
		{name: "a relative socket for the MCP gateway", mcpSocket: "run/agentswarm/mcp.sock", says: "socket for the MCP gateway"},
		{name: "a relative socket for the LLM gateway", llmSocket: "llm.sock", says: "socket for the LLM gateway"},
		{name: "a cert that is not PEM", cert: "a certificate", says: "no PEM certificate"},
		{name: "a cert whose PEM holds a key", cert: key, says: "no PEM certificate"},
		{name: "a cert whose PEM holds no certificate", cert: junk, says: "no PEM certificate"},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := valid()
			if c.mcpSocket != "" {
				raw["mcp"].(map[string]any)["socket"] = c.mcpSocket
			}
			if c.llmSocket != "" {
				raw["llm"].(map[string]any)["socket"] = c.llmSocket
			}
			if c.cert != "" {
				raw["cert"] = c.cert
			}

			s, err := spec.Read(bytes.NewReader(encode(t, raw)))

			if c.says != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), c.says)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, [3]string{c.mcpSocket, c.llmSocket, c.cert}, [3]string{s.MCP.Socket, s.LLM.Socket, s.Cert})
		})
	}
}

// sized returns a valid spec, encoded, with its skill grown so that it is n
// bytes long.
func sized(t *testing.T, n int) []byte {
	t.Helper()
	raw := valid()
	raw["system"] = []string{""}
	short := len(encode(t, raw))
	raw["system"] = []string{strings.Repeat("x", n-short)}
	out := encode(t, raw)
	require.Len(t, out, n)
	return out
}

// counting counts the bytes read of its reader.
type counting struct {
	r io.Reader
	n int
}

func (c *counting) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// A spec is at most MaxSize bytes, 1 MiB: one byte more is refused, and Read
// reads no more of its reader than MaxSize and one byte, whatever follows,
// as the runtime's standard input holds more until the runner closes it.
func TestASpecLargerThanMaxSizeIsRefused(t *testing.T) {
	for _, c := range []struct {
		name    string
		size    int
		refused bool
	}{
		{name: "MaxSize bytes", size: spec.MaxSize},
		{name: "MaxSize and one byte", size: spec.MaxSize + 1, refused: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &counting{r: bytes.NewReader(append(sized(t, c.size), bytes.Repeat([]byte(" "), 64<<10)...))}

			_, err := spec.Read(r)

			if c.refused {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "larger than 1 MiB")
			} else {
				require.NoError(t, err)
			}
			assert.LessOrEqual(t, r.n, spec.MaxSize+1, "Read read on past MaxSize and one byte")
		})
	}
}

// loggedGateway is a gateway's entry as slog's JSON handler logs it.
type loggedGateway struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// logged returns what slog's JSON handler logs for v, under the key "value",
// decoded into into.
func logged(t *testing.T, v, into any) {
	t.Helper()
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("logged", "value", v)
	entry := struct {
		Value any `json:"value"`
	}{Value: into}
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry), logs.String())
}

// A token, a gateway's entry or a spec that is printed, as in an error,
// whatever the verb, or logged, by slog's JSON handler as well as by fmt,
// holds neither of the spec's tokens: each shows as a placeholder. The spec's
// JSON, which the runtime reads, holds the tokens themselves: the process
// runner's TestTheSpecReachesTheTurnOnItsStandardInput reads them there.
func TestATokenPrintsAsAPlaceholder(t *testing.T) {
	s, err := spec.Read(bytes.NewReader(encode(t, valid())))
	require.NoError(t, err)
	values := []struct {
		name string
		v    any
	}{
		{name: "a Token", v: s.MCP.Token}, {name: "a Gateway", v: s.MCP}, {name: "a *Gateway", v: &s.MCP},
		{name: "a Spec", v: *s}, {name: "a *Spec", v: s},
	}

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d"} {
		for _, value := range values {
			printed := fmt.Sprintf(verb, value.v)

			assert.NotContains(t, printed, mcpToken, "%s of %s", verb, value.name)
			assert.NotContains(t, printed, llmToken, "%s of %s", verb, value.name)
			assert.Contains(t, printed, "[session token]", "%s of %s", verb, value.name)
		}
	}
	for _, value := range values {
		var logs bytes.Buffer
		slog.New(slog.NewJSONHandler(&logs, nil)).Info("logged", "value", value.v)

		assert.NotContains(t, logs.String(), mcpToken, value.name)
		assert.NotContains(t, logs.String(), llmToken, value.name)
		assert.Contains(t, logs.String(), `"[session token]"`, value.name)
	}
	for _, v := range []any{*s, s} {
		var got struct {
			Turn string        `json:"turn"`
			MCP  loggedGateway `json:"mcp"`
			LLM  loggedGateway `json:"llm"`
		}
		logged(t, v, &got)
		assert.Equal(t, s.Turn, got.Turn, "the spec's fields are logged")
		assert.Equal(t, s.MCP.URL, got.MCP.URL)
		assert.Equal(t, [2]string{"[session token]", "[session token]"}, [2]string{got.MCP.Token, got.LLM.Token})
	}
	for _, v := range []any{s.MCP, &s.MCP} {
		var got loggedGateway
		logged(t, v, &got)
		assert.Equal(t, loggedGateway{URL: s.MCP.URL, Token: "[session token]"}, got, "the gateway's fields are logged")
	}
}

// The runtime image's label lists the versions its runtime reads,
// comma-separated, and nothing else (C6's handshake).
func TestTheImagesLabelParses(t *testing.T) {
	for _, c := range []struct {
		label string
		want  []int // nil for a label that is refused
	}{
		{label: "2", want: []int{2}},
		{label: "1,2", want: []int{1, 2}},
		{label: ""},
		{label: "1,,2"},
		{label: "1, 2"},
		{label: "0"},
		{label: "a"},
		{label: "1,2,"},
	} {
		t.Run(fmt.Sprintf("%q", c.label), func(t *testing.T) {
			got, err := spec.ParseLabel(c.label)

			if c.want == nil {
				require.Error(t, err)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// A runner writes the newest version that both it and the image know, and
// none when they share no version.
func TestNewestPicksTheNewestVersionBothKnow(t *testing.T) {
	for _, c := range []struct {
		name          string
		image, writes []int
		want          int
		ok            bool
	}{
		{name: "[1 2] and [2]", image: []int{1, 2}, writes: []int{2}, want: 2, ok: true},
		{name: "[2 3] and [1 2]", image: []int{2, 3}, writes: []int{1, 2}, want: 2, ok: true},
		{name: "[1] and [2]", image: []int{1}, writes: []int{2}},
		{name: "nothing and [2]", writes: []int{2}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := spec.Newest(c.image, c.writes)

			assert.Equal(t, c.ok, ok)
			if c.ok {
				assert.Equal(t, c.want, got)
			}
		})
	}
}
