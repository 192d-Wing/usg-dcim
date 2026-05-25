// PR 80 — DNSSEC key generation + enable-dnssec endpoint.
//
// Ports services.dns.generate_dnssec_keypair using Go stdlib crypto.
// All three algorithms (ECDSAP256, Ed25519, RSA) are in stdlib —
// no third-party deps needed.
//
// Output shape matches Python exactly:
//   - public_key_b64: base64 of raw public key bytes per RFC 4034
//     (ECDSAP256: x||y, Ed25519: raw 32 bytes, RSA: RFC 3110 wire)
//   - private_pem: PKCS8-PEM of the private key (plaintext;
//     Fernet-at-rest from Python is a known gap — operators
//     concerned about at-rest must use column-level encryption
//     or KMS)
//   - key_tag: RFC 4034 Appendix B keytag over the DNSKEY rdata
package dns

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// keyTagFromDnskey computes the RFC 4034 Appendix B key tag — sum
// of 16-bit words over the DNSKEY rdata, with high byte of word
// zero given an extra multiplier. Mirrors the Python implementation
// byte-for-byte so cross-backend tags match.
func keyTagFromDnskey(flags uint16, algorithm int, publicKey []byte) int32 {
	rdata := make([]byte, 0, 4+len(publicKey))
	rdata = append(rdata, byte(flags>>8), byte(flags&0xFF))
	rdata = append(rdata, 3) // protocol always 3 for DNSSEC
	rdata = append(rdata, byte(algorithm))
	rdata = append(rdata, publicKey...)
	var acc uint32
	for i, b := range rdata {
		if i%2 == 0 {
			acc += uint32(b) << 8
		} else {
			acc += uint32(b)
		}
	}
	acc += (acc >> 16) & 0xFFFF
	return int32(acc & 0xFFFF)
}

// generatedKey carries the artifacts from one keypair generation.
type generatedKey struct {
	Role         string // "ksk" or "zsk"
	Algorithm    string // enum value (ecdsap256sha256 / ed25519 / rsasha256)
	PrivatePem   string
	PublicKeyB64 string
	KeyTag       int32
}

// generateDnssecKeypair mints one DNSSEC key for `role` using
// `algorithm`. Matches services.dns.generate_dnssec_keypair output
// shape so existing Python-authored keys keep working alongside
// freshly-Go-authored keys.
func generateDnssecKeypair(role, algorithm string) (generatedKey, error) {
	algNum := dnssecAlgNumber(algorithm)
	if algNum == 0 {
		return generatedKey{}, fmt.Errorf("unsupported dnssec algorithm %q", algorithm)
	}
	var privDER []byte
	var pubBytes []byte
	switch algorithm {
	case "ecdsap256sha256":
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return generatedKey{}, err
		}
		// RFC 6605: ECDSAP256 public key is raw x || y, 64 bytes.
		xBytes := priv.PublicKey.X.FillBytes(make([]byte, 32))
		yBytes := priv.PublicKey.Y.FillBytes(make([]byte, 32))
		pubBytes = append(xBytes, yBytes...)
		privDER, err = x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return generatedKey{}, err
		}
	case "ed25519":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return generatedKey{}, err
		}
		pubBytes = pub
		privDER, err = x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return generatedKey{}, err
		}
	case "rsasha256":
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return generatedKey{}, err
		}
		// RFC 3110 wire format: 1-byte exponent length, exponent,
		// modulus.
		eBytes := bigIntBytes(priv.PublicKey.E)
		nBytes := priv.PublicKey.N.Bytes()
		pubBytes = make([]byte, 0, 1+len(eBytes)+len(nBytes))
		pubBytes = append(pubBytes, byte(len(eBytes)))
		pubBytes = append(pubBytes, eBytes...)
		pubBytes = append(pubBytes, nBytes...)
		privDER, err = x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return generatedKey{}, err
		}
	default:
		return generatedKey{}, fmt.Errorf("unsupported algorithm %q", algorithm)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubB64 := base64.StdEncoding.EncodeToString(pubBytes)
	tag := keyTagFromDnskey(keyFlags(role), algNum, pubBytes)
	return generatedKey{
		Role: role, Algorithm: algorithm,
		PrivatePem: string(privPEM), PublicKeyB64: pubB64, KeyTag: tag,
	}, nil
}

// bigIntBytes encodes an int as the minimum-length big-endian byte
// sequence for RFC 3110 exponent encoding. RSA exponents are
// typically 65537 (0x010001 → 3 bytes).
func bigIntBytes(n int) []byte {
	if n == 0 {
		return []byte{0}
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte(n & 0xFF)}, out...)
		n >>= 8
	}
	return out
}

// ---- enable-dnssec ----

func (h *Handler) enableDnssec(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	zone, err := h.Q.GetDnsZone(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "zone not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if !h.enforceFabric(w, r, zone.FabricID, "dns:keys:rotate") {
		return
	}
	if zone.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is frozen — unfreeze before enabling DNSSEC")
		return
	}
	// Idempotent: if any keys exist, just flip signed=true (if not
	// already) and return the existing roster.
	existing, err := h.Q.ListDnsKeysByZone(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if len(existing) > 0 {
		if !zone.Signed {
			if _, err := h.Q.SetDnsZoneSigned(r.Context(), id, true); err != nil {
				status, msg := httpx.Mapped(err)
				httpx.Error(w, status, msg)
				return
			}
		}
		httpx.JSON(w, http.StatusOK, existing)
		return
	}
	// Generate KSK + ZSK. Default algorithm is ECDSAP256 — short
	// keys, ubiquitous resolver support. Matches Python's default.
	defaultAlg := h.defaultDnssecAlgorithm()
	out := make([]dbq.DnsKeyRow, 0, 2)
	for _, role := range []string{"ksk", "zsk"} {
		material, err := generateDnssecKeypair(role, defaultAlg)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "key generation failed: "+err.Error())
			return
		}
		row, err := h.Q.CreateDnsKey(r.Context(), dbq.CreateDnsKeyParams{
			ZoneID: &id, Role: material.Role, Algorithm: material.Algorithm,
			PrivatePem: material.PrivatePem, PublicKeyB64: material.PublicKeyB64,
			KeyTag: material.KeyTag,
		})
		if err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
		out = append(out, row)
	}
	if _, err := h.Q.SetDnsZoneSigned(r.Context(), id, true); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "dns_zone.enable_dnssec",
		TargetType: "dns_zone",
		TargetID:   id.String(),
	})
	httpx.JSON(w, http.StatusOK, out)
}

// defaultDnssecAlgorithm — the Python reads this from settings
// (dns_dnssec_default_algorithm). For the Go port we hardcode
// ECDSAP256 (the Python default); operators wanting other algs
// will get a settings knob in a follow-up.
func (h *Handler) defaultDnssecAlgorithm() string {
	return "ecdsap256sha256"
}
