package prepare

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// controllers are writers whose updates are the system reacting, not a change.
var controllers = map[string]bool{"kube-controller-manager": true, "kubelet": true, "kube-scheduler": true}

// runtimeKinds are objects the system creates and updates on its own.
var runtimeKinds = map[string]bool{
	"Pod": true, "ReplicaSet": true, "Endpoints": true, "EndpointSlice": true,
	"Event": true, "Lease": true, "ControllerRevision": true,
}

// generated are metadata fields and annotations the API server or a
// controller sets, which are never part of a change.
var (
	generatedMeta        = []string{"managedFields", "resourceVersion", "generation", "uid", "creationTimestamp", "selfLink"}
	generatedAnnotations = []string{"deployment.kubernetes.io/revision", "kubectl.kubernetes.io/last-applied-configuration"}
)

type objKey struct{ kind, name string }

// snapshot is one object as one pull saw it.
type snapshot struct {
	at   time.Time
	body map[string]any
}

// readSnapshots reads the snapshots of the namespace's objects taken at or
// before to, oldest first, without the benchmark's own objects.
func readSnapshots(fsys fs.FS, name, namespace string, to time.Time) (map[objKey][]snapshot, error) {
	out := map[objKey][]snapshot{}
	err := readRows(fsys, name, func(r raw.Row) error {
		at, err := raw.ParseTime(r.Get("Timestamp"))
		if err != nil {
			return err
		}
		if at.After(to) {
			return nil
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(r.Get("Body")), &body); err != nil {
			return err
		}
		md := metadata(body)
		if md["namespace"] != namespace || isHarness(body) {
			return nil
		}
		kind, _ := body["kind"].(string)
		objName, _ := md["name"].(string)
		k := objKey{kind, objName}
		out[k] = append(out[k], snapshot{at: at, body: body})
		return nil
	})
	for _, list := range out {
		sort.SliceStable(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	}
	return out, err
}

// pullGap is shorter than the time between two pulls, five minutes, and
// longer than one pull takes, a few seconds.
const pullGap = time.Minute

// buildObjects returns each object's last snapshot, stripped. An object that
// the last pull did not list was deleted before it, so it is left out. A
// pull cut short by the alert would make its unlisted objects look deleted;
// none of the 13 scenarios has one (checks/realdata_test.go).
func buildObjects(snaps map[objKey][]snapshot) []dataset.Object {
	mounts := mountedBy(snaps)
	var lastPull time.Time
	for _, list := range snaps {
		if at := list[len(list)-1].at; at.After(lastPull) {
			lastPull = at
		}
	}
	out := make([]dataset.Object, 0, len(snaps))
	for k, list := range snaps {
		last := list[len(list)-1]
		if last.at.Before(lastPull.Add(-pullGap)) {
			continue
		}
		var body map[string]any
		_ = json.Unmarshal(marshal(last.body), &body)
		stripObject(body)
		out = append(out, dataset.Object{Kind: k.kind, Name: k.name, Service: serviceOf(k, last.body, mounts), Observed: last.at, Body: marshal(body)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// buildChanges derives the changes to desired state made in (from, to]: a
// creation at its creation time, and an update between two snapshots at the
// latest time a writer other than a controller wrote it. Writers stay
// anonymous. Deletions are not derived.
func buildChanges(snaps map[objKey][]snapshot, from, to time.Time) []dataset.Change {
	mounts := mountedBy(snaps)
	var out []dataset.Change
	for k, list := range snaps {
		if runtimeKinds[k.kind] || metadata(list[len(list)-1].body)["ownerReferences"] != nil {
			continue
		}
		service := serviceOf(k, list[len(list)-1].body, mounts)
		created, err := time.Parse(time.RFC3339, str0(metadata(list[0].body)["creationTimestamp"]))
		if err == nil && created.After(from) && !created.After(to) {
			out = append(out, dataset.Change{Time: created, Kind: k.kind, Name: k.name, Service: service, Action: "created"})
		}
		for i := 1; i < len(list); i++ {
			prev, next := list[i-1], list[i]
			fields := diffTrees("", desired(prev.body), desired(next.body))
			if len(fields) == 0 {
				continue
			}
			at, ok := lastWrite(next.body, prev.at)
			if !ok || at.Before(from) || at.After(to) {
				continue
			}
			for j := range fields {
				fields[j].From, fields[j].To = scrubPtr(fields[j].From), scrubPtr(fields[j].To)
			}
			out = append(out, dataset.Change{Time: at, Kind: k.kind, Name: k.name, Service: service, Action: "updated", Fields: fields})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].Kind+"/"+out[i].Name < out[j].Kind+"/"+out[j].Name
	})
	return out
}

// desired is the part of an object that a change sets: no status, no
// generated metadata, and none of the benchmark's labels.
func desired(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		if k != "status" {
			out[k] = v
		}
	}
	md := make(map[string]any)
	for k, v := range metadata(body) {
		md[k] = v
	}
	for _, k := range generatedMeta {
		delete(md, k)
	}
	for _, field := range []string{"labels", "annotations"} {
		kept := make(map[string]any)
		for k, v := range stringMap(md[field]) {
			if !strings.Contains(k, harnessMark) && !strings.Contains(v, harnessMark) {
				kept[k] = v
			}
		}
		for _, k := range generatedAnnotations {
			delete(kept, k)
		}
		md[field] = kept
	}
	out["metadata"] = md
	return out
}

// lastWrite is the latest time after since that a writer other than a
// controller wrote the object.
func lastWrite(body map[string]any, since time.Time) (time.Time, bool) {
	var latest time.Time
	list, _ := metadata(body)["managedFields"].([]any)
	for _, item := range list {
		m, _ := item.(map[string]any)
		if controllers[str0(m["manager"])] {
			continue
		}
		at, err := time.Parse(time.RFC3339, str0(m["time"]))
		if err == nil && at.After(since) && at.After(latest) {
			latest = at
		}
	}
	return latest, !latest.IsZero()
}

// serviceOf names the service an object belongs to: its name label, or for a
// ConfigMap the one service whose workload mounts it.
func serviceOf(k objKey, body map[string]any, mounts map[string]map[string]bool) string {
	labels := stringMap(metadata(body)["labels"])
	for _, l := range []string{"opentelemetry.io/name", "app.kubernetes.io/name"} {
		if s := labels[l]; s != "" {
			return s
		}
	}
	if k.kind == "ConfigMap" && len(mounts[k.name]) == 1 {
		for s := range mounts[k.name] {
			return s
		}
	}
	return ""
}

// mountedBy maps each ConfigMap to the services whose workloads mount it.
func mountedBy(snaps map[objKey][]snapshot) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for k, list := range snaps {
		if k.kind != "Deployment" && k.kind != "StatefulSet" && k.kind != "DaemonSet" {
			continue
		}
		body := list[len(list)-1].body
		service := serviceOf(k, body, nil)
		spec, _ := body["spec"].(map[string]any)
		template, _ := spec["template"].(map[string]any)
		podSpec, _ := template["spec"].(map[string]any)
		volumes, _ := podSpec["volumes"].([]any)
		for _, v := range volumes {
			vm, _ := v.(map[string]any)
			cm, _ := vm["configMap"].(map[string]any)
			if name := str0(cm["name"]); name != "" && service != "" {
				if out[name] == nil {
					out[name] = map[string]bool{}
				}
				out[name][service] = true
			}
		}
	}
	return out
}

func str0(v any) string {
	s, _ := v.(string)
	return s
}

func scrubPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := scrub(*s)
	return &v
}

// marshal encodes JSON without escaping HTML characters.
func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}
