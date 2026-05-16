// Package stencils serves the vendor stencil catalog used by the rack
// visualizer in finch. The catalog is static (not DB-backed) — kept in
// Go so the frontend doesn't have to ship the list in its bundle and
// admins don't have to curate per-org (yet). Wire shape matches the
// FastAPI handler at packages/otter/src/dcim/api/stencils.py.
package stencils

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Handler struct{}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/stencils", h.list)
}

type paletteEntry struct {
	Primary string `json:"primary"`
	Accent  string `json:"accent"`
}

type stencil struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	U            int    `json:"u"`
	KindHint     string `json:"kind_hint"`
	PortCount    *int   `json:"port_count,omitempty"`
	Vertical     *bool  `json:"vertical,omitempty"`
}

type response struct {
	Palette  map[string]paletteEntry `json:"palette"`
	Stencils []stencil               `json:"stencils"`
}

func intPtr(n int) *int    { return &n }
func boolPtr(b bool) *bool { return &b }

var vendorPalette = map[string]paletteEntry{
	"dell":         {Primary: "#0085c3", Accent: "#003a70"},
	"hpe":          {Primary: "#01a982", Accent: "#003c2c"},
	"hp":           {Primary: "#01a982", Accent: "#003c2c"},
	"cisco":        {Primary: "#1ba0d7", Accent: "#005073"},
	"juniper":      {Primary: "#84bd00", Accent: "#003359"},
	"arista":       {Primary: "#f47920", Accent: "#1f2937"},
	"lenovo":       {Primary: "#e2231a", Accent: "#1f2937"},
	"supermicro":   {Primary: "#c8102e", Accent: "#1a1a1a"},
	"ibm":          {Primary: "#0530ad", Accent: "#001a4f"},
	"apc":          {Primary: "#3dae2b", Accent: "#1a1a1a"},
	"schneider":    {Primary: "#3dae2b", Accent: "#0a3a2a"},
	"eaton":        {Primary: "#005eb8", Accent: "#1a1a1a"},
	"vertiv":       {Primary: "#00a3e0", Accent: "#0033a0"},
	"liebert":      {Primary: "#00a3e0", Accent: "#0033a0"},
	"tripp lite":   {Primary: "#0066b3", Accent: "#1a1a1a"},
	"raritan":      {Primary: "#005bbb", Accent: "#1a1a1a"},
	"panduit":      {Primary: "#0070c0", Accent: "#1a1a1a"},
	"demo":         {Primary: "#6b7280", Accent: "#374151"},
}

var catalog = []stencil{
	{Manufacturer: "Dell", Model: "PowerEdge R650", U: 1, KindHint: "server", PortCount: intPtr(4)},
	{Manufacturer: "Dell", Model: "PowerEdge R750", U: 2, KindHint: "server", PortCount: intPtr(4)},
	{Manufacturer: "Dell", Model: "PowerEdge R760", U: 2, KindHint: "server", PortCount: intPtr(4)},
	{Manufacturer: "Dell", Model: "PowerEdge R940", U: 3, KindHint: "server", PortCount: intPtr(4)},
	{Manufacturer: "Dell", Model: "Networking S5248", U: 1, KindHint: "switch", PortCount: intPtr(48)},
	{Manufacturer: "HPE", Model: "ProLiant DL360 Gen11", U: 1, KindHint: "server", PortCount: intPtr(4)},
	{Manufacturer: "HPE", Model: "ProLiant DL380 Gen11", U: 2, KindHint: "server", PortCount: intPtr(4)},
	{Manufacturer: "HPE", Model: "ProLiant DL580 Gen11", U: 4, KindHint: "server", PortCount: intPtr(4)},
	{Manufacturer: "HPE", Model: "Synergy 12000", U: 10, KindHint: "chassis"},
	{Manufacturer: "Cisco", Model: "Nexus 93180YC-FX", U: 1, KindHint: "switch", PortCount: intPtr(48)},
	{Manufacturer: "Cisco", Model: "Nexus 9336C-FX2", U: 1, KindHint: "switch", PortCount: intPtr(36)},
	{Manufacturer: "Cisco", Model: "Catalyst 9300-48P", U: 1, KindHint: "switch", PortCount: intPtr(48)},
	{Manufacturer: "Cisco", Model: "ASR 1001-X", U: 1, KindHint: "router", PortCount: intPtr(6)},
	{Manufacturer: "Cisco", Model: "UCS C240 M6", U: 2, KindHint: "server", PortCount: intPtr(4)},
	{Manufacturer: "Arista", Model: "7050SX3-48YC8", U: 1, KindHint: "switch", PortCount: intPtr(48)},
	{Manufacturer: "Juniper", Model: "QFX5120-48Y", U: 1, KindHint: "switch", PortCount: intPtr(48)},
	{Manufacturer: "APC", Model: "AP8941", U: 0, KindHint: "pdu", PortCount: intPtr(24), Vertical: boolPtr(true)},
	{Manufacturer: "APC", Model: "AP8853", U: 0, KindHint: "pdu", PortCount: intPtr(21), Vertical: boolPtr(true)},
	{Manufacturer: "APC", Model: "AP7900", U: 1, KindHint: "pdu", PortCount: intPtr(8)},
	{Manufacturer: "APC", Model: "Smart-UPS SRT 5000VA", U: 3, KindHint: "ups"},
	{Manufacturer: "APC", Model: "Symmetra LX 16kVA", U: 12, KindHint: "ups"},
	{Manufacturer: "Eaton", Model: "9PX 6000", U: 3, KindHint: "ups"},
	{Manufacturer: "Eaton", Model: "BladeUPS 12kW", U: 6, KindHint: "ups"},
	{Manufacturer: "Liebert", Model: "CRV CR020", U: 0, KindHint: "crac"},
	{Manufacturer: "Vertiv", Model: "Liebert DSE", U: 0, KindHint: "crac"},
	{Manufacturer: "Raritan", Model: "DPX2-T1H1", U: 0, KindHint: "sensor"},
	{Manufacturer: "Demo", Model: "X1", U: 1, KindHint: "server", PortCount: intPtr(4)},
}

func (h *Handler) list(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, response{Palette: vendorPalette, Stencils: catalog})
}
