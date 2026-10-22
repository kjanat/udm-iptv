package telemetry

import (
	"regexp"
	"runtime/debug"
)

var (
	gitRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)
	goToolchain = regexp.MustCompile(`^go[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)
)

type vcsInfo struct {
	revision, modified, toolchain string
}

func vcsStamp(info *debug.BuildInfo, ok bool) vcsInfo {
	if !ok || info == nil {
		return vcsInfo{}
	}
	result := vcsInfo{}
	if goToolchain.MatchString(info.GoVersion) {
		result.toolchain = info.GoVersion
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if gitRevision.MatchString(setting.Value) {
				result.revision = setting.Value
			}
		case "vcs.modified":
			if setting.Value == "true" || setting.Value == "false" {
				result.modified = setting.Value
			}
		}
	}

	return result
}
