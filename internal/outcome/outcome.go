// Package outcome holds the wire's constants between the gateways, the
// runtime and the runner.
package outcome

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

// The exit codes of agentswarm turn. Any other code is a crash.
const (
	ExitEnded       = 10
	ExitFinished    = 11
	ExitRefused     = 12
	ExitVendor      = 13
	ExitInterrupted = 14
	ExitUnavailable = 15
)
