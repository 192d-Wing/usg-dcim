// Adapters from the dbq row shapes to the renderer's
// package-local Scope/Template/Server types. Kept here (not in dbq)
// so the renderer stays decoupled from generated code's import
// surface — bundle tests don't pull in dbq at all.
//
// The renderer needs int64 for timer values (Kea wire shape); the
// dbq columns are int32 because Postgres backs them with INTEGER.
// Promotion is lossless and explicit so a future schema change to
// BIGINT (or int64 timer values from a different source) doesn't
// silently round.
package bundle

import (
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// FromDbqServer projects the bundle-row shape of dhcp_servers onto
// the renderer's Server input. ID is stringified to match what
// Python's render_kea_bundle emits at services/dhcp_bundle.py:175
// (`str(server.id)`) so the wire shape stays byte-identical across
// the language port.
func FromDbqServer(s dbq.DhcpServerBundleRow) Server {
	return Server{
		ID:         s.ID.String(),
		BaseConfig: s.BaseConfig,
	}
}

// FromDbqScope projects a dhcp_scopes row onto the renderer's Scope
// input. Pointer fields stay pointers (nil = unset, matching the
// renderer's "skip key if nil" branches in RenderKeaSubnet4/V6).
// The bool Enabled column is non-nullable in Postgres but propagated
// here as a value because the renderer's partition step filters on it.
func FromDbqScope(s dbq.DhcpScope) Scope {
	return Scope{
		ID:                       s.ID.String(),
		DhcpServerID:             s.DhcpServerID.String(),
		IPFamily:                 int(s.IPFamily),
		Prefix:                   s.Prefix,
		PoolsJSON:                s.PoolsJSON,
		PdPoolsJSON:              s.PdPoolsJSON,
		OptionsJSON:              s.OptionsJSON,
		ReservationsJSON:         s.ReservationsJSON,
		ValidLifetimeSeconds:     int32PtrToInt64Ptr(s.ValidLifetimeSeconds),
		RenewTimerSeconds:        int32PtrToInt64Ptr(s.RenewTimerSeconds),
		RebindTimerSeconds:       int32PtrToInt64Ptr(s.RebindTimerSeconds),
		PreferredLifetimeSeconds: int32PtrToInt64Ptr(s.PreferredLifetimeSeconds),
		TemplateID:               uuidPtrToStringPtr(s.TemplateID),
		// KeaSubnetID widening preserves nil — the renderer
		// branches on "pinned vs deferred" by checking nil at
		// assemble time.
		KeaSubnetID:              int32PtrToInt64Ptr(s.KeaSubnetID),
		Enabled:                  s.Enabled,
	}
}

// FromDbqTemplate projects a dhcp_scope_templates row onto the
// renderer's Template input. The renderer reads OptionsJSON for
// the merge-by-key step and the four timer pointers for the
// inherit-when-scope-nil step; the other columns (fabric_id, name,
// description) are unused by the renderer.
func FromDbqTemplate(t dbq.DhcpScopeTemplate) Template {
	return Template{
		ID:                       t.ID.String(),
		OptionsJSON:              t.OptionsJSON,
		ValidLifetimeSeconds:     int32PtrToInt64Ptr(t.ValidLifetimeSeconds),
		RenewTimerSeconds:        int32PtrToInt64Ptr(t.RenewTimerSeconds),
		RebindTimerSeconds:       int32PtrToInt64Ptr(t.RebindTimerSeconds),
		PreferredLifetimeSeconds: int32PtrToInt64Ptr(t.PreferredLifetimeSeconds),
	}
}

// int32PtrToInt64Ptr widens a nullable int32 column to the renderer's
// int64 pointer shape. Returns nil when the source is nil so the
// renderer's "skip Kea key when nil" branches still fire correctly.
func int32PtrToInt64Ptr(v *int32) *int64 {
	if v == nil {
		return nil
	}
	out := int64(*v)
	return &out
}

// uuidPtrToStringPtr widens a nullable UUID column to the renderer's
// string-keyed template map shape. Concrete *uuid.UUID signature
// (not an interface) so a typed-nil pointer doesn't slip past the
// nil check — `var u *uuid.UUID = nil; iface = u` yields an iface
// with non-nil type-pair, and `iface == nil` returns false.
func uuidPtrToStringPtr(v *uuid.UUID) *string {
	if v == nil {
		return nil
	}
	s := v.String()
	return &s
}

// FromDbqScopeForPush is a shortcut that maps the narrow
// DhcpScopeForPushRow projection (PR 2 of DHCP push) directly to the
// renderer's Scope input. push.PushScope and diff.DiffScope both
// load this row shape; sharing the conversion here keeps each
// orchestrator from carrying its own DhcpScopeForPushRow → dbq.DhcpScope
// shim. The full dbq.DhcpScope path is still used by the bundle
// endpoint, which reads more columns than push needs.
func FromDbqScopeForPush(s dbq.DhcpScopeForPushRow) Scope {
	return FromDbqScope(dbq.DhcpScope{
		ID:                       s.ID,
		DhcpServerID:             s.DhcpServerID,
		IPFamily:                 s.IPFamily,
		Prefix:                   s.Prefix,
		PoolsJSON:                s.PoolsJSON,
		PdPoolsJSON:              s.PdPoolsJSON,
		OptionsJSON:              s.OptionsJSON,
		ReservationsJSON:         s.ReservationsJSON,
		ValidLifetimeSeconds:     s.ValidLifetimeSeconds,
		RenewTimerSeconds:        s.RenewTimerSeconds,
		RebindTimerSeconds:       s.RebindTimerSeconds,
		PreferredLifetimeSeconds: s.PreferredLifetimeSeconds,
		KeaSubnetID:              s.KeaSubnetID,
		TemplateID:               s.TemplateID,
		Enabled:                  s.Enabled,
	})
}
