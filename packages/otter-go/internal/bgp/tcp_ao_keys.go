// TCP AO key CRUD + /rotate-batch. Closes the dark-code gap from
// PR #203: Mount now wires keys/rotate-batch alongside the key-chain
// routes, and the umbrella chart's ingress cuts /api/v1/routing/tcp-
// ao-key-chains and /api/v1/routing/tcp-ao-keys over to otter-go in
// the same PR. The Python TCP-AO routes are deleted alongside.
package bgp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const (
	tcpAoKeyNotFound  = "tcp ao key not found"
	algoHmacSha1_96   = "hmac-sha1-96"
	algoAes128Cmac    = "aes-128-cmac"
)

type tcpAoKeysPage struct {
	Items  []dbq.TcpAoKey `json:"items"`
	Total  int64          `json:"total"`
	Limit  int32          `json:"limit"`
	Offset int32          `json:"offset"`
}

func validAlgorithm(a string) bool {
	return a == algoHmacSha1_96 || a == algoAes128Cmac
}

func (h *Handler) listTcpAoKeys(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	params := dbq.ListTcpAoKeysParams{Limit: limit, Offset: offset}
	if v := q.Get("key_chain_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "key_chain_id is not a uuid")
			return
		}
		params.KeyChainID = &id
	}
	items, err := h.Q.ListTcpAoKeys(r.Context(), params)
	if err != nil {
		writeMapped(w, err)
		return
	}
	if items == nil {
		items = []dbq.TcpAoKey{}
	}
	total, err := h.Q.CountTcpAoKeys(r.Context(), dbq.CountTcpAoKeysParams{KeyChainID: params.KeyChainID})
	if err != nil {
		writeMapped(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tcpAoKeysPage{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

func (h *Handler) getTcpAoKey(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTcpAoID(w, r)
	if !ok {
		return
	}
	key, err := h.Q.GetTcpAoKey(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, tcpAoKeyNotFound)
			return
		}
		writeMapped(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, key)
}

type tcpAoKeyCreateReq struct {
	KeyChainID  uuid.UUID  `json:"key_chain_id"`
	KeyID       int32      `json:"key_id"`
	SendID      int32      `json:"send_id"`
	RecvID      int32      `json:"recv_id"`
	Algorithm   string     `json:"algorithm"`
	Secret      string     `json:"secret"`
	ValidFrom   *time.Time `json:"valid_from"`
	ValidTo     *time.Time `json:"valid_to"`
	Description *string    `json:"description"`
}

// tcpAoKeyCreateReqRaw uses a custom decoder so we can detect omitted
// (vs zero) required ints — Python's TcpAoKeyBase requires key_id,
// send_id, recv_id with no defaults; Pydantic 422s on omission.
type tcpAoKeyCreateRaw struct {
	tcpAoKeyCreateReq
	keyIDSet, sendIDSet, recvIDSet bool
}

func (u *tcpAoKeyCreateRaw) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type alias tcpAoKeyCreateReq
	if err := json.Unmarshal(data, (*alias)(&u.tcpAoKeyCreateReq)); err != nil {
		return err
	}
	_, u.keyIDSet = raw["key_id"]
	_, u.sendIDSet = raw["send_id"]
	_, u.recvIDSet = raw["recv_id"]
	return nil
}

func (h *Handler) createTcpAoKey(w http.ResponseWriter, r *http.Request) {
	var raw tcpAoKeyCreateRaw
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	req := raw.tcpAoKeyCreateReq
	if req.KeyChainID == uuid.Nil || req.Secret == "" {
		httpx.Error(w, http.StatusBadRequest, "key_chain_id and secret required")
		return
	}
	// Python's TcpAoKeyBase requires these three; we mirror so a typo
	// can't silently land a key with id=0.
	if !raw.keyIDSet || !raw.sendIDSet || !raw.recvIDSet {
		httpx.Error(w, http.StatusBadRequest, "key_id, send_id, recv_id are required")
		return
	}
	if req.KeyID < 0 || req.SendID < 0 || req.RecvID < 0 {
		httpx.Error(w, http.StatusBadRequest, "key_id/send_id/recv_id must be non-negative")
		return
	}
	if !validAlgorithm(req.Algorithm) {
		httpx.Error(w, http.StatusBadRequest, "algorithm must be hmac-sha1-96 or aes-128-cmac")
		return
	}
	// Window validation (Python's TcpAoKeyBase._validate_window).
	if req.ValidFrom != nil && req.ValidTo != nil && !req.ValidTo.After(*req.ValidFrom) {
		httpx.Error(w, http.StatusBadRequest, "valid_to must be after valid_from")
		return
	}
	// Refuse if the chain doesn't exist — Python raises NotFoundError
	// here; we mirror with 404 instead of letting the FK violation
	// surface as 409.
	if _, err := h.Q.GetTcpAoKeyChain(r.Context(), req.KeyChainID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, tcpAoChainNotFound)
			return
		}
		writeMapped(w, err)
		return
	}
	out, err := h.Q.CreateTcpAoKey(r.Context(), dbq.CreateTcpAoKeyParams{
		KeyChainID: req.KeyChainID, KeyID: req.KeyID,
		SendID: req.SendID, RecvID: req.RecvID,
		Algorithm: req.Algorithm, Secret: req.Secret,
		ValidFrom: req.ValidFrom, ValidTo: req.ValidTo,
		Description: req.Description,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "tcp_ao_key.create", TargetType: "tcp_ao_key",
		TargetID: out.ID.String(),
	})
	httpx.JSON(w, http.StatusCreated, out)
}

