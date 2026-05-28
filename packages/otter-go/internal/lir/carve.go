// CIDR carver — picks the first free sub-prefix of a target size
// inside a parent prefix that doesn't overlap any already-occupied
// range. Ported as-is from packages/otter-go/internal/ipam/carve.go
// because the algorithm is identical; living in the lir package keeps
// the dependency direction lir → (auth, audit, httpx) without a new
// lir → ipam edge.
//
// Scope iteration is capped at 2^20 candidates to prevent runaway
// scans on pathological inputs (e.g. carving a /128 inside a /16 v6
// parent). At realistic /16..../48 → /24..../64 scale the cap is
// never reached.
package lir

import (
	"math/big"
	"net/netip"
)

const carveScanCap = 1 << 20

func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

// findFirstFreePrefix returns the first sub-prefix of `parent` at the
// requested `size` that doesn't overlap any prefix in `allocated`.
// Returns ("", false) when no free slot exists (or when size is
// malformed: smaller-or-equal to parent, past family max).
//
// Allocation order is network-address ascending (first-fit lowest).
// The carver pre-filters allocated entries to the parent's family so
// cross-family overlap checks don't waste cycles.
func findFirstFreePrefix(parent netip.Prefix, size int, allocated []netip.Prefix) (string, bool) {
	if size <= parent.Bits() {
		return "", false
	}
	totalBits := parent.Addr().BitLen()
	if size > totalBits {
		return "", false
	}
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
	totalShift := uint(size - parent.Bits())
	maxCount := int64(carveScanCap)
	if totalShift < 63 {
		if c := int64(1) << totalShift; c < maxCount {
			maxCount = c
		}
	}

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
			return p.String(), true
		}
	}
	return "", false
}
