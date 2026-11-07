package main

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersionString(t *testing.T) {
	stamped := "v0.2.0-3-gabc1234"
	released := "v0.1.0"
	revision := "0123456789abcdef0123"
	vcs := func(modified string) []debug.BuildSetting {
		return []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: modified}}
	}
	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{name: "stamped by make", stamped: stamped, info: &debug.BuildInfo{Main: debug.Module{Version: released}}, ok: true, want: stamped},
		{name: "go install of a tag", info: &debug.BuildInfo{Main: debug.Module{Version: released}}, ok: true, want: released},
		{name: "local build", info: &debug.BuildInfo{Main: debug.Module{Version: develVersion}, Settings: vcs("false")}, ok: true, want: develVersion + " " + revision[:shortRevision]},
		{name: "local build with changes", info: &debug.BuildInfo{Main: debug.Module{Version: develVersion}, Settings: vcs("true")}, ok: true, want: develVersion + " " + revision[:shortRevision] + " dirty"},
		{name: "no vcs", info: &debug.BuildInfo{Main: debug.Module{Version: develVersion}}, ok: true, want: develVersion},
		{name: "no build info", want: develVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, versionString(tt.stamped, tt.info, tt.ok))
		})
	}
}