// tcpAoKeyUpdateReq tracks set-flags on every nullable / mutable
// field so {"description": null} clears the row, matching Pydantic
// exclude_unset=True semantics. key_id is intentionally NOT
// patchable — it's part of the (chain_id, key_id) natural key and
// the rotation timeline. Operators delete+recreate to renumber.
// Python's TcpAoKeyUpdate omits it for the same reason.
type tcpAoKeyUpdateReq struct {
	SendID         *int32     `json:"send_id,omitempty"`
	RecvID         *int32     `json:"recv_id,omitempty"`
	Algorithm      *string    `json:"algorithm,omitempty"`
	Secret         *string    `json:"secret,omitempty"`
	ValidFrom      *time.Time `json:"valid_from,omitempty"`
	validFromSet   bool
	ValidTo        *time.Time `json:"valid_to,omitempty"`
	validToSet     bool
	Description    *string `json:"description,omitempty"`
	descriptionSet bool
}

func (u *tcpAoKeyUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, ok := raw["key_id"]; ok {
		return errors.New("key_id is not patchable; delete + recreate to renumber")
	}
	if v, ok := raw["send_id"]; ok {
		if err := json.Unmarshal(v, &u.SendID); err != nil {
			return err
		}
	}
	if v, ok := raw["recv_id"]; ok {
		if err := json.Unmarshal(v, &u.RecvID); err != nil {
			return err
		}
	}
	if v, ok := raw["algorithm"]; ok {
		if err := json.Unmarshal(v, &u.Algorithm); err != nil {
			return err
		}
	}
	if v, ok := raw["secret"]; ok {
		if err := json.Unmarshal(v, &u.Secret); err != nil {
			return err
		}
	}
	if v, ok := raw["valid_from"]; ok {
		u.validFromSet = true
		if err := json.Unmarshal(v, &u.ValidFrom); err != nil {
			return err
		}
	}
	if v, ok := raw["valid_to"]; ok {
		u.validToSet = true
		if err := json.Unmarshal(v, &u.ValidTo); err != nil {
			return err
		}
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		if err := json.Unmarshal(v, &u.Description); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) updateTcpAoKey(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTcpAoID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetTcpAoKey(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, tcpAoKeyNotFound)
			return
		}
		writeMapped(w, err)
		return
	}
	var req tcpAoKeyUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if req.Algorithm != nil && !validAlgorithm(*req.Algorithm) {
		httpx.Error(w, http.StatusBadRequest, "algorithm must be hmac-sha1-96 or aes-128-cmac")
		return
	}
	// Window sanity: if both bounds are being set in the same patch
	// (or one is being set and the existing other-bound row would
	// invert it), refuse. Cheap pre-check uses the patch payload
	// only; downstream lifetime correctness still depends on the
	// existing row's other bound, but Python only checked the
	// payload too.
	if req.validFromSet && req.validToSet &&
		req.ValidFrom != nil && req.ValidTo != nil &&
		!req.ValidTo.After(*req.ValidFrom) {
		httpx.Error(w, http.StatusBadRequest, "valid_to must be after valid_from")
		return
	}
	out, err := h.Q.UpdateTcpAoKey(r.Context(), dbq.UpdateTcpAoKeyParams{
		ID:     id,
		SendID: req.SendID, RecvID: req.RecvID,
		Algorithm: req.Algorithm, Secret: req.Secret,
		ValidFromSet: req.validFromSet, ValidFrom: req.ValidFrom,
		ValidToSet: req.validToSet, ValidTo: req.ValidTo,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "tcp_ao_key.update", TargetType: "tcp_ao_key",
		TargetID: id.String(), Diff: tcpAoKeyDiff(req),
	})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteTcpAoKey(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTcpAoID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetTcpAoKey(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, tcpAoKeyNotFound)
			return
		}
		writeMapped(w, err)
		return
	}
	if err := h.Q.DeleteTcpAoKey(r.Context(), id); err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "tcp_ao_key.delete", TargetType: "tcp_ao_key",
		TargetID: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}

