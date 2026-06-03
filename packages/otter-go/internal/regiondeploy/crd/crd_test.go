package crd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

// Fixed UUIDs the testdata fixtures were generated against.
const (
	depID   = "11111111-1111-1111-1111-111111111111"
	siteID  = "22222222-2222-2222-2222-222222222222"
	n1ID    = "33333333-3333-3333-3333-333333333333"
	n2ID    = "44444444-4444-4444-4444-444444444444"
	depName = "region-deploy-east"
)

func defaultDeployment() Deployment {
	return Deployment{
		ID:     depID,
		Name:   depName,
		SiteID: siteID,
		Nodes: []Node{
			{
				ID:                n1ID,
				Hostname:          "n01.region.example.mil",
				MAC:               "aa:bb:cc:dd:ee:01",
				PrimaryIPv6:       "fd00:site:42:1::10",
				BMCAddress:        "fd00:site:42:9::10",
				BMCCredsSecretRef: "dcim-bmc/n01-creds",
			},
			{
				ID:                n2ID,
				Hostname:          "n02",
				MAC:               "aa:bb:cc:dd:ee:02",
				PrimaryIPv6:       "", // exercises the empty-string branch
				BMCAddress:        "10.0.0.42",
				BMCCredsSecretRef: "", // exercises the empty-string branch
			},
		},
	}
}

func loadGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertParity(t *testing.T, fixture, got string) {
	t.Helper()
	want := loadGolden(t, fixture)
	if got != want {
		t.Errorf("Python parity drift for %s:\n--- want ---\n%s\n--- got ---\n%s",
			fixture, want, got)
	}
}

func TestHardwareForNode_WithIPv6(t *testing.T) {
	dep := defaultDeployment()
	got, err := DumpYAML(HardwareForNode(dep, dep.Nodes[0]))
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "hardware_n1_with_ipv6.yaml", got)
}

func TestHardwareForNode_NoIPv6(t *testing.T) {
	dep := defaultDeployment()
	got, err := DumpYAML(HardwareForNode(dep, dep.Nodes[1]))
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "hardware_n2_no_ipv6.yaml", got)
}

