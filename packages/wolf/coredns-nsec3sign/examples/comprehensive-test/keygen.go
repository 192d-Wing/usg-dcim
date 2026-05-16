//go:build ignore
// +build ignore

// keygen — generate a BIND-format ECDSAP256SHA256 ZSK pair for the
// quick-smoke example. Run with:
//
//   go run keygen.go example.test.
//
// Drops `K<zone>.+013+<tag>.key` and `K<zone>.+013+<tag>.private`
// into the current directory. Same format DCIM emits via
// `render_dnssec_key_files` and the same format the plugin reads
// via keys.go.

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/miekg/dns"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run keygen.go <zone>")
		os.Exit(2)
	}
	zone := dns.Fqdn(strings.ToLower(os.Args[1]))

	dk := &dns.DNSKEY{
		Hdr:       dns.RR_Header{Name: zone, Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 3600},
		Flags:     256, // ZSK; SEP=0
		Protocol:  3,
		Algorithm: dns.ECDSAP256SHA256,
	}
	priv, err := dk.Generate(256)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
	tag := dk.KeyTag()
	base := fmt.Sprintf("K%s+%03d+%05d", zone, dns.ECDSAP256SHA256, tag)

	if err := os.WriteFile(base+".key", []byte(dk.String()+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, ".key:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(base+".private", []byte(dk.PrivateKeyString(priv)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, ".private:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s.key and %s.private (keytag %d)\n", base, base, tag)
}