func tcpAoKeyDiff(req tcpAoKeyUpdateReq) map[string]any {
	d := map[string]any{}
	if req.SendID != nil {
		d["send_id"] = *req.SendID
	}
	if req.RecvID != nil {
		d["recv_id"] = *req.RecvID
	}
	if req.Algorithm != nil {
		d["algorithm"] = *req.Algorithm
	}
	if req.Secret != nil {
		// Don't log the secret value in audit_log; flagging that it
		// rotated is enough.
		d["secret"] = "[rotated]"
	}
	if req.validFromSet {
		d["valid_from"] = req.ValidFrom
	}
	if req.validToSet {
		d["valid_to"] = req.ValidTo
	}
	if req.descriptionSet {
		d["description"] = req.Description
	}
	return d
}

// ----- Rotate-batch -----

type tcpAoRotateReq struct {
	Start       *time.Time `json:"start"`
	Count       int        `json:"count"`
	DaysPerKey  int        `json:"days_per_key"`
	Algorithm   string     `json:"algorithm"`
}

const (
	defaultRotateCount      = 12
	defaultRotateDaysPerKey = 30
	maxRotateCount          = 365
	maxRotateDaysPerKey     = 366
	secretBytes             = 32
)

// randomSecretHex is overridable for tests; production wires
// crypto/rand. 32 bytes = 256 bits of entropy, hex-encoded to 64
// characters — same shape Python's secrets.token_hex(32) produces.
var randomSecretHex = func() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// timeNow is overridable for tests so a fixed `start` baseline keeps
// assertions stable across CI runs.
var timeNow = time.Now

func (h *Handler) rotateTcpAoKeyChain(w http.ResponseWriter, r *http.Request) {
	chainID, ok := parseTcpAoID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetTcpAoKeyChain(r.Context(), chainID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, tcpAoChainNotFound)
			return
		}
		writeMapped(w, err)
		return
	}
	req := tcpAoRotateReq{
		Count:      defaultRotateCount,
		DaysPerKey: defaultRotateDaysPerKey,
		Algorithm:  algoHmacSha1_96,
	}
	// Empty body OK; defaults survive. Use io.EOF tolerance so a
	// chunked-encoded POST (ContentLength=-1) still has its body
	// decoded — the prior `if r.ContentLength > 0` heuristic
	// silently dropped chunked bodies and underrotated.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if req.Count < 1 || req.Count > maxRotateCount {
		httpx.Error(w, http.StatusBadRequest,
			fmt.Sprintf("count must be 1..%d", maxRotateCount))
		return
	}
	if req.DaysPerKey < 1 || req.DaysPerKey > maxRotateDaysPerKey {
		httpx.Error(w, http.StatusBadRequest,
			fmt.Sprintf("days_per_key must be 1..%d", maxRotateDaysPerKey))
		return
	}
	if !validAlgorithm(req.Algorithm) {
		httpx.Error(w, http.StatusBadRequest, "algorithm must be hmac-sha1-96 or aes-128-cmac")
		return
	}
	maxID, err := h.Q.MaxKeyIDInTcpAoKeyChain(r.Context(), chainID)
	if err != nil {
		writeMapped(w, err)
		return
	}
	nextKeyID := maxID + 1
	start := timeNow().UTC()
	if req.Start != nil {
		start = req.Start.UTC()
	}
	created, attempted, err := h.rotateInsert(r.Context(), chainID, req, start, nextKeyID)
	if err != nil {
		// `attempted` reports rows committed (DB invariant):
		//   - tx path: 0 (rollback wiped every insert)
		//   - autocommit fallback: len(created) at the moment of failure
		// Audit row mirrors so forensics can join target_id to the
		// keys table and verify the count.
		recordRotateBatchAudit(r.Context(), h.Audit, chainID, req, start, nextKeyID, attempted, false)
		writeMapped(w, err)
		return
	}
	recordRotateBatchAudit(r.Context(), h.Audit, chainID, req, start, nextKeyID, len(created), true)
	httpx.JSON(w, http.StatusCreated, created)
}

