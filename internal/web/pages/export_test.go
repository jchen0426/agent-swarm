package pages

import (
	"strings"

	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
)

// PostRoutes are the paths of every route registered that takes a POST: a
// pattern of method POST, whatever its flag says, and a route with no
// method that is flagged as taking one, as sign-out is.
func PostRoutes() []string {
	s := newSite(&web.Ports{}, &auth.Authenticator{}, nil)
	var out []string
	for _, r := range s.routes() {
		if r.post || strings.HasPrefix(r.pattern, "POST ") {
			path := r.pattern
			if _, p, found := strings.Cut(r.pattern, " "); found {
				path = p
			}
			out = append(out, path)
		}
	}
	return out
}
