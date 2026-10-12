// Package versioninfo reports the version, commit and build date of the running binary.
package versioninfo

import "runtime/debug"

// The linker sets these with -ldflags "-X" (see the Makefile and .goreleaser.yaml).
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// Info describes one build of the binary.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Get returns the build information. When the linker did not set the commit,
// it falls back to the version control data that the Go toolchain records.
func Get() Info {
	info := Info{Version: version, Commit: commit, Date: date}
	if info.Commit != "" {
		return info
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.time":
			info.Date = s.Value
		}
	}
	return info
}
