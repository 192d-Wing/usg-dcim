// On-demand DHCP server sync (api/ipam.py:1845). The same
// leasesync.SyncServer the dhcp_sync cron (PR 16) calls under the
// hood, exposed as a manual endpoint for the operator who just
// edited a server config and wants to verify it works.
//
// Capability: ipam:dhcp-servers:update — matches Python.
package ipam

import (
	"net/http"
	"time"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/leasesync"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

func (h *Handler) syncDhcpServer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpServerFabric(w, r, id, "ipam:dhcp-servers:update") {
		return
	}
	servers, err := h.Q.ListEnabledDhcpServersForLeaseSync(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	var server *dbq.ListEnabledDhcpServersForLeaseSyncRow
	for i, s := range servers {
		if s.ID == id {
			server = &servers[i]
			break
		}
	}
	if server == nil {
		// Server exists (the fabric lookup just succeeded) but isn't
		// in the enabled-server list — operator disabled it. Python
		// happily syncs disabled servers; Go's narrower projection
		// requires the enabled flag. Matches Python's posture
		// closely enough: a disabled server has nothing to sync.
		disabled := "server is disabled"
		httpx.JSON(w, http.StatusOK, dhcpServerSyncBody{
			ServerID: id.String(), Error: &disabled,
		})
		return
	}
	out, err := leasesync.SyncServer(r.Context(), h.Q,
		h.leaseKeaBuilder(), toLeaseSyncServer(*server), time.Now())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_server.sync",
		TargetType: "dhcp_server", TargetID: id.String(),
		Metadata: map[string]any{
			"upserted":          out.Upserted,
			"skipped_no_subnet": out.SkippedNoSubnet,
			"leases_seen":       out.LeasesSeen,
			"error":             nilIfEmpty(out.Error),
		},
	})
	httpx.JSON(w, http.StatusOK, dhcpServerSyncBody{
		ServerID:        out.ServerID,
		Upserted:        out.Upserted,
		SkippedNoSubnet: out.SkippedNoSubnet,
		LeasesSeen:      out.LeasesSeen,
		Error:           nilIfEmpty(out.Error),
	})
}

// dhcpServerSyncBody mirrors Python's response shape at
// api/ipam.py:1871-1877.
type dhcpServerSyncBody struct {
	ServerID        string  `json:"server_id"`
	Upserted        int     `json:"upserted"`
	SkippedNoSubnet int     `json:"skipped_no_subnet"`
	LeasesSeen      int     `json:"leases_seen"`
	Error           *string `json:"error"`
}

// leaseKeaBuilder returns the production builder unless the Handler
// has a test override.
func (h *Handler) leaseKeaBuilder() leasesync.KeaClientBuilder {
	if h.LeaseKea != nil {
		return h.LeaseKea
	}
	return leasesync.DefaultKeaClientBuilder
}

// toLeaseSyncServer maps the dbq row projection to leasesync.Server.
// Same shape conversion the cron driver does at dhcpsync.toServer;
// kept package-local so the cron driver and the handler don't
// import each other.
func toLeaseSyncServer(row dbq.ListEnabledDhcpServersForLeaseSyncRow) leasesync.Server {
	user, pass := "", ""
	if row.AuthUsername != nil {
		user = *row.AuthUsername
	}
	if row.AuthPassword != nil {
		pass = *row.AuthPassword
	}
	return leasesync.Server{
		ID: row.ID, FabricID: row.FabricID,
		KeaURL: row.KeaURL, AuthUsername: user, AuthPassword: pass,
	}
}
