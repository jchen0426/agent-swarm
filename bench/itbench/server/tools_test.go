package server_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

func at(minute, second int) time.Time {
	return time.Date(2025, 12, 15, 17, minute, second, 0, time.UTC)
}

func str(s string) *string { return &s }

func fixture() *dataset.Dataset {
	shippingAlert := dataset.Alert{Name: "RequestErrorRate", Service: "shipping", Namespace: "otel-demo", State: "firing", ActiveAt: at(49, 47)}
	frontendAlert := dataset.Alert{Name: "RequestErrorRate", Service: "frontend", Namespace: "otel-demo", State: "firing", ActiveAt: at(55, 0)}
	first, second := shippingAlert, frontendAlert
	first.Polled, second.Polled = at(50, 35), at(56, 35)
	shippingAgain := shippingAlert
	shippingAgain.Polled = at(56, 35)
	return &dataset.Dataset{
		Meta: dataset.Meta{Namespace: "otel-demo", AlertTime: at(57, 59), WindowStart: at(27, 59), TracesUntil: at(44, 0),
			Trigger: []dataset.Alert{shippingAlert}},
		Alerts: []dataset.Alert{first, shippingAgain, second},
		Changes: []dataset.Change{
			{Time: at(40, 0), Kind: "ConfigMap", Name: "flagd-config", Service: "flagd", Action: "updated"},
			{Time: at(52, 47), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated",
				Fields: []dataset.Field{{Path: "spec.template.spec.containers[shipping].env[QUOTE_ADDR].value", From: str("quote:8080"), To: str("quote:0000")}}},
		},
		Objects: []dataset.Object{
			{Kind: "Deployment", Name: "shipping", Service: "shipping", Observed: at(55, 41), Body: json.RawMessage(`{"kind":"Deployment","metadata":{"name":"shipping"}}`)},
			{Kind: "Pod", Name: "shipping-1", Service: "shipping", Observed: at(55, 41), Body: json.RawMessage(
				`{"kind":"Pod","status":{"phase":"Running","containerStatuses":[{"name":"shipping","ready":false,"restartCount":2,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}`)},
			{Kind: "Pod", Name: "ad-1", Service: "ad", Observed: at(55, 41), Body: json.RawMessage(
				`{"kind":"Pod","status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable"}]}}`)},
		},
		Events: []dataset.Event{
			{Time: at(53, 0), Type: "Warning", Reason: "BackOff", ObjectKind: "Pod", ObjectName: "shipping-1", Message: "Back-off restarting", Count: 3},
			{Time: at(54, 0), Type: "Normal", Reason: "Pulled", ObjectKind: "Pod", ObjectName: "cart-1", Message: "pulled"},
		},
		Logs: []dataset.Log{
			{Time: at(50, 0), Service: "shipping", Body: "Failed to get quote: InvalidUri"},
			{Time: at(51, 0), Service: "shipping", Body: "order shipped"},
			{Time: at(52, 0), Service: "shipping", Body: "FAILED to get quote again"},
			{Time: at(53, 0), Service: "cart", Body: "failed to get cart"},
		},
		Spans: []dataset.Span{
			{Time: at(43, 0), TraceID: "t1", SpanID: "s1", Service: "shipping", Name: "/get-quote", Kind: "Server", Status: "Error", Message: "Internal Server Error"},
			{Time: at(43, 30), TraceID: "t2", SpanID: "s2", Service: "checkout", Name: "PlaceOrder", Kind: "Server", Status: "Error"},
		},
		Edges: []dataset.Edge{{From: "checkout", To: "shipping", Calls: 10, Errors: 2}, {From: "frontend", To: "checkout", Calls: 20}},
		Services: []dataset.ServicePoint{
			{Time: at(55, 0), Service: "shipping", Operation: "/get-quote", Kind: "server", Requests: 10, Errors: 4, P95MS: 12},
			{Time: at(55, 0), Service: "shipping", Operation: "/ship-order", Kind: "server", Requests: 5, P95MS: 30},
			{Time: at(55, 0), Service: "shipping", Operation: "POST quote", Kind: "client", Requests: 10, Errors: 4, P95MS: 2},
			{Time: at(56, 0), Service: "shipping", Operation: "/get-quote", Kind: "server", Requests: 8, Errors: 8, P95MS: 9},
		},
		Pods: []dataset.PodPoint{
			{Time: at(55, 0), Pod: "shipping-1", Service: "shipping", CPUCores: 0.01, MemoryBytes: 2e7},
			{Time: at(55, 0), Pod: "cart-1", Service: "cart", CPUCores: 0.02, MemoryBytes: 3e7},
		},
	}
}

