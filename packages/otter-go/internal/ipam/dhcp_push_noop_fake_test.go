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
