// Package checks_test runs the pipeline on the real downloaded scenarios and
// pins what the mechanism check's plan reports about them.
package checks_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

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

	t.Run("each fault that is a change is visible before the alert", func(t *testing.T) {
		for _, sc := range set.Scenarios {
			var services []string
			for _, c := range datasets[sc.ID].Changes {
				services = append(services, c.Service)
			}
			if !sc.Change {
				assert.Empty(t, services, sc.ID)
				continue
			}
			found := slices.ContainsFunc(sc.Keys, func(k score.Key) bool { return slices.Contains(services, k.Service) })
			assert.True(t, found || sc.ID == "S102", "%s: changes to %v", sc.ID, services)
		}
		quota := datasets["S102"].Changes
		require.Len(t, quota, 1)
		assert.Equal(t, "ResourceQuota", quota[0].Kind, "S102's change belongs to no service")
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
		ctx := context.Background()
		clientSide, serverSide := mcp.NewInMemoryTransports()
		_, err := server.New(datasets["S16"]).Connect(ctx, serverSide, nil)
		require.NoError(t, err)
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "check", Version: "0"}, nil).Connect(ctx, clientSide, nil)
		require.NoError(t, err)
		defer func() { _ = cs.Close() }()
		for _, call := range []struct {
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
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: call.tool, Arguments: call.args})
			require.NoError(t, err, call.tool)
			require.False(t, res.IsError, call.tool)
			text := res.Content[0].(*mcp.TextContent).Text
			assert.Contains(t, text, call.want, call.tool)
			t.Logf("%s: %d bytes", call.tool, len(text))
		}
	})
}
