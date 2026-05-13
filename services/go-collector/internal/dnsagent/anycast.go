package dnsagent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/usg-dcim/services/go-collector/internal/config"
)

// Per-server desired anycast prefix set. The bundle-apply path writes
// this; advertiseLoop reads it every 30s and reconciles gobgpd's RIB.
type anycastState struct {
	mu       sync.Mutex
	prefixes map[string][]string // server-id → prefixes
}

func newAnycastState() *anycastState {
	return &anycastState{prefixes: map[string][]string{}}
}

func (a *anycastState) set(serverID string, prefixes []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]string, len(prefixes))
	copy(cp, prefixes)
	a.prefixes[serverID] = cp
}

func (a *anycastState) get(serverID string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]string, len(a.prefixes[serverID]))
	copy(cp, a.prefixes[serverID])
	return cp
}

// gobgpTargetArgs translates a `host:port` config value into
// `-u host -p port`. The CLI quietly accepts -u host:port but routes
// it through Go's gRPC name resolver which errors on raw IPs; the
// split form sidesteps that path entirely. Matches the Python
// `_gobgp_target_args`.
func gobgpTargetArgs(apiHost string) []string {
	if strings.Count(apiHost, ":") == 1 {
		host, port, _ := strings.Cut(apiHost, ":")
		return []string{"-u", host, "-p", port}
	}
	return []string{"-u", apiHost}
}

func familyFor(prefix string) string {
	if strings.Contains(prefix, ":") {
		return "ipv6"
	}
	return "ipv4"
}

func runGobgp(ctx context.Context, args ...string) (int, string) {
	cmd := exec.CommandContext(ctx, "gobgp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode(), string(out)
		}
		if execErr, ok := err.(*exec.Error); ok && execErr.Err == exec.ErrNotFound {
			return 127, "gobgp binary not on PATH"
		}
		return 1, fmt.Sprintf("gobgp exec failed: %v", err)
	}
	return 0, string(out)
}

// ribPrefixesFromOutput parses prefixes out of `gobgp global rib ipv*`
// output. Each non-header line looks like:
//
//	*> 10.255.0.53/32   0.0.0.0   00:01:23 [...]
//
// We take the second whitespace-separated token from any line that
// starts with `*` (the "best" / valid marker).
func ribPrefixesFromOutput(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "*") {
			continue
		}
		if strings.Contains(fields[1], "/") {
			out[fields[1]] = struct{}{}
		}
	}
	return out
}

func currentRIB(ctx context.Context, apiHost string) map[string]struct{} {
	target := gobgpTargetArgs(apiHost)
	found := map[string]struct{}{}
	for _, fam := range []string{"ipv4", "ipv6"} {
		rc, text := runGobgp(ctx, append(append([]string{}, target...), "global", "rib", fam)...)
		if rc == 0 {
			for p := range ribPrefixesFromOutput(text) {
				found[p] = struct{}{}
			}
		}
	}
	return found
}

// reconcileAdvertise brings gobgpd's RIB to match `desired`: add any
// missing prefix, withdraw any extra. Returns (added, removed, errors)
// so the loop can log a single structured line per cycle.
func reconcileAdvertise(ctx context.Context, server *config.DNSServerConfig, desired []string) (added, removed, errs []string) {
	if server.GoBGPAPIHost == "" {
		return nil, nil, []string{"disabled"}
	}
	target := gobgpTargetArgs(server.GoBGPAPIHost)
	current := currentRIB(ctx, server.GoBGPAPIHost)
	want := map[string]struct{}{}
	for _, p := range desired {
		want[p] = struct{}{}
	}

	// add missing
	for _, p := range diff(want, current) {
		rc, msg := runGobgp(ctx, append(append([]string{}, target...),
			"-a", familyFor(p), "global", "rib", "add", p)...)
		if rc == 0 {
			added = append(added, p)
		} else {
			errs = append(errs, trimErr("add "+p+": ", msg))
		}
	}
	// withdraw extras
	for _, p := range diff(current, want) {
		rc, msg := runGobgp(ctx, append(append([]string{}, target...),
			"-a", familyFor(p), "global", "rib", "del", p)...)
		if rc == 0 {
			removed = append(removed, p)
		} else {
			errs = append(errs, trimErr("del "+p+": ", msg))
		}
	}
	return
}

func diff(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

func trimErr(prefix, msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 80 {
		msg = msg[:80]
	}
	return prefix + msg
}
