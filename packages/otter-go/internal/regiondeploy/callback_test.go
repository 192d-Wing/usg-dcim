package regiondeploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// ─── Token derivation ──────────────────────────────────────────────────

func TestDeriveCallbackToken_EmptySecret_ReturnsEmpty(t *testing.T) {
	if got := deriveCallbackToken(uuid.New(), ""); got != "" {
		t.Errorf("unset secret must return \"\"; got %q", got)
	}
}

func TestDeriveCallbackToken_Deterministic(t *testing.T) {
	id := uuid.New()
	a := deriveCallbackToken(id, "shared-secret")
	b := deriveCallbackToken(id, "shared-secret")
	if a != b || len(a) != 64 {
		t.Errorf("token must be deterministic 64-char hex; got %q / %q", a, b)
	}
}

func TestDeriveCallbackToken_DiffersByDeploymentID(t *testing.T) {
	a := deriveCallbackToken(uuid.New(), "k")
	b := deriveCallbackToken(uuid.New(), "k")
	if a == b {
		t.Errorf("different deployment ids must produce different tokens")
	}
}

func TestDeriveCallbackToken_DiffersBySecret(t *testing.T) {
	id := uuid.New()
	if deriveCallbackToken(id, "k1") == deriveCallbackToken(id, "k2") {
		t.Errorf("different secrets must produce different tokens")
	}
}

func TestCompareCallbackToken_EmptyInputs(t *testing.T) {
	if compareCallbackToken("", "x") || compareCallbackToken("x", "") || compareCallbackToken("", "") {
		t.Errorf("empty-input compare must return false")
	}
}

func TestCompareCallbackToken_Match(t *testing.T) {
	if !compareCallbackToken("abc", "abc") {
		t.Errorf("equal tokens must compare true")
	}
}

func TestCompareCallbackToken_Mismatch(t *testing.T) {
	if compareCallbackToken("abc", "abd") {
		t.Errorf("different tokens must compare false")
	}
}

func TestExtractBearer(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":     "abc",
		"bearer abc":     "abc", // case-insensitive scheme (Python parity)
		"BEARER  abc  ":  "abc", // surrounding whitespace trimmed
		"Basic dXNlcg==": "",
		"abc":            "",
		"":               "",
		"Bearer":         "", // no value
	}
	for in, want := range cases {
		if got := extractBearer(in); got != want {
			t.Errorf("extractBearer(%q) = %q, want %q", in, got, want)
		}
	}
}

// ─── HTTP integration ──────────────────────────────────────────────────

const testSecret = "test-callback-secret-12345"

type fakeK8s struct {
	calls      int
	lastNS     string
	lastName   string
	lastData   map[string]string
	lastLabels map[string]string
	returnErr  error
}

func (k *fakeK8s) CreateOrReplaceSecret(_ context.Context, ns, name string, data, labels map[string]string) error {
	k.calls++
	k.lastNS = ns
	k.lastName = name
	k.lastData = data
	k.lastLabels = labels
	return k.returnErr
}

type callbackFakeQ struct {
	*fakeQ
	setKubeRow      dbq.SetRegionDeploymentKubeconfigSecretRefRow
	setKubeErr      error
	setKubeParams   dbq.SetRegionDeploymentKubeconfigSecretRefParams
	eventCalls      int
	lastEventParams dbq.CreateRegionDeploymentEventParams
}

func (c *callbackFakeQ) SetRegionDeploymentKubeconfigSecretRef(_ context.Context, a dbq.SetRegionDeploymentKubeconfigSecretRefParams) (dbq.SetRegionDeploymentKubeconfigSecretRefRow, error) {
	c.setKubeParams = a
	return c.setKubeRow, c.setKubeErr
}

func (c *callbackFakeQ) CreateRegionDeploymentEvent(_ context.Context, a dbq.CreateRegionDeploymentEventParams) (dbq.RegionDeploymentEvent, error) {
	c.eventCalls++
	c.lastEventParams = a
	return dbq.RegionDeploymentEvent{ID: 1, Stage: a.Stage, Level: a.Level, Message: a.Message, Payload: a.Payload}, nil
}

func mountCallback(q Querier, secret string, k8s k8sSecretWriter) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: q, CallbackSecret: secret, K8s: k8s}).Mount(r)
	return r
}

func doCallback(t *testing.T, h http.Handler, id uuid.UUID, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := authtest.Request(http.MethodPost, "/region-deployments/"+id.String()+"/kubeconfig/callback",
		authtest.PrincipalWithCaps("*"), r)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+deriveCallbackToken(id, secret))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newCallbackFake(prior string, updated int64) *callbackFakeQ {
	sid := uuid.New()
	return &callbackFakeQ{
		fakeQ:      &fakeQ{},
		setKubeRow: dbq.SetRegionDeploymentKubeconfigSecretRefRow{PriorStatus: prior, Updated: updated, SiteID: &sid},
	}
}

