package telemetry

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestVCSStampReadsRevisionAndDropsLdflags(t *testing.T) {
	t.Parallel()
	revision := strings.Repeat("a", 40)
	stamp := vcsStamp(&debug.BuildInfo{
		GoVersion: "go1.27.1",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: revision},
			{Key: "vcs.modified", Value: "false"},
			{Key: "vcs.time", Value: "2026-09-17T03:23:04Z"},
			{Key: "-ldflags", Value: "-X github.com/kjanat/udm-iptv/internal/telemetry.DSN=https://secret@example.invalid/1"},
		},
	}, true)
	if stamp.revision != revision || stamp.modified != "false" || stamp.toolchain != "go1.27.1" {
		t.Fatalf("stamp = %+v", stamp)
	}
}

func TestVCSStampRejectsUnknownValues(t *testing.T) {
	t.Parallel()
	stamp := vcsStamp(&debug.BuildInfo{
		GoVersion: "devel go1.28-abc /home/secret",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "not-a-sha"},
			{Key: "vcs.modified", Value: "maybe"},
		},
	}, true)
	if stamp != (vcsInfo{}) {
		t.Fatalf("stamp = %+v", stamp)
	}
	if got := vcsStamp(nil, true); got != (vcsInfo{}) {
		t.Fatalf("nil info = %+v", got)
	}
	if got := vcsStamp(&debug.BuildInfo{}, false); got != (vcsInfo{}) {
		t.Fatalf("missing info = %+v", got)
	}
}
