// Package ignition is the Go port of Python's
// dcim.regiondeploy.ignition. It produces per-node Flatcar Container
// Linux Ignition JSON (spec 3.4) that the Tinkerbell Workflow CR
// writes to the node's OEM partition during install.
//
// Wire-shape parity with Python is enforced via golden-byte tests:
//   - keys sorted alphabetically (Go's encoding/json.Marshal already
//     sorts map keys; Python passes sort_keys=True);
//   - no whitespace between keys/values (Go default; Python passes
//     separators=(",", ":"));
//   - HTML chars (`<`, `>`, `&`) NOT escaped (Go's default Marshal
//     escapes them; this package uses json.Encoder + SetEscapeHTML(false)
//     to match Python's json.dumps default — the kubeconfig-callback
//     bash script is full of `&&` and would otherwise come out as
//     `&&` against Python).
//
// Inputs come from RegionDeployment + RegionDeploymentNode rows;
// optional fields (kubeadm join token, control-plane endpoint, ssh
// keys, callback token + central URL) come from the orchestrator
// state machine — first control-plane → callback wired, subsequent
// control-planes / workers / edges → join token wired.
package ignition

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/google/uuid"
)

// IgnitionSpec is the spec version field. Flatcar ≥ 3815.x supports
// 3.4. Bump in lockstep with the pinned Flatcar release.
const IgnitionSpec = "3.4.0"

// Deployment is the subset of RegionDeployment the renderer reads.
// Hand-shaped instead of importing dbq because callers may build it
// from either the DB row OR from synthetic test fixtures.
type Deployment struct {
	ID     uuid.UUID
	Config map[string]any
}

// Node is the subset of RegionDeploymentNode the renderer reads.
type Node struct {
	ID       uuid.UUID
	Hostname string
	Role     string // "control_plane" | "worker" | "edge"
}

// Input carries every per-render knob — the deployment + node rows
// plus the orchestrator-derived options. Required vs optional matches
// Python's signature exactly.
type Input struct {
	Deployment Deployment
	Node       Node
	// KubeadmJoinToken + ControlPlaneEp: required for every node
	// EXCEPT the first control-plane (which generates them via
	// `kubeadm init`). Both blank → first-CP path; both set →
	// join path; mismatched (one blank) → ErrTokenAndEpRequired.
	KubeadmJoinToken string
	ControlPlaneEp   string
	// SSHKeys are operator pubkeys for the `core` user. Optional.
	SSHKeys []string
	// CallbackToken + CentralURL wire the post-init kubeconfig
	// callback. Only applies to the first control-plane; if either
	// is blank the callback unit is omitted.
	CallbackToken string
	CentralURL    string
}

// ErrTokenAndEpRequired signals a non-first-CP node was rendered
// without both the join token AND the control-plane endpoint. Same
// shape as Python's raise ValueError("non-first-cp node requires
// kubeadm_join_token + control_plane_ep").
var ErrTokenAndEpRequired = errors.New(
	"non-first-cp node requires kubeadm_join_token + control_plane_ep")

// RenderJSON returns the deterministic, compact JSON string the
// Workflow CR's `hardwareMap.ignition_json` field consumes. Matches
// Python's json.dumps(cfg, separators=(",", ":"), sort_keys=True)
// byte-for-byte (including the absence of HTML escape on `&`).
func RenderJSON(in Input) (string, error) {
	cfg, err := Build(in)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		return "", err
	}
	// Encoder appends a trailing newline; Python's json.dumps does not.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return string(out), nil
}