func happyBody(nodeID uuid.UUID) string {
	b, _ := json.Marshal(map[string]any{
		"node_id":    nodeID,
		"kubeconfig": "apiVersion: v1\nkind: Config\n",
	})
	return string(b)
}

func TestCallback_NoSecret_503(t *testing.T) {
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, "", &fakeK8s{})
	rec := doCallback(t, h, uuid.New(), "anything", happyBody(uuid.New()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("callback_secret_unset")) {
		t.Errorf("envelope should carry code=callback_secret_unset; body=%s", rec.Body.String())
	}
	if q.eventCalls != 0 {
		t.Errorf("503 must not write an event row")
	}
}

func TestCallback_MissingToken_401(t *testing.T) {
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	// Don't pass secret arg so doCallback omits the Authorization header.
	rec := doCallback(t, h, uuid.New(), "", happyBody(uuid.New()))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("invalid_callback_token")) {
		t.Errorf("envelope should carry code=invalid_callback_token; body=%s", rec.Body.String())
	}
}

func TestCallback_WrongToken_401(t *testing.T) {
	id := uuid.New()
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	req := authtest.Request(http.MethodPost, "/region-deployments/"+id.String()+"/kubeconfig/callback",
		authtest.PrincipalWithCaps("*"), strings.NewReader(happyBody(uuid.New())))
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallback_TokenFromDifferentDeployment_401(t *testing.T) {
	// Authorization carries a valid token but for a different
	// deployment id — must be rejected because deriveCallbackToken
	// is HMAC-bound to the id in the path.
	id := uuid.New()
	other := uuid.New()
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	req := authtest.Request(http.MethodPost, "/region-deployments/"+id.String()+"/kubeconfig/callback",
		authtest.PrincipalWithCaps("*"), strings.NewReader(happyBody(uuid.New())))
	req.Header.Set("Authorization", "Bearer "+deriveCallbackToken(other, testSecret))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallback_OK_202_K8sWritten_EventRecorded(t *testing.T) {
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("provisioning", 1)
	k8s := &fakeK8s{}
	h := mountCallback(q, testSecret, k8s)
	rec := doCallback(t, h, id, testSecret, happyBody(nodeID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	wantRef := "tinkerbell/kubeconfig-" + id.String()
	if q.setKubeParams.KubeconfigSecretRef == nil || *q.setKubeParams.KubeconfigSecretRef != wantRef {
		t.Errorf("secret_ref drift: %v", q.setKubeParams.KubeconfigSecretRef)
	}
	if k8s.calls != 1 {
		t.Fatalf("expected one K8s write, got %d", k8s.calls)
	}
	if k8s.lastNS != "tinkerbell" || k8s.lastName != "kubeconfig-"+id.String() {
		t.Errorf("K8s call to %s/%s — wrong", k8s.lastNS, k8s.lastName)
	}
	if k8s.lastData["kubeconfig"] != "apiVersion: v1\nkind: Config\n" {
		t.Errorf("kubeconfig payload not threaded: %q", k8s.lastData["kubeconfig"])
	}
	if k8s.lastLabels["dcim.region-deployment"] != id.String() {
		t.Errorf("missing dcim.region-deployment label; got %+v", k8s.lastLabels)
	}
	if q.eventCalls != 1 || q.lastEventParams.Level != "info" {
		t.Errorf("expected one info event; got calls=%d level=%q", q.eventCalls, q.lastEventParams.Level)
	}
	if q.lastEventParams.Stage != "joining" {
		t.Errorf("event stage should be 'joining'; got %q", q.lastEventParams.Stage)
	}
	if !bytes.Contains(q.lastEventParams.Payload, []byte(`"secret_ref":"`+wantRef+`"`)) {
		t.Errorf("event payload should carry secret_ref; got %s", q.lastEventParams.Payload)
	}
}

func TestCallback_KubeconfigB64_DecodedInPlace(t *testing.T) {
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("provisioning", 1)
	k8s := &fakeK8s{}
	h := mountCallback(q, testSecret, k8s)
	plain := "apiVersion: v1\nkind: Config\n"
	body, _ := json.Marshal(map[string]any{
		"node_id":        nodeID,
		"kubeconfig_b64": base64.StdEncoding.EncodeToString([]byte(plain)),
	})
	rec := doCallback(t, h, id, testSecret, string(body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if k8s.lastData["kubeconfig"] != plain {
		t.Errorf("b64 payload should decode to plain YAML; got %q", k8s.lastData["kubeconfig"])
	}
}

func TestCallback_BothKubeconfigFieldsSet_422(t *testing.T) {
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	body, _ := json.Marshal(map[string]any{
		"node_id":        nodeID,
		"kubeconfig":     "yaml",
		"kubeconfig_b64": base64.StdEncoding.EncodeToString([]byte("yaml")),
	})
	rec := doCallback(t, h, id, testSecret, string(body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallback_NeitherKubeconfigField_422(t *testing.T) {
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	body, _ := json.Marshal(map[string]any{"node_id": nodeID})
	rec := doCallback(t, h, id, testSecret, string(body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallback_MissingNodeID_422(t *testing.T) {
	id := uuid.New()
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	body, _ := json.Marshal(map[string]any{"kubeconfig": "yaml"})
	rec := doCallback(t, h, id, testSecret, string(body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallback_WrongStage_422(t *testing.T) {
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("pending", 0)
	h := mountCallback(q, testSecret, &fakeK8s{})
	rec := doCallback(t, h, id, testSecret, happyBody(nodeID))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("pending")) {
		t.Errorf("error should name prior status; got %s", rec.Body.String())
	}
}

func TestCallback_NoRow_404(t *testing.T) {
	q := newCallbackFake("", 0)
	q.setKubeErr = pgx.ErrNoRows
	h := mountCallback(q, testSecret, &fakeK8s{})
	rec := doCallback(t, h, uuid.New(), testSecret, happyBody(uuid.New()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallback_BadJSON_400(t *testing.T) {
	id := uuid.New()
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	rec := doCallback(t, h, id, testSecret, "{not-json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallback_BadID_400(t *testing.T) {
	q := newCallbackFake("", 0)
	h := mountCallback(q, testSecret, &fakeK8s{})
	req := authtest.Request(http.MethodPost, "/region-deployments/not-a-uuid/kubeconfig/callback",
		authtest.PrincipalWithCaps("*"), strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestCallback_K8sWriteFails_StillRecordsEvent_AsError(t *testing.T) {
	// Python's regiondeploy.py L335-394: when the K8s API call fails,
	// the row's kubeconfig_secret_ref still gets updated and an error
	// event is recorded so an operator can see the failure mode.
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("provisioning", 1)
	k8s := &fakeK8s{returnErr: errors.New("k8s api 403: forbidden")}
	h := mountCallback(q, testSecret, k8s)
	rec := doCallback(t, h, id, testSecret, happyBody(nodeID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("K8s failure must NOT block the 202 response; got %d body=%s", rec.Code, rec.Body.String())
	}
	if q.eventCalls != 1 {
		t.Fatalf("expected one event row, got %d", q.eventCalls)
	}
	if q.lastEventParams.Level != "error" {
		t.Errorf("event level should be 'error' on K8s failure; got %q", q.lastEventParams.Level)
	}
	if !strings.Contains(q.lastEventParams.Message, "forbidden") {
		t.Errorf("event message should carry the underlying K8s error string; got %q", q.lastEventParams.Message)
	}
}

func TestCallback_K8sNotConfigured_StillRecordsEvent_AsError(t *testing.T) {
	// When NewInPodK8sClient returns an error in main.go, Handler.K8s
	// is nil. The callback still records the secret_ref + writes an
	// error event so the orchestrator sees the failure.
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, nil)
	rec := doCallback(t, h, id, testSecret, happyBody(nodeID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d", rec.Code)
	}
	if q.eventCalls != 1 || q.lastEventParams.Level != "error" {
		t.Errorf("expected one error event, got calls=%d level=%q", q.eventCalls, q.lastEventParams.Level)
	}
}

func TestCallback_JoiningStage_AlsoAccepted(t *testing.T) {
	// Python accepts BOTH provisioning AND joining as the stages where
	// the kubeconfig callback may fire. The CTE's status IN clause
	// covers both — verify joining works the same as provisioning.
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("joining", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	rec := doCallback(t, h, id, testSecret, happyBody(nodeID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallback_PrincipalNotRequired(t *testing.T) {
	// The callback is auth'd by HMAC bearer, not session/JWT. A
	// request with no auth.Principal in ctx must still succeed when
	// the bearer token is valid.
	id, nodeID := uuid.New(), uuid.New()
	q := newCallbackFake("provisioning", 1)
	h := mountCallback(q, testSecret, &fakeK8s{})
	req := httptest.NewRequest(http.MethodPost,
		"/region-deployments/"+id.String()+"/kubeconfig/callback",
		strings.NewReader(happyBody(nodeID)))
	// no authtest principal — bypasses the auth.From() path
	req.Header.Set("Authorization", "Bearer "+deriveCallbackToken(id, testSecret))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d body=%s; principal-less request must succeed", rec.Code, rec.Body.String())
	}
}

// Silence unused-import lint when running the file in isolation.
var _ = auth.Scope{}
