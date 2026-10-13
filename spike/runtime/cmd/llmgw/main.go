// Command llmgw runs the spike's stand-in LLM gateway: scripted by default,
// or forwarding to -upstream with the key in its own ANTHROPIC_API_KEY.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/jchen0426/agent-swarm/spike/runtime/llmgw"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	script := flag.String("script", "", "scripted mode: a JSON file holding an array of turns")
	upstream := flag.String("upstream", "", "forward mode: the real API base URL")
	logPath := flag.String("log", "/out/llm.jsonl", "file that receives one JSON line per request")
	flag.Parse()

	out, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	opts := llmgw.Options{Log: out}
	if *script != "" {
		raw, err := os.ReadFile(*script)
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(raw, &opts.Script); err != nil {
			log.Fatalf("parse %s: %v", *script, err)
		}
	}
	if *upstream != "" {
		if opts.Upstream, err = url.Parse(*upstream); err != nil {
			log.Fatal(err)
		}
		if opts.APIKey = os.Getenv("ANTHROPIC_API_KEY"); opts.APIKey == "" {
			log.Fatal("forward mode needs ANTHROPIC_API_KEY")
		}
	}
	srv := &http.Server{Addr: *addr, Handler: llmgw.New(&opts), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
