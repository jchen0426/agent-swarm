//go:build integration && linux

package docker

// The probes of criterion 4 and of the sandbox's limits (Plan 7d-2, Decision
// 11), on a live engine. Each runs the probe image, curl with busybox, in
// place of a runtime, under the runner's own settings (Contract C13),
// through the tests' probe seam (R15), in a sockets directory that serves
// both gateways' stand-ins over TLS with a certificate from NewCertificate.
// A probe's script gets that certificate's PEM, which it writes to
// /tmp/gateways.pem, and prints its findings on its standard output, a
// key=value a line, which the test reads from the turn's output file.
//
// Each probe is a guard, which passed at once against the runner of Tasks 6
// to 8: it keeps what its comment names, a setting of C13's, or else what
// the engine or the image contract gives a sandbox.

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/runner/spec"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

const (
	// probeDeadline is how far away a probe's turn's deadline is, so that
	// no probe is interrupted at its deadline: the test ends each turn first.
	probeDeadline = 10 * time.Minute
	// stopWait bounds how long a stop takes to end a turn.
	stopWait = 10 * time.Second
)

// gatewayStandIn is a gateway's stand-in: it answers every request with its
// text, over TLS 1.3 and HTTP/1.1 alone, as the gateways serve the
// sandboxes (Decision 2), and keeps what each request came over.
type gatewayStandIn struct {
	text string

	mu   sync.Mutex
	seen []request
}

// request is what a stand-in kept of a request: its TLS version, and its
// protocol, such as HTTP/1.1.
type request struct {
	tls   uint16
	proto string
}

func (s *gatewayStandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, request{tls: r.TLS.Version, proto: r.Proto})
	s.mu.Unlock()
	_, _ = io.WriteString(w, s.text)
}

// requests returns the requests the stand-in has answered.
func (s *gatewayStandIn) requests() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// gateways are both gateways' stand-ins, on their sockets in a probe's
// sockets directory, and the certificate they serve.
type gateways struct {
	cert     Certificate
	mcp, llm *gatewayStandIn
}

// serveGateways serves both gateways' stand-ins, with a certificate from
// NewCertificate, on their sockets in dir, mode 0660, as serve makes them,
// so that the sandboxes' group connects. They stop when the test ends.
func serveGateways(t *testing.T, dir string) *gateways {
	t.Helper()
	cert, err := NewCertificate(time.Now())
	require.NoError(t, err)
	return &gateways{
		cert: cert,
		mcp:  serveGateway(t, filepath.Join(dir, MCPSocket), &cert.TLS, "the MCP gateway's stand-in"),
		llm:  serveGateway(t, filepath.Join(dir, LLMSocket), &cert.TLS, "the LLM gateway's stand-in"),
	}
}

// serveGateway serves a gateway's stand-in of text on the socket at the
// path socket.
func serveGateway(t *testing.T, socket string, cert *tls.Certificate, text string) *gatewayStandIn {
	t.Helper()
	l, err := net.Listen("unix", socket)
	require.NoError(t, err)
	s := &gatewayStandIn{text: text}
	var http1 http.Protocols
	http1.SetHTTP1(true)
	srv := &http.Server{
		Handler:   s,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}},
		Protocols: &http1,
		// A handshake a probe's kill cuts short is no finding.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	served := make(chan error, 1)
	go func() { served <- srv.ServeTLS(l, "", "") }()
	t.Cleanup(func() {
		_ = srv.Close()
		<-served
	})
	require.NoError(t, os.Chmod(socket, 0o660))
	return s
}

// sandboxProbe is what a probe runs in its sandboxes: its script, the
// script's arguments, and a change of the runner's options, if it needs
// one.
type sandboxProbe struct {
	script  string
	args    []string
	options func(*Options)
}

// probeEnv is a probe's runner on the live engine (openRunner), under a
// tenant of the test's own, whose sandboxes run the probe's script in place
// of a runtime, in a sockets directory that serves both gateways'
// stand-ins.
type probeEnv struct {
	*liveRunner
	gw *gateways
	// image is the image the probe's turns run, runner.images.thinloop: the
	// probe image, unless the probe's options change it.
	image string
}

// openProbe opens a probe's runner: itOptions's options, under a tenant of
// the probe's own, with the gateways' certificate, the probe image as
// runner.images.thinloop, and the probe's script as the probe seam's
// command (probeCommand), as the probe's options then change them.
func openProbe(t *testing.T, p sandboxProbe) *probeEnv {
	t.Helper()
	c := newClient(t)
	o := itOptions(t, newTenant(t, c))
	gw := serveGateways(t, o.Sockets)
	o.Cert = gw.cert.PEM
	o.probe = probeCommand(gw.cert.PEM, p.script, p.args...)
	o.Images = map[string]string{"thinloop": probeID(t, c)}
	if p.options != nil {
		p.options(&o)
	}
	return &probeEnv{liveRunner: openRunner(t, c, o), gw: gw, image: o.Images["thinloop"]}
}

