// Package crd is the Go port of Python's
// dcim.regiondeploy.crd. Each function takes a RegionDeployment
// (and its child nodes / services) and returns the Go struct for
// the Kubernetes resource that drives bare-metal provisioning at
// the target site. The orchestrator (later PR) applies these via
// the regional cluster's dynamic client.
//
// API group choice (v1alpha1) matches Python's choice; see Python
// docstring for the rationale (upstream chart contract + Smee's
// BackendReader).
//
// Why typed structs instead of map[string]any:
//   Python uses dict literals whose insertion order encodes the
//   wire schema. Go map[string]any iteration is randomized; typed
//   structs encode field order at declaration site, which is what
//   yaml.v2 needs to emit in stable PyYAML-parity order.
//
// One byte-parity caveat: Template.Spec.Data is a YAML-in-YAML
// string. PyYAML's long-quoted-scalar line folding differs from
// yaml.v2's, so the Template fixture is parity-tested
// SEMANTICALLY (parse both → equal trees) rather than
// byte-for-byte. The other 5 single-doc fixtures + the multi-doc
// fixture are byte-parity tested.
package crd

import (
	"bytes"
	"strings"

	"gopkg.in/yaml.v2"
)

const (
	TinkerbellAPIVersion = "tinkerbell.org/v1alpha1"
	BMCAPIVersion        = "bmc.tinkerbell.org/v1alpha1"

	KindHardware   = "Hardware"
	KindTemplate   = "Template"
	KindWorkflow   = "Workflow"
	KindBMCMachine = "Machine"
)

// Deployment is the subset of RegionDeployment the renderers read.
type Deployment struct {
	ID     string
	Name   string
	SiteID string
	Nodes  []Node
}

// Node is the subset of RegionDeploymentNode the renderers read.
// PrimaryIPv6 is "" when the node has no v6 management address —
// matches Python's `if node.primary_ip_v6 is not None:` check.
type Node struct {
	ID                string
	Hostname          string
	MAC               string
	PrimaryIPv6       string
	BMCAddress        string
	BMCCredsSecretRef string // "namespace/name" or "" or "name" (no slash)
}

// ─── Hardware ──────────────────────────────────────────────────────

type Hardware struct {
	APIVersion string             `yaml:"apiVersion"`
	Kind       string             `yaml:"kind"`
	Metadata   ObjectMeta         `yaml:"metadata"`
	Spec       HardwareSpec       `yaml:"spec"`
}

type HardwareSpec struct {
	Metadata   HardwareMetadata    `yaml:"metadata"`
	Interfaces []HardwareInterface `yaml:"interfaces"`
}

type HardwareMetadata struct {
	Facility HardwareFacility `yaml:"facility"`
	Instance HardwareInstance `yaml:"instance"`
}

type HardwareFacility struct {
	FacilityCode string `yaml:"facility_code"`
}

// HardwareInstance carries Hostname, ID, and optionally IPv6Address.
// IPv6Address uses `omitempty` so a node with no v6 management
// address skips the field — matches Python's
// `if primary_ip is not None: spec["metadata"]["instance"]["ipv6_address"] = ...`.
type HardwareInstance struct {
	Hostname    string `yaml:"hostname"`
	ID          string `yaml:"id"`
	IPv6Address string `yaml:"ipv6_address,omitempty"`
}

type HardwareInterface struct {
	DHCP    InterfaceDHCP    `yaml:"dhcp"`
	Netboot InterfaceNetboot `yaml:"netboot"`
}

type InterfaceDHCP struct {
	MAC      string       `yaml:"mac"`
	Hostname string       `yaml:"hostname"`
	IP       InterfaceIP  `yaml:"ip"`
}

// InterfaceIP is the v1alpha1 sentinel v4 entry. See Python source
// for why we populate it even on v6-only sites.
type InterfaceIP struct {
	Address string `yaml:"address"`
	Netmask string `yaml:"netmask"`
	Gateway string `yaml:"gateway"`
	Family  int    `yaml:"family"`
}

type InterfaceNetboot struct {
	AllowPXE bool `yaml:"allowPXE"`
}

