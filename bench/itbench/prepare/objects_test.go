package prepare

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// tsvRow is one raw table row: a timestamp and a JSON body.
type tsvRow struct{ at, body string }

// table renders rows the way pandas exports them: quoted JSON with doubled quotes.
func table(rows ...tsvRow) string {
	var b strings.Builder
	b.WriteString("Timestamp\tTimestampTime\tBody\n")
	for _, r := range rows {
		b.WriteString(r.at + "\t" + r.at[:19] + "\t\"" + strings.ReplaceAll(r.body, `"`, `""`) + "\"\n")
	}
	return b.String()
}

func deployment(env, managers string) string {
	return `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"shipping","namespace":"otel-demo",
 "labels":{"opentelemetry.io/name":"shipping","it-bench/monitoring":"true"},
 "annotations":{"deployment.kubernetes.io/revision":"2"},"resourceVersion":"9","managedFields":[` + managers + `]},
 "spec":{"template":{"spec":{"containers":[{"name":"shipping","image":"ghcr.io/open-telemetry/demo:2.1.3-shipping",
 "env":[{"name":"QUOTE_ADDR","value":"` + env + `"}]}]}}},"status":{"replicas":1}}`
}

const helmWrite = `{"manager":"helm","operation":"Update","time":"2025-12-15T17:15:20Z"}`

func TestReadSnapshotsKeepsTheNamespaceUpToTheCut(t *testing.T) {
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:30:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:30:41.2", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"x","namespace":"data-recorders"}}`},
		tsvRow{"2025-12-15 17:30:41.3", `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":{"name":"agent-access","namespace":"otel-demo","labels":{"app.kubernetes.io/part-of":"it-bench"}}}`},
		tsvRow{"2025-12-15 17:30:41.4", `{"apiVersion":"chaos-mesh.org/v1alpha1","kind":"NetworkChaos","metadata":{"name":"p","namespace":"otel-demo"}}`},
		tsvRow{"2025-12-15 18:00:41.1", deployment("quote:0000", helmWrite)},
	))

	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))

	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Len(t, snaps[objKey{"Deployment", "shipping"}], 1)
}

func TestObjectsAreTheLastSnapshotWithoutWritersOrHarnessLabels(t *testing.T) {
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:55:41.1", deployment("quote:0000", helmWrite)},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))
	require.NoError(t, err)

	objects := buildObjects(snaps)

	require.Len(t, objects, 1)
	o := objects[0]
	assert.Equal(t, "shipping", o.Service)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 55, 41, 100000000, time.UTC), o.Observed)
	body := string(o.Body)
	assert.Contains(t, body, "quote:0000")
	assert.NotContains(t, body, "managedFields")
	assert.NotContains(t, body, "helm\"")
	assert.NotContains(t, body, "it-bench")
}

func TestChangesCarryTheWritersTimeButNotTheWriter(t *testing.T) {
	injected := helmWrite + `,{"manager":"OpenAPI-Generator","operation":"Update","time":"2025-12-15T17:52:47Z"},
 {"manager":"kube-controller-manager","operation":"Update","time":"2025-12-15T17:52:50Z"}`
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:55:41.1", deployment("quote:0000", injected)},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))
	require.NoError(t, err)

	changes := buildChanges(snaps, time.Date(2025, 12, 15, 17, 27, 59, 0, time.UTC), time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))

	require.Len(t, changes, 1)
	assert.Equal(t, dataset.Change{
		Time: time.Date(2025, 12, 15, 17, 52, 47, 0, time.UTC), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated",
		Fields: []dataset.Field{{Path: "spec.template.spec.containers[shipping].env[QUOTE_ADDR].value", From: str("quote:8080"), To: str("quote:0000")}},
	}, changes[0])
	text, err := json.Marshal(changes)
	require.NoError(t, err)
	assert.NotContains(t, string(text), "OpenAPI-Generator")
}

