// Recursive DNS bundle assembler + loader (PR 35 — recursive bundle
// 2/N). Pure-of-DB assembler plus a thin DB loader. Mirrors Python's
// _render_recursive_config + render_bundle_for_server's recursive
// branch at services/dns.py L2145 + L2441.
//
// GoBGP rendering deliberately omitted — Cilium BGP owns the BGP
// session at the cluster level (badger-side cleanup landed in PR
// #257). The recursive bundle therefore composes:
//   - render_corefile_recursive (CoreDNS engine, PR #251) OR
//     render_hickory_recursive_config (Hickory engine, PR #258)
//   - per-blocklist RPZ Primary zones (Hickory only — PR #258)
//   - zone files map: just the RPZ files (CoreDNS bundles its
//     blocklists inline; Hickory loads them as Primary zones)
//   - bundle etag over everything
package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// RecursiveBundleInput bundles the pre-loaded data the recursive-
// path assembler needs. Loading from the DB lives in
// loadRecursiveBundleInput so the assembler is testable with
// synthetic fixtures.
type RecursiveBundleInput struct {
	Server            dbq.DnsServer
	Engine            string // "coredns" or "hickory"
	FabricApexes      []string
	AuthUnicastIP     *string
	UpstreamResolvers []string
	Forwarders        []ConditionalForwarder
	Blocklists        []Blocklist

	// Hickory-only knobs. Empty on CoreDNS path.
	DenyNetworks         []string
	AllowNetworks        []string
	PrometheusListenAddr string
	TLSCertPath          string
	TLSKeyPath           string
	DoTEnabled           bool
	DoHEnabled           bool
	TLSListenPort        int32
	HTTPSListenPort      int32
	DoHPath              string
	AllowNetworksStrict  bool

	// Caller-supplied `now` so RPZ SOA serials are testable. Tests
	// pin literal Unix timestamps; production passes time.Now().UTC().
	Now time.Time
}

// AssembleRecursiveBundle composes the recursive-server bundle.
// Pure function: hand it pre-loaded inputs, get back the response
// shape the HTTP endpoint serializes.
func AssembleRecursiveBundle(in RecursiveBundleInput) BundleResult {
	zoneFiles := map[string]string{}
	var corefile string

	if in.Engine == "hickory" {
		rpzZones, rpzRefs := BuildRpzArtifacts(in.Blocklists, in.Now)
		// Fold the RPZ files into the zone-files map; Hickory loads
		// them from the same on-disk zones directory the collector
		// writes.
		for k, v := range rpzZones {
			zoneFiles[k] = v
		}
		corefile = RenderHickoryRecursiveConfig(HickoryRecursiveInput{
			FabricApexes:          in.FabricApexes,
			AuthUnicastIP:         in.AuthUnicastIP,
			UpstreamResolvers:     in.UpstreamResolvers,
			ConditionalForwarders: in.Forwarders,
			RpzZoneRefs:           rpzRefs,
			DenyNetworks:          in.DenyNetworks,
			AllowNetworks:         in.AllowNetworks,
			PrometheusListenAddr:  in.PrometheusListenAddr,
			TLSCertPath:           in.TLSCertPath,
			TLSKeyPath:            in.TLSKeyPath,
			DoTEnabled:            in.DoTEnabled,
			DoHEnabled:            in.DoHEnabled,
			TLSListenPort:         in.TLSListenPort,
			HTTPSListenPort:       in.HTTPSListenPort,
			DoHPath:               in.DoHPath,
			AllowNetworksStrict:   in.AllowNetworksStrict,
		})
	} else {
		// CoreDNS path: blocklists are emitted inline via
		// `template` directives in the catch-all block; no
		// per-blocklist zone files.
		var authIP *string
		if in.AuthUnicastIP != nil && *in.AuthUnicastIP != "" {
			v := *in.AuthUnicastIP
			authIP = &v
		}
		corefile = RenderCorefileRecursive(CorefileRecursiveInput{
			FabricApexes:          in.FabricApexes,
			AuthUnicastIP:         authIP,
			UpstreamResolvers:     in.UpstreamResolvers,
			ConditionalForwarders: in.Forwarders,
			Blocklists:            in.Blocklists,
		})
	}

	etag := BundleEtag(EtagInput{
		Corefile: corefile,
		Zones:    zoneFiles,
	})

	engine := in.Engine
	if engine == "" {
		engine = "coredns"
	}

	return BundleResult{
		Engine:          engine,
		Corefile:        corefile,
		Zones:           zoneFiles,
		Gobgp:           nil, // deprecated — Cilium BGP owns it
		KeyFiles:        map[string]string{},
		Etag:            etag,
		DnstapSocket:    nil, // Hickory has no dnstap; CoreDNS recursive doesn't emit either
		AnycastPrefixes: nil,
	}
}

// recursiveBundleQuerier is the slice of methods the recursive
// loader needs. Narrowed so the test fake stays small.
type recursiveBundleQuerier interface {
	ListApexZoneNamesByFabric(ctx context.Context, fabricID uuid.UUID) ([]string, error)
	GetSameSiteAuthUnicastIP(ctx context.Context, siteID uuid.UUID) (string, error)
	ListDnsForwardersForBundle(ctx context.Context, fabricID uuid.UUID) ([]dbq.DnsForwarderRow, error)
	ListEnabledBlocklistsWithPatternsByFabric(ctx context.Context, fabricID uuid.UUID) ([]dbq.BlocklistForBundleRow, error)
	GetFabricForRecursiveBundle(ctx context.Context, id uuid.UUID) (dbq.FabricForRecursiveBundle, error)
	GetSystemSetting(ctx context.Context, key string) (dbq.SystemSetting, error)
}

