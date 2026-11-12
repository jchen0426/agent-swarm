//go:build e2e

package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

// The NetLog's phases, as its constants name them.
const (
	netlogPhaseEnd = 2
	// errNameNotResolved is ERR_NAME_NOT_RESOLVED's code, used when the
	// log's constants leave it out.
	errNameNotResolved = -105
)

// netlogEvent is one event of a NetLog.
type netlogEvent struct {
	Type   int `json:"type"`
	Phase  int `json:"phase"`
	Source struct {
		ID int `json:"id"`
	} `json:"source"`
	Params map[string]any `json:"params"`
}

// netlogConstantsLine is the start of a NetLog's first line, which holds
// its constants: the names of its event types and of its errors.
const netlogConstantsLine = `{"constants":`

// ReadNetLog applies R185 to a NetLog file, line by line.
//
// A NetLog is one event a line once its first line, the constants, is read;
// it is complete only once Chrome has exited, and a log read while Chrome
// runs ends at its last whole line. The run fails on each of these, one
// line each, in the order the log first shows them:
//   - a TCP connect, one a socket, to any address and port but allowed's:
//     a TCP_CONNECT_ATTEMPT's address, a TCP_CONNECT's end's
//     remote_address, or a TCP_STREAM_ATTEMPT_* event's address; and an
//     attempt whose socket never names an address at all, as an attempt's
//     end alone names none;
//   - any UDP_BYTES_SENT, one a socket, named by the address its socket
//     connected to, as the bytes' own event names none;
//   - a URL request to a host and port off allowed that did not end in
//     ERR_NAME_NOT_RESOLVED, the resolver rule's answer (R182), or never
//     ended;
//   - no TCP connect at all to allowed, the positive control, "saw no
//     connects".
//
// connects is how many sockets connected to allowed.
func ReadNetLog(path string, allowed []string) (findings []string, connects int, err error) {
	r, err := readNetLog(path, allowed)
	if err != nil {
		return nil, 0, err
	}
	findings, connects = r.verdict()
	return findings, connects, nil
}

// netlogReader is what a NetLog showed, by source.
type netlogReader struct {
	allowed     map[string]bool
	allowedList []string
	names       map[int]string
	errors      map[int]string
	notResolved int

	// findings are keyed by rule and source, in the order first seen.
	order    []string
	findings map[string]*netlogFinding
	// connected are the sockets that connected to allowed.
	connected map[int]bool
	// udp is each UDP socket's connected address; sockets each TCP socket's
	// or stream attempt's.
	udp, sockets map[int]string
	requests     map[int]*netlogRequest
	requestOrder []int
	// complete is whether the log closed its events and itself, as it does
	// once Chrome has exited.
	complete, eventsClosed bool
}

type netlogFinding struct {
	text  string
	count int
	// nameless marks an attempt of source src that named no address: a
	// finding only if src never names one.
	nameless bool
	src      int
}

// netlogRequired are the event types R185's rules read: a NetLog whose
// constants lack one, as a Chrome that renamed it would write, cannot be
// judged, and is an error rather than a pass.
var netlogRequired = []string{"TCP_CONNECT", "TCP_CONNECT_ATTEMPT", "UDP_CONNECT", "UDP_BYTES_SENT", "REQUEST_ALIVE"}

// netlogRequest is a URL request: the URLs it asked for, its redirects
// included, and how it ended.
type netlogRequest struct {
	urls  []string
	ended bool
	err   int
}

