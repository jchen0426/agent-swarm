package prepare

import (
	"io/fs"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

const spanBuckets = "traces_span_metrics_duration_milliseconds_bucket"

// Pod resource metrics: recording rules per container.
const (
	podCPU         = "node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate"
	podMemory      = "node_namespace_pod_container:container_memory_working_set_bytes"
	podCPULimit    = "cluster:namespace:pod_cpu:active:kube_pod_container_resource_limits"
	podMemoryLimit = "cluster:namespace:pod_memory:active:kube_pod_container_resource_limits"
)

// podSuffix is the ReplicaSet hash and pod suffix of a Deployment's pod name.
var podSuffix = regexp.MustCompile(`-[a-z0-9]{6,10}-[a-z0-9]{5}$`)

type sample struct {
	at    time.Time
	value float64
}

// eachRow calls fn for each row of a metrics table with its time and tags.
func eachRow(fsys fs.FS, name string, fn func(r raw.Row, at time.Time, tags map[string]any) error) error {
	return readRows(fsys, name, func(r raw.Row) error {
		at, err := raw.ParseTime(r.Get("timestamp"))
		if err != nil {
			return err
		}
		tags, err := pyMap(r.Get("tags"))
		if err != nil {
			return err
		}
		return fn(r, at, tags)
	})
}

// readServiceMetrics turns one service's span metrics into request counts,
// error counts and p95 latency per operation and minute. An increase between
// two scrapes counts in the minute of the later one, if that is in (from, to].
// The p95 is rounded to 0.1 ms.
func readServiceMetrics(fsys fs.FS, name string, from, to time.Time) ([]dataset.ServicePoint, error) {
	type seriesKey struct{ instance, kind, name, status, le string }
	series := map[seriesKey][]sample{}
	service := ""
	err := eachRow(fsys, name, func(r raw.Row, at time.Time, tags map[string]any) error {
		if r.Get("metric_name") != spanBuckets || r.Get("value") == "" {
			return nil
		}
		v, err := strconv.ParseFloat(r.Get("value"), 64)
		if err != nil {
			return err
		}
		service = r.Get("service_name")
		k := seriesKey{str0(tags["exported_instance"]), str0(tags["span_kind"]), str0(tags["span_name"]), str0(tags["status_code"]), r.Get("bucket_le")}
		series[k] = append(series[k], sample{at, v})
		return nil
	})
	if err != nil {
		return nil, err
	}
	type pointKey struct {
		minute     time.Time
		kind, name string
	}
	type counts struct {
		buckets map[float64]float64
		errors  float64
	}
	agg := map[pointKey]*counts{}
	for k, samples := range series {
		le, err := strconv.ParseFloat(k.le, 64)
		if err != nil {
			return nil, err
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })
		for i := 1; i < len(samples); i++ {
			at := samples[i].at
			if !at.After(from) || at.After(to) {
				continue
			}
			pk := pointKey{at.Truncate(time.Minute), kindName(k.kind), k.name}
			if agg[pk] == nil {
				agg[pk] = &counts{buckets: map[float64]float64{}}
			}
			inc := increase(samples[i-1].value, samples[i].value)
			agg[pk].buckets[le] += inc
			if math.IsInf(le, 1) && k.status == "STATUS_CODE_ERROR" {
				agg[pk].errors += inc
			}
		}
	}
	out := make([]dataset.ServicePoint, 0, len(agg))
	for k, c := range agg {
		out = append(out, dataset.ServicePoint{
			Time: k.minute, Service: service, Operation: k.name, Kind: k.kind,
			Requests: c.buckets[math.Inf(1)], Errors: c.errors, P95MS: math.Round(quantile(0.95, c.buckets)*10) / 10,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].Kind+out[i].Operation < out[j].Kind+out[j].Operation
	})
	return out, nil
}

// increase is a counter's growth between two scrapes; a drop is a reset to zero.
func increase(before, after float64) float64 {
	if after < before {
		return after
	}
	return after - before
}

// quantile estimates a quantile from cumulative histogram buckets, as
// Prometheus' histogram_quantile does.
func quantile(q float64, buckets map[float64]float64) float64 {
	total := buckets[math.Inf(1)]
	if total == 0 {
		return 0
	}
	les := make([]float64, 0, len(buckets))
	for le := range buckets {
		les = append(les, le)
	}
	sort.Float64s(les)
	rank := q * total
	var prevLE, prevCount float64
	for _, le := range les {
		c := buckets[le]
		if c >= rank {
			if math.IsInf(le, 1) {
				return prevLE
			}
			if c == prevCount {
				return le
			}
			return prevLE + (le-prevLE)*(rank-prevCount)/(c-prevCount)
		}
		prevLE, prevCount = le, c
	}
	return prevLE
}

func kindName(k string) string { return strings.ToLower(strings.TrimPrefix(k, "SPAN_KIND_")) }

// podMetrics are the pod tables' metrics that the dataset keeps.
var podMetrics = map[string]bool{podCPU: true, podMemory: true, podCPULimit: true, podMemoryLimit: true}

type podKey struct {
	metric string
	at     time.Time
}

// readPodMetrics turns one pod's table into CPU and memory use and limits per
// minute, summed over its containers, from each minute's last scrape.
func readPodMetrics(fsys fs.FS, name string, from, to time.Time) ([]dataset.PodPoint, error) {
	values := map[podKey]map[string]float64{}
	pod := ""
	err := readRows(fsys, name, func(r raw.Row) error {
		metric, value := r.Get("metric_name"), r.Get("value")
		if !podMetrics[metric] || value == "" {
			return nil
		}
		at, err := raw.ParseTime(r.Get("timestamp"))
		if err != nil || at.Before(from) || at.After(to) {
			return err
		}
		tags, err := pyMap(r.Get("tags"))
		if err != nil {
			return err
		}
		container := str0(tags["container"])
		if container == "" || container == "POD" {
			return nil
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		pod = r.Get("pod_name")
		k := podKey{metric, at}
		if values[k] == nil {
			values[k] = map[string]float64{}
		}
		values[k][container] = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return podPoints(pod, values), nil
}

// podPoints sums each scrape over the containers, and keeps each metric's
// last scrape in each minute.
func podPoints(pod string, values map[podKey]map[string]float64) []dataset.PodPoint {
	last := map[time.Time]map[string]sample{}
	for k, byContainer := range values {
		minute := k.at.Truncate(time.Minute)
		if last[minute] == nil {
			last[minute] = map[string]sample{}
		}
		if prev, ok := last[minute][k.metric]; ok && prev.at.After(k.at) {
			continue
		}
		sum := 0.0
		for _, v := range byContainer {
			sum += v
		}
		last[minute][k.metric] = sample{k.at, sum}
	}
	out := make([]dataset.PodPoint, 0, len(last))
	for minute, m := range last {
		out = append(out, dataset.PodPoint{
			Time: minute, Pod: pod, Service: podSuffix.ReplaceAllString(pod, ""),
			CPUCores: m[podCPU].value, MemoryBytes: m[podMemory].value,
			CPULimit: m[podCPULimit].value, MemoryLimit: m[podMemoryLimit].value,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}
