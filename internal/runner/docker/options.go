package docker

import (
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// Options set how the runner runs turns: the deployment's runner block and
// its runner.docker block (Contract C1), resolved. A bracketed value is the
// deployment's default.
type Options struct {
	// Tenant and Holder are the deployment's: they label every sandbox, and
	// name the holder's lock.
	Tenant, Holder string
	Output         string            // runner.output
	OutputCap      int64             // [1 MiB]
	Slack          time.Duration     // [2 s]
	MaxTokens      int64             // [4,096]
	Images         map[string]string // runner.images: checked at Open and every minute
	Sockets        string            // runner.docker.sockets, as the engine's host sees it
	Store          string            // runner.docker.store
	Group          int               // resolved: never -1
	Labels         map[string]string // runner.docker.labels, which every sandbox carries
	// The sandbox's limits (Decision 3): its memory in bytes [256 MiB], with
	// no swap; its whole CPUs [1]; its processes [64]; its open files [256];
	// and the size of its /tmp in bytes [16 MiB].
	Memory, CPUs, Pids, Files, Tmp int64
	Cert                           string // the gateways' certificate, PEM, which every spec carries

	probe []string // the tests' seam (R15): skips the contract, replaces the entrypoint
}

// check refuses options the runner cannot run turns with: an empty tenant,
// holder, output, sockets or store; a cap, maxTokens or limit below 1; a
// negative slack; a group below 1; a cert that holds no PEM certificate.
// Each refusal names its field.
func (o *Options) check() error {
	for _, f := range []struct{ name, value string }{
		{"Tenant", o.Tenant}, {"Holder", o.Holder}, {"Output", o.Output}, {"Sockets", o.Sockets}, {"Store", o.Store},
	} {
		if f.value == "" {
			return fmt.Errorf("docker: Options.%s is empty", f.name)
		}
	}
	for _, f := range []struct {
		name  string
		value int64
	}{
		{"OutputCap", o.OutputCap}, {"MaxTokens", o.MaxTokens},
		{"Memory", o.Memory}, {"CPUs", o.CPUs}, {"Pids", o.Pids}, {"Files", o.Files}, {"Tmp", o.Tmp},
	} {
		if f.value < 1 {
			return fmt.Errorf("docker: Options.%s is %d, below 1", f.name, f.value)
		}
	}
	switch {
	case o.Slack < 0:
		return fmt.Errorf("docker: Options.Slack is %s, below zero", o.Slack)
	case o.Group < 1:
		return fmt.Errorf("docker: Options.Group is %d, below 1", o.Group)
	case !x509.NewCertPool().AppendCertsFromPEM([]byte(o.Cert)):
		return errors.New("docker: Options.Cert holds no PEM certificate")
	}
	return nil
}
