package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// upsertOidcUser mirrors Python _upsert_oidc_user:
//   1. Try by sso_subject — that's the stable identifier across email
//      changes.
//   2. Fall back to email — covers users who pre-existed as local
//      break-glass accounts before SSO went live.
//   3. Insert if neither matched.
// Existing rows are updated with the latest sso_subject + display_name
// (only when previously unset) + last_login_at.
func upsertOidcUser(ctx context.Context, q Querier, sub, email, name string) (dbq.User, error) {
	subPtr := &sub
	var namePtr *string
	if name != "" {
		namePtr = &name
	}
	if u, err := q.GetUserBySsoSubject(ctx, sub); err == nil {
		return q.UpdateOidcUserOnLogin(ctx, dbq.UpdateOidcUserOnLoginParams{
			ID: u.ID, SsoSubject: subPtr, DisplayName: namePtr,
		})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return dbq.User{}, err
	}
	if u, err := q.GetUserByEmail(ctx, email); err == nil {
		return q.UpdateOidcUserOnLogin(ctx, dbq.UpdateOidcUserOnLoginParams{
			ID: u.ID, SsoSubject: subPtr, DisplayName: namePtr,
		})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return dbq.User{}, err
	}
	return q.CreateOidcUser(ctx, dbq.CreateOidcUserParams{
		Email: email, DisplayName: namePtr, SsoSubject: subPtr,
	})
}