// probeCommand returns the probe seam's command for a probe's script: sh
// runs it with its first argument, the gateways' certificate's PEM, written
// to /tmp/gateways.pem, which curl's --cacert reads, and shifted off, so
// that the script's arguments are args.
func probeCommand(pem, script string, args ...string) []string {
	return append([]string{"sh", "-c", "printf '%s' \"$1\" >/tmp/gateways.pem\nshift\n" + script, "probe", pem}, args...)
}

// turn returns a turn of the probe's image (liveTurn), whose deadline is
// probeDeadline away.
func (e *probeEnv) turn() *app.Turn {
	turn := liveTurn(e.image)
	turn.Deadline = time.Now().Add(probeDeadline)
	return turn
}

// probeRun is what a probe's turn left: its end, its output, and its
// findings, the value of each line of its output of the form key=value, by
// key, in order.
type probeRun struct {
	end      *liveEnd
	output   string
	findings map[string][]string
}

// run starts a turn of the probe, fails the test at once if its launch
// failed, which the runner reports through Launched alone, waits for its
// end, and returns what the turn left.
func (e *probeEnv) run(t *testing.T) *probeRun {
	t.Helper()
	turn := e.turn()
	e.start(t, turn)
	require.NoError(t, e.launch(t, turn).Err, "the turn's launch failed")
	return e.read(t, turn, e.end(t, turn))
}

// runProbe opens a probe's runner, and runs a turn of it.
func runProbe(t *testing.T, p sandboxProbe) *probeRun {
	t.Helper()
	return openProbe(t, p).run(t)
}

// outputFile returns the path of the turn's output file.
func (e *probeEnv) outputFile(turn *app.Turn) string {
	return filepath.Join(e.o.Output, turn.ID+".out")
}

// read returns what the turn, which ended as end says, left: its output file
// and the findings it holds.
func (e *probeEnv) read(t *testing.T, turn *app.Turn, end *liveEnd) *probeRun {
	t.Helper()
	out, err := os.ReadFile(e.outputFile(turn))
	require.NoError(t, err)
	run := &probeRun{end: end, output: string(out), findings: map[string][]string{}}
	for line := range strings.Lines(run.output) {
		if key, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "="); ok {
			run.findings[key] = append(run.findings[key], value)
		}
	}
	return run
}

// specRead begins the script of a probe that the test ends: it reads the
// first byte of the turn's spec, which the runner writes on the sandbox's
// input once it has started it, and prints spec=read, for which the test
// waits (awaitSpecRead). So no spec that failed to reach the sandbox, after
// which the runner kills the container itself, ends the probe.
const specRead = "head -c 1 >/dev/null\necho spec=read\n"

// awaitSpecRead waits until the turn, whose script begins with specRead, has
// read its spec.
func (e *probeEnv) awaitSpecRead(t *testing.T, turn *app.Turn) {
	t.Helper()
	require.Eventually(t, func() bool {
		out, err := os.ReadFile(e.outputFile(turn))
		return err == nil && strings.Contains(string(out), "spec=read\n")
	}, itWait, 20*time.Millisecond, "the turn did not read its spec")
}

// values returns the last value of each key of the findings.
func (p *probeRun) values() map[string]string {
	values := make(map[string]string, len(p.findings))
	for key, all := range p.findings {
		values[key] = all[len(all)-1]
	}
	return values
}

// TestTheSandboxHasNoInterfaceButLoopback (Decision 11, the network): the
// sandbox's network is none, so /sys/class/net lists lo alone. It keeps
// NetworkMode none.
func TestTheSandboxHasNoInterfaceButLoopback(t *testing.T) {
	run := runProbe(t, sandboxProbe{script: `for interface in /sys/class/net/*; do echo "interface=${interface##*/}"; done`})

	assert.Equal(t, []string{"lo"}, run.findings["interface"], run.output)
}

// TestNoNameResolvesInTheSandbox (Decision 11, the network): with no network
// and 127.0.0.1 alone as its resolver, the sandbox resolves no name: neither
// a public one, nor host.docker.internal, the name engines such as
// OrbStack's give their host. It keeps NetworkMode none, and DNS
// [127.0.0.1] with DNSSearch [.]: either alone keeps names from resolving.
func TestNoNameResolvesInTheSandbox(t *testing.T) {
	run := runProbe(t, sandboxProbe{script: `
resolves() { if "$@" >/dev/null 2>&1; then echo "$*=resolves"; else echo "$*=does not resolve"; fi; }
resolves nslookup example.com
resolves getent hosts host.docker.internal`})

	assert.Equal(t, map[string]string{
		"nslookup example.com":              "does not resolve",
		"getent hosts host.docker.internal": "does not resolve",
	}, run.values(), run.output)
}

