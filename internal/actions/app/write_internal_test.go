package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
)

// A proposal's hash tells apart arguments that differ, also when they hold an
// escape that may hide what a rule matches: it is made of the redacted
// arguments and not of the record's copy, which is null then. Otherwise the
// board would take the second of two such proposals for a repeat of the
// first.
func TestArgsHashTellsApartArgumentsThatHoldAnEscape(t *testing.T) {
	red := redact.New(redact.SessionTokens())
	underscore := fmt.Sprintf(`\u%04x`, '_')
	body := "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg"
	one := map[string]any{"limit": "aswl" + underscore + body}
	two := map[string]any{"limit": "aswl" + underscore + strings.ToLower(body)}
	require.True(t, red.EscapedValue(red.Value(one)), "the arguments hide a token behind an escape")
	require.Equal(t, "null", string(redactArgs(red, one)), "so a record keeps none of them")

	assert.NotEqual(t, argsHash(red, one), argsHash(red, two))
	assert.Equal(t, argsHash(red, one), argsHash(red, map[string]any{"limit": "aswl" + underscore + body}), "arguments that repeat each other hash alike")
}
