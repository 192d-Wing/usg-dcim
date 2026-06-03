// Package helmvalues is the Go port of Python's
// dcim.regiondeploy.apps, dns_site, and dhcp_site renderers. Each
// function returns a typed struct that yaml.Marshal turns into the
// same `yaml.safe_dump(..., sort_keys=False)` block-style YAML
// Python produces — verified byte-for-byte by the golden-fixture
// tests in this package.
//
// Why typed structs instead of map[string]any:
//   Python preserves dict insertion order with sort_keys=False; Go's
//   map[string]any iteration is randomized. Typed structs encode
//   field order at declaration site, which is exactly what we need
//   for deterministic YAML. Using anonymous struct literals at call
//   sites would work too but multiplies allocations and adds field-
//   name drift risk — named types let the field-tag layer be the
//   single source of truth.
//
// Why gopkg.in/yaml.v2 over yaml.v3 or sigs.k8s.io/yaml:
//   PyYAML's safe_dump emits list items at the parent map key's
//   column (zero extra indent), but yaml.v3 forcibly indents list
//   items +2 from the key. yaml.v2 matches PyYAML's column policy.
//   yaml.v2 also keeps struct-declaration order (sort_keys=False
//   parity) and uses single quotes for ambiguous scalars (0.26,
//   "true", ""). sigs.k8s.io/yaml goes through JSON marshaling
//   which sorts map keys alphabetically — wrong direction.
//
// Inputs come from RegionDeployment.Config (JSONB) + per-server rows.
// The orchestrator wires the call sites; this package is pure
// rendering and has zero DB or k8s dependencies.
package helmvalues

import (
	"regexp"

	"gopkg.in/yaml.v2"
)

// Deployment is the subset of RegionDeployment the apps renderers
// read. Hand-shaped instead of importing dbq because callers may
// build it from either the DB row OR from synthetic test fixtures.
type Deployment struct {
	ID     string         // stringified uuid — matches Python's str(deployment.id) cast
	SiteID string         // only the collector renderer reads this
	Config map[string]any // pod_cidr_v6, nat64_enabled, dhcp_scopes, replica overrides, …
}

// DumpYAML serializes any of the renderer outputs to a YAML string.
// Matches Python's yaml.safe_dump(values, sort_keys=False) — block
// style, struct-declaration order, no document delimiter, 2-space
// indent, list items at the parent map key's column.
//
// Post-process converts yaml.v2's double-quoted ambiguous scalars
// to PyYAML's single-quoted style. Only matches clean content
// (no backslash escapes, no embedded single quote, no double
// quote), so double-quoted strings containing escape sequences —
// where the difference would actually matter — are left alone.
func DumpYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return pyYAMLQuoteStyle(string(b)), nil
}

// pyYAMLQuoteStyle rewrites "X" → 'X' where X contains no escape
// sequences, single quotes, or double quotes. yaml.v2 picks double
// for ambiguous-but-clean scalars (e.g. "0.26", "true", ""); PyYAML
// picks single. Both are semantically identical YAML.
//
// Load-bearing assumption: the renderer inputs (server names,
// fabric IDs, secret names, URLs) never contain a literal `"`
// character. yaml.v2 would emit such a value in single quotes or
// with escapes, so the regex's `[^"\\']*` class skips it — BUT
// a future addition of a free-form text field that gets emitted
// plain or in a flow sequence with `"X"` substrings would be
// rewritten. If that becomes a real risk, replace this regex
// post-process with a walk over the yaml.v2 events stream that
// only retags scalar styles.
var pyYAMLQuoteRe = regexp.MustCompile(`"([^"\\']*)"`)

func pyYAMLQuoteStyle(s string) string {
	return pyYAMLQuoteRe.ReplaceAllString(s, "'$1'")
}

// ─── apps.py ────────────────────────────────────────────────────────

