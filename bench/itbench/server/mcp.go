package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// New returns an MCP server whose tools read the dataset and nothing else.
// Agents see its name and instructions, so neither names the benchmark.
func New(d *dataset.Dataset) *mcp.Server {
	t := NewTools(d)
	s := mcp.NewServer(&mcp.Implementation{Name: "observability", Version: "1"}, &mcp.ServerOptions{
		Instructions: fmt.Sprintf("Read-only views of namespace %s. Data starts at %s and ends at the alert time, %s. Times are RFC 3339 in UTC.",
			d.Meta.Namespace, d.Meta.WindowStart.Format(time.RFC3339), d.Meta.AlertTime.Format(time.RFC3339)),
	})
	add(s, "list_alerts", "List the alerts firing at the last poll at or before a time; polls are a minute apart.", t.ListAlerts)
	add(s, "list_services", "List the services, with the calls between them seen in traces. traces_until says where the traces end.", t.ListServices)
	add(s, "get_changes", "List changes to Kubernetes objects' desired state, newest first, with the fields each changed.", t.GetChanges)
	add(s, "get_events", "List Kubernetes events by their last occurrence, newest first.", t.GetEvents)
	add(s, "query_logs", "Search one service's logs; returns the most recent matches, oldest first.", t.QueryLogs)
	add(s, "get_error_spans", "List trace spans that ended in error, newest first. traces_until says where the traces end.", t.GetErrorSpans)
	add(s, "get_service_metrics", "Return a service's requests, errors and p95 latency per minute. The counters move about once a minute, so a minute can read zero and the next one double; compare several minutes.", t.GetServiceMetrics)
	add(s, "get_pod_metrics", "Return pods' CPU and memory use and limits per minute.", t.GetPodMetrics)
	add(s, "list_objects", "List the Kubernetes objects of one kind as last observed, with a status line for pods.", t.ListObjects)
	add(s, "get_object", "Return one Kubernetes object as last observed; snapshots are about five minutes apart.", t.GetObject)
	return s
}

func add[In, Out any](s *mcp.Server, name, description string, fn func(*In) (Out, error)) {
	tool := &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}}
	mcp.AddTool(s, tool, func(_ context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := fn(&in)
		return nil, out, err
	})
}

// Handler serves the tools over streamable HTTP at /mcp, and a health check
// at /healthz.
func Handler(d *dataset.Dataset) http.Handler {
	s := New(d)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(w, "ok")
		case "/mcp":
			streamable.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
