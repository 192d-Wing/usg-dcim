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

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
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
