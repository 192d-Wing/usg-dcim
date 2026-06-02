// Shared test stub for DHCP push / diff / history Querier methods.
// Every fakeQ in this package embeds dhcpPushNoop so the package
// only carries one set of no-op implementations of the 10 push/diff/
// history methods (added in PR 5 of the DHCP push port). Tests that
// actually exercise dhcp_push.go endpoints override the relevant
// methods on their own fake; the rest get pgx.ErrNoRows-shaped
// "doesn't exist" returns so unrelated tests still compile and run
// against the broader ipam.Querier surface.
package ipam

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type dhcpPushNoop struct{}

func (dhcpPushNoop) GetDhcpScopeFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, pgx.ErrNoRows
}

func (dhcpPushNoop) GetDhcpScopeForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeForPushRow, error) {
	return dbq.DhcpScopeForPushRow{}, pgx.ErrNoRows
}

func (dhcpPushNoop) GetDhcpServerForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpServerForPushRow, error) {
	return dbq.DhcpServerForPushRow{}, pgx.ErrNoRows
}

func (dhcpPushNoop) GetDhcpScopeTemplateForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	return dbq.DhcpScopeTemplate{}, pgx.ErrNoRows
}

func (dhcpPushNoop) ListKeaSubnetIDsForServer(_ context.Context, _ uuid.UUID) ([]int32, error) {
	return nil, nil
}

func (dhcpPushNoop) UpdateDhcpScopeKeaSubnetID(_ context.Context, _ dbq.UpdateDhcpScopeKeaSubnetIDParams) error {
	return nil
}

func (dhcpPushNoop) UpdateDhcpScopeAfterSuccessfulPush(_ context.Context, _ uuid.UUID) error {
	return nil
}

func (dhcpPushNoop) UpdateDhcpServerLastPush(_ context.Context, _ dbq.UpdateDhcpServerLastPushParams) error {
	return nil
}

func (dhcpPushNoop) InsertDhcpScopePushHistory(_ context.Context, _ dbq.InsertDhcpScopePushHistoryParams) error {
	return nil
}

func (dhcpPushNoop) WriteDhcpScopeDiffState(_ context.Context, _ dbq.WriteDhcpScopeDiffStateParams) error {
	return nil
}

func (dhcpPushNoop) ListDhcpScopePushHistoryByScope(_ context.Context, _ dbq.ListDhcpScopePushHistoryByScopeParams) ([]dbq.DhcpScopePushHistoryRow, error) {
	return nil, nil
}

func (dhcpPushNoop) ListEnabledScopeIDsForServer(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (dhcpPushNoop) ListDriftedScopeIDsForServer(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (dhcpPushNoop) ListAllScopeIDsAndPriorDriftForServer(_ context.Context, _ uuid.UUID) ([]dbq.DhcpScopeIDAndPriorDriftRow, error) {
	return nil, nil
}

// DHCP scope-template CRUD stubs (PR 8). LIST returns an empty page,
// GET returns ErrNoRows so unrelated tests don't accidentally fall
// into a half-defined row; create/update return zero values, delete
// is a no-op. Tests that actually exercise the CRUD endpoints
// override these on their own fake.
func (dhcpPushNoop) ListDhcpScopeTemplates(_ context.Context, _ dbq.ListDhcpScopeTemplatesParams) ([]dbq.DhcpScopeTemplate, error) {
	return nil, nil
}

func (dhcpPushNoop) CountDhcpScopeTemplates(_ context.Context, _ dbq.CountDhcpScopeTemplatesParams) (int64, error) {
	return 0, nil
}

func (dhcpPushNoop) GetDhcpScopeTemplate(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	return dbq.DhcpScopeTemplate{}, pgx.ErrNoRows
}

func (dhcpPushNoop) CreateDhcpScopeTemplate(_ context.Context, _ dbq.CreateDhcpScopeTemplateParams) (dbq.DhcpScopeTemplate, error) {
	return dbq.DhcpScopeTemplate{}, nil
}

func (dhcpPushNoop) UpdateDhcpScopeTemplate(_ context.Context, _ dbq.UpdateDhcpScopeTemplateParams) (dbq.DhcpScopeTemplate, error) {
	return dbq.DhcpScopeTemplate{}, nil
}

func (dhcpPushNoop) DeleteDhcpScopeTemplate(_ context.Context, _ uuid.UUID) error {
	return nil
}

// DHCP drift-summary read paths (PR 9). All three return empty
// slices so unrelated tests don't accidentally fall into a half-
// defined fleet state.
func (dhcpPushNoop) ListDhcpServersForDriftSummary(_ context.Context, _ []uuid.UUID) ([]dbq.DhcpServerDriftSummaryRow, error) {
	return nil, nil
}

func (dhcpPushNoop) ListDhcpScopeDriftStatusByServers(_ context.Context, _ []uuid.UUID) ([]dbq.DhcpScopeDriftStatusRow, error) {
	return nil, nil
}

func (dhcpPushNoop) ListFiringDhcpDriftAlertKeys(_ context.Context) ([]string, error) {
	return nil, nil
}

// DHCP scope CRUD reads (PR 10). LIST returns nil + count zero;
// GetDhcpScope returns ErrNoRows so unrelated tests fall through to
// 404 paths rather than half-defined scope state.
func (dhcpPushNoop) ListDhcpScopesByServer(_ context.Context, _ dbq.ListDhcpScopesByServerParams) ([]dbq.DhcpScope, error) {
	return nil, nil
}

func (dhcpPushNoop) CountDhcpScopesByServer(_ context.Context, _ dbq.CountDhcpScopesByServerParams) (int64, error) {
	return 0, nil
}

func (dhcpPushNoop) GetDhcpScope(_ context.Context, _ uuid.UUID) (dbq.DhcpScope, error) {
	return dbq.DhcpScope{}, pgx.ErrNoRows
}

// DHCP scope mutation stubs (PR 11). CREATE/UPDATE/RESTORE return
// zero values; SoftDelete is a no-op. Tests that exercise the
// mutation paths override these on their own fake.
func (dhcpPushNoop) CreateDhcpScope(_ context.Context, _ dbq.CreateDhcpScopeParams) (dbq.DhcpScope, error) {
	return dbq.DhcpScope{}, nil
}

func (dhcpPushNoop) UpdateDhcpScope(_ context.Context, _ dbq.UpdateDhcpScopeParams) (dbq.DhcpScope, error) {
	return dbq.DhcpScope{}, nil
}

func (dhcpPushNoop) SoftDeleteDhcpScope(_ context.Context, _ uuid.UUID) error {
	return nil
}

func (dhcpPushNoop) RestoreDhcpScope(_ context.Context, _ uuid.UUID) (dbq.DhcpScope, error) {
	return dbq.DhcpScope{}, nil
}
