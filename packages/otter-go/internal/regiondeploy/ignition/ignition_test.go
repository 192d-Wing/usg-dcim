package ignition

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Fixed UUIDs the testdata fixtures were generated against. Bumping
// any of these requires regenerating the .json files (see the
// Python-side generator command in the package docstring) and re-
// running this test suite.
var (
	depID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	n1ID  = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	n2ID  = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	n3ID  = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

func defaultDeployment() Deployment {
	return Deployment{
		ID: depID,
		Config: map[string]any{
			"pod_cidr_v6":    "fd00:site:42:1000::/56",
			"svc_cidr_v6":    "fd00:site:42:2000::/112",
			"vip_v6":         "[fd00:site:42:1::1]:6443",
			"cilium_version": "1.19.3",
		},
	}
}

// loadGolden reads a Python-generated fixture from testdata/. The
// fixture is the exact byte stream Python's json.dumps emits so any
// drift between the Go renderer and Python is a one-byte diff in the
// failure message.
func loadGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRender_FirstCP_WithCallback_MatchesPython(t *testing.T) {
	got, err := RenderJSON(Input{
		Deployment:    defaultDeployment(),
		Node:          Node{ID: n1ID, Hostname: "n01", Role: "control_plane"},
		CallbackToken: "cafebabedeadbeef",
		CentralURL:    "https://dcim.example.mil",
		SSHKeys:       []string{"ssh-ed25519 AAAA...", "ssh-rsa BBBB..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := loadGolden(t, "first_cp_with_callback.json")
	if got != want {
		t.Errorf("Python parity drift; diff to first byte:\n  got=%s\n  want=%s", firstDiff(got, want), firstDiff(want, got))
	}
}

func TestRender_FirstCP_NoCallback_MatchesPython(t *testing.T) {
	got, err := RenderJSON(Input{
		Deployment: defaultDeployment(),
		Node:       Node{ID: n1ID, Hostname: "n01", Role: "control_plane"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := loadGolden(t, "first_cp_no_callback.json")
	if got != want {
		t.Errorf("Python parity drift; diff:\n  got=%s\n  want=%s", firstDiff(got, want), firstDiff(want, got))
	}
}

func TestRender_Worker_Join_MatchesPython(t *testing.T) {
	got, err := RenderJSON(Input{
		Deployment:       defaultDeployment(),
		Node:             Node{ID: n2ID, Hostname: "n02", Role: "worker"},
		KubeadmJoinToken: "abcdef.0123456789abcdef",
		ControlPlaneEp:   "[fd00:site:42:1::1]:6443",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := loadGolden(t, "worker_join.json")
	if got != want {
		t.Errorf("Python parity drift; diff:\n  got=%s\n  want=%s", firstDiff(got, want), firstDiff(want, got))
	}
}

func TestRender_AdditionalCP_Join_MatchesPython(t *testing.T) {
	got, err := RenderJSON(Input{
		Deployment:       defaultDeployment(),
		Node:             Node{ID: n3ID, Hostname: "n03", Role: "control_plane"},
		KubeadmJoinToken: "abcdef.0123456789abcdef",
		ControlPlaneEp:   "[fd00:site:42:1::1]:6443",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := loadGolden(t, "additional_cp_join.json")
	if got != want {
		t.Errorf("Python parity drift; diff:\n  got=%s\n  want=%s", firstDiff(got, want), firstDiff(want, got))
	}
}

func TestRender_Edge_Join_MatchesPython(t *testing.T) {
	got, err := RenderJSON(Input{
		Deployment:       defaultDeployment(),
		Node:             Node{ID: n2ID, Hostname: "n04", Role: "edge"},
		KubeadmJoinToken: "abcdef.0123456789abcdef",
		ControlPlaneEp:   "[fd00:site:42:1::1]:6443",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := loadGolden(t, "edge_join.json")
	if got != want {
		t.Errorf("Python parity drift; diff:\n  got=%s\n  want=%s", firstDiff(got, want), firstDiff(want, got))
	}
}

func TestRender_NonFirstCP_MissingToken_Errors(t *testing.T) {
	_, err := RenderJSON(Input{
		Deployment:     defaultDeployment(),
		Node:           Node{ID: n2ID, Hostname: "n02", Role: "worker"},
		ControlPlaneEp: "[fd00:site:42:1::1]:6443",
		// no KubeadmJoinToken
	})
	if !errors.Is(err, ErrTokenAndEpRequired) {
		t.Errorf("expected ErrTokenAndEpRequired; got %v", err)
	}
}

func TestRender_NonFirstCP_MissingEp_Errors(t *testing.T) {
	_, err := RenderJSON(Input{
		Deployment:       defaultDeployment(),
		Node:             Node{ID: n2ID, Hostname: "n02", Role: "worker"},
		KubeadmJoinToken: "abcdef.0123456789abcdef",
		// no ControlPlaneEp
	})
	if !errors.Is(err, ErrTokenAndEpRequired) {
		t.Errorf("expected ErrTokenAndEpRequired; got %v", err)
	}
}

func TestRender_NoCallbackToken_NoCallbackUnit(t *testing.T) {
	// First CP with no callback token must omit BOTH the
	// /etc/dcim/callback.token storage file AND the
	// kubeconfig-callback.service unit. Defense in depth — a future
	// refactor that wires one without the other would leave dangling
	// state on the node.
	cfg, err := Build(Input{
		Deployment: defaultDeployment(),
		Node:       Node{ID: n1ID, Hostname: "n01", Role: "control_plane"},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := cfg["storage"].(map[string]any)["files"].([]map[string]any)
	for _, f := range files {
		if f["path"] == "/etc/dcim/callback.token" {
			t.Errorf("callback.token file must not appear without CallbackToken; got %+v", f)
		}
	}
	units := cfg["systemd"].(map[string]any)["units"].([]map[string]any)
	for _, u := range units {
		if u["name"] == "kubeconfig-callback.service" {
			t.Errorf("kubeconfig-callback.service must not appear without CallbackToken")
		}
	}
}

func TestRender_CallbackTokenWithoutCentralURL_DropsCallbackUnit(t *testing.T) {
	// Python's branch: callback unit only fires when BOTH token AND
	// central_url are set. Token-only → write the file (the operator
	// might handle it manually) but skip the systemd unit so curl
	// doesn't fire against an empty URL.
	cfg, err := Build(Input{
		Deployment:    defaultDeployment(),
		Node:          Node{ID: n1ID, Hostname: "n01", Role: "control_plane"},
		CallbackToken: "cafebabedeadbeef",
		// no CentralURL
	})
	if err != nil {
		t.Fatal(err)
	}
	files := cfg["storage"].(map[string]any)["files"].([]map[string]any)
	var sawTokenFile bool
	for _, f := range files {
		if f["path"] == "/etc/dcim/callback.token" {
			sawTokenFile = true
		}
	}
	if !sawTokenFile {
		t.Errorf("callback.token file MUST appear when CallbackToken is set, even without CentralURL")
	}
	units := cfg["systemd"].(map[string]any)["units"].([]map[string]any)
	for _, u := range units {
		if u["name"] == "kubeconfig-callback.service" {
			t.Errorf("kubeconfig-callback.service MUST be omitted without CentralURL")
		}
	}
}

func TestRender_AmpersandsNotHTMLEscaped(t *testing.T) {
	// The kubeconfig-callback unit's bash is full of `&&`. Go's
	// default json.Marshal escapes `&` → `&`; Python's
	// json.dumps does not. Without SetEscapeHTML(false) the Go
	// output would diverge byte-for-byte from Python and the golden
	// tests above would already fail — this assert is a focused
	// regression catch so a future refactor that drops the
	// EscapeHTML knob fails loud here instead of in the slow golden
	// diff above.
	got, err := RenderJSON(Input{
		Deployment:    defaultDeployment(),
		Node:          Node{ID: n1ID, Hostname: "n01", Role: "control_plane"},
		CallbackToken: "cafebabedeadbeef",
		CentralURL:    "https://dcim.example.mil",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "\\u0026") {
		t.Errorf("output contains JSON-escaped ampersand `\\u0026`; SetEscapeHTML(false) regressed")
	}
	if !strings.Contains(got, " && ") {
		t.Errorf("output should contain literal ` && ` from the bash command; got %q", got)
	}
}

func TestRender_MissingPodCIDR_EmitsEmpty(t *testing.T) {
	// Python's `config.get('pod_cidr_v6', '')` returns "" for missing
	// keys — the install fails LATER with a clear kubeadm error
	// rather than at render time. Match the behavior so an
	// orchestrator pass with partial config still produces a parseable
	// Ignition (and a renderable error log).
	dep := defaultDeployment()
	delete(dep.Config, "pod_cidr_v6")
	got, err := RenderJSON(Input{
		Deployment: dep,
		Node:       Node{ID: n1ID, Hostname: "n01", Role: "control_plane"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "POD_CIDR_V6=\\n") {
		t.Errorf("missing pod_cidr_v6 should emit empty value; got %q", got)
	}
}

func TestRender_MissingCiliumVersion_DefaultsTo1_19_3(t *testing.T) {
	dep := defaultDeployment()
	delete(dep.Config, "cilium_version")
	got, err := RenderJSON(Input{
		Deployment: dep,
		Node:       Node{ID: n1ID, Hostname: "n01", Role: "control_plane"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "CILIUM_VERSION=1.19.3\\n") {
		t.Errorf("missing cilium_version should fall back to 1.19.3; got %q", got)
	}
}

func TestRender_ExplicitControlPlaneEp_OverridesVipV6(t *testing.T) {
	// Python's `_cluster_env` uses `control_plane_ep or config.get('vip_v6', '')`
	// — explicit endpoint wins when set; vip_v6 is the fallback. Match
	// the precedence so a worker join that names a specific endpoint
	// doesn't get re-routed through the configured VIP.
	got, err := RenderJSON(Input{
		Deployment:       defaultDeployment(),
		Node:             Node{ID: n2ID, Hostname: "n02", Role: "worker"},
		KubeadmJoinToken: "tok",
		ControlPlaneEp:   "[fd00:site:42:1::99]:6443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "CONTROL_PLANE_EP=[fd00:site:42:1::99]:6443\\n") {
		t.Errorf("explicit ControlPlaneEp must win over vip_v6; got %q", got)
	}
}

// firstDiff returns a short snippet showing the bytes around the
// first divergence between a and b. Returns "" when a == b.
func firstDiff(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			start := i - 20
			if start < 0 {
				start = 0
			}
			end := i + 60
			if end > len(a) {
				end = len(a)
			}
			return "[...]" + a[start:end] + "[...]"
		}
	}
	if len(a) != len(b) {
		// Length mismatch but matching prefix.
		if len(a) > n {
			return "[trailing]" + a[n:]
		}
		return "[truncated at " + os.Args[0] + "]"
	}
	return ""
}
