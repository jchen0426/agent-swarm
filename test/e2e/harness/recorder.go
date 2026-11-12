//go:build e2e

package harness

import _ "embed"

const (
	// gateWorld is the isolated world the recorder runs in and Eval
	// evaluates in, and gateBinding the binding exposed to it alone (R183),
	// which recorder.js calls by this name.
	gateWorld   = "gate"
	gateBinding = "gateRecord"
)

// recorderJS is the recorder (R183), which runs in every new document in
// the gate world, and reports through the binding exposed to that world
// alone, each record stamped with the page's Date.now(): the document's
// start, once its parsing ends and before its deferred scripts run, with
// #board's data-order and data-etag; visibilitychange; pageshow with
// persisted; focusin's target; each write to #announce, counted by a
// MutationObserver; the bar's text; and each region that live.js replaces
// or updates in place. It reads the page, and writes to no DOM sink.
//
//go:embed recorder.js
var recorderJS string
