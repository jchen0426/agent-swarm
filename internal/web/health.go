package web

import (
	"io"
	"log/slog"
	"net/http"
)

// health serves the health routes (design D2; R88): /healthz, /readyz and
// /leaderz. They read no cookie and set none, are exempt from the Host
// check, since a load balancer's probe carries the target's address as its
// Host, and are served beside the pages, so that they never answer the
// session key's 503 (m16).
type health struct {
	p   Ports
	log *slog.Logger
}

// healthz answers 200 while the process is up.
func (h *health) healthz(w http.ResponseWriter, _ *http.Request) { text(w, http.StatusOK, "ok") }

// readyz answers as the admin API's /readyz does: whether the instance is
// ready, the process up, the schema compatible, and serve not draining. A
// standby is ready.
func (h *health) readyz(w http.ResponseWriter, r *http.Request) {
	if _, _, draining := h.p.Leader.Status(); draining {
		text(w, http.StatusServiceUnavailable, "draining")
		return
	}
	switch compatible, state, err := h.p.Schema.State(r.Context()); {
	case err != nil:
		h.log.WarnContext(r.Context(), "the web interface could not read the schema", "err", err)
		text(w, http.StatusServiceUnavailable, "the schema could not be read")
	case !compatible:
		text(w, http.StatusServiceUnavailable, "the schema is "+state)
	default:
		text(w, http.StatusOK, "ready")
	}
}

// leaderz answers 200 while this instance holds the lease, the drain
// included, and 503 otherwise: a load balancer routes to the instance whose
// /leaderz is 200, and keeps routing to a draining leader, whose writes are
// still recorded, until the next one takes over.
func (h *health) leaderz(w http.ResponseWriter, _ *http.Request) {
	if _, held, _ := h.p.Leader.Status(); held {
		text(w, http.StatusOK, "leading")
		return
	}
	text(w, http.StatusServiceUnavailable, "standby")
}

// text answers with a plain-text body.
func text(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body) //nolint:gosec // G705: the interface's own words, served as text/plain with nosniff
}
