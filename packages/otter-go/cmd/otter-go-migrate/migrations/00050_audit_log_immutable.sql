-- +goose Up

-- +goose StatementBegin
        CREATE OR REPLACE FUNCTION audit_log_no_update_delete()
        RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION
                'audit_log is append-only — % rejected on row %', TG_OP, OLD
                USING ERRCODE = 'insufficient_privilege';
        END;
        $$ LANGUAGE plpgsql;
-- +goose StatementEnd

        CREATE TRIGGER audit_log_immutable_update
            BEFORE UPDATE ON audit_log
            FOR EACH ROW EXECUTE FUNCTION audit_log_no_update_delete();

        CREATE TRIGGER audit_log_immutable_delete
            BEFORE DELETE ON audit_log
            FOR EACH ROW EXECUTE FUNCTION audit_log_no_update_delete();

-- +goose Down
DROP TRIGGER IF EXISTS audit_log_immutable_delete ON audit_log;
DROP TRIGGER IF EXISTS audit_log_immutable_update ON audit_log;
DROP FUNCTION IF EXISTS audit_log_no_update_delete();