func TestChangesIgnoreStatusRevisionsAndRuntimeKinds(t *testing.T) {
	pod := func(phase string) string {
		return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"shipping-1","namespace":"otel-demo"},"spec":{"nodeName":"` + phase + `"}}`
	}
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:50:41.2", pod("a")},
		tsvRow{"2025-12-15 17:55:41.1", strings.Replace(strings.Replace(deployment("quote:8080", helmWrite), `"revision":"2"`, `"revision":"3"`, 1), `"replicas":1`, `"replicas":2`, 1)},
		tsvRow{"2025-12-15 17:55:41.2", pod("b")},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))
	require.NoError(t, err)

	assert.Empty(t, buildChanges(snaps, time.Date(2025, 12, 15, 17, 27, 59, 0, time.UTC), time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)))
}

func TestACreationInTheWindowIsAChange(t *testing.T) {
	quota := `{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"otel-demo-memory","namespace":"otel-demo","creationTimestamp":"2025-12-15T17:54:21Z"},"spec":{"hard":{"memory":"1Gi"}}}`
	old := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"old","namespace":"otel-demo","creationTimestamp":"2025-12-15T17:15:21Z"}}`
	fsys := files("k8s_objects_raw.tsv", table(tsvRow{"2025-12-15 17:59:03.1", quota}, tsvRow{"2025-12-15 17:59:03.2", old}))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 59, 18, 0, time.UTC))
	require.NoError(t, err)

	changes := buildChanges(snaps, time.Date(2025, 12, 15, 17, 29, 18, 0, time.UTC), time.Date(2025, 12, 15, 17, 59, 18, 0, time.UTC))

	assert.Equal(t, []dataset.Change{{Time: time.Date(2025, 12, 15, 17, 54, 21, 0, time.UTC), Kind: "ResourceQuota", Name: "otel-demo-memory", Action: "created"}}, changes)
}

func TestAConfigMapBelongsToTheOneServiceThatMountsIt(t *testing.T) {
	flagd := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"flagd","namespace":"otel-demo","labels":{"opentelemetry.io/name":"flagd"}},
 "spec":{"template":{"spec":{"volumes":[{"name":"config","configMap":{"name":"flagd-config"}}]}}}}`
	config := func(variant, managers string) string {
		return `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"flagd-config","namespace":"otel-demo","managedFields":[` + managers + `]},
 "data":{"demo.flagd.json":"{\"flags\":{\"productCatalogFailure\":{\"defaultVariant\":\"` + variant + `\"}}}"}}`
	}
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:43:28.1", flagd},
		tsvRow{"2025-12-15 17:43:28.2", config("off", helmWrite)},
		tsvRow{"2025-12-15 17:48:28.2", config("on", helmWrite+`,{"manager":"OpenAPI-Generator","operation":"Update","time":"2025-12-15T17:48:20Z"}`)},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 55, 47, 0, time.UTC))
	require.NoError(t, err)

	changes := buildChanges(snaps, time.Date(2025, 12, 15, 17, 25, 47, 0, time.UTC), time.Date(2025, 12, 15, 17, 55, 47, 0, time.UTC))

	require.Len(t, changes, 1)
	assert.Equal(t, "flagd", changes[0].Service)
	assert.Equal(t, `data["demo.flagd.json"].flags.productCatalogFailure.defaultVariant`, changes[0].Fields[0].Path)
}

func TestAnObjectMissingFromTheLastPullIsGone(t *testing.T) {
	pod := func(name string) string {
		return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"` + name + `","namespace":"otel-demo"},"status":{"phase":"Running"}}`
	}
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:50:41.2", pod("shipping-old")},
		tsvRow{"2025-12-15 17:55:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:55:41.2", pod("shipping-new")},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))
	require.NoError(t, err)

	objects := buildObjects(snaps)

	var names []string
	for _, o := range objects {
		names = append(names, o.Kind+"/"+o.Name)
	}
	assert.Equal(t, []string{"Deployment/shipping", "Pod/shipping-new"}, names, "the old pod was deleted before the last pull")
}
