package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
)

// bodies is an LLM gateway that keeps the size of each request's body it is
// given.
type bodies struct{ sizes []int }

func (b *bodies) Messages(_ context.Context, in *llmapp.Inbound) *llmapp.Reply {
	b.sizes = append(b.sizes, len(in.Body))
	return &llmapp.Reply{Status: http.StatusOK}
}

// Every listener bounds how long a request's header and body take to read
// (Decision 1), and the LLM listener reads no more of a body than the LLM
// gateway takes, from the one setting both have.
func TestServesHTTPServersBoundTheirReads(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	gw := &bodies{}

	s := newServers(nil, gw, http.NotFoundHandler(), log)

	for name, srv := range map[string]*http.Server{"mcp": s.mcp, "llm": s.llm, "admin": s.admin} {
		assert.Equal(t, 10*time.Second, srv.ReadHeaderTimeout, name)
		assert.Equal(t, 30*time.Second, srv.ReadTimeout, name)
	}
	opts := llmOptions(&deploy.Deployment{}, log)
	require.Positive(t, opts.MaxBodyBytes)
	llm := httptest.NewServer(s.llm.Handler)
	defer llm.Close()
	resp, err := http.Post(llm.URL+"/v1/messages", "application/json", bytes.NewReader(make([]byte, opts.MaxBodyBytes+100)))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, []int{opts.MaxBodyBytes + 1}, gw.sizes, "the listener reads the gateway's request size and one byte more, which the gateway refuses")
}