// TestNoTCPConnectionLeavesTheSandbox (Decision 11, the network): no TCP
// connection opens from the sandbox, with busybox's nc -z, to a public
// address, to the cloud's metadata address, to the gateway of the engine's
// bridge network, read as docker network inspect bridge reads it, to
// host.docker.internal, or to a listener of the test's own (controlListener),
// which a sandbox on any network but none would reach, on any host. It keeps
// NetworkMode none.
func TestNoTCPConnectionLeavesTheSandbox(t *testing.T) {
	targets := []string{
		"1.1.1.1:443", "169.254.169.254:80", bridgeGateway(t, newClient(t)) + ":80", "host.docker.internal:80",
		controlListener(t),
	}

	run := runProbe(t, sandboxProbe{script: `
for target in "$@"; do
	if nc -z -w 2 "${target%:*}" "${target##*:}" 2>/dev/null; then echo "$target=connects"; else echo "$target=does not connect"; fi
done`, args: targets})

	want := make(map[string]string, len(targets))
	for _, target := range targets {
		want[target] = "does not connect"
	}
	assert.Equal(t, want, run.values(), run.output)
}

// bridgeGateway returns the IPv4 address of the gateway of the engine's
// bridge network.
func bridgeGateway(t *testing.T, c *client.Client) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()
	bridge, err := c.NetworkInspect(ctx, "bridge", client.NetworkInspectOptions{})
	require.NoError(t, err)
	for _, config := range bridge.Network.IPAM.Config {
		if config.Gateway.Is4() {
			return config.Gateway.String()
		}
	}
	require.FailNow(t, "the engine's bridge network has no IPv4 gateway")
	return ""
}

