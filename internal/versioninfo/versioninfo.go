// Package versioninfo reports the version, commit and build date of the running binary.
package versioninfo

import "runtime/debug"

// The linker sets these with -ldflags "-X" (see the Makefile and .goreleaser.yaml).
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// Info describes one build of the binary. Modified is true when the tree
// had uncommitted changes.
type Info struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Modified bool   `json:"modified,omitempty"`
}

// Build names the code of the build: its commit, with "+modified" when the
// tree had uncommitted changes, or "unknown" when no commit is recorded.
func (i Info) Build() string {
	switch {
	case i.Commit == "":
		return "unknown"
	case i.Modified:
		return i.Commit + "+modified"
	}
	return i.Commit
}

// Get returns the build information. When the linker did not set the commit,
// it falls back to the version control data that the Go toolchain records.
func Get() Info {
	info := Info{Version: version, Commit: commit, Date: date}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	linked := info.Commit != ""
	for _, s := range bi.Settings {
		switch {
		case s.Key == "vcs.modified":
			info.Modified = s.Value == "true"
		case linked:
		case s.Key == "vcs.revision":
			info.Commit = s.Value
		case s.Key == "vcs.time":
			info.Date = s.Value
		}
	}
	return info
}
