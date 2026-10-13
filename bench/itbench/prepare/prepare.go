package prepare

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// Window is how far before the alert the dataset reaches.
const Window = 30 * time.Minute

// forbidden are strings that would tell an agent the data comes from a
// benchmark, or how its fault was injected. A dataset holding one is not
// written.
var forbidden = []string{
	"it-bench", "itbench", "hello-bench", "chaos", "openapi-generator",
	"agent-access", "data-recorders", "invalid-node", "invalid-command",
}

// Run prepares the scenario downloaded in rawDir into out. It reads only
// inside rawDir, and writes nothing if the dataset would leak.
func Run(rawDir, out, namespace string) (_ *dataset.Dataset, err error) {
	root, err := os.OpenRoot(rawDir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	d, err := build(root.FS(), namespace)
	if err != nil {
		return nil, err
	}
	if leaks := Leaks(d); len(leaks) > 0 {
		return nil, fmt.Errorf("the dataset would leak the benchmark: %s", strings.Join(leaks, "; "))
	}
	if err := os.MkdirAll(out, 0o750); err != nil { //nolint:gosec // G703: the operator names the output directory.
		return nil, err
	}
	if err := d.Save(out); err != nil {
		return nil, err
	}
	return d, nil
}

// build reads one downloaded scenario into a dataset.
func build(fsys fs.FS, namespace string) (*dataset.Dataset, error) {
	at, trigger, err := readTrigger(fsys)
	if err != nil {
		return nil, err
	}
	from := at.Add(-Window)
	d := &dataset.Dataset{Meta: dataset.Meta{Namespace: namespace, AlertTime: at, WindowStart: from, Trigger: trigger}}
	if d.Alerts, err = readAlertPolls(fsys, namespace, from, at); err != nil {
		return nil, err
	}
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", namespace, at)
	if err != nil {
		return nil, err
	}
	d.Objects, d.Changes = buildObjects(snaps), buildChanges(snaps, from, at)
	if d.Events, err = readEvents(fsys, "k8s_events_raw.tsv", namespace, from, at); err != nil {
		return nil, err
	}
	if d.Logs, err = readLogs(fsys, "otel_logs_raw.tsv", namespace, from, at); err != nil {
		return nil, err
	}
	if d.Spans, d.Edges, d.Meta.TracesUntil, err = readSpans(fsys, "otel_traces_raw.tsv", namespace, from, at); err != nil {
		return nil, err
	}
	if err := readMetrics(fsys, d, from, at); err != nil {
		return nil, err
	}
	return d, nil
}

func readMetrics(fsys fs.FS, d *dataset.Dataset, from, to time.Time) error {
	services, err := fs.Glob(fsys, "metrics/service_*_raw.tsv")
	if err != nil {
		return err
	}
	for _, name := range services {
		points, err := readServiceMetrics(fsys, name, from, to)
		if err != nil {
			return err
		}
		d.Services = append(d.Services, points...)
	}
	pods, err := fs.Glob(fsys, "metrics/pod_*_raw.tsv")
	if err != nil {
		return err
	}
	for _, name := range pods {
		points, err := readPodMetrics(fsys, name, from, to)
		if err != nil {
			return err
		}
		d.Pods = append(d.Pods, points...)
	}
	sort.SliceStable(d.Services, func(i, j int) bool { return d.Services[i].Time.Before(d.Services[j].Time) })
	sort.SliceStable(d.Pods, func(i, j int) bool { return d.Pods[i].Time.Before(d.Pods[j].Time) })
	return nil
}

// Leaks lists each forbidden string found in the dataset, with the stream
// that holds it and some context, case-insensitively.
func Leaks(d *dataset.Dataset) []string {
	streams := []struct {
		name string
		v    any
	}{
		{"meta", d.Meta}, {"alerts", d.Alerts}, {"changes", d.Changes}, {"objects", d.Objects}, {"events", d.Events},
		{"logs", d.Logs}, {"spans", d.Spans}, {"edges", d.Edges}, {"service_metrics", d.Services}, {"pod_metrics", d.Pods},
	}
	var out []string
	for _, s := range streams {
		text := strings.ToLower(string(marshal(s.v)))
		for _, word := range forbidden {
			if i := strings.Index(text, word); i >= 0 {
				out = append(out, fmt.Sprintf("%s: %q in %q", s.name, word, text[max(0, i-60):min(len(text), i+len(word)+60)]))
			}
		}
	}
	return out
}
