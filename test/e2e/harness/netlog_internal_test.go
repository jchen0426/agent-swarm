//go:build e2e

package harness

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The canned NetLogs' event and source types. Their numbers are not Chrome's
// own, which change from build to build, so that the reader must take the
// names from the log's constants.
const (
	nlTCPConnect        = 7051
	nlTCPConnectAttempt = 7052
	nlStreamConnect     = 7132
	nlStreamAlive       = 7131
	nlUDPConnect        = 7096
	nlUDPBytesSent      = 7099
	nlRequestAlive      = 7002
	nlStartJob          = 7136

	nlSourceRequest = 801
	nlSourceSocket  = 810
	nlSourceUDP     = 824
	nlSourceStream  = 808

	nlBegin = 1
	nlEnd   = 2
	nlNone  = 0
)

// nlAllowed is the environment's one server in the canned NetLogs.
const nlAllowed = "127.0.0.1:4443"

// nlEventTypes are the canned NetLogs' event types, by name.
var nlEventTypes = map[string]int{
	"TCP_CONNECT": nlTCPConnect, "TCP_CONNECT_ATTEMPT": nlTCPConnectAttempt,
	"TCP_STREAM_ATTEMPT_CONNECT": nlStreamConnect, "TCP_STREAM_ATTEMPT_ALIVE": nlStreamAlive,
	"UDP_CONNECT": nlUDPConnect, "UDP_BYTES_SENT": nlUDPBytesSent,
	"REQUEST_ALIVE": nlRequestAlive, "URL_REQUEST_START_JOB": nlStartJob,
}

