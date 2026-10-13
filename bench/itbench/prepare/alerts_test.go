package prepare

import (
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// files builds a file system from name and content pairs.
func files(nameContent ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for i := 0; i+1 < len(nameContent); i += 2 {
		fsys[nameContent[i]] = &fstest.MapFile{Data: []byte(nameContent[i+1])}
	}
	return fsys
}

const errorRateAlert = `{"labels": {"alertname": "RequestErrorRate", "namespace": "otel-demo", "service_name": "%s", "severity": "warning"},
 "annotations": {"description": "Request error rate in service %s is above 0"}, "state": "firing", "activeAt": "%s", "value": "1e-02"}`

func TestTheTriggerTimeIsTheLaterOfTheFileTimeAndItsAlerts(t *testing.T) {
	fsys := files("alerts/alerts_in_alerting_state_2025-12-15T175422.255557Z.json",
		`{"data": {"alerts": [`+fmt.Sprintf(errorRateAlert, "frontend-proxy", "frontend-proxy", "2025-12-15T17:55:47.934820964Z")+`,`+
			fmt.Sprintf(errorRateAlert, "frontend", "frontend", "2025-12-15T17:50:00Z")+
			`]}, "status": "success", "timestamp": "2025-12-15T17:54:22Z"}`)

	at, trigger, err := readTrigger(fsys)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 55, 47, 934820964, time.UTC), at)
	require.Len(t, trigger, 2)
	assert.Equal(t, "frontend-proxy", trigger[0].Service)
	assert.Equal(t, "Request error rate in service frontend-proxy is above 0", trigger[0].Description)
}

func TestReadTriggerNeedsExactlyOneTriggerFile(t *testing.T) {
	_, _, err := readTrigger(files())
	require.ErrorContains(t, err, "want one trigger file")

	trigger := `{"data": {"alerts": []}, "timestamp": "2025-12-15T17:54:22Z"}`
	_, _, err = readTrigger(files("alerts/alerts_in_alerting_state_1.json", trigger, "alerts_in_alerting_state_2.json", trigger))
	assert.ErrorContains(t, err, "want one trigger file, found 2")
}

// One scenario keeps its trigger file beside the tables, with no alert polls.
func TestATriggerFileBesideTheTablesCounts(t *testing.T) {
	fsys := files("alerts_in_alerting_state_2025-12-15T175345.105640Z.json",
		`{"data": {"alerts": [`+fmt.Sprintf(errorRateAlert, "frontend", "frontend", "2025-12-15T17:50:00Z")+`]}, "timestamp": "2025-12-15T17:53:45Z"}`)

	at, trigger, err := readTrigger(fsys)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 53, 45, 0, time.UTC), at)
	assert.Len(t, trigger, 1)
}

func TestAlertPollsKeepTheWindowAndDropOtherNamespaces(t *testing.T) {
	poll := func(service, activeAt string) string {
		return `[` + fmt.Sprintf(errorRateAlert, service, service, activeAt) + `,
 {"labels": {"alertname": "KubePodNotReady", "namespace": "data-recorders", "pod": "recorder-0"}, "annotations": {}, "state": "firing", "activeAt": "2025-12-15T17:20:00Z"},
 {"labels": {"alertname": "KubeSchedulerDown", "severity": "critical"}, "annotations": {}, "state": "firing", "activeAt": "2025-12-15T17:20:00Z"}]`
	}
	fsys := files(
		"alerts/alerts_at_2025-12-15T15-21-58.000000.json", poll("cart", "2025-12-15T15:20:00Z"),
		"alerts/alerts_at_2025-12-15T17-50-35.385790.json", poll("frontend", "2025-12-15T17:49:47Z"),
		"alerts/alerts_at_2025-12-15T18-00-35.000000.json", poll("checkout", "2025-12-15T17:59:00Z"),
	)
	from := time.Date(2025, 12, 15, 17, 25, 0, 0, time.UTC)
	to := time.Date(2025, 12, 15, 17, 55, 0, 0, time.UTC)

	alerts, err := readAlertPolls(fsys, "otel-demo", from, to)

	require.NoError(t, err)
	require.Len(t, alerts, 2)
	polled := time.Date(2025, 12, 15, 17, 50, 35, 385790000, time.UTC)
	assert.Equal(t, polled, alerts[0].Polled)
	assert.Equal(t, "frontend", alerts[0].Service)
	assert.Equal(t, "KubeSchedulerDown", alerts[1].Name)
	assert.Equal(t, polled, alerts[1].Polled)
}
