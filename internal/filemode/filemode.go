// Package filemode names the filesystem permission bits udm-iptv uses, so a
// mode's intent (private vs. shared, directory vs. file) reads at the call
// site instead of as a bare octal literal.
//
// The constants are untyped so they convert directly to os.FileMode,
// uint32 (unix.Open's perm argument) or any other integer permission type.
package filemode

const (
	// PrivateDir is an owner-only directory: config, state and secret material.
	PrivateDir = 0o700
	// SharedDir is a world-traversable directory: installed binaries and caches.
	SharedDir = 0o755
	// PrivateFile is an owner-only file: credentials, tokens and local state.
	PrivateFile = 0o600
	// SharedFile is a world-readable file: status and systemd unit files.
	SharedFile = 0o644
	// Executable is a world-executable file: an installed or downloaded binary.
	Executable = 0o755
	// PrivateExecutable is an owner-only executable: a staged runtime copy
	// nothing but this process is meant to invoke directly.
	PrivateExecutable = 0o700
)
