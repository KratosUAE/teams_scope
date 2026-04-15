// Package version exposes the build-injected version string and reads
// supplementary VCS metadata (commit hash, build date, dirty flag) from
// the Go runtime's BuildInfo. Go stamps vcs.* settings automatically
// into the binary since 1.18 when the build is done from a VCS
// checkout; the semantic version string comes from an -ldflags -X
// override at build time and falls back to "dev" for ad-hoc builds.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version is the semantic version of this build. Overridden at link
// time via:
//
//	go build -ldflags="-X teams_con/internal/version.Version=v1.0.0" .
//
// Kept as a package-level var (not a const) precisely so the linker
// can rewrite it.
var Version = "dev"

// Info groups every build-provenance string we surface through the
// `teams_con version` subcommand and the MCP server's serverInfo.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"goVersion"`
}

// Get returns the current build's version info. `Version` comes from
// the package variable (ldflags-injected); `Commit` and `Date` come
// from debug.BuildInfo.Settings, which Go populates automatically when
// the module is built inside a git checkout.
func Get() Info {
	info := Info{Version: Version, GoVersion: runtime.Version()}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) > 12 {
				info.Commit = s.Value[:12]
			} else {
				info.Commit = s.Value
			}
		case "vcs.time":
			info.Date = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if dirty && info.Commit != "" {
		info.Commit += "-dirty"
	}
	return info
}

// String renders a single-line human-readable build descriptor like:
//
//	v1.0.0 (abc123def456) built 2026-04-13T12:30:00Z go1.25.5
//
// Empty fields are elided so ad-hoc builds render as "dev go1.25.5"
// without placeholder noise.
func (i Info) String() string {
	s := i.Version
	if i.Commit != "" {
		s += fmt.Sprintf(" (%s)", i.Commit)
	}
	if i.Date != "" {
		s += fmt.Sprintf(" built %s", i.Date)
	}
	s += " " + i.GoVersion
	return s
}
