//go:build !linux

// Non-Linux fallback so the package compiles on macOS / Windows dev
// machines. Production deployments only run on Linux; calling
// signalReloads off-Linux is a no-op so the bundle apply still
// succeeds, just without telling the resolver to reload.

package dnsagent

import "github.com/usg-dcim/packages/badger/internal/config"

func signalReloads(_ *config.DNSServerConfig, _ string) (bool, bool) {
	return false, false
}