// HardwareForNode renders a Hardware CR for a single bare-metal
// node. Mirrors crd.hardware_for_node.
func HardwareForNode(dep Deployment, node Node) Hardware {
	hw := Hardware{
		APIVersion: TinkerbellAPIVersion,
		Kind:       KindHardware,
		Metadata: ObjectMeta{
			Name:   resourceName(dep, &node),
			Labels: labels(dep, &node),
		},
		Spec: HardwareSpec{
			Metadata: HardwareMetadata{
				Facility: HardwareFacility{FacilityCode: siteLabel(dep)},
				Instance: HardwareInstance{
					Hostname: node.Hostname,
					ID:       node.ID,
				},
			},
			Interfaces: []HardwareInterface{
				{
					DHCP: InterfaceDHCP{
						MAC:      node.MAC,
						Hostname: node.Hostname,
						IP: InterfaceIP{
							Address: "0.0.0.0",
							Netmask: "255.255.255.255",
							Gateway: "0.0.0.0",
							Family:  4,
						},
					},
					Netboot: InterfaceNetboot{AllowPXE: true},
				},
			},
		},
	}
	if node.PrimaryIPv6 != "" {
		hw.Spec.Metadata.Instance.IPv6Address = node.PrimaryIPv6
	}
	return hw
}

// ─── Template ──────────────────────────────────────────────────────

type Template struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   ObjectMeta   `yaml:"metadata"`
	Spec       TemplateSpec `yaml:"spec"`
}

type TemplateSpec struct {
	// Data is the inner Template document, YAML-serialized. The
	// outer YAML carries it as a quoted string scalar; consumers
	// parse it on the other side. yaml.v2's folding of long
	// quoted scalars differs from PyYAML's — parity tested
	// semantically rather than byte-for-byte.
	Data string `yaml:"data"`
}

// innerTemplate is the YAML-in-YAML document embedded in
// Template.Spec.Data. Kept private — callers only ever see the
// rendered YAML string via TemplateForDeployment.
type innerTemplate struct {
	Version       string             `yaml:"version"`
	Name          string             `yaml:"name"`
	GlobalTimeout int                `yaml:"global_timeout"`
	Tasks         []innerTemplateTask `yaml:"tasks"`
}

type innerTemplateTask struct {
	Name    string                `yaml:"name"`
	Worker  string                `yaml:"worker"`
	Actions []innerTemplateAction `yaml:"actions"`
}

type innerTemplateAction struct {
	Name        string                `yaml:"name"`
	Image       string                `yaml:"image"`
	Timeout     int                   `yaml:"timeout"`
	Environment *innerActionEnv       `yaml:"environment,omitempty"`
	PID         string                `yaml:"pid,omitempty"`
	Command     []string              `yaml:"command,omitempty"`
	Volumes     []string              `yaml:"volumes,omitempty"`
}

// innerActionEnv is the env-vars block for a Template action.
// Typed (not map[string]string) so yaml.v2 emits keys in Python
// dict-insertion order; map[string]string would alphabetize and
// break the byte-parity of the inner Template document. Empty
// strings on all fields → emitted as empty, matching Python's
// dict semantics. Fields are the union of every Action's env in
// the install plan (stream-image, write-ignition, reboot); each
// action sets only the subset it needs, omitempty drops the rest.
type innerActionEnv struct {
	DestDisk   string `yaml:"DEST_DISK,omitempty"`
	ImgURL     string `yaml:"IMG_URL,omitempty"`
	Compressed string `yaml:"COMPRESSED,omitempty"`
	FsType     string `yaml:"FS_TYPE,omitempty"`
	DestPath   string `yaml:"DEST_PATH,omitempty"`
	Contents   string `yaml:"CONTENTS,omitempty"`
	UID        string `yaml:"UID,omitempty"`
	GID        string `yaml:"GID,omitempty"`
	Mode       string `yaml:"MODE,omitempty"`
	DirMode    string `yaml:"DIRMODE,omitempty"`
}

