// Command itbench prepares ITBench-Lite scenarios for the mechanism check,
// serves them through read-only MCP tools, and scores answers by key.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