// CertManagerValues mirrors render_cert_manager_values output.
type CertManagerValues struct {
	InstallCRDs bool                  `yaml:"installCRDs"`
	Global      certManagerGlobal     `yaml:"global"`
	Prometheus  certManagerPrometheus `yaml:"prometheus"`
	DCIM        deploymentIDOnly      `yaml:"dcim"`
}

type certManagerGlobal struct {
	LeaderElection certManagerLE `yaml:"leaderElection"`
}
type certManagerLE struct {
	Namespace string `yaml:"namespace"`
}
type certManagerPrometheus struct {
	Enabled bool `yaml:"enabled"`
}
type deploymentIDOnly struct {
	DeploymentID string `yaml:"deployment_id"`
}

// RenderCertManagerValues ports apps.render_cert_manager_values.
// jetstack/cert-manager upstream chart; CRD install on, leader
// election scoped to cert-manager, Prometheus monitoring on, plus
// the deployment-id annotation block dcim/*.
func RenderCertManagerValues(dep Deployment) CertManagerValues {
	return CertManagerValues{
		InstallCRDs: true,
		Global:      certManagerGlobal{LeaderElection: certManagerLE{Namespace: "cert-manager"}},
		Prometheus:  certManagerPrometheus{Enabled: true},
		DCIM:        deploymentIDOnly{DeploymentID: dep.ID},
	}
}

// DNSAuthValues mirrors render_dns_auth_values output.
type DNSAuthValues struct {
	ReplicaCount int              `yaml:"replicaCount"`
	Image        imageRef         `yaml:"image"`
	Service      v6LoadBalancer   `yaml:"service"`
	Servers      []corednsServer  `yaml:"servers"`
	DCIM         deploymentIDOnly `yaml:"dcim"`
}

type imageRef struct {
	Repository string `yaml:"repository"`
	Tag        string `yaml:"tag"`
}

// v6LoadBalancer is the LoadBalancer Service shape with ipv6 single
// stack; used by dns_auth + dns_recursive (kea-dhcp uses hostNetwork
// instead, so it gets its own type).
type v6LoadBalancer struct {
	Type           string   `yaml:"type"`
	IPFamilies     []string `yaml:"ipFamilies"`
	IPFamilyPolicy string   `yaml:"ipFamilyPolicy"`
}

type corednsServer struct {
	Zones   []corednsZone    `yaml:"zones"`
	Port    int              `yaml:"port"`
	Plugins []corednsPlugin  `yaml:"plugins"`
}
type corednsZone struct {
	Zone string `yaml:"zone"`
}
type corednsPlugin struct {
	Name       string `yaml:"name"`
	Parameters string `yaml:"parameters,omitempty"`
}

// RenderDNSAuthValues ports apps.render_dns_auth_values. CoreDNS
// authoritative; v6-only LB; replicas overridable via
// config.dns_auth_replicas (default 2).
func RenderDNSAuthValues(dep Deployment) DNSAuthValues {
	replicas := intConfig(dep.Config, "dns_auth_replicas", 2)
	return DNSAuthValues{
		ReplicaCount: replicas,
		Image:        imageRef{Repository: "coredns/coredns", Tag: "1.12.0"},
		Service: v6LoadBalancer{
			Type: "LoadBalancer", IPFamilies: []string{"IPv6"}, IPFamilyPolicy: "SingleStack",
		},
		Servers: []corednsServer{
			{
				Zones: []corednsZone{{Zone: "."}},
				Port:  53,
				Plugins: []corednsPlugin{
					{Name: "errors"},
					{Name: "health"},
					{Name: "ready"},
					{Name: "prometheus", Parameters: "0.0.0.0:9153"},
					{Name: "file", Parameters: "/etc/coredns/zones/db.region"},
				},
			},
		},
		DCIM: deploymentIDOnly{DeploymentID: dep.ID},
	}
}