// controlListener listens on a TCP port of the test's own address, the first
// IPv4 address of its interfaces but loopback: on the engine's default
// bridge, where make test-docker runs the test, so that a sandbox on that
// bridge connects to it, and one with no network cannot. It accepts each
// connection, and closes it, until the test ends. It returns its address,
// host:port.
func controlListener(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err)
	var own net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			own = n.IP
			break
		}
	}
	require.NotNil(t, own, "the test has no IPv4 address but loopback")
	l, err := net.Listen("tcp", net.JoinHostPort(own.String(), "0"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return l.Addr().String()
}

// socketsScript asks each gateway its arguments name, three at a time (its
// name, its socket in the sandbox and its URL), with curl, through its
// socket, trusting the gateways' certificate alone, and prints the answer
// and the HTTP version it came over, or else why the connection to the
// socket failed.
const socketsScript = `
while [ "$#" -ge 3 ]; do
	gateway=$1 socket=$2 url=$3
	shift 3
	if version=$(curl -sS -v -o /tmp/answer -w '%{http_version}' --unix-socket "$socket" --cacert /tmp/gateways.pem "$url" 2>/tmp/log); then
		echo "$gateway=$(cat /tmp/answer)"
		echo "$gateway HTTP=$version"
	else
		failure=$(sed -n 's/^\* connect to .* failed: //p' /tmp/log)
		echo "$gateway=${failure:-$(tail -n 1 /tmp/log)}"
	fi
done`

// socketsArgs are socketsScript's arguments for both gateways: their
// sockets in the sandbox and their URLs, as a turn's spec gives them
// (Contract C6).
var socketsArgs = []string{"mcp", path.Join(SocketsDir, MCPSocket), MCPURL, "llm", path.Join(SocketsDir, LLMSocket), LLMURL}

// TestBothSocketsAnswerOverTLS13AndHTTP11WithTheSpecsCertificate (Decision
// 11, the gateways): through the sockets directory's mount, with the
// sockets' group the runner adds, both gateways' sockets answer curl, which
// trusts the certificate the spec carries alone, with the stand-ins' text,
// over HTTP/1.1; each stand-in served its request over TLS 1.3. It keeps
// GroupAdd [runner.docker.group], and the one mount, of the sockets
// directory at /run/agentswarm.
func TestBothSocketsAnswerOverTLS13AndHTTP11WithTheSpecsCertificate(t *testing.T) {
	e := openProbe(t, sandboxProbe{script: socketsScript, args: socketsArgs})

	run := e.run(t)

	assert.Equal(t, map[string]string{
		"mcp": e.gw.mcp.text, "mcp HTTP": "1.1",
		"llm": e.gw.llm.text, "llm HTTP": "1.1",
	}, run.values(), run.output)
	for _, s := range []*gatewayStandIn{e.gw.mcp, e.gw.llm} {
		assert.Equal(t, []request{{tls: tls.VersionTLS13, proto: "HTTP/1.1"}}, s.requests(), s.text)
	}
}

// TestASandboxWithoutTheSocketsGroupCannotConnect (Decision 11, the
// gateways): with Group another group than the sockets directory's, the
// sandbox's connection to each socket is refused with a permission error,
// and neither stand-in sees a request. It keeps User 65532:65532 with
// CapDrop [ALL]: only root with the engine's default capabilities, which
// hold CAP_DAC_OVERRIDE, would connect; neither setting alone opens it.
func TestASandboxWithoutTheSocketsGroupCannotConnect(t *testing.T) {
	e := openProbe(t, sandboxProbe{script: socketsScript, args: socketsArgs, options: func(o *Options) {
		info, err := os.Stat(o.Sockets)
		require.NoError(t, err)
		stat, ok := info.Sys().(*syscall.Stat_t)
		require.True(t, ok, "the sockets directory has no owner's group")
		o.Group = int(stat.Gid) + 1
	}})

	run := e.run(t)

	assert.Equal(t, map[string]string{"mcp": "Permission denied", "llm": "Permission denied"}, run.values(), run.output)
	assert.Empty(t, e.gw.mcp.requests(), "the MCP gateway's stand-in saw a request")
	assert.Empty(t, e.gw.llm.requests(), "the LLM gateway's stand-in saw a request")
}

// TestTheSandboxWritesOnlyWhereItMay (Decision 11, the files): of /, /etc,
// /run/agentswarm, /var, /home, /root, /dev, /tmp and /dev/mqueue, a file
// can be made in /tmp and /dev/mqueue alone: the root and the sockets'
// mount are read-only, and the image's /root, 0700, and the engine's /dev,
// 0755, are root's. A pty opens through /dev/ptmx. The OOM score, which the
// runner raised by 500, can be raised, and not lowered. It keeps
// ReadonlyRootfs, the mount's ReadOnly, User 65532:65532 and OomScoreAdj
// 500; /dev/ptmx is the engine's own devpts.
func TestTheSandboxWritesOnlyWhereItMay(t *testing.T) {
	run := runProbe(t, sandboxProbe{script: `
for dir in / /etc /run/agentswarm /var /home /root /dev /tmp /dev/mqueue; do
	if failure=$(touch "${dir%/}/probe" 2>&1); then echo "$dir=made"; else echo "$dir=${failure##*: }"; fi
done
if (exec 3<>/dev/ptmx) 2>/dev/null; then echo "/dev/ptmx=opens"; else echo "/dev/ptmx=does not open"; fi
echo "oom_score_adj=$(cat /proc/self/oom_score_adj)"
for adj in 499 600; do
	if out=$( (echo "$adj" >/proc/self/oom_score_adj && cat /proc/self/oom_score_adj) 2>&1 ); then echo "oom_score_adj $adj=$out"; else echo "oom_score_adj $adj=${out##*: }"; fi
done`})

	readOnly, denied := "Read-only file system", "Permission denied"
	assert.Equal(t, map[string]string{
		"/": readOnly, "/etc": readOnly, "/run/agentswarm": readOnly, "/var": readOnly, "/home": readOnly,
		"/root": denied, "/dev": denied,
		"/tmp": "made", "/dev/mqueue": "made",
		"/dev/ptmx":     "opens",
		"oom_score_adj": "500", "oom_score_adj 499": denied, "oom_score_adj 600": "600",
	}, run.values(), run.output)
}

// pseudoFilesystems are the types of the filesystems an engine makes for
// each container, which hold nothing of its host's. The host has a tmpfs of
// its own too, so hostMounts tells them apart by where they are mounted, and
// by their devices.
var pseudoFilesystems = []string{"proc", "sysfs", "tmpfs", "devpts", "mqueue"}

// enginesOwnPoint reports whether point is where an engine mounts the pseudo
// filesystems it makes for a container: /proc, /sys and /dev, and below
// them, and the sandbox's /tmp.
func enginesOwnPoint(point string) bool {
	return point == "/tmp" || slices.ContainsFunc([]string{"/proc", "/sys", "/dev"}, func(dir string) bool {
		return point == dir || strings.HasPrefix(point, dir+"/")
	})
}

// hostMounts reads the lines of a /proc/self/mountinfo, and returns each
// mount of its host's files, by where it is mounted, with what it mounts,
// its root. A mount is the container's own when it is its root, the image's
// layers; a pseudo filesystem the engine made for the container, mounted
// whole where an engine mounts one (enginesOwnPoint), or a part of one
// mounted elsewhere, which has its device, such as /proc/sys, or the
// container's /dev/null over /proc/kcore; or its cgroup, whose root is the
// container's subtree where the engine shares the host's cgroup namespace.
// Any other is the host's: a file of the host's tmpfs, such as the engine's
// socket, which lives in /run, and a whole tmpfs of the host's, such as its
// /run, or a credentials directory, mounted anywhere else.
func hostMounts(t *testing.T, lines []string) map[string]string {
	t.Helper()
	type mount struct{ device, root, point, fstype string }
	mounts := make([]mount, 0, len(lines))
	own := map[string]bool{} // the devices of the pseudo filesystems the engine mounted whole
	for _, line := range lines {
		// mountinfo's fields: id, parent, device, root, mount point, options,
		// optional fields up to "-", then the filesystem's type and source.
		fields := strings.Fields(line)
		sep := slices.Index(fields, "-")
		require.True(t, sep >= 6 && len(fields) > sep+2, "a line of mountinfo: %s", line)
		m := mount{device: fields[2], root: fields[3], point: fields[4], fstype: fields[sep+1]}
		if m.root == "/" && slices.Contains(pseudoFilesystems, m.fstype) && enginesOwnPoint(m.point) {
			own[m.device] = true
		}
		mounts = append(mounts, m)
	}
	host := map[string]string{}
	for _, m := range mounts {
		if m.point == "/" || own[m.device] || m.fstype == "cgroup" || m.fstype == "cgroup2" {
			continue
		}
		assert.NotContains(t, host, m.point, "a second mount at %s", m.point)
		host[m.point] = m.root
	}
	return host
}

// TestTheSandboxHoldsNoEngineSocketNorSecrets (Decision 11, the files): the
// sandbox has no /var/run/docker.sock, and no path of a secrets directory
// beside its sockets directory on the engine's host; and of the host's
// filesystems, /proc/self/mountinfo mounts the sockets directory alone, and
// the engine's own files: the container's resolv.conf, hostname and hosts,
// and the engine's init. It keeps the one mount, of the sockets directory,
// and nothing more of the host's: no Binds, no other Mounts.
func TestTheSandboxHoldsNoEngineSocketNorSecrets(t *testing.T) {
	secrets := hostDir(t, "it-secrets-")
	key := filepath.Join(secrets, "vendor.key")
	require.NoError(t, os.WriteFile(key, []byte(hex8()), 0o600))
	e := openProbe(t, sandboxProbe{script: `
for path in /var/run/docker.sock "$@"; do
	if [ -e "$path" ]; then echo "$path=present"; else echo "$path=absent"; fi
done
sed 's/^/mount=/' /proc/self/mountinfo`, args: []string{secrets, key}})

	run := e.run(t)

	for _, p := range []string{"/var/run/docker.sock", secrets, key} {
		assert.Equal(t, []string{"absent"}, run.findings[p], p)
	}
	mounts := run.findings["mount"]
	require.NotEmpty(t, mounts, run.output)
	assert.NotContains(t, strings.Join(mounts, "\n"), secrets, "a mount names the secrets directory")
	roots := hostMounts(t, mounts)
	assert.ElementsMatch(t, []string{SocketsDir, "/etc/resolv.conf", "/etc/hostname", "/etc/hosts", "/sbin/docker-init"},
		slices.Collect(maps.Keys(roots)), run.output)
	// mountinfo's root is a path in its filesystem, which /srv may be: the
	// sockets directory's own name, of the test's, identifies it.
	assert.True(t, strings.HasSuffix(roots[SocketsDir], "/"+filepath.Base(e.o.Sockets)), "%s mounts %s", SocketsDir, roots[SocketsDir])
	id := run.end.shownContainer(t).ID
	for _, file := range []string{"resolv.conf", "hostname", "hosts"} {
		assert.True(t, strings.HasSuffix(roots["/etc/"+file], "/"+id+"/"+file), "/etc/%s mounts %s", file, roots["/etc/"+file])
	}
	assert.True(t, strings.HasSuffix(roots["/sbin/docker-init"], "/docker-init"), "/sbin/docker-init mounts %s", roots["/sbin/docker-init"])
}

// TestTheSandboxsResolverNamesLoopbackAlone (Decision 11, the files): the
// sandbox's /etc/resolv.conf names nameserver 127.0.0.1 alone, and no search
// domain, so that neither the host's resolver nor its search domains reach
// it. It keeps DNS [127.0.0.1] and DNSSearch [.].
func TestTheSandboxsResolverNamesLoopbackAlone(t *testing.T) {
	run := runProbe(t, sandboxProbe{script: `sed 's/^/resolv.conf=/' /etc/resolv.conf`})

	var nameservers, domains []string
	for _, line := range run.findings["resolv.conf"] {
		switch fields := strings.Fields(line); {
		case len(fields) < 2:
		case fields[0] == "nameserver":
			nameservers = append(nameservers, fields[1:]...)
		case fields[0] == "search", fields[0] == "domain":
			domains = append(domains, fields[1:]...)
		}
	}
	assert.Equal(t, []string{"127.0.0.1"}, nameservers, run.output)
	assert.Empty(t, domains, run.output)
}

// TestTheSandboxRunsUnprivileged (Decision 11, privileges): the sandbox runs
// as uid and gid 65532, with no capability in its effective, permitted,
// bounding or ambient set, NoNewPrivs set, and no /dev/shm. For a user who
// is not root, the effective and permitted sets are empty whatever the
// bounding set holds: the bounding set, empty, is the sandbox's own mark of
// CapDrop [ALL], which otherwise holds the engine's defaults. It keeps User
// 65532:65532, CapDrop [ALL], SecurityOpt no-new-privileges and IpcMode none.
func TestTheSandboxRunsUnprivileged(t *testing.T) {
	run := runProbe(t, sandboxProbe{script: `
echo "uid=$(id -u)"
echo "gid=$(id -g)"
sed -En 's/^(CapEff|CapPrm|CapBnd|CapAmb|NoNewPrivs):[[:space:]]*/\1=/p' /proc/self/status
if [ -e /dev/shm ]; then echo "/dev/shm=present"; else echo "/dev/shm=absent"; fi`})

	none := "0000000000000000"
	assert.Equal(t, map[string]string{
		"uid": "65532", "gid": "65532",
		"CapEff": none, "CapPrm": none, "CapBnd": none, "CapAmb": none,
		"NoNewPrivs": "1", "/dev/shm": "absent",
	}, run.values(), run.output)
}

// TestTheSandboxsLimitsAreEnforced (Decision 11, limits): a fork past the
// sandbox's 64 processes fails; and an allocation past its 256 MiB ends the
// container OOMKilled, so the turn reads crashed. The allocation is awk's
// string, doubled until the kernel kills it, its length printed at each
// doubling, so that the test reads how far it got; awk starts once the spec
// is read (specRead), since a spec not delivered would read unavailable
// first. It keeps PidsLimit, and Memory with MemorySwap.
func TestTheSandboxsLimitsAreEnforced(t *testing.T) {
	t.Run("processes", func(t *testing.T) {
		// It tries twice the limit's processes; ash ends at the fork that
		// fails, and the container with it.
		e := openProbe(t, sandboxProbe{script: `
forked=0
while [ "$forked" -lt "$1" ]; do
	sleep 600 &
	forked=$((forked + 1))
	echo "forked=$forked"
done
echo "forks=every one"`, args: []string{"128"}})

		run := e.run(t)

		// The engine's init and the probe's shell are two of the processes.
		forked := run.findings["forked"]
		require.NotEmpty(t, forked, run.output)
		assert.Equal(t, strconv.FormatInt(e.o.Pids-2, 10), forked[len(forked)-1], run.output)
		assert.NotContains(t, run.findings, "forks", run.output)
		assert.Contains(t, run.output, "can't fork: Resource temporarily unavailable")
	})
	t.Run("memory", func(t *testing.T) {
		e := openProbe(t, sandboxProbe{script: specRead + `exec awk 'BEGIN { s = "x"; while (1) { s = s s; print "allocated=" length(s); fflush() } }'`})

		run := e.run(t)

		state := run.end.shownContainer(t).State
		require.NotNil(t, state)
		assert.True(t, state.OOMKilled, "the container did not end OOMKilled")
		assert.Equal(t, item.TurnCrashed, run.end.Class)
		allocated := run.findings["allocated"]
		require.NotEmpty(t, allocated, run.output)
		largest, err := strconv.ParseInt(allocated[len(allocated)-1], 10, 64)
		require.NoError(t, err)
		assert.Less(t, largest, e.o.Memory, "a string of %d bytes was made in a sandbox of %d", largest, e.o.Memory)
	})
}

// TestAnImageThatWouldShapeTheSandboxIsRefused (Decision 11, the image's
// contract; Contract C12): with the contract applied, no probe seam, an
// image that would shape the sandbox, by a VOLUME, a HEALTHCHECK, an
// environment beyond PATH or a label that starts with agentswarm., or one
// with no io.agentswarm.turn-spec, is refused: Open refuses it, naming
// runner.images.thinloop and the field, and a launch of it reports
// ReasonImage. The runner the launches go to opened with an image that
// differs only by that field. It keeps the image contract, which no setting
// of C13's gives.
func TestAnImageThatWouldShapeTheSandboxIsRefused(t *testing.T) {
	c := newClient(t)
	turnSpec := "LABEL " + spec.Label + "=1,2"
	o := itOptions(t, newTenant(t, c))
	o.Images = map[string]string{"thinloop": importImage(t, c, turnSpec)}
	lr := openRunner(t, c, o)
	for _, row := range []struct {
		name    string
		changes []string
		// says is what the refusal says of the field.
		says string
	}{
		{name: "a volume", changes: []string{turnSpec, "VOLUME /data"}, says: "volumes"},
		{name: "a health check", changes: []string{turnSpec, "HEALTHCHECK CMD true"}, says: "health check"},
		{name: "HTTPS_PROXY in its environment", changes: []string{turnSpec, "ENV HTTPS_PROXY=http://proxy.invalid:3128"}, says: "HTTPS_PROXY"},
		{name: "the label agentswarm.data", changes: []string{turnSpec, "LABEL agentswarm.data=/etc"}, says: "agentswarm.data"},
		{name: "no " + spec.Label, says: "no label " + spec.Label},
	} {
		t.Run(row.name, func(t *testing.T) {
			image := importImage(t, c, row.changes...)
			refused := itOptions(t, newTenant(t, c))
			refused.Images = map[string]string{"thinloop": image}
			turn := liveTurn(image)

			_, err := tryOpen(t, c, refused)
			lr.start(t, turn)
			launch := lr.launch(t, turn)

			require.ErrorContains(t, err, keyImages+"thinloop")
			require.ErrorContains(t, err, row.says)
			assert.Equal(t, app.ReasonImage, launch.Reason)
			assert.ErrorContains(t, launch.Err, row.says)
		})
	}
}

// TestAnImagesOwnSettingsDoNotShapeTheSandbox (Decision 3): an image of the
// probe image's filesystem, whose own settings are STOPSIGNAL SIGQUIT, USER
// root and WORKDIR /etc, its contract skipped by the probe seam, runs as
// 65532 in /tmp; and the engine shows its container's stop signal SIGTERM,
// with a timeout of 10 s. It keeps User, WorkingDir, StopSignal and
// StopTimeout.
func TestAnImagesOwnSettingsDoNotShapeTheSandbox(t *testing.T) {
	c := newClient(t)
	image := importProbeFilesystem(t, c, "STOPSIGNAL SIGQUIT", "USER root", "WORKDIR /etc")
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()
	own, err := c.ImageInspect(ctx, image)
	require.NoError(t, err)
	require.NotNil(t, own.Config)
	require.Equal(t, []string{"SIGQUIT", "root", "/etc"}, []string{own.Config.StopSignal, own.Config.User, own.Config.WorkingDir}, "the image's own settings")

	run := runProbe(t, sandboxProbe{
		script:  `echo "uid=$(id -u)"; echo "gid=$(id -g)"; echo "dir=$(pwd)"`,
		options: func(o *Options) { o.Images = map[string]string{"thinloop": image} },
	})

	assert.Equal(t, map[string]string{"uid": "65532", "gid": "65532", "dir": "/tmp"}, run.values(), run.output)
	shown := run.end.shownContainer(t).Config
	require.NotNil(t, shown)
	assert.Equal(t, "SIGTERM", shown.StopSignal)
	require.NotNil(t, shown.StopTimeout, "the container has no stop timeout")
	assert.Equal(t, 10, *shown.StopTimeout)
}

// importProbeFilesystem imports an image of the probe image's filesystem,
// with changes (importLayer): the export of a container of the probe image,
// which never starts, and which is removed when the test ends.
func importProbeFilesystem(t *testing.T, c *client.Client, changes ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()
	created, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: probeID(t, c), Labels: itLabels()},
	})
	require.NoError(t, err)
	t.Cleanup(func() { removeContainer(t, c, created.ID) })
	exported, err := c.ContainerExport(ctx, created.ID, client.ContainerExportOptions{})
	require.NoError(t, err)
	defer func() { _ = exported.Close() }()
	return importLayer(t, c, exported, changes...)
}

