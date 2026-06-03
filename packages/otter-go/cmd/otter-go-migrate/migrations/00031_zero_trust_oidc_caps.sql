-- +goose Up

        DELETE FROM role_scopes
        WHERE assignment_id IN (
            SELECT ur.id FROM user_roles ur
            JOIN users u ON u.id = ur.user_id
            WHERE u.sso_subject IS NOT NULL
        );

        DELETE FROM user_roles
        WHERE user_id IN (SELECT id FROM users WHERE sso_subject IS NOT NULL);

-- +goose Down
-- (no-op downgrade)