// DNSRecursiveValues mirrors render_dns_recursive_values output.
// Dns64 is omitempty so nat64_enabled=false produces identical
// output to Python's branch that doesn't add the key.
type DNSRecursiveValues struct {
	ReplicaCount int                `yaml:"replicaCount"`
	Image        imageRef           `yaml:"image"`
	Service      v6LoadBalancer     `yaml:"service"`
	Upstreams    []string           `yaml:"upstreams"`
	DCIM         deploymentIDOnly   `yaml:"dcim"`
	DNS64        *dnsRecursiveDNS64 `yaml:"dns64,omitempty"`
}
type dnsRecursiveDNS64 struct {
	Enabled bool   `yaml:"enabled"`
	Prefix  string `yaml:"prefix"`
}

// RenderDNSRecursiveValues ports apps.render_dns_recursive_values.
// Optional DNS64 block when config.nat64_enabled is true.
func RenderDNSRecursiveValues(dep Deployment) DNSRecursiveValues {
	cfg := dep.Config
	out := DNSRecursiveValues{
		ReplicaCount: intConfig(cfg, "dns_recursive_replicas", 2),
		Image:        imageRef{Repository: "ghcr.io/usg-dcim/hickory", Tag: "0.26"},
		Service: v6LoadBalancer{
			Type: "LoadBalancer", IPFamilies: []string{"IPv6"}, IPFamilyPolicy: "SingleStack",
		},
		Upstreams: stringSlice(cfg, "upstream_dns_v6"),
		DCIM:      deploymentIDOnly{DeploymentID: dep.ID},
	}
	if boolConfig(cfg, "nat64_enabled", false) {
		out.DNS64 = &dnsRecursiveDNS64{Enabled: true, Prefix: "64:ff9b::/96"}
	}
	return out
}

// DHCPValues mirrors render_dhcp_values output. Scopes pass through
// as `[]any` — same JSONB shape PG returns — but the surrounding
// struct fields stay typed so the YAML key order matches Python.
type DHCPValues struct {
	ReplicaCount int              `yaml:"replicaCount"`
	Image        imageRef         `yaml:"image"`
	HostNetwork  bool             `yaml:"hostNetwork"`
	ControlAgent dhcpCtrlAgent    `yaml:"controlAgent"`
	DHCP6        dhcpFamily       `yaml:"dhcp6"`
	DHCP4        dhcpFamilyOff    `yaml:"dhcp4"`
	DCIM         deploymentIDOnly `yaml:"dcim"`
}
type dhcpCtrlAgent struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}
type dhcpFamily struct {
	Enabled   bool        `yaml:"enabled"`
	Subnets   []DHCPScope `yaml:"subnets"`
	Stateless bool        `yaml:"stateless"`
}

// DHCPScope is the per-subnet shape inside config.dhcp_scopes.
// Python passes the dict through as-is, so any extra Kea keys
// (option-data, id, valid-lifetime, reservations, …) are preserved.
// The typed Go struct here only carries the two keys the
// orchestrator currently writes; the orchestrator caller is PR 6
// (state machine) and will need to extend this with the richer
// schema if it grows to use them. Until then, additional JSONB
// keys flowing through coerceDHCPScopes are dropped silently —
// validating that gap belongs at the orchestrator boundary, not
// here.
type DHCPScope struct {
	Subnet string   `yaml:"subnet"`
	Pool   []string `yaml:"pool"`
}
type dhcpFamilyOff struct {
	Enabled bool `yaml:"enabled"`
}

// RenderDHCPValues ports apps.render_dhcp_values.
func RenderDHCPValues(dep Deployment) DHCPValues {
	cfg := dep.Config
	return DHCPValues{
		ReplicaCount: intConfig(cfg, "dhcp_replicas", 2),
		Image:        imageRef{Repository: "cloudnativelabs/kea", Tag: "2.6.1"},
		HostNetwork:  true,
		ControlAgent: dhcpCtrlAgent{Enabled: true, Port: 8000},
		DHCP6: dhcpFamily{
			Enabled:   true,
			Subnets:   coerceDHCPScopes(cfg["dhcp_scopes"]),
			Stateless: true,
		},
		DHCP4: dhcpFamilyOff{Enabled: false},
		DCIM:  deploymentIDOnly{DeploymentID: dep.ID},
	}
}