// Build returns the Ignition config as a map[string]any. Split from
// RenderJSON so tests can assert structure without JSON-roundtripping.
func Build(in Input) (map[string]any, error) {
	storageFiles := []map[string]any{
		file("/etc/hostname", in.Node.Hostname+"\n", 0o644),
		file("/etc/dcim/cluster.env", clusterEnv(in), 0o600),
	}
	if in.CallbackToken != "" {
		// Mode 0600 — only root reads it. The kubeconfig-callback
		// systemd unit runs as root and includes the file's contents
		// as `Authorization: Bearer …` when POSTing the kubeconfig
		// back to central.
		storageFiles = append(storageFiles,
			file("/etc/dcim/callback.token", in.CallbackToken+"\n", 0o600),
		)
	}

	var systemdUnits []map[string]any
	if in.Node.Role == "control_plane" && in.KubeadmJoinToken == "" {
		// First control-plane node — runs kubeadm init.
		systemdUnits = append(systemdUnits, kubeadmInitUnit())
		// And, when a callback token + central URL are wired, runs
		// the post-init unit that POSTs admin.conf back to central.
		if in.CallbackToken != "" && in.CentralURL != "" {
			systemdUnits = append(systemdUnits, kubeconfigCallbackUnit())
		}
	} else {
		// Worker / edge / additional control-plane — runs kubeadm join.
		if in.KubeadmJoinToken == "" || in.ControlPlaneEp == "" {
			return nil, ErrTokenAndEpRequired
		}
		systemdUnits = append(systemdUnits, kubeadmJoinUnit(
			in.Node.Role, in.KubeadmJoinToken, in.ControlPlaneEp))
	}

	cfg := map[string]any{
		"ignition": map[string]any{"version": IgnitionSpec},
		"storage":  map[string]any{"files": storageFiles},
		"systemd":  map[string]any{"units": systemdUnits},
	}
	if len(in.SSHKeys) > 0 {
		cfg["passwd"] = map[string]any{
			"users": []map[string]any{
				{
					"name":              "core",
					"sshAuthorizedKeys": append([]string(nil), in.SSHKeys...),
				},
			},
		}
	}
	return cfg, nil
}

// file renders one Ignition storage.files entry with inline contents.
// Mirrors Python's _file helper — same field names, same shapes,
// same `overwrite: true` default.
func file(path, contents string, mode int) map[string]any {
	return map[string]any{
		"path":      path,
		"mode":      mode,
		"overwrite": true,
		"contents":  map[string]any{"inline": contents},
	}
}

// clusterEnv renders /etc/dcim/cluster.env — key=value lines, shell-
// safe. Mirrors Python's _cluster_env helper exactly: missing config
// keys come out as empty strings (Python `dict.get(k, ”)`), and the
// vip_v6 fallback for CONTROL_PLANE_EP keeps Python's branch order.
func clusterEnv(in Input) string {
	cfg := in.Deployment.Config
	cp := in.ControlPlaneEp
	if cp == "" {
		cp = configString(cfg, "vip_v6")
	}
	ciliumVersion := configString(cfg, "cilium_version")
	if ciliumVersion == "" {
		ciliumVersion = "1.19.3"
	}
	return "POD_CIDR_V6=" + configString(cfg, "pod_cidr_v6") + "\n" +
		"SVC_CIDR_V6=" + configString(cfg, "svc_cidr_v6") + "\n" +
		"CONTROL_PLANE_EP=" + cp + "\n" +
		"CILIUM_VERSION=" + ciliumVersion + "\n" +
		"DCIM_CENTRAL_URL=" + in.CentralURL + "\n" +
		"DCIM_DEPLOYMENT_ID=" + in.Deployment.ID.String() + "\n" +
		"DCIM_NODE_ID=" + in.Node.ID.String() + "\n"
}

