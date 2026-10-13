package dataset_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func TestSaveThenLoadReturnsTheSameDataset(t *testing.T) {
	at := time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)
	to := "quote:0000"
	in := &dataset.Dataset{
		Meta: dataset.Meta{Namespace: "otel-demo", AlertTime: at, WindowStart: at.Add(-30 * time.Minute),
			Trigger: []dataset.Alert{{Name: "RequestErrorRate", Service: "shipping", State: "firing", ActiveAt: at}}},
		Alerts:   []dataset.Alert{{Polled: at, Name: "RequestErrorRate", Service: "shipping", State: "firing", ActiveAt: at}},
		Changes:  []dataset.Change{{Time: at, Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated", Fields: []dataset.Field{{Path: "env", To: &to}}}},
		Objects:  []dataset.Object{{Kind: "Deployment", Name: "shipping", Observed: at, Body: json.RawMessage(`{"kind":"Deployment"}`)}},
		Events:   []dataset.Event{{Time: at, Type: "Warning", Reason: "BackOff", ObjectKind: "Pod", ObjectName: "shipping-1", Message: "back-off"}},
		Logs:     []dataset.Log{{Time: at, Service: "shipping", Body: "failed"}},
		Spans:    []dataset.Span{{Time: at, TraceID: "t", SpanID: "s", Service: "shipping", Name: "/get-quote", Kind: "Server", Status: "Error"}},
		Edges:    []dataset.Edge{{From: "checkout", To: "shipping", Calls: 10, Errors: 2}},
		Services: []dataset.ServicePoint{{Time: at, Service: "shipping", Operation: "/get-quote", Kind: "server", Requests: 10, Errors: 2, P95MS: 12.5}},
		Pods:     []dataset.PodPoint{{Time: at, Pod: "shipping-1", Service: "shipping", CPUCores: 0.1, MemoryBytes: 1e6}},
	}
	dir := t.TempDir()

	require.NoError(t, in.Save(dir))
	out, err := dataset.Load(dir)

	require.NoError(t, err)
	assert.NotEmpty(t, out.Digest, "Load digests what it reads; Save writes no digest")
	out.Digest = ""
	assert.Equal(t, in, out)
}

func TestLoadFailsWithoutMeta(t *testing.T) {
	_, err := dataset.Load(t.TempDir())

	assert.Error(t, err)
}

func TestSaveWritesOneFilePerStream(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, (&dataset.Dataset{}).Save(dir))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"meta.json", "alerts.jsonl", "changes.jsonl", "objects.jsonl", "events.jsonl",
		"logs.jsonl", "spans.jsonl", "edges.jsonl", "service_metrics.jsonl", "pod_metrics.jsonl"}, names)
	meta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	require.NoError(t, err)
	assert.Contains(t, string(meta), `"alert_time"`)
}

func TestLoadDigestsWhatItReads(t *testing.T) {
	dir := t.TempDir()
	d := &dataset.Dataset{Meta: dataset.Meta{Namespace: "otel-demo"}, Logs: []dataset.Log{{Service: "cart", Body: "ok"}}}
	require.NoError(t, d.Save(dir))

	first, err := dataset.Load(dir)
	require.NoError(t, err)
	again, err := dataset.Load(dir)
	require.NoError(t, err)
	d.Logs[0].Body = "not ok"
	require.NoError(t, d.Save(dir))
	changed, err := dataset.Load(dir)
	require.NoError(t, err)

	assert.Len(t, first.Digest, 64)
	assert.Equal(t, first.Digest, again.Digest)
	assert.NotEqual(t, first.Digest, changed.Digest)
}