// CollectorValues mirrors render_collector_values output. central.*
// placeholders are filled by the seed stage post-install.
type CollectorValues struct {
	ReplicaCount int                `yaml:"replicaCount"`
	Image        imageRef           `yaml:"image"`
	Site         collectorSite      `yaml:"site"`
	Central      collectorCentral   `yaml:"central"`
}
type collectorSite struct {
	ID           string `yaml:"id"`
	DeploymentID string `yaml:"deployment_id"`
}
type collectorCentral struct {
	APIURL          string `yaml:"api_url"`
	EnrollmentToken string `yaml:"enrollment_token"`
}

// RenderCollectorValues ports apps.render_collector_values.
func RenderCollectorValues(dep Deployment) CollectorValues {
	return CollectorValues{
		ReplicaCount: 1,
		Image:        imageRef{Repository: "ghcr.io/usg-dcim/go-collector", Tag: "dev"},
		Site:         collectorSite{ID: dep.SiteID, DeploymentID: dep.ID},
		Central:      collectorCentral{APIURL: "", EnrollmentToken: ""},
	}
}

// ─── dns_site.py ────────────────────────────────────────────────────

// DNSServer is the subset of models.dns.DnsServer the dns-site
// renderer reads.
type DNSServer struct {
	ID       string
	Name     string
	Role     string // "authoritative" | "recursive"
	FabricID string
	SiteID   string
}

// AnycastGroup is the subset of models.dns.AnycastGroup the dns-site
// renderer reads. Zero strings = absent (mirrors Python's
// `if getattr(group, 'anycast_ipv4', None):` check).
type AnycastGroup struct {
	AnycastIPv4 string
	AnycastIPv6 string
}

// DNSSiteOptions carries the renderer's keyword args. Mirrors
// Python's signature 1:1, with the caveat below.
//
// Caveat: zero-valued numeric fields and empty-string fields
// trigger the Python default. Go can't distinguish "unset" from
// "explicit zero" without pointer types, so a caller wanting to
// pass `replica_count=0` (scale to zero) or `poll_seconds=0`
// (disable polling) cannot do so through this struct — they get
// the default instead. The orchestrator caller (PR 6) does not
// need explicit zero today; if/when it does, switch the fields
// to `*int` / `*string` and update the defaulting branches to
// nil-checks.
type DNSSiteOptions struct {
	AnycastGroup          *AnycastGroup
	BundleAPIBaseURL      string
	BundleTokenSecretName string // "" → default "dcim-dns-site-token"
	BundleTokenSecretKey  string // "" → default "token"
	BundleCABundleSecret  string // "" → omit caBundleSecretName
	PollSeconds           int    // 0 → default 60
	ReplicaCount          int    // 0 → default 1
}