func readNetLog(path string, allowed []string) (*netlogReader, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the run directory's NetLog, which the harness names.
	if err != nil {
		return nil, fmt.Errorf("harness: read the NetLog: %w", err)
	}
	defer func() { _ = f.Close() }()
	r := &netlogReader{
		allowed: map[string]bool{}, allowedList: allowed, notResolved: errNameNotResolved,
		findings: map[string]*netlogFinding{}, connected: map[int]bool{},
		udp: map[int]string{}, sockets: map[int]string{}, requests: map[int]*netlogRequest{},
	}
	for _, a := range allowed {
		r.allowed[normalAddr(a)] = true
	}
	br := bufio.NewReaderSize(f, 1<<16)
	first, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("harness: read the NetLog: %w", err)
	}
	if err := r.constants(first); err != nil {
		return nil, err
	}
	for n := 2; ; n++ {
		line, err := br.ReadBytes('\n')
		whole := err == nil
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("harness: read the NetLog: %w", err)
		}
		if len(line) > 0 {
			if lerr := r.line(bytes.TrimSpace(line)); lerr != nil {
				if !whole {
					// The last line, still being written while Chrome runs.
					break
				}
				return nil, fmt.Errorf("harness: the NetLog's line %d: %w", n, lerr)
			}
		}
		if !whole {
			break
		}
	}
	return r, nil
}

