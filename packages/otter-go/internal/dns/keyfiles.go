// BIND DNSSEC key file rendering + RFC 7344 CDNSKEY/CDS rendering
// (PR 26 — DNS bundle 3/N). Pure helpers: hand in zone + DnsKey
// rows, get back the BIND-format `.key` / `.private` text and the
// CDNSKEY/CDS apex records.
//
// Byte-equivalent with services/dns.py L508-L571 (render_dnssec_
// key_files, render_cdnskey_cds_lines) plus their helpers
// (_bind_key_basename, _bind_public_key_file, _bind_private_key_file,
// _ecdsa_private_scalar_b64, _ed25519_private_raw_b64).
//
// RSA support deferred: in-prod usage is rare and the BIND `.private`
// CRT-field rendering requires multi-prime extraction Python gets
// for free via cryptography.hazmat. For now, RSA returns an error
// at render time; the bundle assembler can skip the key with a
// log line and the operator gets a clear failure mode.
package dns

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// BindKeyBasename — `K<fqdn>+<alg:03d>+<tag:05d>` per BIND convention.
// CoreDNS reads `<basename>.key` and `<basename>.private` from the
// keys directory.
func BindKeyBasename(zoneName string, algNumber, keyTag int) string {
	fqdn := strings.TrimRight(zoneName, ".") + "."
	return fmt.Sprintf("K%s+%03d+%05d", fqdn, algNumber, keyTag)
}

// RenderBindPublicKeyFile — text of the `.key` file. One DNSKEY RR
// in BIND presentation form with a leading comment that mirrors
// Python's render_dnssec_key_files output.
func RenderBindPublicKeyFile(zoneName, role string, alg int, keyTag int32, publicKeyB64 string) string {
	fqdn := strings.TrimRight(zoneName, ".") + "."
	flags := keyFlags(role)
	return fmt.Sprintf(
		"; This is a %s-type key, keyid %d, for %s\n"+
			"%s IN DNSKEY %d 3 %d %s\n",
		strings.ToUpper(role), keyTag, fqdn,
		fqdn, flags, alg, publicKeyB64,
	)
}

// RenderBindPrivateKeyFile — text of the `.private` file. Decrypted
// PEM goes in; BIND-format presentation comes out. ECDSAP256SHA256
// and Ed25519 are supported; RSASHA256 returns an error (RSA support
// is deferred until an operator actually needs it).
//
// The `privatePem` must be the decrypted PKCS8 PEM — callers handle
// the at-rest Fernet unwrap before passing in.
func RenderBindPrivateKeyFile(algorithm, privatePem string) (string, error) {
	algNumber := dnssecAlgNumber(algorithm)
	if algNumber == 0 {
		return "", fmt.Errorf("unsupported dnssec algorithm %q", algorithm)
	}
	algLabel, err := bindPrivateAlgLabel(algorithm)
	if err != nil {
		return "", err
	}
	header := "Private-key-format: v1.3\n" +
		fmt.Sprintf("Algorithm: %d (%s)\n", algNumber, algLabel)

	block, _ := pem.Decode([]byte(privatePem))
	if block == nil {
		return "", fmt.Errorf("private_pem: no PEM block found")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("private_pem: parse PKCS8: %w", err)
	}

	switch algorithm {
	case "ecdsap256sha256":
		ec, ok := priv.(*ecdsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("private_pem: expected ECDSA P-256, got %T", priv)
		}
		// BIND wants the raw 32-byte big-endian scalar, base64-
		// encoded with NO PEM framing.
		scalar := ec.D.Bytes()
		padded := make([]byte, 32)
		copy(padded[32-len(scalar):], scalar)
		return header + fmt.Sprintf("PrivateKey: %s\n",
			base64.StdEncoding.EncodeToString(padded)), nil
	case "ed25519":
		ed, ok := priv.(ed25519.PrivateKey)
		if !ok {
			return "", fmt.Errorf("private_pem: expected Ed25519, got %T", priv)
		}
		// ed25519.PrivateKey is seed||public (64 bytes total).
		// BIND wants just the 32-byte seed.
		if len(ed) != ed25519.PrivateKeySize {
			return "", fmt.Errorf("private_pem: Ed25519 wrong size %d", len(ed))
		}
		seed := ed.Seed()
		return header + fmt.Sprintf("PrivateKey: %s\n",
			base64.StdEncoding.EncodeToString(seed)), nil
	case "rsasha256":
		return "", fmt.Errorf("RSASHA256 private-key rendering deferred — see PR 26 comment")
	}
	return "", fmt.Errorf("unsupported algorithm %q reached default branch", algorithm)
}

func bindPrivateAlgLabel(algorithm string) (string, error) {
	switch algorithm {
	case "ecdsap256sha256":
		return "ECDSAP256SHA256", nil
	case "ed25519":
		return "ED25519", nil
	case "rsasha256":
		return "RSASHA256", nil
	}
	return "", fmt.Errorf("unsupported dnssec algorithm %q", algorithm)
}

// KeyFileEntry is one (filename, content) pair from
// RenderDnssecKeyFiles. Returned as a slice so callers see a stable
// emit order (sorted by filename) for etag stability.
type KeyFileEntry struct {
	Filename string
	Content  string
}

// RenderDnssecKeyFiles — every active key on the zone, two files
// each (.key + .private). Caller passes the decrypted PEM in
// `decryptedPrivatePems` keyed by KeyTag, so this function stays
// pure (no Fernet/settings touch).
//
// Returns a stable slice (sorted by filename) so the bundle etag
// doesn't flap on map-iteration randomness.
func RenderDnssecKeyFiles(
	zoneName string,
	keys []dbq.DnsKeyRow,
	decryptedPrivatePems map[int32]string,
) ([]KeyFileEntry, error) {
	out := make([]KeyFileEntry, 0, 2*len(keys))
	for _, k := range keys {
		alg := dnssecAlgNumber(k.Algorithm)
		if alg == 0 {
			return nil, fmt.Errorf("key %d: unsupported algorithm %q", k.KeyTag, k.Algorithm)
		}
		base := BindKeyBasename(zoneName, alg, int(k.KeyTag))
		out = append(out, KeyFileEntry{
			Filename: base + ".key",
			Content:  RenderBindPublicKeyFile(zoneName, k.Role, alg, k.KeyTag, k.PublicKeyB64),
		})
		pem := decryptedPrivatePems[k.KeyTag]
		priv, err := RenderBindPrivateKeyFile(k.Algorithm, pem)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", k.KeyTag, err)
		}
		out = append(out, KeyFileEntry{
			Filename: base + ".private",
			Content:  priv,
		})
	}
	sortKeyFileEntries(out)
	return out, nil
}

func sortKeyFileEntries(out []KeyFileEntry) {
	// Insertion sort — N is tiny (2 entries per key, ≤4 keys per zone).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Filename > out[j].Filename; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
}
