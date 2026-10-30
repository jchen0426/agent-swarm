package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
)

// twoInstances holds what a first deployment of two instances on one host
// copies (Plan 7d-2, Decision 13): their deployment files, serve's unit, and
// the units of their shared configuration clone.
const twoInstances = "../../examples/two-instances"

// The instances' unit gives serve's drain its time: after its SIGTERM,
// systemd's stop waits TimeoutStopSec before it kills serve, which must be
// above the worst drain that serve computes, and logs at its start, for a.yaml
// as it loads (Decision 13; R22, as Task 13 amends it).
func TestTheExampleUnitStopsAfterTheWorstDrain(t *testing.T) {
	d, err := deploy.Load(filepath.Join(twoInstances, "a.yaml"))
	require.NoError(t, err)
	stop := readUnit(t, "agentswarm@.service").values("Service", "TimeoutStopSec")
	require.Len(t, stop, 1, "TimeoutStopSec= is given once")

	seconds, err := strconv.Atoi(stop[0])

	require.NoError(t, err, "TimeoutStopSec= is a whole number of seconds")
	assert.Greater(t, time.Duration(seconds)*time.Second, worstDrain(d))
}

// The units keep Decision 13's settings. No Go code reads a unit, so this
// test is their only gate against drift: it is a change detector by design,
// and a deliberate change edits the unit and this test together, with
// Decision 13's reasons. Serve runs as agentswarm, in the docker group; after
// the engine, which it wants but neither requires nor is bound to, so that an
// engine restart stops neither instance; restarted always, every 5 s, with no
// limit, since serve refuses to start while the engine is down; weighted
// above each sandbox's CPU weight of 100, in system.slice beside the
// sandboxes' scopes rather than in its template's own slice, where the weight
// would weigh it against the other instance alone; in a sockets directory
// that systemd makes for it, mode 0750; and with the system's limit of open
// files, its own /tmp, and the sockets' path as the engine sees it. The
// fetcher fetches main fast-forward only, and names a unit that notifies
// someone when it fails; its timer fires every minute, and the gc's weekly.
func TestTheExampleUnitsKeepTheirSettings(t *testing.T) {
	serve := readUnit(t, "agentswarm@.service")
	for _, s := range []struct{ section, key, value string }{
		{"Service", "User", "agentswarm"},
		{"Service", "Group", "agentswarm"},
		{"Service", "SupplementaryGroups", "docker"},
		{"Service", "Restart", "always"},
		{"Service", "RestartSec", "5"},
		{"Unit", "StartLimitIntervalSec", "0"},
		{"Service", "Slice", "system.slice"},
		{"Service", "CPUWeight", "1000"},
		{"Service", "RuntimeDirectory", "agentswarm/%i"},
		{"Service", "RuntimeDirectoryMode", "0750"},
	} {
		assert.Equal(t, []string{s.value}, serve.values(s.section, s.key), "[%s] %s=", s.section, s.key)
	}
	for _, key := range []string{"After", "Wants"} {
		assert.Contains(t, serve.words("Unit", key), "docker.service", "%s=", key)
	}
	for _, key := range []string{"Requires", "BindsTo", "LimitNOFILE", "PrivateTmp", "BindPaths"} {
		assert.False(t, serve.has(key), "%s= is not given", key)
	}

	fetch := readUnit(t, "agentswarm-config.service")
	assert.Equal(t, []string{"git --git-dir=/var/lib/agentswarm/config.git fetch --no-tags origin refs/heads/main:refs/heads/main"},
		fetch.values("Service", "ExecStart"), "the fetch, fast-forward only")
	assert.NotEmpty(t, fetch.words("Unit", "OnFailure"), "OnFailure= names the unit that notifies someone")
	assert.Equal(t, []string{"minutely"}, readUnit(t, "agentswarm-config.timer").values("Timer", "OnCalendar"), "the fetch's timer")
	assert.Equal(t, []string{"weekly"}, readUnit(t, "agentswarm-config-gc.timer").values("Timer", "OnCalendar"), "the gc's timer")
}

// unit is a systemd unit file as these tests read it: each value given for a
// key, by "Section.Key", in the file's order. As systemd reads it, a line
// that starts with # or ; is a comment, and a value runs to its line's end.
type unit map[string][]string

// readUnit reads the unit file name of the two instances' example. It fails
// the test on a line it cannot read: one outside a section, one without its
// =, and a continued line, which it does not join.
func readUnit(t *testing.T, name string) unit {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(twoInstances, name))
	require.NoError(t, err)
	u, section := unit{}, ""
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = line[1 : len(line)-1]
		default:
			key, value, ok := strings.Cut(line, "=")
			require.True(t, ok && section != "", "%s:%d: %q is no key=value of a section", name, i+1, line)
			require.False(t, strings.HasSuffix(value, `\`), "%s:%d: a continued line, which these tests do not join", name, i+1)
			k := section + "." + strings.TrimSpace(key)
			u[k] = append(u[k], strings.TrimSpace(value))
		}
	}
	return u
}

// values returns the values given for a key of a section.
func (u unit) values(section, key string) []string { return u[section+"."+key] }

// words returns the words of every value given for a key of a section, as
// systemd reads a list, such as After=.
func (u unit) words(section, key string) []string {
	var words []string
	for _, v := range u.values(section, key) {
		words = append(words, strings.Fields(v)...)
	}
	return words
}

// has reports whether a key is given in any section.
func (u unit) has(key string) bool {
	for k := range u {
		if _, name, _ := strings.Cut(k, "."); name == key {
			return true
		}
	}
	return false
}