// RecursiveBundleConfig threads operator-side env/settings into the
// loader. Kept on the Handler so the HTTP layer wires it from
// env.String calls in main.go.
type RecursiveBundleConfig struct {
	// SystemDefaultUpstreams falls through when the system_settings
	// row has no override and the fabric has no override.
	SystemDefaultUpstreams []string

	// Hickory-only listener config — caller-resolved from env.
	PrometheusListenAddr string
	TLSCertPath          string
	TLSKeyPath           string
	DoTEnabled           bool
	DoHEnabled           bool
	TLSListenPort        int32
	HTTPSListenPort      int32
	DoHPath              string
	AllowNetworksStrict  bool
}

// loadRecursiveBundleInput fetches the data the recursive-path
// assembler needs.
func loadRecursiveBundleInput(
	ctx context.Context, q recursiveBundleQuerier,
	server dbq.DnsServer, cfg RecursiveBundleConfig, now time.Time,
) (RecursiveBundleInput, error) {
	in := RecursiveBundleInput{Server: server, Now: now}

	fabric, err := q.GetFabricForRecursiveBundle(ctx, server.FabricID)
	if err != nil {
		return in, fmt.Errorf("fabric lookup: %w", err)
	}
	engine := fabric.RecursiveEngine
	if engine == "" {
		engine = "coredns"
	}
	in.Engine = engine

	in.FabricApexes, err = q.ListApexZoneNamesByFabric(ctx, server.FabricID)
	if err != nil {
		return in, fmt.Errorf("apex names: %w", err)
	}

	if server.SiteID != uuid.Nil {
		ip, err := q.GetSameSiteAuthUnicastIP(ctx, server.SiteID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return in, fmt.Errorf("local auth IP lookup: %w", err)
		}
		if err == nil && ip != "" {
			in.AuthUnicastIP = &ip
		}
	}

	in.UpstreamResolvers, err = resolveRecursiveUpstreams(ctx, q, fabric, cfg.SystemDefaultUpstreams)
	if err != nil {
		return in, err
	}

	fwdRows, err := q.ListDnsForwardersForBundle(ctx, server.FabricID)
	if err != nil {
		return in, fmt.Errorf("forwarders: %w", err)
	}
	for _, row := range fwdRows {
		var ups []string
		_ = json.Unmarshal(row.Upstreams, &ups)
		in.Forwarders = append(in.Forwarders, ConditionalForwarder{Pattern: row.ZonePattern, Upstreams: ups})
	}

	blRows, err := q.ListEnabledBlocklistsWithPatternsByFabric(ctx, server.FabricID)
	if err != nil {
		return in, fmt.Errorf("blocklists: %w", err)
	}
	for _, row := range blRows {
		var patterns []string
		_ = json.Unmarshal(row.PatternsJson, &patterns)
		in.Blocklists = append(in.Blocklists, Blocklist{
			Action:   row.Action,
			Patterns: patterns,
			SinkIPv4: row.SinkIPv4,
			SinkIPv6: row.SinkIPv6,
		})
	}

	if engine == "hickory" {
		applyHickoryFields(&in, fabric, cfg)
	}

	return in, nil
}

// applyHickoryFields copies the Hickory-only listener + ACL knobs
// from the fabric row + caller config into the assembler input.
// Pulled out so loadRecursiveBundleInput stays under SonarCloud's
// 15-branch cognitive complexity cap.
func applyHickoryFields(
	in *RecursiveBundleInput,
	fabric dbq.FabricForRecursiveBundle,
	cfg RecursiveBundleConfig,
) {
	_ = json.Unmarshal(fabric.DnsDenyNetworks, &in.DenyNetworks)
	_ = json.Unmarshal(fabric.DnsAllowNetworks, &in.AllowNetworks)
	in.PrometheusListenAddr = cfg.PrometheusListenAddr
	in.TLSCertPath = cfg.TLSCertPath
	in.TLSKeyPath = cfg.TLSKeyPath
	in.DoTEnabled = cfg.DoTEnabled
	in.DoHEnabled = cfg.DoHEnabled
	in.TLSListenPort = cfg.TLSListenPort
	in.HTTPSListenPort = cfg.HTTPSListenPort
	in.DoHPath = cfg.DoHPath
	in.AllowNetworksStrict = cfg.AllowNetworksStrict
}

// resolveRecursiveUpstreams: fabric override → system_settings row →
// caller-supplied default. Matches Python's
// _recursive_upstreams_for_fabric chain.
func resolveRecursiveUpstreams(
	ctx context.Context, q recursiveBundleQuerier,
	fabric dbq.FabricForRecursiveBundle, defaults []string,
) ([]string, error) {
	if fabric.DnsRecursiveUpstreams != nil {
		var out []string
		if err := json.Unmarshal(fabric.DnsRecursiveUpstreams, &out); err == nil && len(out) > 0 {
			return out, nil
		}
	}
	// System-wide setting (operator-edited via PUT
	// /admin/system/dns-settings).
	row, err := q.GetSystemSetting(ctx, "dns_recursive_upstreams")
	if err == nil && len(row.Value) > 0 {
		var out []string
		if jerr := json.Unmarshal(row.Value, &out); jerr == nil && len(out) > 0 {
			return out, nil
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("system upstreams lookup: %w", err)
	}
	return defaults, nil
}

// recursiveEngineKnown returns true for the engines the assembler
// can actually produce a bundle for. Anything else is a config
// error (operator wrote a fabric.recursive_engine value the
// renderer doesn't know about).
func recursiveEngineKnown(s string) bool {
	switch strings.ToLower(s) {
	case "", "coredns", "hickory":
		return true
	}
	return false
}
