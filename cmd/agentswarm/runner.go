package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/admin"
	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/registry/current"
	"github.com/jchen0426/agent-swarm/internal/runner/docker"
	"github.com/jchen0426/agent-swarm/internal/runner/process"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// turnRunner is a runner as serve uses it: the coordinator's turns, and the
// kill of every turn that each of serve's exits makes (Plan 7d-2, Decision
// 5).
type turnRunner interface {
	app.Turns
	KillAll(ctx context.Context)
}

// checkSystem refuses runner.kind docker on a system other than Linux: a
// sandbox reaches serve through a socket in a bind mount, which carries a
// connection only on the containers' own kernel (Plan 7d-2, Decision 1).
func checkSystem(goos, kind string) error {
	if kind == deploy.KindDocker && goos != "linux" {
		return fmt.Errorf("runner.kind: %s runs on Linux alone, and this system is %s: a sandbox reaches serve through "+
			"a Unix socket in a bind mount, which carries a connection only on the containers' own kernel", kind, goos)
	}
	return nil
}

// mcpDebug is the variable the MCP SDK reads its compatibility flags from.
const mcpDebug = "MCPGODEBUG"

// checkEnvironment refuses a non-empty MCPGODEBUG, naming it: the MCP SDK
// reads it once, at its init, and its flags change the SDK's behaviour for the
// whole process, both gateways' included (Plan 7b-1, Decision 15). An empty
// value is taken, since the SDK ignores it; a malformed one never gets here,
// since the SDK panics at its init with its own message. The refusal never
// repeats the value. start calls it with os.Getenv, beside checkSystem.
func checkEnvironment(getenv func(string) string) error {
	if getenv(mcpDebug) != "" {
		return fmt.Errorf("%s is set in the environment, and serve refuses to start while it is: the MCP SDK reads it once, "+
			"at its init, and its flags change the behaviour of both gateways; unset it", mcpDebug)
	}
	return nil
}

// sandboxGroup is runner.docker.group resolved: its default, -1, is serve's
// own group, gid (Plan 7d-2, R9).
func sandboxGroup(b *deploy.Docker, gid int) int {
	if b.Group == -1 {
		return gid
	}
	return b.Group
}

// enginePath is the path of the engine's socket, runner.docker.endpoint
// without unix://, which the deployment's check requires.
func enginePath(b *deploy.Docker) string { return strings.TrimPrefix(b.Endpoint, "unix://") }

// later holds the parts that the Docker runner's ports reach, which wire
// makes once start has opened the runner (Plan 7d-2, R20). wire sets each
// before any port is called: no turn starts before the coordinator takes
// over, under the lead, and neither the runner's start sweep nor its
// minute's job calls a port.
type later struct {
	revs   *current.Revisions
	boards *app.Boards
	coord  *app.Coordinator
}

// openDocker makes the gateways' certificate (Plan 7d-2, Decision 2), and
// opens the Docker runner with it on the deployment's engine (R20), its
// ports bound to parts, which wire fills: the runner checks its engine, its
// store, its holder's lock and its images, and sweeps, each refusal naming
// its key. It returns the certificate, which the agents' listeners serve.
// First, before any engine call, it refuses the sandboxes' group that
// runner.docker.group resolves to, by serve's gid, when that is root's,
// naming the key (Decision 8): only a group left unset can resolve to 0, as
// the deployment's check refuses a group of 0.
func (s *server) openDocker(ctx context.Context, parts *later, gid int) (*tls.Certificate, error) {
	group := sandboxGroup(s.d.Runner.Docker, gid)
	if group < 1 {
		return nil, fmt.Errorf("runner.docker.group: unset, so serve's own group, %d, which is root's: "+
			"the sockets and every sandbox would share it; set a gid above 0", group)
	}
	cert, err := docker.NewCertificate(time.Now())
	if err != nil {
		return nil, err
	}
	engine, err := docker.Dial(s.d.Runner.Docker.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("runner.docker.endpoint: %w", err)
	}
	p, o := s.dockerPorts(ctx, engine, parts), s.dockerOptions(cert.PEM, group)
	r, err := openRunner(ctx, &p, &o)
	if err != nil {
		return nil, err
	}
	s.runner, s.closer = r, r
	return &cert.TLS, nil
}