func TestWorkflowForNode(t *testing.T) {
	dep := defaultDeployment()
	got, err := DumpYAML(WorkflowForNode(dep, dep.Nodes[0],
		"https://example.mil/flatcar.img.bz2",
		`{"ignition":{"version":"3.4.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "workflow_n1.yaml", got)
}

func TestBMCMachineForNode_WithSecret(t *testing.T) {
	dep := defaultDeployment()
	got, err := DumpYAML(BMCMachineForNode(dep, dep.Nodes[0]))
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "bmc_machine_n1.yaml", got)
}

func TestBMCMachineForNode_EmptySecret(t *testing.T) {
	dep := defaultDeployment()
	got, err := DumpYAML(BMCMachineForNode(dep, dep.Nodes[1]))
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "bmc_machine_n2_empty_secret.yaml", got)
}

// TestTemplateForDeployment_Semantic parity-tests the Template CR
// at the parsed-dict level rather than byte-for-byte. The Template
// has YAML-in-YAML (spec.data is a quoted scalar holding another
// YAML document) and PyYAML's long-quoted-scalar line folding does
// not match yaml.v2's, so byte-parity would require reimplementing
// PyYAML's emitter. Semantic equality is the parity Tinkerbell
// actually cares about — same parsed structure on the cluster side.
func TestTemplateForDeployment_Semantic(t *testing.T) {
	dep := defaultDeployment()
	tpl, err := TemplateForDeployment(dep)
	if err != nil {
		t.Fatal(err)
	}
	gotYAML, err := DumpYAML(tpl)
	if err != nil {
		t.Fatal(err)
	}

	gotTree := mustParse(t, gotYAML)
	wantTree := mustParse(t, loadGolden(t, "template.yaml"))

	gotSpec := gotTree["spec"].(map[any]any)
	wantSpec := wantTree["spec"].(map[any]any)

	// First check the outer keys (everything except spec.data) match
	// byte-tree.
	if !sameSubTree(gotTree, wantTree, []string{"spec.data"}) {
		t.Errorf("Template outer tree drift:\n got=%#v\nwant=%#v", gotTree, wantTree)
	}

	// Now parse the inner spec.data string and compare its tree.
	gotInner := mustParse(t, gotSpec["data"].(string))
	wantInner := mustParse(t, wantSpec["data"].(string))
	if !reflect.DeepEqual(gotInner, wantInner) {
		t.Errorf("Template inner-document drift:\n got=%#v\nwant=%#v", gotInner, wantInner)
	}
}

// TestCRDsForDeployment_MultiDoc parity-tests the multi-doc bundle.
// The Template's spec.data scalar bytes differ from PyYAML — see
// TestTemplateForDeployment_Semantic — so we compare each
// document's parsed tree, treating spec.data semantically.
func TestCRDsForDeployment_MultiDoc(t *testing.T) {
	dep := defaultDeployment()
	ignitionFor := map[string]string{
		n1ID: `{"ignition":{"version":"3.4.0","node":"n01"}}`,
		n2ID: `{"ignition":{"version":"3.4.0","node":"n02"}}`,
	}
	crds, err := CRDsForDeployment(dep, "https://example.mil/flatcar.img.bz2", ignitionFor)
	if err != nil {
		t.Fatal(err)
	}
	gotYAML, err := DumpYAMLAll(crds)
	if err != nil {
		t.Fatal(err)
	}

	gotDocs := mustParseAll(t, gotYAML)
	wantDocs := mustParseAll(t, loadGolden(t, "multi_doc.yaml"))

	if len(gotDocs) != len(wantDocs) {
		t.Fatalf("doc count drift: got %d want %d", len(gotDocs), len(wantDocs))
	}

	for i := range gotDocs {
		got, want := gotDocs[i], wantDocs[i]
		kind := want["kind"]
		if kind == "Template" {
			// Normalize inner Template data for comparison.
			gotSpec := got["spec"].(map[any]any)
			wantSpec := want["spec"].(map[any]any)
			gotInner := mustParse(t, gotSpec["data"].(string))
			wantInner := mustParse(t, wantSpec["data"].(string))
			if !reflect.DeepEqual(gotInner, wantInner) {
				t.Errorf("doc %d (Template) inner drift:\n got=%#v\nwant=%#v", i, gotInner, wantInner)
			}
			// Compare the outer modulo spec.data.
			delete(gotSpec, "data")
			delete(wantSpec, "data")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("doc %d (%v) drift:\n got=%#v\nwant=%#v", i, kind, got, want)
		}
	}
}

// TestResourceNameDNS1123 pins the ≤63-char DNS-1123 invariant.
// The Python source mentions this is critical because Tinkerbell's
// admission webhook rejects CR names that violate DNS-1123.
func TestResourceNameDNS1123(t *testing.T) {
	dep := Deployment{ID: depID}
	veryLongHostname := strings.Repeat("a", 100)
	node := Node{Hostname: veryLongHostname + ".long.domain.example.mil"}
	got := resourceName(dep, &node)
	if len(got) > 63 {
		t.Errorf("resourceName must be ≤63 chars; got %d: %q", len(got), got)
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("resourceName must not end with `-` (DNS-1123 rule); got %q", got)
	}
}

// TestPartitionSecretRef pins the namespace/name split semantics
// to match Python's str.partition("/") return order: head goes to
// ns, tail goes to name. Specifically the no-separator case
// returns (input, "") — NOT ("", input).
func TestPartitionSecretRef(t *testing.T) {
	cases := []struct {
		in            string
		wantNs, wantN string
	}{
		{"dcim-bmc/n01-creds", "dcim-bmc", "n01-creds"},
		{"", "", ""},
		// No-slash: Python's partition puts the whole string in head
		// (ns), tail (name) is empty.
		{"name-only", "name-only", ""},
		{"/leading-slash", "", "leading-slash"},
		{"ns/", "ns", ""},
	}
	for _, c := range cases {
		ns, n := partitionSecretRef(c.in)
		if ns != c.wantNs || n != c.wantN {
			t.Errorf("partitionSecretRef(%q) = (%q, %q); want (%q, %q)", c.in, ns, n, c.wantNs, c.wantN)
		}
	}
}

// ─── helpers ───────────────────────────────────────────────────────

func mustParse(t *testing.T, s string) map[any]any {
	t.Helper()
	var m map[any]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("yaml parse: %v\n---\n%s", err, s)
	}
	return m
}

func mustParseAll(t *testing.T, s string) []map[any]any {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(s))
	var out []map[any]any
	for {
		var m map[any]any
		err := dec.Decode(&m)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			// yaml.v2 returns io.EOF wrapped — check string match
			// since we don't want to import io just for this.
			if strings.Contains(err.Error(), "EOF") {
				break
			}
			t.Fatalf("yaml.Decoder: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// sameSubTree compares two yaml-parsed trees while ignoring paths
// in `skip` (dot-separated). For TestTemplateForDeployment_Semantic
// we compare the outer shape but skip "spec.data" which gets
// compared structurally (parsed tree) instead.
func sameSubTree(a, b map[any]any, skip []string) bool {
	// Build a copy of each map without the skipped paths.
	aCopy := copyAndPrune(a, skip)
	bCopy := copyAndPrune(b, skip)
	return reflect.DeepEqual(aCopy, bCopy)
}

func copyAndPrune(m map[any]any, skip []string) map[any]any {
	out := make(map[any]any, len(m))
	for k, v := range m {
		ks := toString(k)
		matched := false
		var subSkip []string
		for _, s := range skip {
			parts := strings.SplitN(s, ".", 2)
			if parts[0] != ks {
				continue
			}
			if len(parts) == 1 {
				matched = true
				break
			}
			subSkip = append(subSkip, parts[1])
		}
		if matched {
			continue
		}
		if subM, ok := v.(map[any]any); ok && len(subSkip) > 0 {
			out[k] = copyAndPrune(subM, subSkip)
		} else {
			out[k] = v
		}
	}
	return out
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
