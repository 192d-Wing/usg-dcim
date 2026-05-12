// Key file loading.
//
// Reads BIND-format DNSSEC key pairs from `<basename>.key` +
// `<basename>.private` on disk. The format matches what DCIM's
// `render_dnssec_key_files` writes into the dns-state volume, which
// in turn matches `dnssec-keygen` output (RFC 5074 §5.1.1).
//
// We delegate parsing to miekg/dns: `dns.ReadRR` handles the
// presentation-format DNSKEY in the .key file, and
// `DNSKEY.ReadPrivateKey` handles the BIND-private format including
// ECDSA, Ed25519, and RSA. This is the same code path the upstream
// CoreDNS `dnssec` plugin takes — deliberate, so a zone signed today
// by `dnssec` can be flipped to `nsec3sign` with no key-material
// changes on disk.

package nsec3sign

import (
	"crypto"
	"fmt"
	"os"

	"github.com/miekg/dns"
)

// signingKey pairs a DNSKEY public RR with its private half. The
// signer (step 4) consumes these to produce RRSIG records.
type signingKey struct {
	// KeyTag is the DNSSEC keytag — RFC 4034 §App.B — computed once
	// at load and cached. miekg/dns recomputes from rdata on every
	// .KeyTag() call, and every RRSIG header carries the tag, so
	// caching avoids re-hashing the DNSKEY rdata per signature.
	KeyTag uint16

	// IsKSK is true when the DNSKEY's Flags field has the SEP bit
	// set (RFC 4034 §2.1.1 — flags 257 = ZONE|SEP for a KSK, 256 =
	// ZONE alone for a ZSK). The signer uses this to decide which
	// key signs which RRset: DNSKEY RRsets get signed by both KSK
	// and ZSK; everything else by ZSK only.
	IsKSK bool

	// Public is the DNSKEY resource record as it appears at the
	// zone apex. The signer attaches a copy on DNSKEY queries and
	// reads the algorithm/owner for RRSIG construction.
	Public *dns.DNSKEY

	// Private is the crypto.Signer (in practice *ecdsa.PrivateKey,
	// ed25519.PrivateKey, or *rsa.PrivateKey) we pass to
	// `dns.RRSIG.Sign`.
	Private crypto.PrivateKey
}

// loadKeys walks Nsec3Sign.KeyFiles, opens each pair, and populates
// Nsec3Sign.Keys. Called from setup() after parse() so file I/O
// errors surface at startup instead of first query.
//
// An empty KeyFiles slice is permitted while the plugin is still in
// noop mode (steps 1–3). Once ServeDNS actually signs, a zero-key
// configuration will be promoted to a hard error in setup().
func (n *Nsec3Sign) loadKeys() error {
	if len(n.KeyFiles) == 0 {
		return nil
	}
	out := make([]*signingKey, 0, len(n.KeyFiles))
	for _, base := range n.KeyFiles {
		k, err := loadKey(base)
		if err != nil {
			return fmt.Errorf("nsec3sign: loading %s: %w", base, err)
		}
		out = append(out, k)
	}
	n.Keys = out
	return nil
}

// loadKey opens `<basename>.key` + `<basename>.private` and parses
// both halves. The basename is treated as a path — relative paths
// resolve against CoreDNS's working directory, mirroring the
// behaviour of the upstream `dnssec` plugin's `key file` directive.
func loadKey(basename string) (*signingKey, error) {
	pubPath := basename + ".key"
	privPath := basename + ".private"

	dk, err := readDNSKEY(pubPath)
	if err != nil {
		return nil, err
	}

	priv, err := readPrivateKey(dk, privPath)
	if err != nil {
		return nil, err
	}

	return &signingKey{
		KeyTag:  dk.KeyTag(),
		IsKSK:   dk.Flags&dns.SEP != 0,
		Public:  dk,
		Private: priv,
	}, nil
}

// readDNSKEY parses the public half. Anything other than exactly one
// DNSKEY RR in the file is treated as an error — the BIND `.key`
// format is one-RR-per-file by definition and we don't want to
// silently pick the first if the operator concatenated something.
func readDNSKEY(path string) (*dns.DNSKEY, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	rr, err := dns.ReadRR(f, path)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	dk, ok := rr.(*dns.DNSKEY)
	if !ok {
		return nil, fmt.Errorf("%s: expected DNSKEY, got %s", path, dns.TypeToString[rr.Header().Rrtype])
	}
	return dk, nil
}

// readPrivateKey parses the BIND `.private` file. miekg/dns reads
// the `Algorithm:` line out of the file and validates it matches the
// public DNSKEY's algorithm, so a mismatched pair fails here.
func readPrivateKey(dk *dns.DNSKEY, path string) (crypto.PrivateKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	priv, err := dk.ReadPrivateKey(f, path)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return priv, nil
}

// ZSKs returns the loaded keys without the SEP bit. The signer uses
// these for every RRset except DNSKEY. Returns a fresh slice so
// callers can sort/shuffle without aliasing internal storage.
func (n *Nsec3Sign) ZSKs() []*signingKey {
	out := make([]*signingKey, 0, len(n.Keys))
	for _, k := range n.Keys {
		if !k.IsKSK {
			out = append(out, k)
		}
	}
	return out
}

// KSKs returns the loaded keys with the SEP bit set. Used to sign
// the DNSKEY RRset specifically — that RR's signature is what a
// validator chains back to the parent zone's DS record.
func (n *Nsec3Sign) KSKs() []*signingKey {
	out := make([]*signingKey, 0, len(n.Keys))
	for _, k := range n.Keys {
		if k.IsKSK {
			out = append(out, k)
		}
	}
	return out
}
