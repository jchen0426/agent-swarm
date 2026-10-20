//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	toxiproxyImage = "ghcr.io/shopify/toxiproxy:2.12.0"
	toxiproxyAlias = "toxiproxy"
	toxiproxyAPI   = "8474/tcp"

	// A proxy listens on a port of the toxiproxy container, and a container
	// publishes only the ports it was created with, so the harness publishes
	// maxInstances of them, from proxyBasePort on, and gives one to each
	// instance that asks for a database URL.
	proxyBasePort = 8666
	maxInstances  = 8
)

// postgresSSLRequest is the first message a Postgres client may send: its
// length, 8, and the code 80877103, which asks whether the server speaks TLS.
// A server answers one byte, S or N, so the message tells a Postgres behind a
// proxy from a port that only accepts connections.
var postgresSSLRequest = []byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}

// probePostgres sends an SSLRequest to addr, and returns nil when a server
// answers it.
func probePostgres(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write(postgresSSLRequest); err != nil {
		return err
	}
	var reply [1]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply[0] != 'S' && reply[0] != 'N' {
		return fmt.Errorf("answered %q, not S or N", reply[0])
	}
	return nil
}

// toxiproxy is the toxiproxy container, with one proxy in front of Postgres
// for each instance that asked for its database.
type toxiproxy struct {
	container *testcontainers.DockerContainer
	api       string
	// published holds the address, as the test process reaches it, of each
	// port a proxy may listen on in the container.
	published []string

	mu      sync.Mutex
	proxies map[string]*proxy
}

type proxy struct {
	name string
	// addr is where the test process reaches the proxy.
	addr string
}

// startToxiproxy starts a toxiproxy on the network, labelled for make
// test-clean.
func startToxiproxy(ctx context.Context, networkName string) (*toxiproxy, error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	ports := []string{toxiproxyAPI}
	for i := range maxInstances {
		ports = append(ports, strconv.Itoa(proxyBasePort+i)+"/tcp")
	}
	ctr, err := testcontainers.Run(ctx, toxiproxyImage,
		testcontainers.WithExposedPorts(ports...),
		testcontainers.WithLabels(map[string]string{projectLabel: projectName}),
		network.WithNetworkName([]string{toxiproxyAlias}, networkName),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/version").WithPort(toxiproxyAPI)),
	)
	x := &toxiproxy{container: ctr, proxies: map[string]*proxy{}}
	if err != nil {
		return x, err
	}
	api, err := ctr.PortEndpoint(ctx, toxiproxyAPI, "http")
	if err != nil {
		return x, fmt.Errorf("find the toxiproxy API: %w", err)
	}
	x.api = api
	for _, port := range ports[1:] {
		addr, err := ctr.PortEndpoint(ctx, port, "")
		if err != nil {
			return x, fmt.Errorf("find the published port %s: %w", port, err)
		}
		x.published = append(x.published, addr)
	}
	return x, nil
}

// call sends a request to the toxiproxy's API, and returns an error for a
// status other than 2xx.
func (x *toxiproxy) call(ctx context.Context, path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.api+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req) //nolint:gosec // G704: the harness's own toxiproxy.
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("toxiproxy answered %s to POST %s: %s", resp.Status, path, bytes.TrimSpace(answer))
	}
	return nil
}

// proxyFor returns the instance's proxy, creating it on first use: one that
// listens in the container on the instance's own port, and forwards to the
// Postgres alias on the network. It returns once the proxy answers an
// SSLRequest.
func (x *toxiproxy) proxyFor(ctx context.Context, instance string) (*proxy, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if p := x.proxies[instance]; p != nil {
		return p, nil
	}
	n := len(x.proxies)
	if n >= maxInstances {
		return nil, fmt.Errorf("the harness has a proxy for %d instances at most", maxInstances)
	}
	p := &proxy{name: "postgres-" + instance, addr: x.published[n]}
	err := x.call(ctx, "/proxies", map[string]any{
		"name": p.name, "listen": "0.0.0.0:" + strconv.Itoa(proxyBasePort+n),
		"upstream": postgresAlias + ":5432", "enabled": true,
	})
	if err != nil {
		return nil, fmt.Errorf("create the proxy of instance %s: %w", instance, err)
	}
	if err := waitUntil(ctx, 15*time.Second, func() error { return probePostgres(p.addr) }); err != nil {
		return nil, fmt.Errorf("the proxy of instance %s does not answer: %w", instance, err)
	}
	x.proxies[instance] = p
	return p, nil
}

// setEnabled cuts the instance's database connections (enabled false), or
// restores them. Cutting closes the connections the proxy has; the proxy
// accepts no new one until it is restored.
func (x *toxiproxy) setEnabled(ctx context.Context, instance string, enabled bool) error {
	p, err := x.proxyFor(ctx, instance)
	if err != nil {
		return err
	}
	if err := x.call(ctx, "/proxies/"+url.PathEscape(p.name), map[string]any{"enabled": enabled}); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	return waitUntil(ctx, 15*time.Second, func() error { return probePostgres(p.addr) })
}

// waitUntil calls check until it returns nil, and returns its last error when
// the timeout passes first.
func waitUntil(ctx context.Context, timeout time.Duration, check func() error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := check()
	for err != nil {
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
		err = check()
	}
	return nil
}

// cutError says why the harness cannot cut a database, or returns nil when it
// can.
func (e *Env) cutError() error {
	switch {
	case e.db == nil:
		return errors.New("there is no database to cut: Options.NoDatabase is set")
	case e.db.external:
		return fmt.Errorf("a given database (%s) cannot be cut: the harness has no proxy in front of it", databaseEnv)
	case e.tox == nil:
		return errors.New("a database is cut through a toxiproxy: set Options.Toxiproxy")
	}
	return nil
}

// Cut makes the instance's database connections fail: the toxiproxy in front
// of its database closes the connections it has and accepts no new one, while
// the other instances' proxies go on. Restore undoes it.
func (e *Env) Cut(t *testing.T, instance string) {
	t.Helper()
	e.setCut(t, instance, false)
}

// Restore lets the instance's database connections through again.
func (e *Env) Restore(t *testing.T, instance string) {
	t.Helper()
	e.setCut(t, instance, true)
}

func (e *Env) setCut(t testing.TB, instance string, enabled bool) {
	t.Helper()
	if err := e.cutError(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if instance == "" {
		t.Fatal("harness: name the instance whose database to cut")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := e.tox.setEnabled(ctx, instance, enabled); err != nil {
		t.Fatalf("harness: %v", err)
	}
}
