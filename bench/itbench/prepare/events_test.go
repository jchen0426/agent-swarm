package prepare

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func eventRow(uid, ns, kind, name, reason, message, first, last string, count int) string {
	return `{"object":{"apiVersion":"v1","kind":"Event","metadata":{"uid":"` + uid + `","namespace":"` + ns + `"},
 "involvedObject":{"kind":"` + kind + `","name":"` + name + `","namespace":"` + ns + `"},"reason":"` + reason + `","message":"` + message + `",
 "type":"Warning","count":` + strconv.Itoa(count) + `,"firstTimestamp":"` + first + `","lastTimestamp":"` + last + `","source":{"component":"kubelet"}},"type":"ADDED"}`
}

func TestEventsKeepTheLatestStateOfEachCoreEventInTheWindow(t *testing.T) {
	fsys := files("k8s_events_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:09.1", eventRow("u1", "otel-demo", "Pod", "pc-1", "Failed", `Failed to pull image \"quay.io/it-bench/hello-bench-invalid:1.0.0\"`, "2025-12-15T17:50:09Z", "2025-12-15T17:50:09Z", 1)},
		tsvRow{"2025-12-15 17:52:09.1", eventRow("u1", "otel-demo", "Pod", "pc-1", "Failed", `Failed to pull image \"quay.io/it-bench/hello-bench-invalid:1.0.0\"`, "2025-12-15T17:50:09Z", "2025-12-15T17:52:09Z", 3)},
		tsvRow{"2025-12-15 17:56:09.1", eventRow("u1", "otel-demo", "Pod", "pc-1", "Failed", `Failed to pull image`, "2025-12-15T17:50:09Z", "2025-12-15T17:56:09Z", 5)},
		tsvRow{"2025-12-15 17:50:10.1", `{"object":{"apiVersion":"events.k8s.io/v1","kind":"Event","metadata":{"uid":"u2","namespace":"otel-demo"},"regarding":{"kind":"Pod","name":"pc-1","namespace":"otel-demo"},"reason":"Failed","note":"dup"},"type":"ADDED"}`},
		tsvRow{"2025-12-15 17:50:11.1", eventRow("u3", "chaos-mesh", "StressChaos", "s", "Applied", "x", "2025-12-15T17:50:11Z", "2025-12-15T17:50:11Z", 1)},
		tsvRow{"2025-12-15 17:10:11.1", eventRow("u4", "otel-demo", "Pod", "old-1", "BackOff", "old run", "2025-12-15T15:10:11Z", "2025-12-15T17:10:11Z", 1)},
	))

	events, err := readEvents(fsys, "k8s_events_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 25, 50, 0, time.UTC), time.Date(2025, 12, 15, 17, 55, 50, 0, time.UTC))

	require.NoError(t, err)
	assert.Equal(t, []dataset.Event{{
		Time: time.Date(2025, 12, 15, 17, 52, 9, 0, time.UTC), First: time.Date(2025, 12, 15, 17, 50, 9, 0, time.UTC),
		Type: "Warning", Reason: "Failed", ObjectKind: "Pod", ObjectName: "pc-1", Count: 3, Source: "kubelet",
		Message: `Failed to pull image "quay.io/open-telemetry/demo-product-catalog:1.0.0"`,
	}}, events)
}