// DNSSiteValues mirrors render_dns_site_values output.
type DNSSiteValues struct {
	Server       dnsSiteServer  `yaml:"server"`
	Service      dnsSiteService `yaml:"service"`
	Bundle       dnsSiteBundle  `yaml:"bundle"`
	ReplicaCount int            `yaml:"replicaCount"`
}
type dnsSiteServer struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Role     string `yaml:"role"`
	FabricID string `yaml:"fabricId"`
	SiteID   string `yaml:"siteId"`
}
type dnsSiteService struct {
	Type        string            `yaml:"type"`
	Port        int               `yaml:"port"`
	AnycastIPs  []string          `yaml:"anycastIPs"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}
type dnsSiteBundle struct {
	APIBaseURL          string `yaml:"apiBaseUrl"`
	TokenSecretName     string `yaml:"tokenSecretName"`
	TokenSecretKey      string `yaml:"tokenSecretKey"`
	PollSeconds         int    `yaml:"pollSeconds"`
	CABundleSecretName  string `yaml:"caBundleSecretName,omitempty"`
}

// RenderDNSSiteValues ports dns_site.render_dns_site_values.
func RenderDNSSiteValues(srv DNSServer, opts DNSSiteOptions) DNSSiteValues {
	var anycastIPs []string
	if opts.AnycastGroup != nil {
		if opts.AnycastGroup.AnycastIPv4 != "" {
			anycastIPs = append(anycastIPs, opts.AnycastGroup.AnycastIPv4)
		}
		if opts.AnycastGroup.AnycastIPv6 != "" {
			anycastIPs = append(anycastIPs, opts.AnycastGroup.AnycastIPv6)
		}
	}
	tokenName := opts.BundleTokenSecretName
	if tokenName == "" {
		tokenName = "dcim-dns-site-token"
	}
	tokenKey := opts.BundleTokenSecretKey
	if tokenKey == "" {
		tokenKey = "token"
	}
	poll := opts.PollSeconds
	if poll == 0 {
		poll = 60
	}
	replicas := opts.ReplicaCount
	if replicas == 0 {
		replicas = 1
	}
	return DNSSiteValues{
		Server: dnsSiteServer{
			ID: srv.ID, Name: srv.Name, Role: srv.Role,
			FabricID: srv.FabricID, SiteID: srv.SiteID,
		},
		Service: dnsSiteService{
			Type: "LoadBalancer", Port: 53,
			AnycastIPs:  anycastIPs,
			Labels:      map[string]string{"dcim.io/bgp-advertise": "true", "dcim.io/dns-role": srv.Role},
			Annotations: map[string]string{},
		},
		Bundle: dnsSiteBundle{
			APIBaseURL: opts.BundleAPIBaseURL, TokenSecretName: tokenName,
			TokenSecretKey: tokenKey, PollSeconds: poll,
			CABundleSecretName: opts.BundleCABundleSecret,
		},
		ReplicaCount: replicas,
	}
}

// ─── dhcp_site.py ───────────────────────────────────────────────────

// DHCPServer is the subset of models.ipam.DhcpServer the dhcp-site
// renderer reads.
type DHCPServer struct {
	ID       string
	Name     string
	FabricID string
}

// DHCPSiteOptions mirrors render_dhcp_site_values's keyword args.
type DHCPSiteOptions struct {
	AnycastIPs           []string
	DHCPv6               bool
	CtrlAgentPort        int    // 0 → default 8000
	CtrlAgentConfigMap   string // "" → default "kea-ctrl-agent-config"
	CtrlAgentAuthSecret  string // "" → default "kea-ctrl-agent-auth"
	CtrlAgentTLSSecret   string // "" → tls disabled
	ReplicaCount         int    // 0 → default 2
}

// DHCPSiteValues mirrors render_dhcp_site_values output.
type DHCPSiteValues struct {
	Server       dhcpSiteServer  `yaml:"server"`
	Service      dhcpSiteService `yaml:"service"`
	CtrlAgent    dhcpSiteCtrl    `yaml:"ctrlAgent"`
	ReplicaCount int             `yaml:"replicaCount"`
}
type dhcpSiteServer struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	FabricID string `yaml:"fabricId"`
	DHCPv6   bool   `yaml:"dhcpv6"`
}
type dhcpSiteService struct {
	Type        string            `yaml:"type"`
	AnycastIPs  []string          `yaml:"anycastIPs"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}
type dhcpSiteCtrl struct {
	Port          int          `yaml:"port"`
	ConfigMapName string       `yaml:"configMapName"`
	ConfigKey     string       `yaml:"configKey"`
	TLS           dhcpSiteTLS  `yaml:"tls"`
	BasicAuth     dhcpSiteAuth `yaml:"basicAuth"`
}
type dhcpSiteTLS struct {
	Enabled          bool   `yaml:"enabled"`
	ServerCertSecret string `yaml:"serverCertSecret"`
}
type dhcpSiteAuth struct {
	SecretName string `yaml:"secretName"`
	SecretKey  string `yaml:"secretKey"`
}

