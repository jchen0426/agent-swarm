package prepare

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func bucketRow(at, le, status, value string) []string {
	tags := `{'exported_instance': 'i-1', 'le': '` + le + `', 'service_name': 'shipping', 'span_kind': 'SPAN_KIND_SERVER', 'span_name': '/get-quote', 'status_code': '` + status + `'}`
	return []string{"traces_span_metrics_duration_milliseconds_bucket", at, value, "shipping", "otel-demo", le, tags, "duration_p95", ""}
}

func TestServiceMetricsAreIncreasesPerMinuteWithAP95(t *testing.T) {
	header := []string{"metric_name", "timestamp", "value", "service_name", "namespace", "bucket_le", "tags", "metric_type", "status_code"}
	var rows [][]string
	for _, b := range []struct{ le, status, before, after string }{
		{"10.0", "STATUS_CODE_UNSET", "100", "110"}, {"100.0", "STATUS_CODE_UNSET", "100", "130"},
		{"1000.0", "STATUS_CODE_UNSET", "100", "140"}, {"+Inf", "STATUS_CODE_UNSET", "100", "140"},
		{"10.0", "STATUS_CODE_ERROR", "0", "0"}, {"100.0", "STATUS_CODE_ERROR", "0", "4"},
		{"1000.0", "STATUS_CODE_ERROR", "0", "4"}, {"+Inf", "STATUS_CODE_ERROR", "0", "4"},
	} {
		rows = append(rows, bucketRow("2025-12-15 17:50:00.0", b.le, b.status, b.before), bucketRow("2025-12-15 17:51:00.0", b.le, b.status, b.after))
	}
	rows = append(rows, bucketRow("2025-12-15 17:58:30.0", "+Inf", "STATUS_CODE_UNSET", "999"))
	fsys := files("service.tsv", columns(header, rows...))

	points, err := readServiceMetrics(fsys, "service.tsv", windowFrom, windowTo)

	require.NoError(t, err)
	assert.Equal(t, []dataset.ServicePoint{{
		Time: time.Date(2025, 12, 15, 17, 51, 0, 0, time.UTC), Service: "shipping", Operation: "/get-quote", Kind: "server",
		Requests: 44, Errors: 4, P95MS: 802,
	}}, points)
}

func TestACounterResetCountsFromZero(t *testing.T) {
	assert.InDelta(t, 3.0, increase(10, 3), 1e-9)
	assert.InDelta(t, 5.0, increase(10, 15), 1e-9)
}

func podRow(metric, at, container, value string) []string {
	tags := `{'container': '` + container + `', 'namespace': 'otel-demo', 'pod': 'valkey-cart-58df56c79c-4cvft'}`
	if container == "" {
		tags = `{'namespace': 'otel-demo', 'pod': 'valkey-cart-58df56c79c-4cvft'}`
	}
	return []string{metric, at, value, "valkey-cart-58df56c79c-4cvft", "otel-demo", tags}
}

func TestPodMetricsSumContainersAndKeepTheMinutesLastSample(t *testing.T) {
	header := []string{"metric_name", "timestamp", "value", "pod_name", "namespace", "tags"}
	const (
		cpu = "node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate"
		mem = "node_namespace_pod_container:container_memory_working_set_bytes"
		lim = "cluster:namespace:pod_memory:active:kube_pod_container_resource_limits"
	)
	fsys := files("pod.tsv", columns(header,
		podRow(cpu, "2025-12-15 17:50:00.6", "valkey-cart", "0.002"),
		podRow(cpu, "2025-12-15 17:50:30.6", "valkey-cart", "0.004"),
		podRow(cpu, "2025-12-15 17:50:30.6", "", "0.5"),
		podRow(mem, "2025-12-15 17:50:16.2", "valkey-cart", "3000000"),
		podRow(mem, "2025-12-15 17:50:16.2", "POD", "225280"),
		podRow(lim, "2025-12-15 17:50:12.3", "valkey-cart", "10485760"),
		podRow(lim, "2025-12-15 17:50:12.3", "valkey-cart", "10485760"),
	))

	points, err := readPodMetrics(fsys, "pod.tsv", windowFrom, windowTo)

	require.NoError(t, err)
	assert.Equal(t, []dataset.PodPoint{{
		Time: time.Date(2025, 12, 15, 17, 50, 0, 0, time.UTC), Pod: "valkey-cart-58df56c79c-4cvft", Service: "valkey-cart",
		CPUCores: 0.004, MemoryBytes: 3000000, MemoryLimit: 10485760,
	}}, points)
}