// TemplateForDeployment renders a Template CR for the deployment's
// install flow. Mirrors crd.template_for_deployment.
//
// The action list here is the minimum viable plan: stream-image,
// write-ignition, reboot. See Python docstring for rationale.
func TemplateForDeployment(dep Deployment) (Template, error) {
	name := templateName(dep)
	inner := innerTemplate{
		Version:       "0.1",
		Name:          name,
		GlobalTimeout: 1800,
		Tasks: []innerTemplateTask{
			{
				Name:   "os-install",
				Worker: "{{.device_1}}",
				Actions: []innerTemplateAction{
					{
						Name:    "stream-image",
						Image:   "quay.io/tinkerbell-actions/image2disk:v1.0.0",
						Timeout: 600,
						Environment: &innerActionEnv{
							DestDisk:   "/dev/sda",
							ImgURL:     "{{.image_url}}",
							Compressed: "true",
						},
					},
					{
						Name:    "write-ignition",
						Image:   "quay.io/tinkerbell-actions/writefile:v1.0.0",
						Timeout: 90,
						Environment: &innerActionEnv{
							DestDisk: "/dev/sda6",
							FsType:   "ext4",
							DestPath: "/ignition.json",
							Contents: "{{.ignition_json}}",
							UID:      "0",
							GID:      "0",
							Mode:     "0644",
							DirMode:  "0755",
						},
					},
					{
						Name:    "reboot",
						Image:   "ghcr.io/jacobweinstock/waitdaemon:0.2.0",
						Timeout: 90,
						PID:     "host",
						Command: []string{"reboot"},
						Volumes: []string{"/worker:/worker"},
					},
				},
			},
		},
	}
	innerYAML, err := DumpYAML(inner)
	if err != nil {
		return Template{}, err
	}
	return Template{
		APIVersion: TinkerbellAPIVersion,
		Kind:       KindTemplate,
		Metadata: ObjectMeta{
			Name:   name,
			Labels: labels(dep, nil),
		},
		Spec: TemplateSpec{Data: innerYAML},
	}, nil
}

// ─── Workflow ──────────────────────────────────────────────────────

type Workflow struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   ObjectMeta   `yaml:"metadata"`
	Spec       WorkflowSpec `yaml:"spec"`
}

type WorkflowSpec struct {
	TemplateRef string      `yaml:"templateRef"`
	HardwareRef string      `yaml:"hardwareRef"`
	HardwareMap HardwareMap `yaml:"hardwareMap"`
}

// HardwareMap is the typed shape of Workflow.spec.hardwareMap.
// Typed (not map[string]string) so yaml.v2 emits the keys in the
// Python source's insertion order: device_1, image_url,
// ignition_json. map[string]string would sort alphabetically and
// diverge.
type HardwareMap struct {
	Device1      string `yaml:"device_1"`
	ImageURL     string `yaml:"image_url"`
	IgnitionJSON string `yaml:"ignition_json"`
}

// WorkflowForNode renders a Workflow CR that binds the deployment
// Template to a specific Hardware (by MAC) with per-node Ignition.
// Mirrors crd.workflow_for_node.
//
// `imageURL` and `ignitionJSON` are caller-provided (rather than
// derived from the node) since both depend on deployment-wide
// config the orchestrator owns: the Flatcar release URL
// (mirror-cached centrally) and the rendered Ignition.
func WorkflowForNode(dep Deployment, node Node, imageURL, ignitionJSON string) Workflow {
	return Workflow{
		APIVersion: TinkerbellAPIVersion,
		Kind:       KindWorkflow,
		Metadata: ObjectMeta{
			Name:   resourceName(dep, &node),
			Labels: labels(dep, &node),
		},
		Spec: WorkflowSpec{
			TemplateRef: templateName(dep),
			HardwareRef: resourceName(dep, &node),
			HardwareMap: HardwareMap{
				Device1:      node.MAC,
				ImageURL:     imageURL,
				IgnitionJSON: ignitionJSON,
			},
		},
	}
}

// ─── BMC Machine (Rufio) ───────────────────────────────────────────

type BMCMachine struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   ObjectMeta     `yaml:"metadata"`
	Spec       BMCMachineSpec `yaml:"spec"`
}

type BMCMachineSpec struct {
	Connection BMCConnection `yaml:"connection"`
}

type BMCConnection struct {
	Host            string         `yaml:"host"`
	Port            int            `yaml:"port"`
	InsecureTLS     bool           `yaml:"insecureTLS"`
	AuthSecretRef   BMCSecretRef   `yaml:"authSecretRef"`
	ProviderOptions map[string]any `yaml:"providerOptions"`
}

