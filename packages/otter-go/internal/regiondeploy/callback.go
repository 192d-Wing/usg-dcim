// kubeconfigCallback handles POST /api/v1/region-deployments/{id}/kubeconfig/callback.
// Ports the Python endpoint at regiondeploy.py L247-398 — same status
// gate, same fail-closed-on-unset-secret semantics, same audit/event
// shape. Two intentional gaps vs Python that follow-up PRs close:
//
//   - No Redis pubsub publish. The SSE port hooks pubsub in; the
//     persisted event row alone is enough for GET /events history.
//   - Secret labels match Python's (`dcim.region-deployment` +
//     `app.kubernetes.io/component`).
package regiondeploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// callbackReq mirrors Python's RegionDeploymentKubeconfigCallback.
// Exactly one of kubeconfig / kubeconfig_b64 must be present; the
// validator decodes b64 in-place so the handler downstream only ever
// sees the YAML form.
type callbackReq struct {
	NodeID        uuid.UUID `json:"node_id"`
	Kubeconfig    string    `json:"kubeconfig"`
	KubeconfigB64 string    `json:"kubeconfig_b64"`
}

func (req *callbackReq) normalize() (string, bool) {
	if req.NodeID == uuid.Nil {
		return "node_id is required", false
	}
	bothSet := req.Kubeconfig != "" && req.KubeconfigB64 != ""
	neitherSet := req.Kubeconfig == "" && req.KubeconfigB64 == ""
	if bothSet {
		return "supply either kubeconfig or kubeconfig_b64, not both", false
	}
	if neitherSet {
		return "kubeconfig or kubeconfig_b64 is required", false
	}
	if req.KubeconfigB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(req.KubeconfigB64)
		if err != nil {
			return "kubeconfig_b64 is not valid base64: " + err.Error(), false
		}
		req.Kubeconfig = string(decoded)
		req.KubeconfigB64 = ""
	}
	return "", true
}

// callbackErrEnvelope matches Python's {"error":{"code":..., "message":...}}
// shape on the two callback-specific failure modes (503 + 401). Other
// otter-go endpoints use httpx.Error's `{"detail":...}` shape; we keep
// the richer envelope here because the in-cluster Workflow action's
// operator logs already grep for `code` to distinguish a server-side
// misconfig (503) from a bad token (401).
type callbackErrEnvelope struct {
	Error callbackErrBody `json:"error"`
}

type callbackErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeCallbackErr(w http.ResponseWriter, status int, code, msg string) {
	httpx.JSON(w, status, callbackErrEnvelope{Error: callbackErrBody{Code: code, Message: msg}})
}

func (h *Handler) kubeconfigCallback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, badIDMsg)
		return
	}
	// Fail-closed when the server-side HMAC secret is unset: 503 +
	// callback_secret_unset code so the operator can grep prod logs.
	expected := deriveCallbackToken(id, h.CallbackSecret)
	if expected == "" {
		writeCallbackErr(w, http.StatusServiceUnavailable,
			"callback_secret_unset",
			"regiondeploy_callback_secret is not configured")
		return
	}
	if !compareCallbackToken(extractBearer(r.Header.Get("Authorization")), expected) {
		writeCallbackErr(w, http.StatusUnauthorized,
			"invalid_callback_token",
			"missing or invalid kubeconfig-callback token")
		return
	}
	var req callbackReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if msg, ok := req.normalize(); !ok {
		httpx.Error(w, http.StatusUnprocessableEntity, msg)
		return
	}
	secretRef := "tinkerbell/kubeconfig-" + id.String()
	res, err := h.Q.SetRegionDeploymentKubeconfigSecretRef(r.Context(),
		dbq.SetRegionDeploymentKubeconfigSecretRefParams{ID: id, KubeconfigSecretRef: &secretRef})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, notFound)
			return
		}
		writeMapped(w, err)
		return
	}
	if res.Updated == 0 {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"deployment is "+res.PriorStatus+
				"; kubeconfig callback only accepted during provisioning/joining")
		return
	}
	// Best-effort K8s Secret write. Failure here doesn't undo the
	// secret_ref update (Python takes the same stance) — the row
	// already names the namespace/name the orchestrator will later
	// reconcile; the event row carries the error string operators can
	// grep for. Returns 202 either way.
	writeErr := h.writeKubeconfigSecret(r.Context(), id, req.Kubeconfig)
	h.recordCallbackEvent(r.Context(), id, secretRef, req.NodeID, len(req.Kubeconfig), writeErr)
	httpx.JSON(w, http.StatusAccepted, struct{}{})
}

func (h *Handler) writeKubeconfigSecret(ctx context.Context, id uuid.UUID, kubeconfig string) error {
	if h.K8s == nil {
		return errors.New("K8sClient: not configured (KUBERNETES_SERVICE_HOST unset or SA token missing)")
	}
	return h.K8s.CreateOrReplaceSecret(ctx, "tinkerbell", "kubeconfig-"+id.String(),
		map[string]string{"kubeconfig": kubeconfig},
		map[string]string{
			"dcim.region-deployment":      id.String(),
			"app.kubernetes.io/component": "region-deploy",
		})
}

// recordCallbackEvent appends the success-or-failure event row. The
// stage is always "joining" — the kubeconfig callback fires from the
// joining stage; the level + message vary with the Secret-write outcome.
func (h *Handler) recordCallbackEvent(ctx context.Context, id uuid.UUID, secretRef string, nodeID uuid.UUID, kubeconfigBytes int, writeErr error) {
	level := "info"
	var msg string
	var payload json.RawMessage
	if writeErr == nil {
		msg = "kubeconfig callback received from node " + nodeID.String() +
			" (" + strconv.Itoa(kubeconfigBytes) + " bytes); Secret " + secretRef + " created"
		// Match Python's payload shape: just the secret ref so SSE
		// subscribers can render a link to it without re-querying.
		p, _ := json.Marshal(map[string]any{"secret_ref": secretRef})
		payload = p
	} else {
		level = "error"
		msg = "kubeconfig callback received from node " + nodeID.String() +
			" but Secret write failed: " + writeErr.Error() +
			". Check the central-cluster RBAC (see deploy/k8s/central/region-deploy-rbac.yaml)."
		payload = json.RawMessage("{}")
	}
	_, _ = h.Q.CreateRegionDeploymentEvent(ctx, dbq.CreateRegionDeploymentEventParams{
		DeploymentID: id, Stage: "joining", Level: level,
		Message: msg, Payload: payload,
	})
}

// extractBearer strips the "Bearer " scheme prefix from an Authorization
// header. Returns "" on missing / wrong scheme / empty value. Case-
// insensitive match on the scheme matches Python's authorization.partition
// + scheme.lower().
func extractBearer(header string) string {
	scheme, value, ok := strings.Cut(header, " ")
	if !ok {
		return ""
	}
	if !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}
