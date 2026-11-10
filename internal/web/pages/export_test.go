package pages

import (
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
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

// SetPreviewTimeout sets the bound of the on's preview on the control's
// page, and returns the function that restores it.
func SetPreviewTimeout(d time.Duration) (restore func()) {
	old := previewTimeout
	previewTimeout = d
	return func() { previewTimeout = old }
}

// InertText and InertLine are the two display functions for stored text
// (design D11; R154).
var (
	InertText = inertText
	InertLine = inertLine
)

// SetBoardWait sets the bound of a board read through the watch (R141), and
// returns the function that restores it.
func SetBoardWait(d time.Duration) (restore func()) {
	old := boardWait
	boardWait = d
	return func() { boardWait = old }
}

// SetMonotonic sets the clock of the windows the sites made after it keep,
// the board pages' read of the lease's holder and a board read's failure
// logged once a minute, and returns the function that restores it.
func SetMonotonic(f func() time.Time) (restore func()) {
	old := monotonic
	monotonic = f
	return func() { monotonic = old }
}

// RegionNames are the board page's regions' ids, in the page's order: the
// page's own list, which the tests read and never change.
var RegionNames = regionNames

// SetVisible sets the board's visibility seam, and returns the function
// that restores it.
func SetVisible(f func(*app.View) bool) (restore func()) {
	old := visible
	visible = f
	return func() { visible = old }
}

// BoardRoutes are the paths of every route registered that shows a board.
func BoardRoutes() []string {
	s := newSite(&web.Ports{}, &auth.Authenticator{}, nil)
	var out []string
	for _, r := range s.routes() {
		if r.board {
			_, p, _ := strings.Cut(r.pattern, " ")
			out = append(out, p)
		}
	}
	return out
}
