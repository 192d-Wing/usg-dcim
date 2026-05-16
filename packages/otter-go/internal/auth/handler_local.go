package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// ---- POST /auth/login (local break-glass) -----------------------------

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// login implements the bcrypt break-glass path. Rate limiting + audit
// logging are intentionally omitted from this PR — they live in the
// security/rate_limit + audit modules which are themselves not yet
// ported. PR 38+ adds them back; for now operators relying on local
// login should keep DCIM_LOCAL_LOGIN_DISABLED=true in shared envs.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	user, err := h.Q.GetUserByEmail(r.Context(), req.Email)
	if err != nil || !user.IsActive || user.PasswordHash == nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(req.Password)); err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	_ = h.Q.UpdateUserLastLogin(r.Context(), user.ID)
	tok, _, err := Mint(h.Mint, user.ID, nil, false)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "jwt mint failed")
		return
	}
	httpx.JSON(w, http.StatusOK, tokenOut{AccessToken: tok, ExpiresIn: h.Mint.TTLSecond})
}

// ---- POST /auth/logout (jti revocation) -------------------------------

// logout is idempotent: a missing/malformed/expired bearer still
// returns 204. We decode without verifying exp so a near-expiry logout
// still revokes; we *do* verify the signature so an attacker can't
// poison revoked_jtis with fabricated jtis.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	raw, ok := bearerToken(r)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Parse without exp/nbf checks; verify signature only.
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithoutClaimsValidation(),
	)
	claims := jwt.MapClaims{}
	if _, err := parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		return h.Mint.Secret, nil
	}); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	jti, _ := claims["jti"].(string)
	if jti == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	expiresAt := time.Now()
	if v, ok := claims["exp"].(float64); ok {
		expiresAt = time.Unix(int64(v), 0)
	}
	var userPtr *uuid.UUID
	if sub, _ := claims["sub"].(string); sub != "" {
		if u, err := uuid.Parse(sub); err == nil {
			userPtr = &u
		}
	}
	reason := "user_logout"
	_ = h.Q.InsertRevokedJti(r.Context(), dbq.InsertRevokedJtiParams{
		Jti: jti, UserID: userPtr, Reason: &reason, ExpiresAt: expiresAt,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---- /auth/tokens CRUD ------------------------------------------------

type apiTokenOut struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	PermissionCodes []string   `json:"permission_codes"`
	ScopeJson       any        `json:"scope_json"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	Revoked         bool       `json:"revoked"`
	Plaintext       string     `json:"plaintext,omitempty"`
}

func marshalApiToken(t dbq.ApiToken) apiTokenOut {
	codes, _ := decodeStringArray(t.PermissionCodes)
	var scope any
	if len(t.ScopeJson) > 0 {
		_ = json.Unmarshal(t.ScopeJson, &scope)
	}
	if scope == nil {
		scope = map[string]any{}
	}
	return apiTokenOut{
		ID: t.ID.String(), Name: t.Name, PermissionCodes: codes, ScopeJson: scope,
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt,
		Revoked: t.Revoked,
	}
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	p, ok := From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "no principal")
		return
	}
	if !HasCapability(p.Capabilities, "admin:api-tokens:read") {
		httpx.Error(w, http.StatusForbidden, "admin:api-tokens:read required")
		return
	}
	rows, err := h.Q.ListApiTokensByOwner(r.Context(), p.Subject)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]apiTokenOut, 0, len(rows))
	for _, t := range rows {
		out = append(out, marshalApiToken(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type tokenIssueReq struct {
	Name            string     `json:"name"`
	PermissionCodes []string   `json:"permission_codes"`
	ScopeJson       any        `json:"scope_json"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

func (h *Handler) issueToken(w http.ResponseWriter, r *http.Request) {
	p, ok := From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "no principal")
		return
	}
	if !HasCapability(p.Capabilities, "admin:api-tokens:create") {
		httpx.Error(w, http.StatusForbidden, "admin:api-tokens:create required")
		return
	}
	var req tokenIssueReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if req.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name required")
		return
	}
	// No-escalation: every requested code must be granted by an
	// existing capability the issuer holds (exact or wildcard match).
	var extra []string
	for _, code := range req.PermissionCodes {
		if !HasCapability(p.Capabilities, code) {
			extra = append(extra, code)
		}
	}
	if len(extra) > 0 {
		httpx.Error(w, http.StatusForbidden, "cannot grant capabilities you don't hold")
		return
	}
	permJSON, _ := json.Marshal(req.PermissionCodes)
	scopeJSON := []byte("{}")
	if req.ScopeJson != nil {
		scopeJSON, _ = json.Marshal(req.ScopeJson)
	}
	raw, digest, err := generateAPIToken()
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	tok, err := h.Q.CreateApiToken(r.Context(), dbq.CreateApiTokenParams{
		Name:            req.Name,
		OwnerUserID:     p.Subject,
		TokenHash:       digest,
		PermissionCodes: permJSON,
		ScopeJson:       scopeJSON,
		ExpiresAt:       req.ExpiresAt,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "token persist failed")
		return
	}
	out := marshalApiToken(tok)
	out.Plaintext = raw // only returned on first creation
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	p, ok := From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "no principal")
		return
	}
	if !HasCapability(p.Capabilities, "admin:api-tokens:delete") {
		httpx.Error(w, http.StatusForbidden, "admin:api-tokens:delete required")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	if _, err := h.Q.GetApiToken(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "token not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if err := h.Q.RevokeApiToken(r.Context(), id); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "revoke failed")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"revoked": true})
}
