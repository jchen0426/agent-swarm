// Command mcpgw runs the spike's stand-in MCP gateway.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/spike/runtime/mcpgw"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	outputs := flag.String("outputs", "", "a JSON file mapping read tools to their canned output")
	round := flag.Int("round", 1, "the round to start in")
	tools := flag.String("tools", "get_alert,get_recent_changes,query_logs,post_hypothesis,end_turn", "the round's tools")
	addAfter := flag.String("add-after-first-call", "", "a tool to add after the first call of round 1")
	slow := flag.Duration("slow", 30*time.Second, "how long slow_read takes")
	logPath := flag.String("log", "/out/mcp.jsonl", "file that receives one JSON line per call")
	flag.Parse()

	out, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	opts := mcpgw.Options{
		Log:               out,
		AddAfterFirstCall: *addAfter,
		Delays:            map[string]time.Duration{"slow_read": *slow},
	}
	if *outputs != "" {
		raw, err := os.ReadFile(*outputs)
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(raw, &opts.Outputs); err != nil {
			log.Fatalf("parse %s: %v", *outputs, err)
		}
	}
	gw := mcpgw.New(opts)
	gw.SetRound(*round, strings.Split(*tools, ",")...)
	srv := &http.Server{Addr: *addr, Handler: gw, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