// TestAKillTakesTheSetsidChildrenWithIt (Decision 11, the setsid escape): a
// probe starts three children in sessions of their own, which ignore TERM
// and HUP, each asking the MCP gateway's stand-in, through its socket,
// every 100 ms, with curl, and the stand-in sees their requests. Stop ends
// the turn; then no container of it remains, and no request reaches the
// stand-in for 2 s. The test observes the children through the stand-in:
// it runs in the test container's PID namespace, and on a Mac the engine's
// host is OrbStack's VM, so it cannot list the host's processes (plan
// review 1, M13). It keeps the runner's Stop, and what the engine gives:
// the container's PID namespace, whose end ends every process of it.
func TestAKillTakesTheSetsidChildrenWithIt(t *testing.T) {
	e := openProbe(t, sandboxProbe{script: `
for child in 1 2 3; do
	setsid sh -c '
		trap "" TERM HUP
		read -r _ _ _ _ _ session _ </proc/self/stat
		echo "child session=$session"
		while :; do
			curl -s -o /dev/null --unix-socket "$1" --cacert /tmp/gateways.pem "$2"
			sleep 0.1
		done' child "$1" "$2" &
done
read -r _ _ _ _ _ session _ </proc/self/stat
echo "session=$session"
exec sleep 600`, args: []string{path.Join(SocketsDir, MCPSocket), MCPURL}})
	turn := e.turn()
	e.start(t, turn)
	require.NoError(t, e.launch(t, turn).Err)
	require.Eventually(t, func() bool { return len(e.gw.mcp.requests()) >= 10 }, itWait, 50*time.Millisecond,
		"the stand-in saw no request of the children's")

	stopped := time.Now()
	e.r.Stop(turn.Run, turn.Agent)

	end := e.end(t, turn)
	assert.Less(t, time.Since(stopped), stopWait, "the stop did not end the turn")
	assert.Equal(t, item.TurnInterrupted, end.Class)
	e.awaitGone(t, turn.ID)
	seen := len(e.gw.mcp.requests())
	assert.Never(t, func() bool { return len(e.gw.mcp.requests()) > seen }, 2*time.Second, 50*time.Millisecond,
		"a request reached the stand-in once the turn's container was gone")
	run := e.read(t, turn, end)
	require.Len(t, run.findings["session"], 1, run.output)
	assert.Len(t, run.findings["child session"], 3, run.output)
	assert.NotContains(t, run.findings["child session"], run.findings["session"][0], "a child ran in the probe's own session")
}