// RenderDHCPSiteValues ports dhcp_site.render_dhcp_site_values.
func RenderDHCPSiteValues(srv DHCPServer, opts DHCPSiteOptions) DHCPSiteValues {
	port := opts.CtrlAgentPort
	if port == 0 {
		port = 8000
	}
	configMap := opts.CtrlAgentConfigMap
	if configMap == "" {
		configMap = "kea-ctrl-agent-config"
	}
	authSecret := opts.CtrlAgentAuthSecret
	if authSecret == "" {
		authSecret = "kea-ctrl-agent-auth"
	}
	replicas := opts.ReplicaCount
	if replicas == 0 {
		replicas = 2
	}
	tlsEnabled := opts.CtrlAgentTLSSecret != ""
	anycast := opts.AnycastIPs
	if anycast == nil {
		anycast = []string{}
	}
	return DHCPSiteValues{
		Server: dhcpSiteServer{ID: srv.ID, Name: srv.Name, FabricID: srv.FabricID, DHCPv6: opts.DHCPv6},
		Service: dhcpSiteService{
			Type: "LoadBalancer", AnycastIPs: anycast,
			Labels: map[string]string{
				"dcim.io/bgp-advertise": "true",
				"dcim.io/dhcp-role":     "ctrl-agent",
			},
			Annotations: map[string]string{},
		},
		CtrlAgent: dhcpSiteCtrl{
			Port: port, ConfigMapName: configMap, ConfigKey: "kea-ctrl-agent.conf",
			TLS: dhcpSiteTLS{Enabled: tlsEnabled, ServerCertSecret: opts.CtrlAgentTLSSecret},
			BasicAuth: dhcpSiteAuth{SecretName: authSecret, SecretKey: "auth.csv"},
		},
		ReplicaCount: replicas,
	}
}

// ─── helpers ────────────────────────────────────────────────────────

// intConfig reads an int-typed key from the deployment config map.
// Missing key → default. Numeric values that come through JSONB
// arrive as float64 (json.Unmarshal default); int and float64 both
// coerce.
func intConfig(cfg map[string]any, key string, def int) int {
	v, ok := cfg[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return def
}

// boolConfig reads a bool-typed key. Missing key → default.
func boolConfig(cfg map[string]any, key string, def bool) bool {
	v, ok := cfg[key]
	if !ok || v == nil {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

// stringSlice reads a []string-typed key. Missing → []string{} so
// the YAML emits an empty list, not null (Python's `list(... or [])`
// produces the same).
func stringSlice(cfg map[string]any, key string) []string {
	v, ok := cfg[key]
	if !ok || v == nil {
		return []string{}
	}
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return []string{}
}

// coerceDHCPScopes coerces config.dhcp_scopes into typed scopes.
// pgx returns JSONB as either []any of map[string]any (default JSON
// unmarshal) OR []DHCPScope (when callers pre-decoded). Both shapes
// flow through unchanged.
func coerceDHCPScopes(v any) []DHCPScope {
	if v == nil {
		return []DHCPScope{}
	}
	switch s := v.(type) {
	case []DHCPScope:
		return s
	case []any:
		out := make([]DHCPScope, 0, len(s))
		for _, item := range s {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			scope := DHCPScope{}
			if subnet, ok := m["subnet"].(string); ok {
				scope.Subnet = subnet
			}
			if pool, ok := m["pool"].([]any); ok {
				for _, p := range pool {
					if str, ok := p.(string); ok {
						scope.Pool = append(scope.Pool, str)
					}
				}
			} else if pool, ok := m["pool"].([]string); ok {
				scope.Pool = pool
			}
			out = append(out, scope)
		}
		return out
	}
	return []DHCPScope{}
}
