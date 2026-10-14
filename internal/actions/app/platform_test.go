package app_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

func TestAPlatformReadSelectsItsFieldFromTheRedactedOutput(t *testing.T) {
	e := newEnv(t)
	e.up.set(func(u *upstream) {
		u.replies["owners"] = text(`{"owners":[{"user":"ana@example.com"},{"user":"bo@example.com"}]}`)
	})
	pr := &config.PlatformRead{Tool: "flags.owners", Args: map[string]string{"flag": "{args.flag}", "limit": "2"}, Field: "$.owners[*].user"}

	got, err := e.gw.PlatformRead(t.Context(), app.Origin{Run: "run-1", Commit: e.rev.Commit()}, "", pr, map[string]string{"args.flag": "{evil} checkout"})

	require.NoError(t, err)
	assert.Equal(t, []any{"ana@example.com", "bo@example.com"}, got.Values)
	sent := e.up.sent()
	require.Len(t, sent, 1)
	sameBytes(t, `{"flag":"{evil} checkout","limit":2}`, sent[0].args)
	assert.Equal(t, app.Conn{Server: app.Server{Name: "flags", Endpoint: "https://flags.internal/mcp", CredentialRef: "secret://mcp/flags"}}, sent[0].conn)
	rec, _, _, _ := e.audit.Record(got.CallID)
	assert.Empty(t, rec.Agent, "the platform's own identity")
	assert.Equal(t, board.RunID("run-1"), rec.Run)
}

func TestAPlatformReadThatSelectsNothingOrOnlyRedactedValuesIsAnError(t *testing.T) {
	e := newEnv(t)
	pr := &config.PlatformRead{Tool: "owners", Field: "$.owners[*].user"}
	for _, reply := range []string{`{"owners":[]}`, `{"owners":[{"user":"-----BEGIN RSA PRIVATE KEY-----"}]}`} {
		e.up.set(func(u *upstream) { u.replies["owners"] = text(reply) })

		_, err := e.gw.PlatformRead(t.Context(), app.Origin{Commit: e.rev.Commit()}, "flags", pr, nil)

		require.ErrorIs(t, err, app.ErrNothingRead, reply)
	}
	e.up.set(func(u *upstream) {
		u.replies["owners"] = &output.Result{IsError: true, Blocks: []output.Block{{Kind: output.Text, Text: "boom"}}}
	})
	_, err := e.gw.PlatformRead(t.Context(), app.Origin{Commit: e.rev.Commit()}, "flags", pr, nil)
	require.ErrorIs(t, err, app.ErrReadFailed)
	assert.NotErrorIs(t, err, app.ErrNothingRead)
}

// A failed look is told in the gateway's own words, so a server's text never
// reaches the platform read's error.
func TestAPlatformReadsErrorNeverQuotesTheServer(t *testing.T) {
	e := newEnv(t)
	e.up.set(func(u *upstream) {
		u.listErr = errors.New("list tools: backend refused key -----BEGIN RSA PRIVATE KEY-----")
	})
	pr := &config.PlatformRead{Tool: "owners", Field: "$.owners[*].user"}

	_, err := e.gw.PlatformRead(t.Context(), app.Origin{Commit: e.rev.Commit()}, "flags", pr, nil)

	require.ErrorIs(t, err, app.ErrReadFailed)
	assert.NotContains(t, err.Error(), "PRIVATE KEY")
}
