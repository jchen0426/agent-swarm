// Package checks_test runs the pipeline on the real downloaded scenarios and
// pins what the mechanism check's plan reports about them.
package checks_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/baseline"
	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/prepare"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

// TestTheRealScenarios needs ITBENCH_RAW, the directory that itbench fetch
// downloaded into. Preparing all thirteen takes about a minute.
func TestTheRealScenarios(t *testing.T) {
	rawDir := os.Getenv("ITBENCH_RAW")
	if rawDir == "" {
		t.Skip("ITBENCH_RAW is not set")
	}
	set, err := score.Load("../scenarios.yaml")
	require.NoError(t, err)
	out := t.TempDir()
	datasets := map[string]*dataset.Dataset{}
	for _, sc := range set.Scenarios {
		d, err := prepare.Run(filepath.Join(rawDir, sc.Dir), filepath.Join(out, sc.ID), set.Namespace)
		require.NoError(t, err, sc.ID)
		datasets[sc.ID] = d
	}

	t.Run("the changes in the window are exactly the injections", func(t *testing.T) {
		want := map[string][]string{
			"S4":   {"17:48:20 updated ConfigMap/flagd-config", "17:48:22 updated Deployment/product-catalog"},
			"S5":   {"17:47:32 updated ConfigMap/flagd-config", "17:47:33 updated Deployment/ad"},
			"S16":  {"17:52:47 updated Deployment/shipping"},
			"S20":  {"17:50:05 updated Deployment/product-catalog"},
			"S23":  {"17:54:47 updated Deployment/checkout"},
			"S24":  {"17:49:57 updated Deployment/checkout"},
			"S33":  {"17:54:51 updated Deployment/ad", "17:54:59 updated Deployment/cart"},
			"S102": {"17:54:21 created ResourceQuota/otel-demo-memory"},
			"S105": {"17:54:13 updated Deployment/product-catalog"},
		}
		for _, sc := range set.Scenarios {
			var got []string
			for _, c := range datasets[sc.ID].Changes {
				got = append(got, c.Time.Format(time.TimeOnly)+" "+c.Action+" "+c.Kind+"/"+c.Name)
			}
			assert.Equal(t, want[sc.ID], got, sc.ID)
			assert.Equal(t, sc.Change, len(got) > 0, sc.ID)
		}
	})

	t.Run("objects the last pull did not list are gone", func(t *testing.T) {
		deleted := map[string]string{
			"S4": "product-catalog-7c7f8b68dc-5spmp", "S5": "ad-554b849958-w5fhc", "S16": "shipping-75d58f5d84-gw77q",
			"S20": "product-catalog-7c7f8b68dc-qz548", "S23": "checkout-5dccddf8bb-xq5bq", "S24": "checkout-5dccddf8bb-9d9c8",
			"S102": "ad-554b849958-p226w", "S105": "product-catalog-7c7f8b68dc-wzpwm",
		}
		for id, pod := range deleted {
			d := datasets[id]
			assert.False(t, slices.ContainsFunc(d.Objects, func(o dataset.Object) bool { return o.Name == pod }), "%s serves %s", id, pod)
			assert.True(t, slices.ContainsFunc(d.Events, func(e dataset.Event) bool {
				return e.Reason == "SuccessfulDelete" && strings.Contains(e.Message, pod)
			}), "%s has no deletion event for %s", id, pod)
		}
	})

	t.Run("service metrics cover whole minutes", func(t *testing.T) {
		for _, sc := range set.Scenarios {
			d := datasets[sc.ID]
			totals := map[time.Time]float64{}
			for _, p := range d.Services {
				assert.False(t, p.Time.Before(d.Meta.WindowStart), "%s: %s starts before the window", sc.ID, p.Time)
				assert.False(t, p.Time.Add(time.Minute).After(d.Meta.AlertTime), "%s: %s ends after the alert", sc.ID, p.Time)
				if p.Kind == "server" {
					totals[p.Time] += p.Requests
				}
			}
			var last time.Time
			for minute := range totals {
				if minute.After(last) {
					last = minute
				}
			}
			assert.Positive(t, totals[last], "%s: the last minute, %s, has no requests", sc.ID, last)
		}
	})

	t.Run("the baseline's scores", func(t *testing.T) {
		service, namespace := score.Answers{}, score.Answers{}
		for _, sc := range set.Scenarios {
			s, n := baseline.Evaluate(datasets[sc.ID], set.FlagStore)
			if s != nil {
				service[sc.ID] = *s
			}
			if n != nil {
				namespace[sc.ID] = *n
			}
		}
		r := score.Compare(set, namespace, service)
		assert.Equal(t, 8, r.Hits, "namespace scope")
		assert.Equal(t, 8, r.ChangeHits, "namespace scope, change scenarios")
		assert.Equal(t, 9, r.ChangeScenarios)
		assert.Equal(t, 1, r.BaselineHits, "service scope")
	})

	t.Run("the tools answer from the real data", func(t *testing.T) {
		cs := connect(t, datasets["S16"])
		for _, c := range []struct {
			tool string
			args map[string]any
			want string
		}{
			{"list_alerts", nil, "RequestErrorRate"},
			{"list_services", nil, "shipping"},
			{"get_changes", nil, "QUOTE_ADDR"},
			{"get_events", map[string]any{"type": "Normal"}, "shipping"},
			{"query_logs", map[string]any{"service": "frontend-proxy", "contains": "/api/checkout", "limit": 5}, "POST"},
			{"get_error_spans", map[string]any{"service": "shipping", "limit": 5}, "InvalidUri"},
			{"get_service_metrics", map[string]any{"service": "shipping"}, "max_p95_ms"},
			{"get_pod_metrics", map[string]any{"service": "shipping"}, "memory_bytes"},
			{"list_objects", map[string]any{"kind": "Pod"}, "shipping"},
			{"get_object", map[string]any{"kind": "Deployment", "name": "shipping"}, "quote:0000"},
		} {
			text := call(t, cs, c.tool, c.args)
			assert.Contains(t, text, c.want, c.tool)
			t.Logf("%s: %d bytes", c.tool, len(text))
		}
	})

	t.Run("every tool answers on every dataset", func(t *testing.T) {
		for _, sc := range set.Scenarios {
			d := datasets[sc.ID]
			cs := connect(t, d)
			service := d.Logs[0].Service
			pod := d.Pods[0].Pod
			deployment := ""
			for _, o := range d.Objects {
				if o.Kind == "Deployment" {
					deployment = o.Name
				}
			}
			for tool, args := range map[string]map[string]any{
				"list_alerts":         nil,
				"list_services":       nil,
				"get_changes":         nil,
				"get_events":          nil,
				"query_logs":          {"service": service},
				"get_error_spans":     nil,
				"get_service_metrics": {"service": d.Services[0].Service},
				"get_pod_metrics":     {"pod": pod},
				"list_objects":        {"kind": "Pod"},
				"get_object":          {"kind": "Deployment", "name": deployment},
			} {
				call(t, cs, tool, args)
			}
		}
	})
}

// connect opens an MCP session on a dataset's tools.
func connect(t *testing.T, d *dataset.Dataset) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientSide, serverSide := mcp.NewInMemoryTransports()
	_, err := server.New(d).Connect(ctx, serverSide, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "check", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call calls a tool and returns its text, failing the test on a tool error.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err, tool)
	text := res.Content[0].(*mcp.TextContent).Text
	require.False(t, res.IsError, "%s: %s", tool, text)
	return text
}