// nlConstants is the first line's constants, as Chrome writes them, without
// the event types drop names.
func nlConstants(drop ...string) string {
	types := maps.Clone(nlEventTypes)
	for _, d := range drop {
		delete(types, d)
	}
	raw, err := json.Marshal(map[string]any{
		"logEventTypes": types,
		"logSourceType": map[string]int{"URL_REQUEST": nlSourceRequest, "SOCKET": nlSourceSocket, "UDP_SOCKET": nlSourceUDP, "TCP_STREAM_ATTEMPT": nlSourceStream},
		"logEventPhase": map[string]int{"PHASE_BEGIN": nlBegin, "PHASE_END": nlEnd, "PHASE_NONE": nlNone},
		"netError":      map[string]int{"ERR_ABORTED": -3, "ERR_CONNECTION_REFUSED": -102, "ERR_NAME_NOT_RESOLVED": -105},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// netlogConstants are the canned NetLogs' constants, every event type in.
var netlogConstants = nlConstants()

// nlEvent is one event's line: params is a JSON object, or "" for none.
func nlEvent(typ, phase, source, sourceType int, params string) string {
	p := ""
	if params != "" {
		p = `"params":` + params + `,`
	}
	return fmt.Sprintf(`{%s"phase":%d,"source":{"id":%d,"start_time":"100","type":%d},"time":"101","type":%d}`,
		p, phase, source, sourceType, typ)
}

// nlConnect is an allowed TCP connect, so that a log holds the positive
// control's connect.
func nlConnect(source int, addr string) []string {
	return []string{
		nlEvent(nlTCPConnect, nlBegin, source, nlSourceSocket, `{"address_list":["`+addr+`"],"aliases":[]}`),
		nlEvent(nlTCPConnectAttempt, nlBegin, source, nlSourceSocket, `{"address":"`+addr+`"}`),
		nlEvent(nlTCPConnectAttempt, nlEnd, source, nlSourceSocket, ""),
		nlEvent(nlTCPConnect, nlEnd, source, nlSourceSocket, `{"local_address":"127.0.0.1:50000","remote_address":"`+addr+`"}`),
	}
}

// nlRequest is a URL request's events: its start and, when ended, its end
// with netError, 0 for none.
func nlRequest(source int, url string, ended bool, netError int) []string {
	lines := []string{
		nlEvent(nlRequestAlive, nlBegin, source, nlSourceRequest, `{"priority":"IDLE","url":"`+url+`"}`),
		nlEvent(nlStartJob, nlBegin, source, nlSourceRequest, `{"load_flags":64,"method":"GET","url":"`+url+`"}`),
	}
	if !ended {
		return lines
	}
	end := ""
	if netError != 0 {
		end = fmt.Sprintf(`{"net_error":%d}`, netError)
	}
	return append(lines,
		nlEvent(nlStartJob, nlEnd, source, nlSourceRequest, end),
		nlEvent(nlRequestAlive, nlEnd, source, nlSourceRequest, end))
}

// netlogFile writes a NetLog as Chrome lays one out: the constants on the
// first line, then one event a line, the last closing the list, then the
// polled data and the closing brace.
func netlogFile(t *testing.T, events ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"constants":` + netlogConstants + ",\n")
	b.WriteString("\"events\": [\n")
	for i, e := range events {
		if i == len(events)-1 {
			b.WriteString(e + "],\n")
		} else {
			b.WriteString(e + ",\n")
		}
	}
	b.WriteString(`"polledData": {"socketPoolInfo":[]}` + "\n}\n")
	path := filepath.Join(t.TempDir(), "netlog.json")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

func concat(parts ...[]string) []string {
	var all []string
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

// A TCP connect anywhere but the environment's servers is a finding, each
// way the NetLog shows one: an attempt, a connect's end, and a stream
// attempt, an address-less one included; a connect to the environment
// counts as one.
func TestNetLogFindsOffLoopbackConnects(t *testing.T) {
	path := netlogFile(t, concat(
		nlConnect(50, nlAllowed),
		[]string{
			nlEvent(nlTCPConnectAttempt, nlBegin, 60, nlSourceSocket, `{"address":"192.0.2.1:443"}`),
			nlEvent(nlTCPConnect, nlEnd, 61, nlSourceSocket, `{"local_address":"127.0.0.1:50001","remote_address":"127.0.0.1:9999"}`),
			nlEvent(nlStreamConnect, nlBegin, 62, nlSourceStream, `{"ip_endpoint":"198.51.100.7:443"}`),
			nlEvent(nlStreamConnect, nlEnd, 62, nlSourceStream, `{"net_error":-102}`),
			nlEvent(nlStreamAlive, nlBegin, 63, nlSourceStream, ""),
			nlEvent(nlStreamConnect, nlBegin, 64, nlSourceStream, `{"ip_endpoint":"`+nlAllowed+`"}`),
			nlEvent(nlStreamConnect, nlEnd, 64, nlSourceStream, ""),
			// An attempt's ALIVE may begin before its CONNECT names the
			// address: a source is address-less only if it never names one.
			nlEvent(nlStreamAlive, nlBegin, 65, nlSourceStream, ""),
			nlEvent(nlStreamConnect, nlBegin, 65, nlSourceStream, `{"ip_endpoint":"`+nlAllowed+`"}`),
			nlEvent(nlStreamConnect, nlEnd, 65, nlSourceStream, ""),
			nlEvent(nlStreamAlive, nlEnd, 65, nlSourceStream, ""),
		},
	)...)
	findings, connects, err := ReadNetLog(path, []string{nlAllowed})
	require.NoError(t, err)
	require.Len(t, findings, 4, "%q", findings)
	assert.Contains(t, findings[0], "192.0.2.1:443")
	assert.Contains(t, findings[0], "TCP_CONNECT_ATTEMPT")
	assert.Contains(t, findings[1], "127.0.0.1:9999", "a loopback port off the allowed list is off the environment")
	assert.Contains(t, findings[1], "TCP_CONNECT")
	assert.Contains(t, findings[2], "198.51.100.7:443")
	assert.Contains(t, findings[2], "TCP_STREAM_ATTEMPT_CONNECT")
	assert.Contains(t, findings[3], "TCP_STREAM_ATTEMPT_ALIVE")
	assert.Contains(t, findings[3], "no address")
	for _, f := range findings {
		assert.NotContains(t, f, "\n", "a finding is one line")
	}
	assert.Equal(t, 3, connects, "the attempt and the two stream attempts to the environment")
}

// Any UDP bytes sent are a finding, on a socket connected off loopback,
// named by its connect, as the bytes' own event names no address, or on
// one connected to loopback; a UDP connect alone, a route probe, is not.
func TestNetLogFailsOnAnyUDPBytes(t *testing.T) {
	path := netlogFile(t, concat(
		nlConnect(50, nlAllowed),
		[]string{
			nlEvent(nlUDPConnect, nlBegin, 19, nlSourceUDP, `{"address":"[2001:4860:4860::8888]:443"}`),
			nlEvent(nlUDPConnect, nlEnd, 19, nlSourceUDP, ""),
			nlEvent(nlUDPBytesSent, nlNone, 19, nlSourceUDP, `{"byte_count":1200}`),
			nlEvent(nlUDPBytesSent, nlNone, 19, nlSourceUDP, `{"byte_count":1200}`),
			nlEvent(nlUDPConnect, nlBegin, 20, nlSourceUDP, `{"address":"[2001:4860:4860::8888]:443"}`),
			nlEvent(nlUDPConnect, nlEnd, 20, nlSourceUDP, ""),
			nlEvent(nlUDPConnect, nlBegin, 21, nlSourceUDP, `{"address":"127.0.0.1:443"}`),
			nlEvent(nlUDPBytesSent, nlNone, 21, nlSourceUDP, `{"byte_count":30}`),
		},
	)...)
	findings, connects, err := ReadNetLog(path, []string{nlAllowed})
	require.NoError(t, err)
	require.Len(t, findings, 2, "%q", findings)
	assert.Contains(t, findings[0], "UDP_BYTES_SENT")
	assert.Contains(t, findings[0], "[2001:4860:4860::8888]:443", "the bytes are named by their socket's connect")
	assert.Contains(t, findings[0], "2 times")
	assert.Contains(t, findings[1], "127.0.0.1:443", "any UDP bytes, loopback's too")
	assert.Equal(t, 1, connects)
}

// A URL request to a host off the environment is allowed only when it ended
// in ERR_NAME_NOT_RESOLVED, which the resolver rule gives: one that ended
// otherwise, in success, or never is a finding.
func TestNetLogAllowsOnlyUnresolvedRequestsOffLoopback(t *testing.T) {
	path := netlogFile(t, concat(
		nlConnect(50, nlAllowed),
		nlRequest(20, "http://clients2.google.com/time/1/current", true, -105),
		nlRequest(29, "https://www.google.com/async/folae", true, -3),
		nlRequest(30, "https://example.com/", true, 0),
		nlRequest(31, "https://"+nlAllowed+"/items", true, 0),
		nlRequest(32, "https://"+nlAllowed+"/items/a/board", true, -3),
		nlRequest(33, "https://www.gstatic.com/ohttp", false, 0),
		nlRequest(34, "https://localhost:4443/b", true, -105),
	)...)
	findings, _, err := ReadNetLog(path, []string{nlAllowed})
	require.NoError(t, err)
	require.Len(t, findings, 3, "%q", findings)
	assert.Contains(t, findings[0], "https://www.google.com/async/folae")
	assert.Contains(t, findings[0], "ERR_ABORTED")
	assert.Contains(t, findings[1], "https://example.com/")
	assert.Contains(t, findings[1], "success")
	assert.Contains(t, findings[2], "https://www.gstatic.com/ohttp")
	assert.Contains(t, findings[2], "not ended")
}

// A log with no connect to the environment fails as "saw no connects", the
// positive control; so does one whose only connect is off the allowed list.
// A log cut mid-line, as one read while Chrome runs, is read up to its last
// whole line.
func TestNetLogWantsAConnect(t *testing.T) {
	path := netlogFile(t, nlRequest(20, "http://clients2.google.com/time/1/current", true, -105)...)
	findings, connects, err := ReadNetLog(path, []string{nlAllowed})
	require.NoError(t, err)
	require.Len(t, findings, 1, "%q", findings)
	assert.Contains(t, findings[0], "saw no connects")
	assert.Contains(t, findings[0], nlAllowed)
	assert.Zero(t, connects)
	r, err := readNetLog(path, []string{nlAllowed})
	require.NoError(t, err)
	assert.True(t, r.complete, "a finished log, its events and itself closed")

	path = netlogFile(t, nlConnect(50, "127.0.0.1:5555")...)
	findings, connects, err = ReadNetLog(path, []string{nlAllowed})
	require.NoError(t, err)
	require.Len(t, findings, 2, "%q", findings)
	assert.Contains(t, findings[0], "127.0.0.1:5555", "one finding a socket, its attempt and its end alike")
	assert.Contains(t, findings[1], "saw no connects")
	assert.Zero(t, connects)

	whole := concat(nlConnect(50, nlAllowed), nlRequest(20, "http://clients2.google.com/time", true, -105))
	cut := `{"constants":` + netlogConstants + ",\n\"events\": [\n" + strings.Join(whole, ",\n") + ",\n" + whole[0][:20]
	path = filepath.Join(t.TempDir(), "netlog.json")
	require.NoError(t, os.WriteFile(path, []byte(cut), 0o600))
	findings, connects, err = ReadNetLog(path, []string{nlAllowed})
	require.NoError(t, err, "a log being written ends mid-line")
	assert.Empty(t, findings)
	assert.Equal(t, 1, connects)
	r, err = readNetLog(path, []string{nlAllowed})
	require.NoError(t, err)
	assert.False(t, r.complete, "a log cut while Chrome runs, which /netlog answers as provisional")

	_, _, err = ReadNetLog(filepath.Join(t.TempDir(), "absent.json"), []string{nlAllowed})
	require.Error(t, err)
	path = filepath.Join(t.TempDir(), "netlog.json")
	require.NoError(t, os.WriteFile(path, []byte("not a NetLog\n"), 0o600))
	_, _, err = ReadNetLog(path, []string{nlAllowed})
	require.Error(t, err, "a file without the constants is no NetLog")

	// A log whose constants lack an event type a rule reads is an error,
	// never a pass, as when a later Chrome renames one.
	for _, name := range []string{"TCP_CONNECT", "TCP_CONNECT_ATTEMPT", "UDP_CONNECT", "UDP_BYTES_SENT", "REQUEST_ALIVE"} {
		lines := `{"constants":` + nlConstants(name) + ",\n\"events\": [\n" + strings.Join(nlConnect(50, nlAllowed), ",\n") + "],\n}\n"
		path = filepath.Join(t.TempDir(), "netlog.json")
		require.NoError(t, os.WriteFile(path, []byte(lines), 0o600))
		_, _, err = ReadNetLog(path, []string{nlAllowed})
		require.ErrorContains(t, err, name, "constants without %s", name)
	}
}
