// Per-deployment bootstrap-token helpers for the kubeconfig callback.
// Ports dcim.regiondeploy.tokens — same domain-separation tag, same
// HMAC-SHA256 construction, same hex-encoded 64-char output so the
// orchestrator and the verifier compute identical tokens on either
// side of the Python→Go cutover. The orchestrator embeds the token
// in Ignition at /etc/dcim/callback.token; the in-cluster Workflow
// action sends it back as `Authorization: Bearer …`.
package regiondeploy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
)

// callbackCtx is the domain-separation tag baked into the HMAC so the
// same callback secret can't be re-used to forge any other deployment
// artifact (Ignition payload, kubeconfig itself, etc). Must match
// Python's `_CTX` byte-for-byte.
var callbackCtx = []byte("dcim/regiondeploy/kubeconfig-callback/v1")

// deriveCallbackToken returns the bootstrap token for deploymentID, or
// "" when the server-side secret is unset (fail-closed path: callers
// MUST refuse to embed or accept any token in that state). The token
// is a hex-encoded HMAC-SHA256 digest (64 chars).
func deriveCallbackToken(deploymentID uuid.UUID, secret string) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(callbackCtx)
	mac.Write([]byte(":"))
	mac.Write([]byte(deploymentID.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

// compareCallbackToken constant-time compares a presented bearer token
// against the expected value. Both inputs are required non-empty —
// returning false on either-empty matches Python's secrets.compare_digest
// pre-check and prevents an attacker probing for the "no secret
// configured" state through timing.
func compareCallbackToken(presented, expected string) bool {
	if presented == "" || expected == "" {
		return false
	}
	return hmac.Equal([]byte(presented), []byte(expected))
}
