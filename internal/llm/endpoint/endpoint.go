// Package endpoint is the LLM gateway's agent-facing endpoint: the base URL
// a runtime's Messages API client is given (spec Section 2). Each request
// carries the session's token, as the client's API key or as a bearer
// token; the token names the run and the agent, and nothing in a request's
// content does. The endpoint reads a request and leaves every decision to
// the gateway, which answers it.
package endpoint

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/outcome"
)

// Gateway is what the endpoint serves.
type Gateway interface {
	Messages(ctx context.Context, in *app.Inbound) *app.Reply
}

// New returns the endpoint's HTTP handler. It reads at most maxBodyBytes
// and one byte more of a request, so that the gateway can refuse a larger
// one. Every reply that is not a success carries outcome.Header: the
// gateway's replies carry the marker it gave them, and a request the
// endpoint could not read is its own failure.
func New(gw Gateway, maxBodyBytes int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Api-Key")
		if token == "" {
			token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBodyBytes)+1))
		if err != nil {
			w.Header().Set(outcome.Header, outcome.Unavailable)
			http.Error(w, "the request could not be read", http.StatusBadRequest)
			return
		}
		reply := gw.Messages(r.Context(), &app.Inbound{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Token: token, Header: r.Header, Body: body, Length: r.ContentLength,
		})
		for name, value := range reply.Header {
			w.Header().Set(name, value)
		}
		w.WriteHeader(reply.Status)
		_, _ = w.Write(reply.Body) //nolint:gosec // G705: a JSON answer to an API client, served as application/json, never a page
	})
}
