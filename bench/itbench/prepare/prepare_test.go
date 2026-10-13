package prepare

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// scenario is a small downloaded scenario with every table, whose log body
// is logBody.
func scenario(logBody string) fstest.MapFS {
	injected := helmWrite + `,{"manager":"OpenAPI-Generator","operation":"Update","time":"2025-12-15T17:52:47Z"}`
	return files(
		"alerts/alerts_in_alerting_state_2025-12-15T175759.000000Z.json",
		`{"data": {"alerts": [`+sprintfAlert("shipping", "2025-12-15T17:55:00Z")+`]}, "timestamp": "2025-12-15T17:57:59Z"}`,
		"alerts/alerts_at_2025-12-15T17-56-35.000000.json", `[`+sprintfAlert("shipping", "2025-12-15T17:55:00Z")+`]`,
		"k8s_objects_raw.tsv", table(
			tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
			tsvRow{"2025-12-15 17:55:41.1", deployment("quote:0000", injected)},
		),
		"k8s_events_raw.tsv", table(
			tsvRow{"2025-12-15 17:53:00.1", eventRow("u1", "otel-demo", "Pod", "shipping-1", "BackOff", "back-off", "2025-12-15T17:53:00Z", "2025-12-15T17:53:00Z", 1)},
		),
		"otel_logs_raw.tsv", columns([]string{"Timestamp", "TraceId", "SeverityText", "SeverityNumber", "ServiceName", "Body", "ResourceAttributes"},
			[]string{"2025-12-15 17:50:00.5", "", "INFO", "9", "shipping", logBody, resource("otel-demo", "shipping-1")},
		),
		"otel_traces_raw.tsv", columns([]string{"Timestamp", "TraceId", "SpanId", "ParentSpanId", "SpanName", "SpanKind", "ServiceName",
			"ResourceAttributes", "SpanAttributes", "Duration", "StatusCode", "StatusMessage", "Events.Name", "Events.Attributes"},
			spanRow("2025-12-15 17:50:00.0", "t1", "c1", "", "checkout", "POST shipping", "Client", "Unset", "", "2000000"),
			spanRow("2025-12-15 17:50:00.1", "t1", "s1", "c1", "shipping", "/get-quote", "Server", "Error", "Internal Server Error", "1500000"),
		),
		"metrics/service_shipping_raw.tsv", columns([]string{"metric_name", "timestamp", "value", "service_name", "namespace", "bucket_le", "tags", "metric_type", "status_code"},
			bucketRow("2025-12-15 17:50:00.0", "+Inf", "STATUS_CODE_UNSET", "10"),
			bucketRow("2025-12-15 17:51:00.0", "+Inf", "STATUS_CODE_UNSET", "20"),
		),
		"metrics/pod_shipping-75d58f5d84-gw77q_raw.tsv", columns([]string{"metric_name", "timestamp", "value", "pod_name", "namespace", "tags"},
			podRow("node_namespace_pod_container:container_memory_working_set_bytes", "2025-12-15 17:50:16.2", "valkey-cart", "3000000"),
		),
	)
}

func sprintfAlert(service, activeAt string) string {
	return `{"labels": {"alertname": "RequestErrorRate", "namespace": "otel-demo", "service_name": "` + service + `"},
 "annotations": {}, "state": "firing", "activeAt": "` + activeAt + `"}`
}

func TestBuildPreparesEveryStream(t *testing.T) {
	d, err := build(scenario("failed to get quote"), "otel-demo")

	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC), d.Meta.AlertTime)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 27, 59, 0, time.UTC), d.Meta.WindowStart)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 50, 0, 100000000, time.UTC), d.Meta.TracesUntil)
	assert.Len(t, d.Meta.Trigger, 1)
	assert.Len(t, d.Alerts, 1)
	require.Len(t, d.Changes, 1)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 52, 47, 0, time.UTC), d.Changes[0].Time)
	assert.Len(t, d.Objects, 1)
	assert.Len(t, d.Events, 1)
	assert.Len(t, d.Logs, 1)
	assert.Len(t, d.Spans, 1)
	assert.Equal(t, []dataset.Edge{{From: "checkout", To: "shipping", Calls: 1, Errors: 1}}, d.Edges)
	assert.Len(t, d.Services, 1)
	assert.Len(t, d.Pods, 1)
}

func TestRunWritesTheDataset(t *testing.T) {
	raw, out := t.TempDir(), filepath.Join(t.TempDir(), "S16")
	require.NoError(t, os.CopyFS(raw, scenario("failed to get quote")))

	d, err := Run(raw, out, "otel-demo")

	require.NoError(t, err)
	loaded, err := dataset.Load(out)
	require.NoError(t, err)
	assert.Equal(t, d.Changes, loaded.Changes)
}

func TestRunWritesNothingWhenTheDatasetWouldLeak(t *testing.T) {
	raw, out := t.TempDir(), filepath.Join(t.TempDir(), "S16")
	require.NoError(t, os.CopyFS(raw, scenario("StressChaos applied to valkey-cart")))

	_, err := Run(raw, out, "otel-demo")

	require.ErrorContains(t, err, `logs: "chaos"`)
	assert.NoDirExists(t, out)
}

func TestLeaksFindsForbiddenStringsInAnyStream(t *testing.T) {
	d := &dataset.Dataset{
		Logs:   []dataset.Log{{Time: time.Unix(0, 0), Service: "cart", Body: "pulled quay.io/IT-Bench/collector"}},
		Events: []dataset.Event{{Reason: "Applied", Message: "StressChaos applied"}},
		Spans:  []dataset.Span{{Attributes: map[string]string{"k": "fine"}}},
	}

	leaks := Leaks(d)

	assert.Len(t, leaks, 2)
	assert.Contains(t, leaks[0], `events: "chaos"`)
	assert.Contains(t, leaks[1], `logs: "it-bench"`)
	assert.Empty(t, Leaks(&dataset.Dataset{Logs: []dataset.Log{{Body: "InvalidImageName"}}}))
}
