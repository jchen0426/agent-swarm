package app

import (
	"fmt"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// PersonText checks and redacts a person's text, such as a reason's, as the
// board's snapshot and the records hold it (Decision 9): the person's twin of
// the MCP gateway's agentText. red holds the rules that apply always, and
// field names the text as the snapshot and the records hold it, as the member
// field=text. Text with a character a reader could miss (config.Hidden) is
// refused. So is text whose escapes may hide what red removes, alone or in
// its member, which the LLM gateway checks when it reads the snapshot
// (redact.EscapedValue): stored, it would make the gateway refuse every
// snapshot that holds it. Any other text is redacted with red, not refused
// (R1). A refusal wraps ErrInvalid, says why, and never quotes the text.
func PersonText(red *redact.Redactor, field, text string) (string, error) {
	if strings.ContainsFunc(text, config.Hidden) {
		return "", fmt.Errorf("%w: the text holds a character a reader could miss, such as a zero-width space or a bidi control; write it plainly", ErrInvalid)
	}
	if red.Escaped(text) || red.Escaped(field+"="+text) {
		return "", fmt.Errorf("%w: the text holds an escape that may hide what the redaction rules remove; write it plainly", ErrInvalid)
	}
	return red.Text(text), nil
}