func TestListAlertsReturnsTheLastPollAtOrBeforeTheTime(t *testing.T) {
	tools := server.NewTools(fixture())

	latest, err := tools.ListAlerts(&server.AlertsInput{})
	require.NoError(t, err)
	assert.Equal(t, at(56, 35), latest.Polled)
	assert.Len(t, latest.Alerts, 2)

	earlier, err := tools.ListAlerts(&server.AlertsInput{At: "2025-12-15T17:51:00Z"})
	require.NoError(t, err)
	assert.Equal(t, at(50, 35), earlier.Polled)
	assert.Equal(t, "shipping", earlier.Alerts[0].Service)

	future, err := tools.ListAlerts(&server.AlertsInput{At: "2025-12-15T19:00:00Z"})
	require.NoError(t, err)
	assert.Equal(t, at(56, 35), future.Polled, "times after the alert are clamped to it")

	_, err = tools.ListAlerts(&server.AlertsInput{At: "yesterday"})
	assert.ErrorContains(t, err, "RFC 3339")
}

func TestGetChangesFiltersByServiceNewestFirst(t *testing.T) {
	tools := server.NewTools(fixture())

	all, err := tools.GetChanges(&server.ChangesInput{})
	require.NoError(t, err)
	require.Len(t, all.Changes, 2)
	assert.Equal(t, "shipping", all.Changes[0].Name)

	flagd, err := tools.GetChanges(&server.ChangesInput{Service: "flagd"})
	require.NoError(t, err)
	require.Len(t, flagd.Changes, 1)
	assert.Equal(t, "flagd-config", flagd.Changes[0].Name)

	none, err := tools.GetChanges(&server.ChangesInput{Window: server.Window{Since: "2025-12-15T17:53:00Z"}})
	require.NoError(t, err)
	assert.Empty(t, none.Changes)
}

func TestGetEventsFilters(t *testing.T) {
	tools := server.NewTools(fixture())

	warnings, err := tools.GetEvents(&server.EventsInput{Type: "Warning"})
	require.NoError(t, err)
	require.Len(t, warnings.Events, 1)
	assert.Equal(t, "BackOff", warnings.Events[0].Reason)

	cart, err := tools.GetEvents(&server.EventsInput{Object: "cart-1"})
	require.NoError(t, err)
	require.Len(t, cart.Events, 1)
	assert.Equal(t, "Pulled", cart.Events[0].Reason)
}

func TestQueryLogsMatchesTextAndKeepsTheMostRecent(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.QueryLogs(&server.LogsInput{Service: "shipping", Contains: "failed", Limit: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, out.Total)
	assert.True(t, out.Truncated)
	require.Len(t, out.Logs, 1)
	assert.Equal(t, "FAILED to get quote again", out.Logs[0].Body)

	_, err = tools.QueryLogs(&server.LogsInput{})
	assert.ErrorContains(t, err, "service")
}

func TestGetErrorSpansSaysWhereTracesEnd(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.GetErrorSpans(&server.SpansInput{Service: "shipping"})

	require.NoError(t, err)
	assert.Equal(t, at(44, 0), out.TracesUntil)
	require.Len(t, out.Spans, 1)
	assert.Equal(t, "/get-quote", out.Spans[0].Name)
}

func TestServiceMetricsAggregateOperationsPerMinute(t *testing.T) {
	tools := server.NewTools(fixture())

	total, err := tools.GetServiceMetrics(&server.ServiceMetricsInput{Service: "shipping"})
	require.NoError(t, err)
	assert.Equal(t, []server.MetricPoint{
		{Time: at(55, 0), Requests: 15, Errors: 4, MaxP95MS: 30},
		{Time: at(56, 0), Requests: 8, Errors: 8, MaxP95MS: 9},
	}, total.Points)

	client, err := tools.GetServiceMetrics(&server.ServiceMetricsInput{Service: "shipping", Kind: "client", Operation: "POST quote"})
	require.NoError(t, err)
	assert.Equal(t, []server.MetricPoint{{Time: at(55, 0), Requests: 10, Errors: 4, MaxP95MS: 2}}, client.Points)
}

func TestGetPodMetricsSelectsTheServicesPods(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.GetPodMetrics(&server.PodMetricsInput{Service: "cart"})

	require.NoError(t, err)
	require.Len(t, out.Points, 1)
	assert.Equal(t, "cart-1", out.Points[0].Pod)
}

func TestListObjectsSummarizesPods(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.ListObjects(&server.ListObjectsInput{Kind: "Pod"})

	require.NoError(t, err)
	require.Len(t, out.Objects, 2)
	assert.Equal(t, "ad-1", out.Objects[0].Name)
	assert.Equal(t, "Pending; Unschedulable", out.Objects[0].Status)
	assert.Equal(t, "Running; ready 0/1; restarts 2; CrashLoopBackOff", out.Objects[1].Status)
}

func TestGetObjectReturnsTheSnapshot(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.GetObject(&server.GetObjectInput{Kind: "Deployment", Name: "shipping"})
	require.NoError(t, err)
	assert.Equal(t, at(55, 41), out.Observed)
	assert.Equal(t, map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "shipping"}}, out.Object)

	_, err = tools.GetObject(&server.GetObjectInput{Kind: "Deployment", Name: "nope"})
	assert.ErrorContains(t, err, "no Deployment named nope")
}

