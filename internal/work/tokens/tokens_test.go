package tokens_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// A token is its gateway's prefix and 43 letters and digits, from 32 random
// bytes, so every token has the same width; no two are alike.
func TestATokenIsItsPrefixAndFortyThreeLettersAndDigits(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range []struct {
		gateway tokens.Gateway
		form    string
	}{{tokens.MCP, `^aswm_[A-Za-z0-9]{43}$`}, {tokens.LLM, `^aswl_[A-Za-z0-9]{43}$`}} {
		for range 500 {
			s := tokens.New(c.gateway)

			assert.Regexp(t, c.form, s.Reveal())
			assert.False(t, seen[s.Reveal()])
			seen[s.Reveal()] = true
		}
	}
	assert.Equal(t, strings.Repeat("0", 43), tokens.Encode([32]byte{}), "leading zeros keep the width")
	assert.Equal(t, "0000000000000000000000000000000000000000001", tokens.Encode([32]byte{31: 1}))
	full := [32]byte{}
	for i := range full {
		full[i] = 0xff
	}
	assert.Len(t, tokens.Encode(full), 43, "2^256 - 1 fits in 43 digits of base 62")
}

// A token never prints: as a Secret it shows as [session token] however it
// is formatted, written as JSON, or logged.
func TestASecretNeverPrints(t *testing.T) {
	s := tokens.New(tokens.LLM)
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("issued", "token", s)
	raw, err := json.Marshal(map[string]any{"token": s})
	require.NoError(t, err)

	for _, shown := range []string{
		fmt.Sprintf("%s %v %+v %#v %q", s, s, s, s, s), string(raw), logged.String(), s.String(),
	} {
		assert.NotContains(t, shown, s.Reveal())
		assert.Contains(t, shown, "[session token]")
	}
	assert.Equal(t, tokens.Hash(s.Reveal()), s.Hash())
}

// A token never prints as a field of another struct either, exported or
// not: fmt and slog print a field they cannot call methods on, an
// unexported one, by its own fields, which hold no more than an address.
func TestASecretInAFieldNeverPrints(t *testing.T) {
	s := tokens.New(tokens.MCP)
	held := struct {
		agent string
		token tokens.Secret
		Token tokens.Secret
	}{"release-investigator", s, s}
	body := s.Reveal()[len("aswm_"):]
	var text, logged bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("held", "value", held)
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("held", "value", held)

	for _, shown := range []string{
		fmt.Sprintf("%v", held), fmt.Sprintf("%+v", held), fmt.Sprintf("%#v", held),
		fmt.Sprintf("%v", []any{held}), fmt.Sprintf("%v", &held), text.String(), logged.String(),
	} {
		assert.NotContains(t, shown, body)
	}
}

// Every token matches the rule both gateways redact with, glued to other
// text or not, and the replacement is the one no rule may change.
func TestEveryTokenMatchesTheRuleThatHidesIt(t *testing.T) {
	red := redact.New(redact.SessionTokens())
	for _, g := range []tokens.Gateway{tokens.MCP, tokens.LLM} {
		s := tokens.New(g)

		assert.Equal(t, "key "+config.SessionTokenReplacement+" here", red.Text("key "+s.Reveal()+" here"))
		assert.Equal(t, "x"+config.SessionTokenReplacement+"y", red.Text("x"+s.Reveal()+"y"), "no word boundary to defeat")
	}
}
