package main

import (
	"runtime/debug"
	"testing"
)

func TestPickVersion(t *testing.T) {
	release := &debug.BuildInfo{} // goreleaser stamps the version; build info is irrelevant
	install := &debug.BuildInfo{Main: debug.Module{Version: "v1.4.0"}}
	devBuild := &debug.BuildInfo{
		Main: debug.Module{Version: "1.4.1-0.20260808004201-d7b9d3ff5354+dirty"},
		Settings: []debug.BuildSetting{
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: "d7b9d3ff5354"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	for _, tc := range []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{"release stamp wins", "1.4.0", release, "1.4.0"},
		{"release stamp beats build info", "1.4.0", install, "1.4.0"},
		{"go install falls back to the module version", versionPlaceholder, install, "1.4.0"},
		{"local build keeps the placeholder", versionPlaceholder, devBuild, versionPlaceholder},
		{"no build info keeps the placeholder", versionPlaceholder, nil, versionPlaceholder},
		{"module reporting devel keeps the placeholder", versionPlaceholder, &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, versionPlaceholder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickVersion(tc.stamped, tc.info); got != tc.want {
				t.Errorf("pickVersion(%q) = %q, want %q", tc.stamped, got, tc.want)
			}
		})
	}
}