// openRunner opens the Docker runner, and closes p.Engine when Open refuses:
// a refusal leaves the engine to its caller (Contract C14), and serve dialed
// it. Once Open returns the runner, the runner owns the engine.
func openRunner(ctx context.Context, p *docker.Ports, o *docker.Options) (*docker.Runner, error) {
	r, err := docker.Open(ctx, *p, *o)
	if err != nil {
		_ = p.Engine.Close()
		return nil, err
	}
	return r, nil
}

// dockerPorts are the Docker runner's ports: the engine, the holding, and
// the registry's skills, the board's turn ends and the coordinator's Exited
// and Launched, which reach through parts what wire makes after the runner
// opens (R20), as the process runner's Exited reaches the coordinator.
func (s *server) dockerPorts(ctx context.Context, engine docker.Engine, parts *later) docker.Ports {
	return docker.Ports{
		Engine:  engine,
		Holding: s.hold,
		Skills: func(ctx context.Context, commit, agent string) ([]string, error) {
			return parts.revs.Skills(ctx, commit, agent)
		},
		Ended: func(ctx context.Context, run board.RunID, round int, agent string) (bool, error) {
			return turnEnded(parts.boards)(ctx, run, round, agent)
		},
		Exited:   func(e app.Exit) { parts.coord.Exited(ctx, e) },
		Launched: func(l app.Launch) { parts.coord.Launched(ctx, l) },
		Log:      s.log,
	}
}

// dockerOptions are the Docker runner's options (Contract C14): the
// deployment's runner block and its runner.docker block, with group, the
// sandboxes' group resolved, and the gateways' certificate's PEM, which
// every spec carries.
func (s *server) dockerOptions(certPEM string, group int) docker.Options {
	d, b := s.d, s.d.Runner.Docker
	return docker.Options{
		Tenant: d.Tenant, Holder: d.Holder, Output: d.Runner.Output, OutputCap: d.Runner.OutputCap, Slack: d.Timing.Runtime.Slack,
		MaxTokens: d.Runner.MaxTokens, Images: d.Runner.Images, Sockets: b.Sockets, Store: b.Store, Group: group,
		Labels: b.Labels, Memory: b.Memory, CPUs: b.CPUs, Pids: b.Pids, Files: b.Files, Tmp: b.Tmp, Cert: certPEM,
	}
}

// runnerStatus is the runner's port of the admin API's status (Plan 7d-2,
// Decision 7; Contract C4), by the runner's kind.
func runnerStatus(r turnRunner) interface{ Status() admin.RunnerStatus } {
	if d, ok := r.(*docker.Runner); ok {
		return dockerStatus{d}
	}
	return processStatus{}
}

// dockerStatus is the Docker runner as /v1/status shows it: its turns
// sandboxed, and what its status says of the engine and the images, its
// missing images a list, never nil.
type dockerStatus struct{ r *docker.Runner }

func (s dockerStatus) Status() admin.RunnerStatus {
	st := s.r.Status()
	return admin.RunnerStatus{
		Kind: deploy.KindDocker, Sandboxed: true, Engine: st.Engine, MissingImages: append([]string{}, st.MissingImages...),
	}
}

// processStatus is the process runner as /v1/status shows it: its turns run
// unsandboxed, as host processes.
type processStatus struct{}

func (processStatus) Status() admin.RunnerStatus { return admin.RunnerStatus{Kind: deploy.KindProcess} }

// processRunner makes the process runner, which runs each turn as serve's
// own executable, or the deployment's (R5), and gives it the gateways' bound
// addresses. A turn's end goes to exited.
func (s *server) processRunner(revs *current.Revisions, st *stores, exited func(app.Exit)) (*process.Runner, error) {
	d := s.d
	return process.New(process.Ports{
		Holding: s.hold, Skills: revs.Skills, Ended: turnEnded(st.boards), Exited: exited, Log: s.log,
	}, process.Options{
		Binary: d.Runner.Binary, Output: d.Runner.Output, OutputCap: d.Runner.OutputCap, Slack: d.Timing.Runtime.Slack,
		MCP: "http://" + s.listeners.mcp.Addr().String(), LLM: "http://" + s.listeners.llm.Addr().String(), MaxTokens: d.Runner.MaxTokens,
	})
}

// turnEnded reports whether the board holds an agent's own turn end in a
// round, as the coordinator reads it.
func turnEnded(boards *app.Boards) func(ctx context.Context, run board.RunID, round int, agent string) (bool, error) {
	return func(ctx context.Context, run board.RunID, round int, agent string) (bool, error) {
		report, err := boards.Report(ctx, run, round)
		if err != nil {
			return false, err
		}
		return slices.Contains(report.Facts.Ended, agent), nil
	}
}
