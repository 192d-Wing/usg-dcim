//go:build linux

package dnsagent

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/usg-dcim/packages/badger/internal/config"
)

// signalReloads sends the resolver + GoBGP the reload signals each
// process expects. CoreDNS reloads on SIGUSR1, Hickory on SIGHUP,
// GoBGP also on SIGHUP. Resolver PID comes from the pidfile when set;
// for Hickory (no pidfile) we fall back to scanning /proc for the
// binary name. Production deployments run the collector with `pid:
// host` so it can see the resolver's PID namespace directly.
func signalReloads(server *config.DNSServerConfig, engine string) (resolverReloaded, gobgpReloaded bool) {
	resolverPID := resolvePID(server, engine)
	var resolverSig syscall.Signal = syscall.SIGUSR1 // CoreDNS
	if engine == "hickory" {
		resolverSig = syscall.SIGHUP
	}
	resolverReloaded = kill(resolverPID, resolverSig)

	if server.Role == "recursive" {
		gobgpReloaded = kill(readPID(server.GoBGPPIDFile), syscall.SIGHUP)
	}
	return
}

func resolvePID(server *config.DNSServerConfig, engine string) int {
	if pid := readPID(server.CoreDNSPIDFile); pid > 0 {
		return pid
	}
	if engine == "hickory" {
		return findPIDByComm("hickory-dns")
	}
	return 0
}

func readPID(path string) int {
	if path == "" {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

// findPIDByComm walks /proc/<pid>/comm looking for a match. Linux
// truncates comm to 15 chars (TASK_COMM_LEN); we trim our target to
// match so process names longer than that still hit.
func findPIDByComm(comm string) int {
	if comm == "" {
		return 0
	}
	if len(comm) > 15 {
		comm = comm[:15]
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(raw)) == comm {
			return pid
		}
	}
	return 0
}

func kill(pid int, sig syscall.Signal) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, sig) == nil
}
