// Capability wildcard matching, mirroring the Python implementation in
// packages/otter/src/dcim/security/deps.py::find_matching_capability.
//
// A held capability `pattern` grants `code` when, after splitting both
// on ":", the segment counts match and every segment in pattern is
// equal to or "*" the matching segment in code. The bare single-segment
// "*" short-circuits any check.
package auth

import "strings"

// HasCapability reports whether any capability in held grants `code`.
// O(n) over held; held is normally a few dozen codes max, so this is
// cheap enough to run per request without indexing.
func HasCapability(held []string, code string) bool {
	for _, c := range held {
		if c == code || c == "*" {
			return true
		}
	}
	target := strings.Split(code, ":")
	for _, pattern := range held {
		if pattern == "*" {
			return true
		}
		parts := strings.Split(pattern, ":")
		if len(parts) != len(target) {
			continue
		}
		match := true
		for i, p := range parts {
			if p != "*" && p != target[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
