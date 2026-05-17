// /auth/refresh — mint a fresh session JWT using the user's stored
// IdP refresh_token. Mirrors packages/otter/src/dcim/api/auth.py
// refresh_session: decode bearer (no exp check), fetch user, decrypt
// the stored refresh_token, POST to issuer's token endpoint, verify
// the returned id_token, optionally persist the rotated refresh_token,
// mint and return a new session JWT.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// FernetCfg lets the handler decrypt/encrypt the stored refresh_token.
// Field on Handler so tests + main wire it once at startup.
//
// (Declared here rather than in handler.go to keep the refresh-only
// plumbing co-located with the endpoint that uses it.)
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		httpx.Error(w, http.StatusBadRequest, "OIDC not configured")
		return
	}
	raw, ok := bearerToken(r)
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing bearer")
		return
	}
	// Decode bearer without exp check; the whole point of /refresh is
	// to handle expired-or-about-to-expire sessions. Signature is still
	// verified — an attacker can't forge a refresh request.
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithoutClaimsValidation(),
	)
	claims := jwt.MapClaims{}
	if _, err := parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		return h.Mint.Secret, nil
	}); err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid bearer")
		return
	}
	sub, _ := claims["sub"].(string)
	userID, err := uuid.Parse(sub)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bearer sub is not a uuid")
		return
	}
	user, err := h.Q.GetUser(r.Context(), userID)
	if err != nil || !user.IsActive {
		httpx.Error(w, http.StatusUnauthorized, "user not found or inactive")
		return
	}
	if user.IdpRefreshToken == nil || *user.IdpRefreshToken == "" {
		httpx.Error(w, http.StatusUnauthorized, "no refresh token on file; sign in again")
		return
	}
	plainRefresh, err := decryptRefreshToken(*user.IdpRefreshToken, h.Fernet)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "refresh token unreadable")
		return
	}
	newTokens, err := h.OIDC.refreshAtIdP(r.Context(), plainRefresh)
	if err != nil {
		// IdP rejected — clear the stored token so we don't retry on
		// the next call. Operator must re-auth.
		_ = h.Q.UpdateUserRefreshToken(r.Context(), dbq.UpdateUserRefreshTokenParams{ID: userID, IdpRefreshToken: nil})
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	// Verify the freshly-issued id_token. Same verifier the callback
	// path uses; nonce check is skipped (refreshed id_tokens don't
	// carry the original session nonce).
	idTok, err := h.OIDC.verifier.Verify(r.Context(), newTokens.IDToken)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, fmt.Sprintf("refreshed id_token invalid: %v", err))
		return
	}
	var idClaims map[string]any
	if err := idTok.Claims(&idClaims); err != nil {
		httpx.Error(w, http.StatusUnauthorized, "id_token claims unreadable")
		return
	}
	idpRoles := extractIdpRoles(idClaims)
	mfa := mfaSatisfied(idClaims, h.OIDC.cfg.MFAAMRValues)

	// Keycloak rotates refresh_tokens by default — stash the new one.
	if newTokens.RefreshToken != "" {
		enc, err := encryptRefreshToken(newTokens.RefreshToken, h.Fernet)
		if err == nil {
			_ = h.Q.UpdateUserRefreshToken(r.Context(), dbq.UpdateUserRefreshTokenParams{
				ID: userID, IdpRefreshToken: &enc,
			})
		}
	}
	_ = h.Q.UpdateUserLastLogin(r.Context(), userID)
	tok, _, err := Mint(h.Mint, userID, idpRoles, mfa)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "jwt mint failed")
		return
	}
	uid := userID
	h.auditAuth(r.Context(), "auth.refresh", userID.String(), &uid, "user:"+userID.String(), true,
		map[string]any{"mfa": mfa})
	httpx.JSON(w, http.StatusOK, tokenOut{
		AccessToken: tok, ExpiresIn: h.Mint.TTLSecond, IDToken: newTokens.IDToken,
	})
}

// refreshAtIdP POSTs grant_type=refresh_token to the issuer's token
// endpoint. Returns the parsed JSON on 200; errors include the IdP's
// raw response body so operators can debug Keycloak side rejections.
type idpRefreshTokens struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (o *OIDC) refreshAtIdP(ctx context.Context, refreshToken string) (*idpRefreshTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", o.cfg.ClientID)
	form.Set("client_secret", o.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.oauth2.Endpoint.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("idp refresh request: %w", err)
	}
	defer resp.Body.Close()
	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("idp refresh rejected (%d): %s", resp.StatusCode, string(body))
	}
	var out idpRefreshTokens
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("idp refresh response malformed: %w", err)
	}
	if out.IDToken == "" {
		return nil, errors.New("idp refresh response missing id_token")
	}
	return &out, nil
}

// Silence imported-and-not-used: oidc is referenced via field access
// on *OIDC.verifier which is *oidc.IDTokenVerifier. Keeping an import
// reference here so go vet stays quiet across the file split.
var _ = oidc.ScopeOpenID
