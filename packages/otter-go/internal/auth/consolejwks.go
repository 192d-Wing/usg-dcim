// Console JWKS cache: fetches the usg-ops-console gateway's
// /.well-known/jwks.json and serves P-256 verification keys by kid.
//
// Behaviour (docs/INTEGRATING-A-TOOL.md in usg-ops-console):
//   - Keys are considered fresh for Refresh (default 5 min).
//   - A stale cache or an unknown kid triggers a refetch, rate-limited to
//     one attempt per MinRefetch (default 60 s) so a flood of forged kids
//     cannot turn otter-go into a JWKS amplifier.
//   - The last good key set is served while refetches fail, but only up
//     to MaxAge (default 10 min) after the last successful fetch; past
//     that every lookup fails closed with ErrJWKSUnavailable (503).
//   - The fetch is bounded: 5 s timeout, 64 KiB body, no redirects,
//     at most 16 keys, EC P-256 with a kid only.
//
// COMPLIANCE MAPPING:
// - NIST 800-53: SC-12 (Cryptographic Key Establishment and Management) — kid-selected keys, rotation via refetch
// - NIST 800-53: SC-23 (Session Authenticity) — keys come only from the configured console JWKS endpoint
// - NIST 800-53: SI-10 (Information Input Validation) — bounded, strictly-typed JWKS parse
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	jwksMaxBytes = 64 << 10
	jwksMaxKeys  = 16
)

// ErrJWKSUnavailable means no JWKS fetch has succeeded within MaxAge.
// Callers map it to 503 (fail closed, never fall back).
var ErrJWKSUnavailable = errors.New("console jwks unavailable")

// errUnknownKID means the JWKS is healthy but has no such kid.
var errUnknownKID = errors.New("unknown kid")

// JWKSCache is safe for concurrent use.
type JWKSCache struct {
	URL        string
	Client     *http.Client
	Refresh    time.Duration
	MaxAge     time.Duration
	MinRefetch time.Duration
	Now        func() time.Time

	mu        sync.RWMutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time // last successful fetch

	fetchMu     sync.Mutex // single-flight for fetches
	lastAttempt time.Time
}

// NewJWKSCache validates the URL (no userinfo; https, or http only for
// a loopback host or when allowHTTP is set — env
// OTTER_CONSOLE_JWKS_ALLOW_HTTP=true, DEV ONLY) and returns a cache with
// the documented defaults.
//
// COMPLIANCE MAPPING:
// - NIST 800-53: SC-8 (Transmission Confidentiality and Integrity) — key set fetched over TLS outside loopback
func NewJWKSCache(rawURL string, allowHTTP bool) (*JWKSCache, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("console jwks url must be an absolute http(s) url without credentials")
	}
	if u.Scheme == "http" && !allowHTTP && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("console jwks url must use https (http only for loopback or OTTER_CONSOLE_JWKS_ALLOW_HTTP=true)")
	}
	return &JWKSCache{
		URL: rawURL,
		Client: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Refresh:    5 * time.Minute,
		MaxAge:     10 * time.Minute,
		MinRefetch: 60 * time.Second,
		Now:        time.Now,
	}, nil
}

// Key returns the verification key for kid.
func (c *JWKSCache) Key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	now := c.Now()
	if k, ok, fresh := c.lookup(kid, now); ok && fresh {
		return k, nil
	}
	c.refetch(ctx, kid, now)
	k, ok, _ := c.lookup(kid, now)
	c.mu.RLock()
	fetchedAt := c.fetchedAt
	c.mu.RUnlock()
	if fetchedAt.IsZero() || now.Sub(fetchedAt) > c.MaxAge {
		return nil, ErrJWKSUnavailable
	}
	if !ok {
		return nil, errUnknownKID
	}
	return k, nil
}

func (c *JWKSCache) lookup(kid string, now time.Time) (*ecdsa.PublicKey, bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	k, ok := c.keys[kid]
	fresh := !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < c.Refresh
	return k, ok, fresh
}

func (c *JWKSCache) refetch(ctx context.Context, kid string, now time.Time) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	// Another goroutine may have fetched while we waited.
	if _, ok, fresh := c.lookup(kid, now); ok && fresh {
		return
	}
	if !c.lastAttempt.IsZero() && now.Sub(c.lastAttempt) < c.MinRefetch {
		return
	}
	c.lastAttempt = now
	keys, err := c.fetch(ctx)
	if err != nil {
		return // keep last good keys; Key() enforces MaxAge
	}
	c.mu.Lock()
	c.keys = keys
	c.fetchedAt = now
	c.mu.Unlock()
}

type jwkJSON struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

func (c *JWKSCache) fetch(ctx context.Context) (map[string]*ecdsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > jwksMaxBytes {
		return nil, errors.New("jwks too large")
	}
	return parseJWKS(body)
}

func parseJWKS(body []byte) (map[string]*ecdsa.PublicKey, error) {
	var doc struct {
		Keys []jwkJSON `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if len(doc.Keys) > jwksMaxKeys {
		return nil, errors.New("jwks has too many keys")
	}
	out := map[string]*ecdsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" || len(k.Kid) > 128 {
			continue
		}
		if (k.Alg != "" && k.Alg != "ES256") || (k.Use != "" && k.Use != "sig") {
			continue
		}
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		point := append(append([]byte{0x04}, x...), y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
		if err != nil {
			continue // not on the curve
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, errors.New("jwks has no usable keys")
	}
	return out, nil
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