// constants reads the first line: {"constants":{...},.
func (r *netlogReader) constants(line []byte) error {
	raw, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte(netlogConstantsLine))
	if !ok {
		return errors.New("harness: not a NetLog: its first line holds no constants")
	}
	raw = bytes.TrimSuffix(raw, []byte(","))
	var c struct {
		LogEventTypes map[string]int `json:"logEventTypes"`
		NetError      map[string]int `json:"netError"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("harness: the NetLog's constants: %w", err)
	}
	for _, name := range netlogRequired {
		if _, ok := c.LogEventTypes[name]; !ok {
			return fmt.Errorf("harness: the NetLog's constants name no %s, which R185 reads: it cannot be judged", name)
		}
	}
	r.names = make(map[int]string, len(c.LogEventTypes))
	for name, id := range c.LogEventTypes {
		r.names[id] = name
	}
	r.errors = make(map[int]string, len(c.NetError))
	for name, code := range c.NetError {
		r.errors[code] = name
	}
	if code, ok := c.NetError["ERR_NAME_NOT_RESOLVED"]; ok {
		r.notResolved = code
	}
	return nil
}

// line reads one line after the constants: an event, the events' list's
// opening or closing, or what follows it.
func (r *netlogReader) line(line []byte) error {
	r.complete = false
	switch {
	case len(line) == 0:
		return nil
	case bytes.Equal(line, []byte("}")):
		r.complete = r.eventsClosed
		return nil
	case line[0] != '{':
		if bytes.HasPrefix(line, []byte("]")) {
			r.eventsClosed = true
		}
		return nil
	}
	raw := bytes.TrimSuffix(line, []byte(","))
	if trimmed, closed := bytes.CutSuffix(raw, []byte("]")); closed {
		raw, r.eventsClosed = trimmed, true
	}
	var e netlogEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	r.event(&e)
	return nil
}

// event applies R185 to one event.
func (r *netlogReader) event(e *netlogEvent) {
	name := r.names[e.Type]
	src := e.Source.ID
	switch {
	case name == "TCP_CONNECT_ATTEMPT" || strings.HasPrefix(name, "TCP_STREAM_ATTEMPT_"):
		// An attempt's end names no address, and its ALIVE may begin before
		// its CONNECT names one: an event without one is judged by its
		// source's address, in verdict, once the whole log is read.
		addr := param(e.Params, "address", "ip_endpoint", "remote_address")
		switch {
		case addr != "":
			r.sockets[src] = addr
			r.connect(src, name, addr)
		case r.sockets[src] == "":
			r.findNameless(src, fmt.Sprintf("a TCP connect that names no address (%s, source %d)", name, src))
		}
	case name == "TCP_CONNECT" && e.Phase == netlogPhaseEnd:
		if addr := param(e.Params, "remote_address"); addr != "" {
			r.sockets[src] = addr
			r.connect(src, name, addr)
		}
	case name == "UDP_CONNECT":
		if addr := param(e.Params, "address"); addr != "" {
			r.udp[src] = addr
		}
	case name == "UDP_BYTES_SENT":
		addr := param(e.Params, "address")
		if addr == "" {
			addr = r.udp[src]
		}
		if addr == "" {
			addr = "an address it never named"
		}
		r.find("udp", src, fmt.Sprintf("UDP bytes sent on a socket connected to %s (UDP_BYTES_SENT, source %d)", addr, src))
	case name == "REQUEST_ALIVE" || name == "URL_REQUEST_START_JOB":
		r.request(src, name, e)
	}
}

// connect counts a socket's connect to allowed, or finds one elsewhere.
func (r *netlogReader) connect(src int, event, addr string) {
	if r.allowed[normalAddr(addr)] {
		r.connected[src] = true
		return
	}
	r.find("tcp", src, fmt.Sprintf("a TCP connect to %s, off the environment (%s, source %d)", addr, event, src))
}

// request keeps a URL request's URLs and how it ended.
func (r *netlogReader) request(src int, event string, e *netlogEvent) {
	q := r.requests[src]
	if q == nil {
		q = &netlogRequest{}
		r.requests[src] = q
		r.requestOrder = append(r.requestOrder, src)
	}
	if u := param(e.Params, "url"); u != "" && (len(q.urls) == 0 || q.urls[len(q.urls)-1] != u) {
		q.urls = append(q.urls, u)
	}
	if event == "REQUEST_ALIVE" && e.Phase == netlogPhaseEnd {
		q.ended = true
		if code, ok := e.Params["net_error"].(float64); ok {
			q.err = int(code)
		}
	}
}

// find adds a finding, once for its rule and source, counting the rest.
func (r *netlogReader) find(rule string, src int, text string) {
	key := rule + "/" + strconv.Itoa(src)
	if f := r.findings[key]; f != nil {
		f.count++
		return
	}
	r.findings[key] = &netlogFinding{text: text, count: 1}
	r.order = append(r.order, key)
}

// findNameless notes an attempt of src that named no address, which verdict
// reports only if src never names one, in its place among the findings.
func (r *netlogReader) findNameless(src int, text string) {
	key := "nameless/" + strconv.Itoa(src)
	if r.findings[key] == nil {
		r.findings[key] = &netlogFinding{text: text, count: 1, nameless: true, src: src}
		r.order = append(r.order, key)
	}
}

// verdict is R185's findings, and the connects to allowed.
func (r *netlogReader) verdict() (findings []string, connects int) {
	for _, key := range r.order {
		f := r.findings[key]
		if f.nameless && r.sockets[f.src] != "" {
			continue // its source named an address after all, and was judged by it
		}
		text := f.text
		if f.count > 1 {
			text += fmt.Sprintf(", %d times", f.count)
		}
		findings = append(findings, text)
	}
	for _, src := range r.requestOrder {
		q := r.requests[src]
		i := slices.IndexFunc(q.urls, func(u string) bool { return !r.allowedURL(u) })
		if i < 0 {
			continue
		}
		switch {
		case !q.ended:
			findings = append(findings, fmt.Sprintf("a URL request to %s that had not ended (source %d)", q.urls[i], src))
		case q.err != r.notResolved:
			findings = append(findings, fmt.Sprintf("a URL request to %s that ended in %s, not ERR_NAME_NOT_RESOLVED (source %d)",
				q.urls[i], r.errorName(q.err), src))
		}
	}
	if len(r.connected) == 0 {
		findings = append(findings, "saw no connects to "+strings.Join(r.allowedList, ", ")+": the check saw nothing it could pass")
	}
	return findings, len(r.connected)
}

func (r *netlogReader) errorName(code int) string {
	if code == 0 {
		return "success"
	}
	if name, ok := r.errors[code]; ok {
		return name
	}
	return "error " + strconv.Itoa(code)
}

// allowedURL says whether u is on a host and port of allowed.
func (r *netlogReader) allowedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https", "wss":
			port = "443"
		case "http", "ws":
			port = "80"
		default:
			return false
		}
	}
	return r.allowed[normalAddr(net.JoinHostPort(u.Hostname(), port))]
}

// normalAddr is a host and port as one string, an address's in its
// canonical form, so that the NetLog's [::1]:443 and an option's match.
func normalAddr(hostport string) string {
	if ap, err := netip.ParseAddrPort(hostport); err == nil {
		return ap.String()
	}
	return strings.ToLower(hostport)
}

// param is the first of keys that params holds as a non-empty string.
func param(params map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := params[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
