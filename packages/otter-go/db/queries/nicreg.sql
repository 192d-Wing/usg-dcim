-- DoD NIC registration intake — header lifecycle.
--
-- Header + typed-detail: this file owns nic_registrations (the shared
-- lifecycle row). Per-type detail create/get live in nicreg_details.sql.
-- Field shapes are the canonical schema in internal/nicreg/templates.json.
--
-- Status state machine (CHECK ck_nicreg_status in migration 0068):
--   draft → submitted → approved | rejected
--   draft | submitted → cancelled
-- approve/reject record decided_* and (optionally) push_to_arin.

-- name: CreateNicRegistration :one
-- status is 'draft' or 'submitted'; submitted_at is stamped only when the
-- caller submits straight away (the form's "Submit" vs "Save draft").
INSERT INTO nic_registrations (
    id, template_type, action_type, organization_id, requester_user_id,
    status, submitted_at, created_at, updated_at
)
VALUES (
    gen_random_uuid(), $1, $2, $3, $4,
    sqlc.arg(status)::text,
    CASE WHEN sqlc.arg(status)::text = 'submitted' THEN NOW() ELSE NULL END,
    NOW(), NOW()
)
RETURNING *;

-- name: GetNicRegistration :one
SELECT * FROM nic_registrations WHERE id = $1;

-- name: ListNicRegistrations :many
-- Org-scope filter mirrors the LIR pattern: NULL scope_org_ids = global
-- (no org filter); a non-empty array restricts. The handler short-circuits
-- an empty in-scope set to an empty page without hitting this query.
SELECT * FROM nic_registrations
WHERE (sqlc.narg(scope_org_ids)::uuid[] IS NULL
       OR organization_id = ANY(sqlc.narg(scope_org_ids)::uuid[]))
  AND (sqlc.narg(status_filter)::text IS NULL OR status = sqlc.narg(status_filter)::text)
  AND (sqlc.narg(type_filter)::text IS NULL OR template_type = sqlc.narg(type_filter)::text)
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountNicRegistrations :one
SELECT count(*)::bigint FROM nic_registrations
WHERE (sqlc.narg(scope_org_ids)::uuid[] IS NULL
       OR organization_id = ANY(sqlc.narg(scope_org_ids)::uuid[]))
  AND (sqlc.narg(status_filter)::text IS NULL OR status = sqlc.narg(status_filter)::text)
  AND (sqlc.narg(type_filter)::text IS NULL OR template_type = sqlc.narg(type_filter)::text);

-- name: SubmitNicRegistration :one
-- Atomic draft → submitted. Matches only draft rows so a double-submit or a
-- submit of an already-decided row leaves zero rows → handler returns 409.
UPDATE nic_registrations
SET status = 'submitted', submitted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND status = 'draft'
RETURNING *;

-- name: CancelNicRegistration :one
-- Atomic cancel of a not-yet-decided row (draft or submitted). decided_* stay
-- NULL — the cancelled status carries the audit signal, and
-- ck_nicreg_decision_consistency permits cancelled rows to skip them.
UPDATE nic_registrations
SET status = 'cancelled', decision_notes = sqlc.narg(notes)::text, updated_at = NOW()
WHERE id = $1 AND status IN ('draft', 'submitted')
RETURNING *;

-- name: ApproveNicRegistration :one
-- Atomic submitted → approved. push_to_arin is the NIC reviewer's upstream
-- routing decision (NULL/true/false). decided_* are always set.
UPDATE nic_registrations
SET status = 'approved',
    push_to_arin = sqlc.narg(push_to_arin)::boolean,
    decided_at = NOW(),
    decided_by_user_id = sqlc.arg(decided_by)::uuid,
    decision_notes = sqlc.narg(notes)::text,
    updated_at = NOW()
WHERE id = $1 AND status = 'submitted'
RETURNING *;

-- name: RejectNicRegistration :one
-- Atomic submitted → rejected.
UPDATE nic_registrations
SET status = 'rejected',
    decided_at = NOW(),
    decided_by_user_id = sqlc.arg(decided_by)::uuid,
    decision_notes = sqlc.narg(notes)::text,
    updated_at = NOW()
WHERE id = $1 AND status = 'submitted'
RETURNING *;
