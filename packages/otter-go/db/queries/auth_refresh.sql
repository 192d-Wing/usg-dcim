-- name: UpdateUserRefreshToken :exec
-- Sets or clears the encrypted IdP refresh_token on a user. Pass NULL
-- for both to clear (used when the IdP rejects a refresh). When set,
-- iat is bumped to NOW() so operators can tell how stale the stored
-- token is.
UPDATE users
SET idp_refresh_token = $2,
    idp_refresh_token_iat = CASE WHEN $2::text IS NULL THEN NULL ELSE NOW() END,
    updated_at = NOW()
WHERE id = $1;
