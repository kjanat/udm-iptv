// Package updater selects, downloads, verifies and activates releases.
//
// The CLI supplies progress writers and a restart/health-check callback. Network
// setup and daemon supervision stay outside this package. Installation state,
// operation locks and Debian package operations belong to installer.
//
// Standalone updates hold the installation lock through download, replacement
// and recovery. Debian updates delegate to apt without holding that lock because
// postinstall acquires it. Recovery uses its own bounded context so cancelling
// the initiating command cannot cancel rollback.
//
// This is an internal separation, not a separately deployed updater or a
// candidate-version preflight protocol; release verification remains mandatory.
package updater
