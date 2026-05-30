package admin

import (
	"net/http"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// capabilityCatalog mirrors Python's CAPABILITY_CATALOG
// (packages/otter/src/dcim/security/capabilities.py:28). domain → resource
// → [actions]. Every (domain, resource, action) triple materialises a
// capability code of shape `domain:resource:action`.
//
// Hand-maintained against the Python source: capabilities flow from the
// Python catalog to the role-create UI, and the role engine then enforces
// the codes both stacks see. When a new code lands in the Python catalog,
// add it here too (and to the role-bundle defaults if relevant). The
// `internal/admin/capabilities_test.go` test asserts the surface area is
// non-empty for every Python domain — a regression where a Python domain
// is dropped here will trip the test.
var capabilityCatalog = map[string]map[string][]string{
	"inventory": {
		"sites":         {"create", "read", "update", "delete"},
		"regions":       {"create", "read", "update", "delete"},
		"buildings":     {"create", "read", "update", "delete"},
		"rooms":         {"create", "read", "update", "delete"},
		"rows":          {"create", "read", "update", "delete"},
		"racks":         {"create", "read", "update", "delete"},
		"assets":        {"create", "read", "update", "delete"},
		"cables":        {"create", "read", "update", "delete"},
		"stencils":      {"create", "read", "update", "delete"},
		"organizations": {"create", "read", "update", "delete"},
		"bulk":          {"execute"},
	},
	"routing": {
		"asns":                   {"create", "read", "update", "delete"},
		"tcp-ao-key-chains":      {"create", "read", "update", "delete", "rotate"},
		"tcp-ao-keys":            {"create", "read", "update", "delete"},
		"prefix-lists":           {"create", "read", "update", "delete"},
		"prefix-list-entries":    {"create", "read", "update", "delete"},
		"community-lists":        {"create", "read", "update", "delete"},
		"community-list-entries": {"create", "read", "update", "delete"},
		"route-maps":             {"create", "read", "update", "delete"},
		"route-map-entries":      {"create", "read", "update", "delete"},
	},
	"search": {
		"search": {"read"},
	},
	"ipam": {
		"fabrics":              {"create", "read", "update", "delete"},
		"vrfs":                 {"create", "read", "update", "delete"},
		"vrf-bgp-peers":        {"create", "read", "update", "delete"},
		"supernets":            {"create", "read", "update", "delete"},
		"subnets":              {"create", "read", "update", "delete"},
		"addresses":            {"create", "read", "update", "delete"},
		"overlays":             {"create", "read", "update", "delete"},
		"vnis":                 {"create", "read", "update", "delete"},
		"vteps":                {"create", "read", "update", "delete"},
		"vtep-memberships":     {"create", "read", "update", "delete"},
		"dhcp-servers":         {"create", "read", "update", "delete", "bundle"},
		"dhcp-scopes":          {"create", "read", "update", "delete", "push", "reconcile", "reconcile-sync"},
		"dhcp-scope-templates": {"create", "read", "update", "delete"},
	},
	"dns": {
		"zones":             {"create", "read", "update", "delete"},
		"records":           {"create", "read", "update", "delete"},
		"servers":           {"create", "read", "update", "delete", "bundle"},
		"keys":              {"create", "read", "update", "delete", "rotate"},
		"forwarders":        {"create", "read", "update", "delete"},
		"blocklists":        {"create", "read", "update", "delete"},
		"views":             {"create", "read", "update", "delete"},
		"health-checks":     {"create", "read", "update", "delete"},
		"anycast-groups":    {"create", "read", "update", "delete"},
		"anycast-bindings":  {"create", "read", "update", "delete"},
		"bgp-peers":         {"create", "read", "update", "delete"},
	},
	"collectors": {
		"collectors": {"create", "read", "update", "delete", "enroll"},
		"ingest":     {"write"},
	},
	"alerts": {
		"alerts":   {"read", "ack"},
		"rules":    {"create", "read", "update", "delete"},
		"silences": {"create", "read", "update", "delete"},
	},
	"telemetry": {
		"metrics": {"read"},
		"events":  {"read"},
	},
	"dashboards": {
		"dashboards": {"create", "read", "update", "delete"},
	},
	"maintenance": {
		"windows": {"create", "read", "update", "delete"},
	},
	"power": {
		"outlets": {"create", "read", "delete"},
	},
	"audit": {
		"events": {"read", "export"},
	},
	"admin": {
		"users":          {"create", "read", "update", "delete"},
		"roles":          {"create", "read", "update", "delete"},
		"oidc-mappings":  {"create", "read", "update", "delete"},
		"api-tokens":     {"create", "read", "update", "delete"},
		// Deployment-wide rows in system_settings. Today: dns
		// recursive_upstreams override. The pattern is generic so
		// future settings don't need their own catalog entry.
		"system-settings": {"read", "update"},
	},
	"notifications": {
		"channels": {"create", "read", "update", "delete"},
	},
	"infrastructure": {
		"region-deployments": {
			"create", "read", "update", "delete",
			"start", "abort", "download-kubeconfig",
		},
	},
	"lir": {
		"pools":    {"create", "read", "update", "delete"},
		"requests": {"create", "read", "cancel", "approve", "reject"},
		"allocations": {
			"read",
			"return-request", "return-confirm",
			"arin-retry",
		},
	},
}

// specialtyCapabilities mirrors Python's SPECIALTY_CAPABILITIES
// (security/capabilities.py:156). Two-segment codes that don't fit the
// resource:action shape; the picker UI renders these in a "Specialty"
// section per domain.
var specialtyCapabilities = map[string]string{
	"power:control": "Issue power-control commands to assets",
	"power:approve": "Approve pending power-control requests",
}

// capabilityCatalogOut is the wire shape, byte-identical to Python's
// schemas.auth.CapabilityCatalogOut: {catalog: {<domain>: {<resource>:
// [<action>...]}}, specialties: {<code>: <label>}}.
type capabilityCatalogOut struct {
	Catalog     map[string]map[string][]string `json:"catalog"`
	Specialties map[string]string              `json:"specialties"`
}

func (h *Handler) getCapabilitiesCatalog(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, capabilityCatalogOut{
		Catalog:     capabilityCatalog,
		Specialties: specialtyCapabilities,
	})
}
