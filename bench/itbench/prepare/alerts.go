package prepare

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// promAlert is one alert as the Prometheus API returns it.
type promAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    time.Time         `json:"activeAt"`
}

func (a *promAlert) convert(polled time.Time) dataset.Alert {
	return dataset.Alert{
		Polled:      polled,
		Name:        a.Labels["alertname"],
		Service:     a.Labels["service_name"],
		Pod:         a.Labels["pod"],
		Namespace:   a.Labels["namespace"],
		Severity:    a.Labels["severity"],
		State:       a.State,
		ActiveAt:    a.ActiveAt,
		Description: a.Annotations["description"],
	}
}

// readTrigger reads the benchmark's trigger: the alerts in alerting state when
// it started its agent. The alert time is the later of the file's timestamp
// and the latest activeAt, so that nothing in the trigger postdates it. The
// file is in alerts/, or in one scenario beside the tables.
func readTrigger(fsys fs.FS) (time.Time, []dataset.Alert, error) {
	var files []string
	for _, pattern := range []string{"alerts/alerts_in_alerting_state_*.json", "alerts_in_alerting_state_*.json"} {
		found, err := fs.Glob(fsys, pattern)
		if err != nil {
			return time.Time{}, nil, err
		}
		files = append(files, found...)
	}
	if len(files) != 1 {
		return time.Time{}, nil, fmt.Errorf("want one trigger file, found %d", len(files))
	}
	data, err := fs.ReadFile(fsys, files[0])
	if err != nil {
		return time.Time{}, nil, err
	}
	var file struct {
		Data struct {
			Alerts []promAlert `json:"alerts"`
		} `json:"data"`
		Timestamp time.Time `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return time.Time{}, nil, fmt.Errorf("%s: %w", files[0], err)
	}
	at := file.Timestamp.UTC()
	trigger := make([]dataset.Alert, 0, len(file.Data.Alerts))
	for i := range file.Data.Alerts {
		a := file.Data.Alerts[i].convert(time.Time{})
		if a.ActiveAt.After(at) {
			at = a.ActiveAt.UTC()
		}
		trigger = append(trigger, a)
	}
	return at, trigger, nil
}

// readAlertPolls reads the per-minute alert polls taken in [from, to], keeping
// alerts of the namespace and alerts with no namespace.
func readAlertPolls(fsys fs.FS, namespace string, from, to time.Time) ([]dataset.Alert, error) {
	files, err := fs.Glob(fsys, "alerts/alerts_at_*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []dataset.Alert
	for _, file := range files {
		stamp := strings.TrimSuffix(strings.TrimPrefix(path.Base(file), "alerts_at_"), ".json")
		polled, err := time.ParseInLocation("2006-01-02T15-04-05.999999", stamp, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if polled.Before(from) || polled.After(to) {
			continue
		}
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		var alerts []promAlert
		if err := json.Unmarshal(data, &alerts); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for i := range alerts {
			if ns := alerts[i].Labels["namespace"]; ns == "" || ns == namespace {
				out = append(out, alerts[i].convert(polled))
			}
		}
	}
	return out, nil
}
