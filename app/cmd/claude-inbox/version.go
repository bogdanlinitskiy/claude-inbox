package main

import (
	"runtime/debug"
	"strings"
)

const (
	develVersion  = "(devel)"
	shortRevision = 12
)

// version is set at build time by make (-ldflags "-X main.version=…"); go install fills the build info instead.
var version string

// versionString names this build: the version make stamped, else the module version go install recorded, else
// the commit the build info carries, marked dirty for uncommitted changes.
func versionString(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "" {
		return stamped
	}
	if !ok {
		return develVersion
	}
	if v := info.Main.Version; v != "" && v != develVersion {
		return v
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision == "" {
		return develVersion
	}
	parts := []string{develVersion, revision[:min(len(revision), shortRevision)]}
	if modified == "true" {
		parts = append(parts, "dirty")
	}
	return strings.Join(parts, " ")
}
