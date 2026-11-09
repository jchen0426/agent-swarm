package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/jchen0426/agent-swarm/internal/deploy"
)

// listeners are serve's three: MCP and LLM for the agents' runtimes, and the
// admin API's, on loopback; with a web block, a fourth, the web interface's;
// and, with listeners.inbound, a fifth, the inbound events'; each of the two
// on loopback, behind the deployment's TLS proxy (Plan 9a-1; spec Section
// 3.6). Under the Docker runner, unlock releases the flock of the sockets
// directory, which holds the agents' two.
type listeners struct {
	mcp, llm, admin, web, inbound net.Listener
	unlock                        func()
}

// listen binds the listeners, so that their addresses are known before
// anything that needs them is made, and refuses an admin, web or inbound
// address that is not on loopback (Decision 1; Plan 9a-1, R86), as the
// deployment's check does first. The web listener is bound only with a web
// block, and the inbound listener only with its key. Under the
// process runner, the agents' two are TCP, on loopback, plain HTTP. Under the
// Docker runner, the admin API's is bound first, and the agents' two are the
// sockets of runner.docker.sockets (listenSockets), for the sandboxes' group,
// each serving TLS with the gateways' certificate, cert (Plan 7d-2,
// Decisions 1 and 2).
func listen(d *deploy.Deployment, cert *tls.Certificate) (_ listeners, err error) {
	l := &d.Listeners
	if err := loopbackOnly(d); err != nil {
		return listeners{}, err
	}
	var lns listeners
	defer func() {
		if err != nil {
			lns.close()
		}
	}()
	if d.Runner.Kind == deploy.KindDocker {
		if lns.admin, err = net.Listen("tcp", l.Admin); err != nil {
			return listeners{}, fmt.Errorf("listeners.admin: %w", err)
		}
		b := d.Runner.Docker
		var mcp, llm net.Listener
		if mcp, llm, lns.unlock, err = listenSockets(d, sandboxGroup(b, os.Getgid()), enginePath(b)); err != nil {
			return listeners{}, err
		}
		lns.mcp, lns.llm = sandboxListener(mcp, cert), sandboxListener(llm, cert)
		if err := lns.listenWeb(d); err != nil {
			return listeners{}, err
		}
		if err := lns.listenInbound(d); err != nil {
			return listeners{}, err
		}
		return lns, nil
	}
	for _, b := range []struct {
		ln        *net.Listener
		key, addr string
	}{{&lns.mcp, "listeners.mcp", l.MCP}, {&lns.llm, "listeners.llm", l.LLM}, {&lns.admin, "listeners.admin", l.Admin}} {
		if *b.ln, err = net.Listen("tcp", b.addr); err != nil {
			return listeners{}, fmt.Errorf("%s: %w", b.key, err)
		}
	}
	if err := lns.listenWeb(d); err != nil {
		return listeners{}, err
	}
	if err := lns.listenInbound(d); err != nil {
		return listeners{}, err
	}
	return lns, nil
}

// loopbackOnly refuses an admin address, a web one with a web block, or an
// inbound one when set, that is not on loopback.
func loopbackOnly(d *deploy.Deployment) error {
	l := &d.Listeners
	if err := onLoopback(l.Admin); err != nil {
		return fmt.Errorf("listeners.admin: %w", err)
	}
	if d.Web != nil {
		if err := onLoopback(l.Web); err != nil {
			return fmt.Errorf("listeners.web: %w", err)
		}
	}
	if l.Inbound != "" {
		if err := onLoopback(l.Inbound); err != nil {
			return fmt.Errorf("listeners.inbound: %w", err)
		}
	}
	return nil
}

// listenWeb binds the web listener, under either runner, with a web block;
// without one, there is none.
func (l *listeners) listenWeb(d *deploy.Deployment) (err error) {
	if d.Web == nil {
		return nil
	}
	if l.web, err = net.Listen("tcp", d.Listeners.Web); err != nil {
		return fmt.Errorf("listeners.web: %w", err)
	}
	return nil
}

// listenInbound binds the inbound listener, under either runner, with
// listeners.inbound; without it, there is none.
func (l *listeners) listenInbound(d *deploy.Deployment) (err error) {
	if d.Listeners.Inbound == "" {
		return nil
	}
	if l.inbound, err = net.Listen("tcp", d.Listeners.Inbound); err != nil {
		return fmt.Errorf("listeners.inbound: %w", err)
	}
	return nil
}

// onLoopback refuses an address whose host is not a loopback IP.
func onLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(host); err != nil || !ip.Unmap().IsLoopback() {
		return fmt.Errorf("%s is not on loopback", addr)
	}
	return nil
}

// close closes the listeners, which removes the Docker runner's sockets, and
// then releases the sockets directory's flock.
func (l *listeners) close() {
	for _, ln := range []net.Listener{l.mcp, l.llm, l.admin, l.web, l.inbound} {
		if ln != nil {
			_ = ln.Close()
		}
	}
	if l.unlock != nil {
		l.unlock()
	}
}

// addresses are what serve writes once its listeners are bound (Contract
// C2). Web is the web listener's, which a file of a deployment without a web
// block leaves out; Inbound is the inbound listener's, which a file of a
// deployment without listeners.inbound leaves out.
type addresses struct {
	PID     int    `json:"pid"`
	MCP     string `json:"mcp"`
	LLM     string `json:"llm"`
	Admin   string `json:"admin"`
	Web     string `json:"web,omitempty"`
	Inbound string `json:"inbound,omitempty"`
}

func (l *listeners) addresses() *addresses {
	a := &addresses{PID: os.Getpid(), MCP: address(l.mcp), LLM: address(l.llm), Admin: address(l.admin)}
	if l.web != nil {
		a.Web = address(l.web)
	}
	if l.inbound != nil {
		a.Inbound = address(l.inbound)
	}
	return a
}

// address is a listener's address as the addresses file gives it (Contract
// C2): host:port for TCP, and unix:<path> for a Unix socket, as the agents'
// listeners are under the Docker runner.
func address(ln net.Listener) string {
	a := ln.Addr()
	if a.Network() == "unix" {
		return "unix:" + a.String()
	}
	return a.String()
}

// writeAddresses writes the addresses file whole: a file of its own in the
// same directory, renamed over it, so that no reader sees part of one.
func writeAddresses(path string, a *addresses) (err error) {
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".addresses-*")
	if err != nil {
		return fmt.Errorf("listeners.addresses: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name()) //nolint:gosec // G703: the file serve just made, beside the deployment's addresses file.
		}
	}()
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("listeners.addresses: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("listeners.addresses: %w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil { //nolint:gosec // G703: the deployment's own addresses file (Contract C2).
		return fmt.Errorf("listeners.addresses: %w", err)
	}
	return nil
}
