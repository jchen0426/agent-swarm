package prepare

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// columns renders a table with the given header and rows, quoting fields as pandas does.
func columns(header []string, rows ...[]string) string {
	lines := []string{strings.Join(header, "\t")}
	for _, r := range rows {
		quoted := make([]string, len(r))
		for i, f := range r {
			if strings.ContainsAny(f, "\"\t\n") {
				f = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
			}
			quoted[i] = f
		}
		lines = append(lines, strings.Join(quoted, "\t"))
	}
	return strings.Join(lines, "\n") + "\n"
}

func resource(ns, pod string) string {
	return `{'k8s.namespace.name': '` + ns + `', 'k8s.pod.name': '` + pod + `', 'service.name': 'x'}`
}

var (
	windowFrom = time.Date(2025, 12, 15, 17, 27, 59, 0, time.UTC)
	windowTo   = time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)
)

func TestLogsKeepTheNamespaceInTheWindow(t *testing.T) {
	header := []string{"Timestamp", "TraceId", "SeverityText", "SeverityNumber", "ServiceName", "Body", "ResourceAttributes"}
	fsys := files("otel_logs_raw.tsv", columns(header,
		[]string{"2025-12-15 17:50:00.5", "abc", "INFO", "9", "shipping", "failed to get quote:\n invalid uri", resource("otel-demo", "shipping-1")},
		[]string{"2025-12-15 17:50:01.0", "", "", "0", "otel-collector", "noise", resource("opentelemetry-collectors", "c-1")},
		[]string{"2025-12-15 17:58:00.0", "", "INFO", "9", "shipping", "after the alert", resource("otel-demo", "shipping-1")},
		[]string{"2025-12-15 17:20:00.0", "", "INFO", "9", "shipping", "before the window", resource("otel-demo", "shipping-1")},
	))

	logs, err := readLogs(fsys, "otel_logs_raw.tsv", "otel-demo", windowFrom, windowTo)

	require.NoError(t, err)
	assert.Equal(t, []dataset.Log{{
		Time: time.Date(2025, 12, 15, 17, 50, 0, 500000000, time.UTC), Service: "shipping", Pod: "shipping-1",
		Severity: "INFO", Body: "failed to get quote:\n invalid uri", TraceID: "abc",
	}}, logs)
}

func spanRow(at, trace, span, parent, service, name, kind, status, message, durationNS string) []string {
	return []string{at, trace, span, parent, name, kind, service, resource("otel-demo", service+"-1"),
		`{'http.response.status_code': '500', 'server.address': 'shipping'}`, durationNS, status, message,
		`['exception']`, `[{'exception.message': 'boom', 'exception.stacktrace': 'long'}]`}
}

func TestSpansKeepErrorsAndCountEveryCallBetweenServices(t *testing.T) {
	header := []string{"Timestamp", "TraceId", "SpanId", "ParentSpanId", "SpanName", "SpanKind", "ServiceName", "ResourceAttributes",
		"SpanAttributes", "Duration", "StatusCode", "StatusMessage", "Events.Name", "Events.Attributes"}
	fsys := files("otel_traces_raw.tsv", columns(header,
		spanRow("2025-12-15 17:50:00.0", "t1", "c1", "", "checkout", "POST shipping", "Client", "Unset", "", "2000000"),
		spanRow("2025-12-15 17:50:00.1", "t1", "s1", "c1", "shipping", "/get-quote", "Server", "Error", "Internal Server Error", "1500000"),
		spanRow("2025-12-15 17:50:01.0", "t2", "c2", "", "checkout", "POST shipping", "Client", "Unset", "", "2000000"),
		spanRow("2025-12-15 17:50:01.1", "t2", "s2", "c2", "shipping", "/get-quote", "Server", "Unset", "", "1000000"),
		spanRow("2025-12-15 17:59:00.0", "t3", "s3", "", "shipping", "/get-quote", "Server", "Error", "late", "1000000"),
	))

	spans, edges, until, err := readSpans(fsys, "otel_traces_raw.tsv", "otel-demo", windowFrom, windowTo)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 50, 1, 100000000, time.UTC), until)
	require.Len(t, spans, 1)
	assert.Equal(t, dataset.Span{
		Time: time.Date(2025, 12, 15, 17, 50, 0, 100000000, time.UTC), TraceID: "t1", SpanID: "s1", ParentID: "c1",
		Service: "shipping", Name: "/get-quote", Kind: "Server", DurationMS: 1.5, Status: "Error", Message: "Internal Server Error",
		Attributes: map[string]string{"http.response.status_code": "500", "server.address": "shipping", "exception.message": "boom"},
	}, spans[0])
	assert.Equal(t, []dataset.Edge{{From: "checkout", To: "shipping", Calls: 2, Errors: 1}}, edges)
}