// TestASandboxEndsWhenItsAttachmentEnds (Decision 11, serve's death): a
// probe running cat, which reads its input until it ends, runs, its spec
// read, until the runner's attachment of it ends, as serve's death ends it;
// the container then ends within 2 s, by the end the engine records: the
// engine and the test read one kernel's clock. It keeps OpenStdin with
// StdinOnce.
func TestASandboxEndsWhenItsAttachmentEnds(t *testing.T) {
	e := openProbe(t, sandboxProbe{script: specRead + `exec cat >/dev/null`})
	turn := e.turn()
	e.start(t, turn)
	require.NoError(t, e.launch(t, turn).Err)
	e.awaitSpecRead(t, turn)
	running, err := e.runs(turn)
	require.NoError(t, err)
	require.True(t, running, "the container does not run")
	att := e.attachment(t, turn)

	closed := time.Now()
	require.NoError(t, att.Close())

	state := e.end(t, turn).shownContainer(t).State
	require.NotNil(t, state)
	finished, err := time.Parse(time.RFC3339Nano, state.FinishedAt)
	require.NoError(t, err)
	assert.WithinRange(t, finished, closed, closed.Add(2*time.Second), "the container did not end within 2 s of its attachment's end")
}

// TestAContainerTheEngineStopsOrKillsReadsUnavailable (Review Focus 1): a
// probe running sleep 600, its spec read, that the engine ends, as its stop
// or restart does, and as the runner never does, reads unavailable: docker
// stop's SIGTERM ends it with 143; docker kill's SIGKILL, with 137, and not
// OOMKilled. The stop's row keeps StopSignal SIGTERM, and Init, the engine's
// init, which passes the signal on; the kill's row keeps byContainer's rule
// for a signal the runner did not send.
func TestAContainerTheEngineStopsOrKillsReadsUnavailable(t *testing.T) {
	for _, row := range []struct {
		name string
		// act is what the engine is asked to do to the container named name.
		act  func(ctx context.Context, c *client.Client, name string) error
		exit int
	}{
		{
			name: "docker stop",
			act: func(ctx context.Context, c *client.Client, name string) error {
				_, err := c.ContainerStop(ctx, name, client.ContainerStopOptions{})
				return err
			},
			exit: 128 + int(syscall.SIGTERM),
		},
		{
			name: "docker kill",
			act: func(ctx context.Context, c *client.Client, name string) error {
				_, err := c.ContainerKill(ctx, name, client.ContainerKillOptions{Signal: "SIGKILL"})
				return err
			},
			exit: exitKilled,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			e := openProbe(t, sandboxProbe{script: specRead + `exec sleep 600`})
			turn := e.turn()
			e.start(t, turn)
			require.NoError(t, e.launch(t, turn).Err)
			e.awaitSpecRead(t, turn)
			ctx, cancel := context.WithTimeout(t.Context(), itWait)
			defer cancel()

			require.NoError(t, row.act(ctx, e.c, containerName(turn.ID)))

			end := e.end(t, turn)
			assert.Equal(t, item.TurnUnavailable, end.Class)
			state := end.shownContainer(t).State
			require.NotNil(t, state)
			assert.Equal(t, row.exit, state.ExitCode)
			assert.False(t, state.OOMKilled)
		})
	}
}