type BMCSecretRef struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// BMCMachineForNode renders a Rufio Machine CR — the BMC
// connection Rufio uses to power-cycle and set boot order on the
// node. Credentials live in a separate Kubernetes Secret created
// by the orchestrator at deploy-start.
//
// secretRef format is "namespace/name". Empty or no slash → both
// fields empty-string (matches Python's `partition("/")` behavior).
func BMCMachineForNode(dep Deployment, node Node) BMCMachine {
	secretNs, secretName := partitionSecretRef(node.BMCCredsSecretRef)
	return BMCMachine{
		APIVersion: BMCAPIVersion,
		Kind:       KindBMCMachine,
		Metadata: ObjectMeta{
			Name:   resourceName(dep, &node),
			Labels: labels(dep, &node),
		},
		Spec: BMCMachineSpec{
			Connection: BMCConnection{
				Host:        node.BMCAddress,
				Port:        443,
				InsecureTLS: true,
				AuthSecretRef: BMCSecretRef{
					Name:      secretName,
					Namespace: secretNs,
				},
				ProviderOptions: map[string]any{},
			},
		},
	}
}

// ─── CRDs aggregate + dump ─────────────────────────────────────────

// CRDsForDeployment renders the full set of CRDs for a deployment.
// Returns [Template, Hardware_n1, BMCMachine_n1, Workflow_n1,
// Hardware_n2, BMCMachine_n2, Workflow_n2, ...] — Template first
// so subsequent Workflow references resolve.
//
// ignitionFor maps node id → rendered Ignition JSON. When a node
// id is missing, the empty string is used (matches Python).
func CRDsForDeployment(dep Deployment, imageURL string, ignitionFor map[string]string) ([]any, error) {
	if ignitionFor == nil {
		ignitionFor = map[string]string{}
	}
	tpl, err := TemplateForDeployment(dep)
	if err != nil {
		return nil, err
	}
	out := []any{tpl}
	for _, n := range dep.Nodes {
		out = append(out, HardwareForNode(dep, n))
		out = append(out, BMCMachineForNode(dep, n))
		out = append(out, WorkflowForNode(dep, n, imageURL, ignitionFor[n.ID]))
	}
	return out, nil
}

// DumpYAML serializes one document. Matches Python's
// yaml.safe_dump(values, sort_keys=False) — block style,
// struct-declaration order, 2-space indent, list items at the
// parent map key's column.
func DumpYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return pyYAMLQuoteStyle(string(b)), nil
}

// DumpYAMLAll serializes a list of CRDs as a multi-doc YAML
// stream — the format `kubectl apply -f -` expects. Matches
// Python's yaml.safe_dump_all(crds, sort_keys=False).
func DumpYAMLAll(crds []any) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	for _, c := range crds {
		if err := enc.Encode(c); err != nil {
			return "", err
		}
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return pyYAMLQuoteStyle(buf.String()), nil
}

// pyYAMLQuoteStyle rewrites yaml.v2's `"X"` → PyYAML's `'X'` for
// ambiguous-but-clean scalars. Operates line-by-line and ONLY
// converts when the entire scalar-value position on a line is a
// single double-quoted run with clean content (no escapes, no
// embedded `'`).
//
// Why line-by-line: yaml.v2 emits inline JSON content (the
// Workflow's ignition_json) as a single-quoted scalar containing
// literal `"…"` substrings. A naive document-wide regex would
// rewrite those inner quotes and corrupt the JSON. By scoping to
// the value-position pattern `(: |- )"X"$`, we avoid touching
// quotes inside other scalars.
//
// Load-bearing assumption: renderer inputs (hostnames, MAC
// addresses, image URLs, secret names) contain no literal `"` at
// the START of their value position. Multi-line yaml.v2 scalars
// (block literals) start with `|` or `>` markers, not `"`, so
// they're untouched.
func pyYAMLQuoteStyle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(rewriteLine(line))
	}
	return b.String()
}

