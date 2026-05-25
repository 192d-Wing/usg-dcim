// CIDR carver (PR 66). Ports services.ipam.find_free_prefixes_in_supernet:
// walk every sub-prefix of a parent at the target size, skip those
// that overlap any existing allocation, return the first `limit`.
//
// Iteration is capped at 2^20 (~1M) candidates to prevent runaway
// scans on pathological inputs (e.g. /16 v6 parent → /128 target
// would yield 2^112 candidates). At /24-/64 scale the cap is never
// reached.
package ipam

import (
	"math/big"
	"net/netip"
)

const carveScanCap = 1 << 20

// prefixesOverlap returns true iff the two CIDR ranges share any
// address. Containment-based check works because two prefixes share
// any address iff one contains the other's network address.
func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

// findFreePrefixesInSupernet walks the parent's sub-prefixes at
// `size` and returns up to `limit` CIDR strings that don't overlap
// any prefix in `allocated`. Mirrors Python's iteration semantics:
// candidates emitted in network-address order, lazy stop on hitting
// limit.
//
// Returns nil if size is malformed (smaller-or-equal to parent or
// past family max). An empty (but non-nil) slice means "every slot
// is taken" — caller distinguishes via len.
func findFreePrefixesInSupernet(
	parent netip.Prefix, size int, allocated []netip.Prefix, limit int,
) []string {
	if size <= parent.Bits() {
		return nil
	}
	totalBits := parent.Addr().BitLen()
	if size > totalBits {
		return nil
	}
	// Pre-filter allocated to ones in the same family — cross-family
	// "overlap" checks always return false and waste cycles.
	parentIs4 := parent.Addr().Is4()
	occupied := make([]netip.Prefix, 0, len(allocated))
	for _, a := range allocated {
		if a.Addr().Is4() == parentIs4 {
			occupied = append(occupied, a)
		}
	}

	addrBytes := parent.Addr().AsSlice()
	base := new(big.Int).SetBytes(addrBytes)
	step := new(big.Int).Lsh(big.NewInt(1), uint(totalBits-size))
	// Total candidates = 2^(size - parent.Bits()); cap defensively.
	totalShift := uint(size - parent.Bits())
	maxCount := int64(carveScanCap)
	if totalShift < 63 {
		if c := int64(1) << totalShift; c < maxCount {
			maxCount = c
		}
	}

	out := make([]string, 0, limit)
	one := big.NewInt(1)
	maxCountInt := big.NewInt(maxCount)
	idx := new(big.Int)
	cand := new(big.Int)
	byteLen := len(addrBytes)
	for ; idx.Cmp(maxCountInt) < 0; idx.Add(idx, one) {
		cand.Mul(idx, step)
		cand.Add(cand, base)
		b := cand.Bytes()
		if len(b) > byteLen {
			break
		}
		if len(b) < byteLen {
			padded := make([]byte, byteLen)
			copy(padded[byteLen-len(b):], b)
			b = padded
		}
		addr, ok := netip.AddrFromSlice(b)
		if !ok {
			break
		}
		p := netip.PrefixFrom(addr, size)
		overlapsAny := false
		for _, o := range occupied {
			if prefixesOverlap(p, o) {
				overlapsAny = true
				break
			}
		}
		if !overlapsAny {
			out = append(out, p.String())
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}