func TestListServicesJoinsObjectsAndCalls(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.ListServices(&server.ListServicesInput{})

	require.NoError(t, err)
	names := make([]string, 0, len(out.Services))
	for _, s := range out.Services {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"ad", "cart", "checkout", "flagd", "frontend", "shipping"}, names)
	for _, s := range out.Services {
		if s.Name == "checkout" {
			assert.Equal(t, []server.Call{{Service: "shipping", Calls: 10, Errors: 2}}, s.Calls)
			assert.Equal(t, []server.Call{{Service: "frontend", Calls: 20}}, s.CalledBy)
		}
	}
}

// A name the data does not hold must not read as "no pods" or "no traffic":
// models that know older releases of the demo ask for cartservice.
func TestUnknownNamesAreToolErrors(t *testing.T) {
	tools := server.NewTools(fixture())

	_, err := tools.QueryLogs(&server.LogsInput{Service: "cartservice"})
	require.ErrorContains(t, err, `no service named "cartservice"`)
	require.ErrorContains(t, err, "shipping", "the error lists the services")
	_, err = tools.GetChanges(&server.ChangesInput{Service: "cartservice"})
	require.Error(t, err)
	_, err = tools.GetErrorSpans(&server.SpansInput{Service: "cartservice"})
	require.Error(t, err)
	_, err = tools.GetPodMetrics(&server.PodMetricsInput{Service: "cartservice"})
	require.Error(t, err)
	_, err = tools.GetPodMetrics(&server.PodMetricsInput{Pod: "shipping-9"})
	require.ErrorContains(t, err, `no pod named "shipping-9"`)
	_, err = tools.GetEvents(&server.EventsInput{Object: "shipping-9"})
	require.ErrorContains(t, err, `no object named "shipping-9"`)
	_, err = tools.GetEvents(&server.EventsInput{Type: "Error"})
	require.ErrorContains(t, err, "Warning or Normal")
	_, err = tools.GetServiceMetrics(&server.ServiceMetricsInput{Service: "shipping", Kind: "internal"})
	require.ErrorContains(t, err, "server or client")
	_, err = tools.ListObjects(&server.ListObjectsInput{Kind: "Gadget"})
	require.ErrorContains(t, err, `no objects of kind "Gadget"`)
	require.ErrorContains(t, err, "Deployment", "the error lists the kinds")
}

func TestKindsMatchWhateverTheirCase(t *testing.T) {
	tools := server.NewTools(fixture())

	pods, err := tools.ListObjects(&server.ListObjectsInput{Kind: "pod"})
	require.NoError(t, err)
	assert.Len(t, pods.Objects, 2)
	object, err := tools.GetObject(&server.GetObjectInput{Kind: "deployment", Name: "shipping"})
	require.NoError(t, err)
	assert.Equal(t, "Deployment", object.Object["kind"])
	metrics, err := tools.GetServiceMetrics(&server.ServiceMetricsInput{Service: "shipping", Kind: "SERVER"})
	require.NoError(t, err)
	assert.Len(t, metrics.Points, 2)
}

func TestListServicesSaysWhereTracesEnd(t *testing.T) {
	out, err := server.NewTools(fixture()).ListServices(&server.ListServicesInput{})

	require.NoError(t, err)
	assert.Equal(t, at(44, 0), out.TracesUntil)
}
