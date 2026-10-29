package docker

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/runner/spec"
)

// runnerWrites lists the spec's versions the Docker runner writes: version 2
// alone, which carries the gateways' sockets and their certificate (R1).
var runnerWrites = []int{spec.V2}

// checkImage applies Contract C12's contract, and returns the spec's version
// to write: the newest the image and the runner (spec.V2 alone) both know.
// arch is the engine's architecture. A refusal names the image, and says
// which of its fields breaks the contract. The runner checks a runtime image
// through Options.specVersion, which holds the contract half of the tests'
// probe seam (R15), never through checkImage itself.
func checkImage(img *Image, arch string) (version int, err error) {
	version, err = contractVersion(img, arch)
	if err == nil {
		err = contractConfig(img)
	}
	if err != nil {
		return 0, fmt.Errorf("docker: the image %s breaks the runtime image's contract: %w", img.ID, err)
	}
	return version, nil
}

// contractVersion checks the image's platform, Linux on the engine's
// architecture, and its label of the spec's versions, and returns the newest
// version it shares with the runner.
func contractVersion(img *Image, arch string) (int, error) {
	switch {
	case img.OS != "linux":
		return 0, fmt.Errorf("its OS is %q, not linux", img.OS)
	case img.Arch != arch:
		return 0, fmt.Errorf("its architecture is %q, not the engine's %q", img.Arch, arch)
	}
	label, ok := img.Labels[spec.Label]
	if !ok {
		return 0, fmt.Errorf("it has no label %s", spec.Label)
	}
	versions, err := spec.ParseLabel(label)
	if err != nil {
		return 0, err
	}
	version, ok := spec.Newest(versions, runnerWrites)
	if !ok {
		return 0, fmt.Errorf("its label %s lists %q, and the runner writes version %d alone", spec.Label, label, spec.V2)
	}
	return version, nil
}

// contractConfig checks the image's configuration: no volume, health check
// (but NONE, R24) or ONBUILD instruction; no environment but PATH; and no
// label that starts with agentswarm., since an image's labels join its
// containers', which the sweep and make test-clean read. A refusal names an
// environment variable, never its value.
func contractConfig(img *Image) error {
	switch {
	case len(img.Volumes) > 0:
		return fmt.Errorf("it declares the volumes %q", img.Volumes)
	case len(img.Healthcheck) > 0 && img.Healthcheck[0] != "NONE":
		return fmt.Errorf("it has a health check, %q", img.Healthcheck)
	case len(img.OnBuild) > 0:
		return fmt.Errorf("it has ONBUILD instructions, %q", img.OnBuild)
	}
	for _, entry := range img.Env {
		if !strings.HasPrefix(entry, "PATH=") {
			name, _, _ := strings.Cut(entry, "=")
			return fmt.Errorf("its environment sets %s, and an image's may set PATH alone", name)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(img.Labels)) {
		if strings.HasPrefix(key, "agentswarm.") {
			return fmt.Errorf("it has the label %s, and the prefix agentswarm. is the runner's", key)
		}
	}
	return nil
}

// specVersion returns the spec's version a launch of img writes, once the
// image meets the contract (checkImage). Under the tests' probe seam (R15),
// which only the package's tests set, it skips the contract, which the probe
// image does not meet, and gives the version the runner writes.
func (o *Options) specVersion(img *Image, arch string) (int, error) {
	if o.probe != nil {
		return spec.V2, nil
	}
	return checkImage(img, arch)
}