// configString reads a string-typed key from the deployment config
// map. Missing key, wrong-type value → "". Mirrors Python's
// `config.get(key, ”)` where the dict's get returns the default for
// any missing key and the caller wraps in an f-string (so non-string
// values would also stringify; we treat non-string as missing for
// safety).
func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	v, ok := cfg[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// kubeadmInitUnit returns the first-control-plane install unit.
// Reads pod/service CIDR + control-plane endpoint from cluster.env
// at runtime (EnvironmentFile=…) so the unit text stays static.
func kubeadmInitUnit() map[string]any {
	const contents = "[Unit]\n" +
		"Description=DCIM-managed kubeadm init (first control-plane)\n" +
		"After=network-online.target containerd.service\n" +
		"Wants=network-online.target containerd.service\n" +
		"ConditionPathExists=!/etc/kubernetes/admin.conf\n" +
		"\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"RemainAfterExit=yes\n" +
		"EnvironmentFile=/etc/dcim/cluster.env\n" +
		"ExecStart=/usr/local/bin/kubeadm init" +
		" --pod-network-cidr=${POD_CIDR_V6}" +
		" --service-cidr=${SVC_CIDR_V6}" +
		" --control-plane-endpoint=${CONTROL_PLANE_EP}" +
		" --skip-phases=addon/kube-proxy\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
	return map[string]any{
		"name":     "kubeadm-init.service",
		"enabled":  true,
		"contents": contents,
	}
}

// kubeadmJoinUnit returns the worker / additional-control-plane join
// unit. Role decides whether `--control-plane` lands on the kubeadm
// command line. Mirrors Python's _kubeadm_join_unit exactly.
func kubeadmJoinUnit(role, token, controlPlaneEp string) map[string]any {
	cpFlag := ""
	if role == "control_plane" {
		cpFlag = " --control-plane"
	}
	contents := "[Unit]\n" +
		"Description=DCIM-managed kubeadm join (" + role + ")\n" +
		"After=network-online.target containerd.service\n" +
		"Wants=network-online.target containerd.service\n" +
		"ConditionPathExists=!/etc/kubernetes/kubelet.conf\n" +
		"\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"RemainAfterExit=yes\n" +
		"ExecStart=/usr/local/bin/kubeadm join " + controlPlaneEp +
		" --token " + token +
		" --discovery-token-unsafe-skip-ca-verification" +
		cpFlag + "\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
	return map[string]any{
		"name":     "kubeadm-join.service",
		"enabled":  true,
		"contents": contents,
	}
}

// kubeconfigCallbackUnit returns the post-init unit that POSTs
// admin.conf back to central. Idempotent across reboots via the
// /var/lib/dcim/callback-sent sentinel.
//
// The ExecStart shell is byte-for-byte identical to Python's
// _kubeconfig_callback_unit so the golden-byte parity test catches
// any shell-quoting drift (Flatcar's base image is bash + curl + base64;
// no jq, hence the printf-built JSON body).
func kubeconfigCallbackUnit() map[string]any {
	const execStart = `/usr/bin/bash -c 'TOKEN=$(cat /etc/dcim/callback.token) && ` +
		`KCFG=$(base64 -w0 /etc/kubernetes/admin.conf) && ` +
		`BODY=$(printf "{\"node_id\":\"%s\",\"kubeconfig_b64\":\"%s\"}" "$DCIM_NODE_ID" "$KCFG") && ` +
		`curl --fail --silent --show-error ` +
		`--retry 30 --retry-delay 10 --retry-connrefused ` +
		`-H "Authorization: Bearer $TOKEN" ` +
		`-H "Content-Type: application/json" ` +
		`--data "$BODY" ` +
		`"$DCIM_CENTRAL_URL/api/v1/region-deployments/$DCIM_DEPLOYMENT_ID/kubeconfig/callback"'`
	contents := "[Unit]\n" +
		"Description=POST kubeadm-generated kubeconfig back to DCIM central\n" +
		"After=kubeadm-init.service network-online.target\n" +
		"Requires=kubeadm-init.service\n" +
		"Wants=network-online.target\n" +
		"ConditionPathExists=/etc/kubernetes/admin.conf\n" +
		"ConditionPathExists=/etc/dcim/callback.token\n" +
		"ConditionPathExists=!/var/lib/dcim/callback-sent\n" +
		"\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"RemainAfterExit=yes\n" +
		"EnvironmentFile=/etc/dcim/cluster.env\n" +
		"ExecStart=" + execStart + "\n" +
		"ExecStartPost=/usr/bin/install -d -m 0700 /var/lib/dcim\n" +
		"ExecStartPost=/usr/bin/touch /var/lib/dcim/callback-sent\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
	return map[string]any{
		"name":     "kubeconfig-callback.service",
		"enabled":  true,
		"contents": contents,
	}
}

// modeString is exposed only to make the golden-test diffs readable
// when a mode mismatch shows up — `0o644` vs `0o600` is a one-bit
// human-grep, but the JSON output is the decimal integer. Kept here
// because callers reading the renderer might want it too.
func modeString(m int) string { return strconv.Itoa(m) }