// rotateInsert runs the insert loop. When h.Pool is set the loop runs
// inside a single pgx.Tx — partial failure rolls back atomically. When
// nil (tests pre-PR #206), inserts autocommit one-by-one and partial
// failure leaves prior rows in the DB (the previous behavior). The
// returned `attempted` counter reflects loop iterations that called
// CreateTcpAoKey before any failure — handy in the autocommit fallback
// for the audit row's `actual` field.
func (h *Handler) rotateInsert(ctx context.Context, chainID uuid.UUID, req tcpAoRotateReq, start time.Time, nextKeyID int32) ([]dbq.TcpAoKey, int, error) {
	window := time.Duration(req.DaysPerKey) * 24 * time.Hour
	run := func(q rotateInsertQ) ([]dbq.TcpAoKey, int, error) {
		created := make([]dbq.TcpAoKey, 0, req.Count)
		for i := 0; i < req.Count; i++ {
			validFrom := start.Add(window * time.Duration(i))
			validTo := validFrom.Add(window)
			keyID := nextKeyID + int32(i)
			secret, err := randomSecretHex()
			if err != nil {
				return created, len(created), err
			}
			desc := fmt.Sprintf("Auto-generated rotation #%d/%d", i+1, req.Count)
			out, err := q.CreateTcpAoKey(ctx, dbq.CreateTcpAoKeyParams{
				KeyChainID: chainID, KeyID: keyID,
				SendID: keyID, RecvID: keyID,
				Algorithm: req.Algorithm, Secret: secret,
				ValidFrom: &validFrom, ValidTo: &validTo,
				Description: &desc,
			})
			if err != nil {
				// Return len(created), NOT i+1: the failing
				// CreateTcpAoKey did not produce a row. attempted ==
				// rows in DB (autocommit path) or 0 (tx path after
				// rollback) — the caller decides which to audit.
				return created, len(created), err
			}
			created = append(created, out)
		}
		return created, len(created), nil
	}
	if h.Pool == nil {
		// Test fallback: autocommit per insert via the existing fake.
		return run(h.Q)
	}
	tx, err := h.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, 0, err
	}
	// Rollback uses a fresh background context with a short timeout so
	// the cleanup still runs when the request context is cancelled
	// mid-loop (client disconnect). Without this the rollback fires
	// on a cancelled ctx and pgxpool has to discard the conn —
	// correct end-state but noisy on a hot instance.
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()
	// Re-read MaxKeyIDInTcpAoKeyChain inside the tx so two concurrent
	// rotate-batch calls can't compute the same nextKeyID. pgx routes
	// the inner Querier through the tx, and the row read sits behind
	// the chain row's lock-effects so PG MVCC serializes concurrent
	// rotations.
	if maxID, err := dbq.New(tx).MaxKeyIDInTcpAoKeyChain(ctx, chainID); err != nil {
		return nil, 0, err
	} else if maxID != nextKeyID-1 {
		// Recompute baseline: a concurrent rotation snuck in between
		// the pre-tx MaxKeyID read and BeginTx. Rebuild nextKeyID so
		// the inserts pick up where the OTHER batch left off.
		nextKeyID = maxID + 1
	}
	q := dbq.New(tx)
	created, _, runErr := run(q)
	if runErr != nil {
		// Tx rollback discards every row inserted in this iteration of
		// the loop; surface `attempted=0` to make that explicit in the
		// audit row.
		return nil, 0, runErr
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return created, len(created), nil
}

// rotateInsertQ is the slim sub-interface rotateInsert calls — lets
// the in-tx path run against a Querier built from the tx (dbq.New(tx))
// without exposing the full handler.Querier surface.
type rotateInsertQ interface {
	CreateTcpAoKey(ctx context.Context, arg dbq.CreateTcpAoKeyParams) (dbq.TcpAoKey, error)
}

// recordRotateBatchAudit is split out so the success + partial-failure
// paths share exactly one shape. `complete=false` distinguishes a
// partial-failure audit row from a clean rotation in forensics, and
// `actual` is the number of rows that survived before the bail.
func recordRotateBatchAudit(ctx context.Context, rec audit.Recorder, chainID uuid.UUID, req tcpAoRotateReq, start time.Time, firstKeyID int32, actual int, complete bool) {
	audit.Record(ctx, rec, nil, audit.Event{
		Action: "tcp_ao_key.rotate_batch", TargetType: "tcp_ao_key_chain",
		TargetID: chainID.String(),
		Diff: map[string]any{
			"count":        req.Count,
			"actual":       actual,
			"complete":     complete,
			"days_per_key": req.DaysPerKey,
			"start":        start.Format(time.RFC3339),
			"first_key_id": firstKeyID,
		},
	})
}