// rewriteLine matches `<prefix>"<clean>"` where prefix ends in
// `: ` or `- ` (value position) and the rest of the line is just
// the quoted scalar (allowing trailing whitespace). On a hit, the
// `"X"` becomes `'X'`. Misses pass through unchanged.
//
// Finds the LAST `: ` or `- ` on the line so a list-of-maps with
// an inline-keyed value (`- key: "x"`) is converted at the `key:`
// position, not the leading `- `. Caller-provided strings like
// URLs that contain `:` followed by space are left intact because
// any non-quote char after the prefix early-exits.
func rewriteLine(line string) string {
	// Scan for the rightmost `: ` or `- ` whose tail is a clean
	// quoted scalar.
	for i := len(line) - 2; i >= 0; i-- {
		if !((line[i] == ':' || line[i] == '-') && line[i+1] == ' ') {
			continue
		}
		prefixEnd := i + 2
		if prefixEnd >= len(line) || line[prefixEnd] != '"' {
			continue
		}
		j := prefixEnd + 1
		clean := true
		for j < len(line) && line[j] != '"' {
			c := line[j]
			if c == '\\' || c == '\'' {
				clean = false
				break
			}
			j++
		}
		if !clean || j >= len(line) {
			continue
		}
		// Trailing whitespace only — qualifies as scalar value.
		tail := true
		for k := j + 1; k < len(line); k++ {
			if line[k] != ' ' && line[k] != '\t' {
				tail = false
				break
			}
		}
		if !tail {
			continue
		}
		return line[:prefixEnd] + "'" + line[prefixEnd+1:j] + "'" + line[j+1:]
	}
	return line
}

// ─── internal helpers ──────────────────────────────────────────────

// ObjectMeta is k8s ObjectMeta restricted to the fields the
// renderers populate. Labels is map[string]string — yaml.v2 sorts
// keys alphabetically, which happens to match Python's dict literal
// order for the three labels we emit (region-deployment <
// region-deployment-name < region-deployment-node).
type ObjectMeta struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels"`
}

// resourceName produces a stable per-(deployment, node) DNS-1123
// label, ≤63 runes. Mirrors Python's `_resource_name`.
//
// Slicing is by rune (Python `str[:N]` is code-point-based) — a
// byte slice could split a multi-byte UTF-8 sequence and produce
// invalid UTF-8 that the k8s apiserver rejects on admission. The
// downstream admission webhook also enforces DNS-1123 (lowercase
// letters, digits, `-`), so multi-byte runes wouldn't actually be
// accepted, but emitting valid UTF-8 keeps the failure mode at
// admission (clear error) rather than corruption (cryptic error).
func resourceName(dep Deployment, node *Node) string {
	depPart := runeSlice(dep.ID, 8)
	if node == nil {
		return "rd-" + depPart
	}
	host := strings.ToLower(strings.ReplaceAll(node.Hostname, ".", "-"))
	name := "rd-" + depPart + "-" + host
	name = runeSlice(name, 63)
	return strings.TrimRight(name, "-")
}

// templateName produces a per-deployment Template CR name. Mirrors
// Python's `_template_name`.
func templateName(dep Deployment) string {
	return "rd-" + runeSlice(dep.ID, 8) + "-install"
}

// siteLabel produces the Hardware.metadata.facility code. Mirrors
// Python's `_site_label`.
func siteLabel(dep Deployment) string {
	return "site-" + runeSlice(dep.SiteID, 8)
}

// runeSlice returns the first n runes of s. Python's `str[:n]`
// equivalent; the byte-slice `s[:n]` would split multi-byte UTF-8.
func runeSlice(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// labels are the common labels every CR carries. Mirrors Python's
// `_labels`. Order matters for byte-parity but yaml.v2 alphabetizes
// map keys; the chosen Python label names happen to sort
// alphabetically in the order Python's dict insertion produces, so
// parity holds without explicit ordering.
func labels(dep Deployment, node *Node) map[string]string {
	out := map[string]string{
		"dcim.region-deployment":      dep.ID,
		"dcim.region-deployment-name": dep.Name,
	}
	if node != nil {
		out["dcim.region-deployment-node"] = node.ID
	}
	return out
}

// partitionSecretRef splits "namespace/name" into (namespace, name).
// Mirrors Python's `(node.bmc_creds_secret_ref or "/").partition("/")`
// where `partition` returns (head, sep, tail). Cases:
//   - "ns/name"  → ("ns",  "name")    — normal case
//   - "name"     → ("name", "")       — Python's partition with no
//                                       separator puts the whole
//                                       string in the head slot
//   - ""         → ("",    "")        — Python falls back to "/"
//                                       which partitions to all-empty
//   - "/name"    → ("",    "name")
//   - "ns/"      → ("ns",  "")
func partitionSecretRef(ref string) (ns, name string) {
	if ref == "" {
		return "", ""
	}
	i := strings.Index(ref, "/")
	if i < 0 {
		// No separator — Python's partition puts the whole string
		// in the head, leaving the tail empty.
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}
