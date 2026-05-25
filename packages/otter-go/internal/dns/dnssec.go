// PR 79 — DNSSEC reads: zone keys list + DS records.
//
//   GET /dns/zones/{id}/keys        — list every DNSKEY row for the zone
//   GET /dns/zones/{id}/ds-records  — compute DS digest for active KSKs
//
// Key generation, rotation, and the enable/disable surface stay
// Python: those need the `cryptography` crate to mint ECDSA / Ed25519
// / RSA keypairs and the state-machine wrangling around retired_at
// timestamps. The reads here are pure crypto (SHA-256 of a known
// byte layout) and pure SQL — safe to mirror in Go without
// duplicating the keygen.
package dns

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// dnssecAlgNumber: enum-name → IANA DNS Security Algorithm Number.
// Mirrors services.dns._DNSSEC_ALG_NUMBER. Unknown algorithm → 0,
// caught by the handler as "skip this key" (matches a future enum
// row that hasn't been wired here yet).
func dnssecAlgNumber(alg string) int {
	switch alg {
	case "rsasha256":
		return 8
	case "ecdsap256sha256":
		return 13
	case "ed25519":
		return 15
	}
	return 0
}

// keyFlags returns the DNSKEY flags field: ZSK = 256, KSK = 257
// (KSK adds the Secure Entry Point bit). RFC 4034 §2.1.1.
func keyFlags(role string) uint16 {
	if role == "ksk" {
		return 257
	}
	return 256
}

// dsRecord is the wire shape for one DS row (key_tag + digest +
// BIND presentation form). Matches Python's render_ds_records out.
type dsRecord struct {
	KeyTag     int32  `json:"key_tag"`
	Algorithm  int    `json:"algorithm"`
	DigestType int    `json:"digest_type"`
	Digest     string `json:"digest"`
	RR         string `json:"rr"`
}

// dnsWireName encodes a FQDN as a sequence of length-prefixed
// lowercase labels terminated by a zero byte. RFC 1035 §3.1.
func dnsWireName(fqdn string) []byte {
	var b []byte
	// Strip any trailing dot and split into labels.
	trimmed := strings.TrimSuffix(fqdn, ".")
	if trimmed == "" {
		return []byte{0}
	}
	for _, label := range strings.Split(trimmed, ".") {
		lower := strings.ToLower(label)
		b = append(b, byte(len(lower)))
		b = append(b, []byte(lower)...)
	}
	b = append(b, 0)
	return b
}

// computeDSRecord builds the DS row for one DNSKEY (KSK + active).
// digest_type=2 (SHA-256). Returns the row + nil on success, or
// "skip" via empty key_tag when the algorithm or public key is
// malformed — surfaces as an empty list entry the caller filters.
func computeDSRecord(zoneName string, key dbq.DnsKeyRow) (dsRecord, error) {
	alg := dnssecAlgNumber(key.Algorithm)
	if alg == 0 {
		return dsRecord{}, fmt.Errorf("unsupported algorithm %q", key.Algorithm)
	}
	pubKey, err := base64.StdEncoding.DecodeString(key.PublicKeyB64)
	if err != nil {
		return dsRecord{}, fmt.Errorf("invalid public_key_b64: %w", err)
	}
	// DNSKEY rdata: flags (2 bytes, big-endian) || protocol=3 ||
	// algorithm || public key bytes. RFC 4034 §2.1.
	flags := keyFlags(key.Role)
	rdata := make([]byte, 0, 4+len(pubKey))
	rdata = append(rdata, byte(flags>>8), byte(flags&0xFF))
	rdata = append(rdata, 3) // protocol always 3 for DNSSEC
	rdata = append(rdata, byte(alg))
	rdata = append(rdata, pubKey...)

	// DS digest input: canonical owner name (wire format) ||
	// DNSKEY rdata. SHA-256 (digest_type=2).
	fqdn := strings.TrimSuffix(zoneName, ".") + "."
	nameWire := dnsWireName(fqdn)
	h := sha256.Sum256(append(nameWire, rdata...))
	digest := strings.ToUpper(hex.EncodeToString(h[:]))
	return dsRecord{
		KeyTag:     key.KeyTag,
		Algorithm:  alg,
		DigestType: 2,
		Digest:     digest,
		RR:         fmt.Sprintf("%s IN DS %d %d 2 %s", fqdn, key.KeyTag, alg, digest),
	}, nil
}

// ---- list keys ----

func (h *Handler) listZoneKeys(w http.ResponseWriter, r *http.Request) {
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
	// PR 79 capability matches Python (_CAP_KEYS_READ — granular
	// admin:dns-keys:read in the catalog). Use the same code so
	// the cross-backend ABAC behaves identically.
	if !h.enforceFabric(w, r, zone.FabricID, "dns:keys:read") {
		return
	}
	keys, err := h.Q.ListDnsKeysByZone(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, keys)
}

// ---- list DS records ----

func (h *Handler) listZoneDsRecords(w http.ResponseWriter, r *http.Request) {
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
	if !h.enforceFabric(w, r, zone.FabricID, "dns:keys:read") {
		return
	}
	keys, err := h.Q.ListDnsKeysByZone(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	out := []dsRecord{}
	for _, k := range keys {
		// Only active KSKs contribute DS rows. ZSK and retired
		// KSKs are skipped: the parent zone shouldn't be asked
		// to publish a DS for a key we won't sign with.
		if k.Role != "ksk" || k.RetiredAt != nil {
			continue
		}
		ds, err := computeDSRecord(zone.Name, k)
		if err != nil {
			// Skip malformed rows rather than failing the whole
			// response. A bad key in the table is an operator
			// problem to fix; the working keys should still
			// surface.
			continue
		}
		out = append(out, ds)
	}
	httpx.JSON(w, http.StatusOK, out)
}
