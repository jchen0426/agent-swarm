// Package outcome holds the wire's constants between the gateways, the
// runtime and the runner.
package outcome

import "syscall"

// Header carries a gateway's account of a reply that is not a success.
const Header = "X-Agentswarm-Outcome"

// The values of Header.
const (
	Refused     = "refused"
	Vendor      = "vendor"
	Cut         = "cut"
	Unavailable = "unavailable"
)

// The MCP endpoint's JSON-RPC error codes.
const (
	CodeSessionGone = -32001
	CodeUnavailable = -32002
)

// The exit codes of agentswarm turn. Any other code is a crash, except an
// end by one of OutsideSignals that the turn's runner did not send, and not
// for the turn's memory: only the engine, an operator or the host sends
// those, so such a turn is unavailable (Plan 7d-2, Decision 9). A container
// whose process ended by a signal exits with 128 and the signal's number.
const (
	ExitEnded       = 10
	ExitFinished    = 11
	ExitRefused     = 12
	ExitVendor      = 13
	ExitInterrupted = 14
	ExitUnavailable = 15
)

// OutsideSignals end a turn the runner did not end: SIGHUP, SIGINT, SIGKILL
// and SIGTERM.
var OutsideSignals = []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGKILL, syscall.SIGTERM}