func TestMetricsSkipEmptyValues(t *testing.T) {
	fsys := files(
		"pod.tsv", columns([]string{"metric_name", "timestamp", "value", "pod_name", "namespace", "tags"},
			podRow("node_namespace_pod_container:container_memory_working_set_bytes", "2025-12-15 17:50:16.2", "valkey-cart", "")),
		"service.tsv", columns([]string{"metric_name", "timestamp", "value", "service_name", "namespace", "bucket_le", "tags", "metric_type", "status_code"},
			bucketRow("2025-12-15 17:50:00.0", "+Inf", "STATUS_CODE_UNSET", "")),
	)

	pods, err := readPodMetrics(fsys, "pod.tsv", windowFrom, windowTo)
	require.NoError(t, err)
	assert.Empty(t, pods)
	services, err := readServiceMetrics(fsys, "service.tsv", windowFrom, windowTo)
	require.NoError(t, err)
	assert.Empty(t, services)
}

var serviceHeader = []string{"metric_name", "timestamp", "value", "service_name", "namespace", "bucket_le", "tags", "metric_type", "status_code"}

func servicePoints(t *testing.T, rows ...[]string) []dataset.ServicePoint {
	t.Helper()
	points, err := readServiceMetrics(files("service.tsv", columns(serviceHeader, rows...)), "service.tsv", windowFrom, windowTo)
	require.NoError(t, err)
	return points
}

// The counters move once a minute, so a minute that the alert cuts short
// would read as a drop in traffic.
func TestServiceMetricsKeepOnlyCompleteMinutes(t *testing.T) {
	points := servicePoints(t,
		bucketRow("2025-12-15 17:55:30.0", "+Inf", "STATUS_CODE_UNSET", "100"),
		bucketRow("2025-12-15 17:56:30.0", "+Inf", "STATUS_CODE_UNSET", "110"),
		bucketRow("2025-12-15 17:57:30.0", "+Inf", "STATUS_CODE_UNSET", "125"),
	)

	require.Len(t, points, 1, "17:57 ends after the alert at 17:57:59")
	assert.Equal(t, time.Date(2025, 12, 15, 17, 56, 0, 0, time.UTC), points[0].Time)
	assert.InDelta(t, 10.0, points[0].Requests, 1e-9)
}

func TestDuplicateSamplesAddNothing(t *testing.T) {
	points := servicePoints(t,
		bucketRow("2025-12-15 17:50:00.0", "+Inf", "STATUS_CODE_UNSET", "100"),
		bucketRow("2025-12-15 17:50:00.0", "+Inf", "STATUS_CODE_UNSET", "100"),
		bucketRow("2025-12-15 17:51:00.0", "+Inf", "STATUS_CODE_UNSET", "110"),
		bucketRow("2025-12-15 17:51:00.0", "+Inf", "STATUS_CODE_UNSET", "110"),
	)

	require.Len(t, points, 1, "a repeated first sample is not a minute of zero traffic")
	assert.Equal(t, time.Date(2025, 12, 15, 17, 51, 0, 0, time.UTC), points[0].Time)
	assert.InDelta(t, 10.0, points[0].Requests, 1e-9)
}

// A series that appears after the table's first scrape is a new counter, so
// its first value is new: the first errors of a fault are in it.
func TestANewSeriesCountsItsFirstSample(t *testing.T) {
	points := servicePoints(t,
		bucketRow("2025-12-15 17:50:00.0", "+Inf", "STATUS_CODE_UNSET", "100"),
		bucketRow("2025-12-15 17:51:00.0", "+Inf", "STATUS_CODE_UNSET", "110"),
		bucketRow("2025-12-15 17:52:00.0", "+Inf", "STATUS_CODE_UNSET", "120"),
		bucketRow("2025-12-15 17:53:00.0", "+Inf", "STATUS_CODE_UNSET", "130"),
		bucketRow("2025-12-15 17:52:00.0", "+Inf", "STATUS_CODE_ERROR", "4"),
		bucketRow("2025-12-15 17:53:00.0", "+Inf", "STATUS_CODE_ERROR", "6"),
	)

	errors := map[string]float64{}
	for _, p := range points {
		errors[p.Time.Format("15:04")] = p.Errors
	}
	assert.Equal(t, map[string]float64{"17:51": 0, "17:52": 4, "17:53": 2}, errors)
}

// A table that starts inside the window starts mid-minute: its first minute
// has no scrape before it, so it is partial too.
func TestTheTablesFirstMinuteIsLeftOut(t *testing.T) {
	points := servicePoints(t,
		bucketRow("2025-12-15 17:50:20.0", "+Inf", "STATUS_CODE_UNSET", "100"),
		bucketRow("2025-12-15 17:50:50.0", "+Inf", "STATUS_CODE_UNSET", "110"),
		bucketRow("2025-12-15 17:51:20.0", "+Inf", "STATUS_CODE_UNSET", "120"),
		bucketRow("2025-12-15 17:51:50.0", "+Inf", "STATUS_CODE_UNSET", "130"),
	)

	require.Len(t, points, 1)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 51, 0, 0, time.UTC), points[0].Time)
	assert.InDelta(t, 20.0, points[0].Requests, 1e-9)
}
