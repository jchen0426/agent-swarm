// Package tokens issues the tokens that name an agent's sessions at the two
// gateways (spec Section 2): a prefix for each gateway and 43 letters and
// digits from 32 random bytes. Every token has the same form, so a rule that
// applies always finds it whole, wherever an agent writes it
// (config.SessionTokenPattern).
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"math/big"
)

// Gateway names the gateway a token is for. A token of one gateway is
// unknown to the other.
type Gateway string

// The gateways.
const (
	MCP Gateway = "mcp"
	LLM Gateway = "llm"
)

const (
	// width is how many base-62 digits 32 bytes take: 62^43 > 2^256.
	width    = 43
	alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// shown is what a Secret prints as.
	shown = "[session token]"
)

// Secret is a token. It prints as [session token] however it is formatted,
// written as JSON or logged, so that no log, error or record holds it. As a
// field whose methods fmt and slog cannot call, an unexported one, it prints
// by its own field, an address. Reveal gives the token itself, for the
// runtime that sends it.
type Secret struct{ p *string }

// New issues a token for a gateway.
func New(g Gateway) Secret {
	var b [32]byte
	// crypto/rand.Read never returns an error: it fills b or crashes.
	_, _ = rand.Read(b[:])
	prefix := "aswm_"
	if g == LLM {
		prefix = "aswl_"
	}
	token := prefix + encode(b)
	return Secret{p: &token}
}

// encode writes 32 bytes as 43 base-62 digits, zeros first where the number
// is small.
func encode(b [32]byte) string {
	n, base, digit := new(big.Int).SetBytes(b[:]), big.NewInt(int64(len(alphabet))), new(big.Int)
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		n.DivMod(n, base, digit)
		out[i] = alphabet[digit.Int64()]
	}
	return string(out)
}

// Reveal returns the token itself, or nothing for the zero Secret.
func (s Secret) Reveal() string {
	if s.p == nil {
		return ""
	}
	return *s.p
}

// Hash returns the token's SHA-256, by which the gateways look it up.
func (s Secret) Hash() [32]byte { return Hash(s.Reveal()) }

// String prints the token as [session token].
func (s Secret) String() string { return shown }

// Format prints the token as [session token], whatever the verb.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, shown) }

// MarshalJSON writes the token as [session token].
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + shown + `"`), nil }

// LogValue logs the token as [session token].
func (s Secret) LogValue() slog.Value { return slog.StringValue(shown) }

// Hash returns a token's SHA-256.
func Hash(token string) [32]byte { return sha256.Sum256([]byte(token)) }
