package ipam

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	lastVrf        dbq.ListVrfsParams
	lastSubnet     dbq.ListSubnetsParams
	lastAddr       dbq.ListIPAddressesParams
	lastFabric     dbq.ListFabricsParams
	lastSupernet   dbq.ListSupernetsParams
	lastOverlay    dbq.ListOverlaysParams
	lastVni        dbq.ListVnisParams
	lastVtep       dbq.ListVtepsParams
	lastMembership dbq.ListVtepMembershipsParams
	lastDhcp       dbq.ListDhcpServersParams
	lastVrfPeer    dbq.ListVrfBgpPeersParams
}

func (f *fakeQ) ListVrfs(_ context.Context, a dbq.ListVrfsParams) ([]dbq.Vrf, error) {
	f.lastVrf = a
	return nil, nil
}
func (f *fakeQ) CountVrfs(_ context.Context, _ dbq.CountVrfsParams) (int64, error) { return 0, nil }
func (f *fakeQ) GetVrf(_ context.Context, _ uuid.UUID) (dbq.Vrf, error) {
	return dbq.Vrf{}, pgx.ErrNoRows
}
func (f *fakeQ) ListSubnets(_ context.Context, a dbq.ListSubnetsParams) ([]dbq.Subnet, error) {
	f.lastSubnet = a
	return nil, nil
}
func (f *fakeQ) CountSubnets(_ context.Context, _ dbq.CountSubnetsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetSubnet(_ context.Context, _ uuid.UUID) (dbq.Subnet, error) {
	return dbq.Subnet{}, pgx.ErrNoRows
}
func (f *fakeQ) ListIPAddresses(_ context.Context, a dbq.ListIPAddressesParams) ([]dbq.IPAddress, error) {
	f.lastAddr = a
	return nil, nil
}
func (f *fakeQ) CountIPAddresses(_ context.Context, _ dbq.CountIPAddressesParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetIPAddress(_ context.Context, _ uuid.UUID) (dbq.IPAddress, error) {
	return dbq.IPAddress{}, pgx.ErrNoRows
}
func (f *fakeQ) ListAddressStringsInSubnet(_ context.Context, _ uuid.UUID) ([]string, error) {
	return nil, nil
}
func (f *fakeQ) ListSubnetsForFreeSpace(_ context.Context, _ dbq.ListSubnetsForFreeSpaceParams) ([]dbq.SubnetForFreeSpaceRow, error) {
	return nil, nil
}
func (f *fakeQ) ListAddressesInSubnets(_ context.Context, _ []uuid.UUID) ([]dbq.AddressInSubnetRow, error) {
	return nil, nil
}
func (f *fakeQ) ListSupernetsForCarver(_ context.Context, _ dbq.ListSupernetsForCarverParams) ([]dbq.SupernetForCarverRow, error) {
	return nil, nil
}
func (f *fakeQ) ListSubnetPrefixesBySupernets(_ context.Context, _ []uuid.UUID) ([]dbq.SubnetPrefixBySupernetRow, error) {
	return nil, nil
}

func (f *fakeQ) ListFabrics(_ context.Context, a dbq.ListFabricsParams) ([]dbq.Fabric, error) {
	f.lastFabric = a
	return nil, nil
}
func (f *fakeQ) CountFabrics(_ context.Context, _ dbq.CountFabricsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetFabric(_ context.Context, _ uuid.UUID) (dbq.Fabric, error) {
	return dbq.Fabric{}, pgx.ErrNoRows
}
func (f *fakeQ) ListSupernets(_ context.Context, a dbq.ListSupernetsParams) ([]dbq.Supernet, error) {
	f.lastSupernet = a
	return nil, nil
}
func (f *fakeQ) CountSupernets(_ context.Context, _ dbq.CountSupernetsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetSupernet(_ context.Context, _ uuid.UUID) (dbq.Supernet, error) {
	return dbq.Supernet{}, pgx.ErrNoRows
}
func (f *fakeQ) ListSubnetPrefixesBySupernet(_ context.Context, _ uuid.UUID) ([]string, error) {
	return nil, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
// ---- Mutation stubs (PR 42) ----

func (f *fakeQ) CreateFabric(_ context.Context, a dbq.CreateFabricParams) (dbq.Fabric, error) {
	return dbq.Fabric{ID: uuid.New(), Name: a.Name, Slug: a.Slug}, nil
}
func (f *fakeQ) UpdateFabric(_ context.Context, a dbq.UpdateFabricParams) (dbq.Fabric, error) {
	return dbq.Fabric{ID: a.ID}, nil
}
func (f *fakeQ) CountVrfsInFabric(_ context.Context, _ uuid.UUID) (int64, error) { return 0, nil }
func (f *fakeQ) DeleteFabric(_ context.Context, _ uuid.UUID) error               { return nil }
func (f *fakeQ) CreateVrf(_ context.Context, a dbq.CreateVrfParams) (dbq.Vrf, error) {
	return dbq.Vrf{ID: uuid.New(), FabricID: a.FabricID, Name: a.Name, IsDefault: a.IsDefault}, nil
}
func (f *fakeQ) UpdateVrf(_ context.Context, a dbq.UpdateVrfParams) (dbq.Vrf, error) {
	return dbq.Vrf{ID: a.ID}, nil
}
func (f *fakeQ) CountSupernetsInVrf(_ context.Context, _ uuid.UUID) (int64, error) { return 0, nil }
func (f *fakeQ) DeleteVrf(_ context.Context, _ uuid.UUID) error                    { return nil }
func (f *fakeQ) CreateVrfBgpPeer(_ context.Context, a dbq.CreateVrfBgpPeerParams) (dbq.VrfBgpPeer, error) {
	return dbq.VrfBgpPeer{ID: uuid.New(), VrfID: a.VrfID, BgpPeerID: a.BgpPeerID, AddressFamily: a.AddressFamily, Enabled: a.Enabled}, nil
}
func (f *fakeQ) UpdateVrfBgpPeer(_ context.Context, a dbq.UpdateVrfBgpPeerParams) (dbq.VrfBgpPeer, error) {
	return dbq.VrfBgpPeer{ID: a.ID}, nil
}
func (f *fakeQ) DeleteVrfBgpPeer(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateSupernet(_ context.Context, a dbq.CreateSupernetParams) (dbq.Supernet, error) {
	return dbq.Supernet{ID: uuid.New(), FabricID: a.FabricID, VrfID: a.VrfID, Prefix: a.Prefix}, nil
}
func (f *fakeQ) UpdateSupernet(_ context.Context, a dbq.UpdateSupernetParams) (dbq.Supernet, error) {
	return dbq.Supernet{ID: a.ID}, nil
}
func (f *fakeQ) CountSubnetsInSupernet(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *fakeQ) DeleteSupernet(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) GetSupernetVrfAndFabric(_ context.Context, _ uuid.UUID) (dbq.SupernetVrfAndFabric, error) {
	return dbq.SupernetVrfAndFabric{VrfID: uuid.New(), FabricID: uuid.New()}, nil
}
func (f *fakeQ) CreateSubnet(_ context.Context, a dbq.CreateSubnetParams) (dbq.Subnet, error) {
	return dbq.Subnet{ID: uuid.New(), SupernetID: a.SupernetID, FabricID: a.FabricID, VrfID: a.VrfID, Prefix: a.Prefix}, nil
}
func (f *fakeQ) UpdateSubnet(_ context.Context, a dbq.UpdateSubnetParams) (dbq.Subnet, error) {
	return dbq.Subnet{ID: a.ID}, nil
}
func (f *fakeQ) CountAddressesInSubnet(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *fakeQ) DeleteSubnet(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateIPAddress(_ context.Context, a dbq.CreateIPAddressParams) (dbq.IPAddress, error) {
	return dbq.IPAddress{ID: uuid.New(), SubnetID: a.SubnetID, Address: a.Address}, nil
}
func (f *fakeQ) UpdateIPAddress(_ context.Context, a dbq.UpdateIPAddressParams) (dbq.IPAddress, error) {
	return dbq.IPAddress{ID: a.ID}, nil
}
func (f *fakeQ) DeleteIPAddress(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateOverlay(_ context.Context, a dbq.CreateOverlayParams) (dbq.Overlay, error) {
	return dbq.Overlay{ID: uuid.New(), FabricID: a.FabricID, Name: a.Name, Kind: a.Kind, UDPPort: a.UDPPort}, nil
}
func (f *fakeQ) UpdateOverlay(_ context.Context, a dbq.UpdateOverlayParams) (dbq.Overlay, error) {
	return dbq.Overlay{ID: a.ID}, nil
}
func (f *fakeQ) CountVnisInOverlay(_ context.Context, _ uuid.UUID) (int64, error) { return 0, nil }
func (f *fakeQ) DeleteOverlay(_ context.Context, _ uuid.UUID) error               { return nil }
func (f *fakeQ) CreateVni(_ context.Context, a dbq.CreateVniParams) (dbq.Vni, error) {
	return dbq.Vni{ID: uuid.New(), OverlayID: a.OverlayID, VNI: a.VNI, Kind: a.Kind}, nil
}
func (f *fakeQ) UpdateVni(_ context.Context, a dbq.UpdateVniParams) (dbq.Vni, error) {
	return dbq.Vni{ID: a.ID}, nil
}
func (f *fakeQ) DeleteVni(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateVtep(_ context.Context, a dbq.CreateVtepParams) (dbq.Vtep, error) {
	return dbq.Vtep{ID: uuid.New(), OverlayID: a.OverlayID, AssetID: a.AssetID, Role: a.Role}, nil
}
func (f *fakeQ) UpdateVtep(_ context.Context, a dbq.UpdateVtepParams) (dbq.Vtep, error) {
	return dbq.Vtep{ID: a.ID}, nil
}
func (f *fakeQ) DeleteVtep(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateVtepMembership(_ context.Context, a dbq.CreateVtepMembershipParams) (dbq.VtepVniMembership, error) {
	return dbq.VtepVniMembership{ID: uuid.New(), VtepID: a.VtepID, VniID: a.VniID}, nil
}
func (f *fakeQ) DeleteVtepMembership(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDhcpServer(_ context.Context, a dbq.CreateDhcpServerParams) (dbq.DhcpServer, error) {
	return dbq.DhcpServer{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, KeaURL: a.KeaURL, Enabled: a.Enabled}, nil
}
func (f *fakeQ) UpdateDhcpServer(_ context.Context, a dbq.UpdateDhcpServerParams) (dbq.DhcpServer, error) {
	return dbq.DhcpServer{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDhcpServer(_ context.Context, _ uuid.UUID) error { return nil }

// ABAC parent-fabric lookups (PRs 54 + 55). Tests that don't care
// about scope can let these return uuid.Nil (treated as "no fabric to
// enforce" by EnforceFabricScope, so global behavior).
func (f *fakeQ) GetVrfFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetOverlayFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetDhcpServerFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetSubnetFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetIPAddressFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetVniFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetVtepFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetVtepMembershipFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}

func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
	return rec
}

func TestListVrfs_FabricFilter(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/ipam/vrfs?fabric_id="+fid.String())
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastVrf.FabricID == nil || *f.lastVrf.FabricID != fid {
		t.Errorf("fabric_id not threaded")
	}
}

func TestListSubnets_AllFilters(t *testing.T) {
	fid, vid, sid := uuid.New(), uuid.New(), uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/ipam/subnets?fabric_id="+fid.String()+"&vrf_id="+vid.String()+"&site_id="+sid.String()+"&purpose=mgmt")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastSubnet.FabricID == nil || *f.lastSubnet.FabricID != fid {
		t.Error("fabric_id")
	}
	if f.lastSubnet.VrfID == nil || *f.lastSubnet.VrfID != vid {
		t.Error("vrf_id")
	}
	if f.lastSubnet.SiteID == nil || *f.lastSubnet.SiteID != sid {
		t.Error("site_id")
	}
	if f.lastSubnet.Purpose == nil || *f.lastSubnet.Purpose != "mgmt" {
		t.Error("purpose")
	}
}

func TestListAddresses_AllFilters(t *testing.T) {
	sid, aid := uuid.New(), uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/ipam/addresses?subnet_id="+sid.String()+"&asset_id="+aid.String()+"&role=gateway&status=active")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastAddr.SubnetID == nil || *f.lastAddr.SubnetID != sid {
		t.Error("subnet_id")
	}
	if f.lastAddr.AssetID == nil || *f.lastAddr.AssetID != aid {
		t.Error("asset_id")
	}
	if f.lastAddr.Role == nil || *f.lastAddr.Role != "gateway" {
		t.Error("role")
	}
	if f.lastAddr.Status == nil || *f.lastAddr.Status != "active" {
		t.Error("status")
	}
}

func TestBadFilterUUIDs(t *testing.T) {
	for _, p := range []string{
		"/ipam/vrfs?fabric_id=x",
		"/ipam/subnets?fabric_id=x",
		"/ipam/subnets?vrf_id=x",
		"/ipam/subnets?site_id=x",
		"/ipam/addresses?subnet_id=x",
		"/ipam/addresses?asset_id=x",
	} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}

func TestGets_NotFound(t *testing.T) {
	for _, p := range []string{
		"/ipam/vrfs/" + uuid.New().String(),
		"/ipam/subnets/" + uuid.New().String(),
		"/ipam/addresses/" + uuid.New().String(),
	} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}

func TestGets_BadID(t *testing.T) {
	for _, p := range []string{
		"/ipam/vrfs/x", "/ipam/subnets/x", "/ipam/addresses/x",
	} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}

func TestListVrfs_PageSizeAlias(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/ipam/vrfs?page_size=200")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastVrf.Limit != 200 {
		t.Errorf("page_size not honored: %d", f.lastVrf.Limit)
	}
}

// ---- Fabrics ----

func TestListFabrics_EnclaveFilter(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/ipam/fabrics?enclave=siprnet")
	if f.lastFabric.Enclave == nil || *f.lastFabric.Enclave != "siprnet" {
		t.Errorf("enclave not threaded: %+v", f.lastFabric)
	}
}

func TestGetFabric_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/ipam/fabrics/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

// ---- Supernets parent_filter ----

func TestListSupernets_NoParentFilter(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/ipam/supernets")
	if f.lastSupernet.ParentFilterMode != "any" {
		t.Errorf("want any, got %q", f.lastSupernet.ParentFilterMode)
	}
}

func TestListSupernets_TopLevelOnly(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/ipam/supernets?top_level=true")
	if f.lastSupernet.ParentFilterMode != "null" {
		t.Errorf("top_level should map to null mode, got %q", f.lastSupernet.ParentFilterMode)
	}
}

func TestListSupernets_ParentNullLiteral(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/ipam/supernets?parent_supernet_id=null")
	if f.lastSupernet.ParentFilterMode != "null" {
		t.Errorf("literal null should map to null mode, got %q", f.lastSupernet.ParentFilterMode)
	}
}

func TestListSupernets_ParentSpecific(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/supernets?parent_supernet_id="+id.String())
	if f.lastSupernet.ParentFilterMode != "eq" || f.lastSupernet.ParentSupernetID == nil || *f.lastSupernet.ParentSupernetID != id {
		t.Errorf("uuid should map to eq mode: %+v", f.lastSupernet)
	}
}

func TestListSupernets_TopLevelWinsOverParentID(t *testing.T) {
	// Python semantics — explicit top_level wins.
	id := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/supernets?top_level=true&parent_supernet_id="+id.String())
	if f.lastSupernet.ParentFilterMode != "null" {
		t.Errorf("top_level should win, got mode %q", f.lastSupernet.ParentFilterMode)
	}
}

func TestListSupernets_BadParentID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/ipam/supernets?parent_supernet_id=garbage")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListSupernets_FabricVrfFilters(t *testing.T) {
	fid, vid := uuid.New(), uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/supernets?fabric_id="+fid.String()+"&vrf_id="+vid.String())
	if f.lastSupernet.FabricID == nil || *f.lastSupernet.FabricID != fid {
		t.Errorf("fabric_id not threaded")
	}
	if f.lastSupernet.VrfID == nil || *f.lastSupernet.VrfID != vid {
		t.Errorf("vrf_id not threaded")
	}
}

// ---- Overlays/VNIs/VTEPs/Memberships/DHCP fakeQ stubs ----

func (f *fakeQ) ListOverlays(_ context.Context, a dbq.ListOverlaysParams) ([]dbq.Overlay, error) {
	f.lastOverlay = a
	return nil, nil
}
func (f *fakeQ) CountOverlays(_ context.Context, _ dbq.CountOverlaysParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListVnis(_ context.Context, a dbq.ListVnisParams) ([]dbq.Vni, error) {
	f.lastVni = a
	return nil, nil
}
func (f *fakeQ) CountVnis(_ context.Context, _ dbq.CountVnisParams) (int64, error) { return 0, nil }
func (f *fakeQ) ListVteps(_ context.Context, a dbq.ListVtepsParams) ([]dbq.Vtep, error) {
	f.lastVtep = a
	return nil, nil
}
func (f *fakeQ) CountVteps(_ context.Context, _ dbq.CountVtepsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListVtepMemberships(_ context.Context, a dbq.ListVtepMembershipsParams) ([]dbq.VtepVniMembership, error) {
	f.lastMembership = a
	return nil, nil
}
func (f *fakeQ) CountVtepMemberships(_ context.Context, _ dbq.CountVtepMembershipsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListDhcpServers(_ context.Context, a dbq.ListDhcpServersParams) ([]dbq.DhcpServer, error) {
	f.lastDhcp = a
	return nil, nil
}
func (f *fakeQ) CountDhcpServers(_ context.Context, _ dbq.CountDhcpServersParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListVrfBgpPeers(_ context.Context, a dbq.ListVrfBgpPeersParams) ([]dbq.VrfBgpPeer, error) {
	f.lastVrfPeer = a
	return nil, nil
}
func (f *fakeQ) CountVrfBgpPeers(_ context.Context, _ dbq.CountVrfBgpPeersParams) (int64, error) {
	return 0, nil
}

func TestListOverlays_FabricFilter(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/overlays?fabric_id="+fid.String())
	if f.lastOverlay.FabricID == nil || *f.lastOverlay.FabricID != fid {
		t.Error("fabric_id not threaded")
	}
}

func TestListVnis_AllFilters(t *testing.T) {
	oid, fid := uuid.New(), uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/vnis?overlay_id="+oid.String()+"&fabric_id="+fid.String()+"&kind=l3")
	if f.lastVni.OverlayID == nil || *f.lastVni.OverlayID != oid {
		t.Error("overlay_id")
	}
	if f.lastVni.FabricID == nil || *f.lastVni.FabricID != fid {
		t.Error("fabric_id")
	}
	if f.lastVni.Kind == nil || *f.lastVni.Kind != "l3" {
		t.Error("kind")
	}
}

func TestListVteps_BothFilters(t *testing.T) {
	oid, aid := uuid.New(), uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/vteps?overlay_id="+oid.String()+"&asset_id="+aid.String())
	if f.lastVtep.OverlayID == nil || *f.lastVtep.OverlayID != oid {
		t.Error("overlay_id")
	}
	if f.lastVtep.AssetID == nil || *f.lastVtep.AssetID != aid {
		t.Error("asset_id")
	}
}

func TestListVtepMemberships_AllThree(t *testing.T) {
	v, n, o := uuid.New(), uuid.New(), uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/vtep-memberships?vtep_id="+v.String()+"&vni_id="+n.String()+"&overlay_id="+o.String())
	if f.lastMembership.VtepID == nil || *f.lastMembership.VtepID != v {
		t.Error("vtep_id")
	}
	if f.lastMembership.VniID == nil || *f.lastMembership.VniID != n {
		t.Error("vni_id")
	}
	if f.lastMembership.OverlayID == nil || *f.lastMembership.OverlayID != o {
		t.Error("overlay_id")
	}
}

func TestListDhcpServers_FabricFilter(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/dhcp/servers?fabric_id="+fid.String())
	if f.lastDhcp.FabricID == nil || *f.lastDhcp.FabricID != fid {
		t.Error("fabric_id not threaded")
	}
}

func TestListVrfBgpPeers_AllFilters(t *testing.T) {
	vid, pid := uuid.New(), uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/ipam/vrf-bgp-peers?vrf_id="+vid.String()+"&bgp_peer_id="+pid.String()+"&address_family=vpnv4")
	if f.lastVrfPeer.VrfID == nil || *f.lastVrfPeer.VrfID != vid {
		t.Error("vrf_id")
	}
	if f.lastVrfPeer.BgpPeerID == nil || *f.lastVrfPeer.BgpPeerID != pid {
		t.Error("bgp_peer_id")
	}
	if f.lastVrfPeer.AddressFamily == nil || *f.lastVrfPeer.AddressFamily != "vpnv4" {
		t.Error("address_family")
	}
}

func TestBadUUIDs_OverlayDhcp(t *testing.T) {
	for _, p := range []string{
		"/ipam/overlays?fabric_id=x",
		"/ipam/vnis?overlay_id=x",
		"/ipam/vnis?fabric_id=x",
		"/ipam/vteps?overlay_id=x",
		"/ipam/vteps?asset_id=x",
		"/ipam/vtep-memberships?vtep_id=x",
		"/ipam/vtep-memberships?vni_id=x",
		"/ipam/vtep-memberships?overlay_id=x",
		"/ipam/dhcp/servers?fabric_id=x",
	} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}
