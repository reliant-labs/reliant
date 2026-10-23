package hostedimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const base = "registry.reliant.dev/org-7"

// OffBase reports only what it should: a host the platform will not admit.
// The three non-findings are each a different reason, and collapsing any of
// them into a finding would make the check noise an author learns to ignore.
func TestOffBase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []Item
		base  string
		want  []string // owners expected to be reported
	}{
		{
			name:  "a foreign host is reported",
			items: []Item{{Owner: "api", Image: "ghcr.io/acme/api"}},
			base:  base, want: []string{"api"},
		},
		{
			name:  "a BARE image is the resolved shape, not a finding",
			items: []Item{{Owner: "api", Image: "api"}},
			base:  base, want: nil,
		},
		{
			name:  "an image already under the base is redundant, not wrong",
			items: []Item{{Owner: "api", Image: base + "/api"}},
			base:  base, want: nil,
		},
		{
			name:  "the base itself is under the base",
			items: []Item{{Owner: "api", Image: base}},
			base:  base, want: nil,
		},
		{
			name:  "a tag or digest does not change the judgement",
			items: []Item{{Owner: "api", Image: base + "/api:v1"}},
			base:  base, want: nil,
		},
		{
			name:  "a near-miss prefix is NOT under the base",
			items: []Item{{Owner: "api", Image: base + "-evil/api"}},
			base:  base, want: []string{"api"},
		},
		{
			name:  "with no base a host-bearing image is still reported, weakly",
			items: []Item{{Owner: "api", Image: "ghcr.io/acme/api"}},
			base:  "", want: []string{"api"},
		},
		{
			name:  "with no base a bare image is still fine",
			items: []Item{{Owner: "api", Image: "api"}},
			base:  "", want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := OffBase(tc.items, tc.base)
			var owners []string
			for _, f := range got {
				owners = append(owners, f.Owner)
			}
			if strings.Join(owners, ",") != strings.Join(tc.want, ",") {
				t.Errorf("OffBase(%+v, %q) reported %v, want %v", tc.items, tc.base, owners, tc.want)
			}
		})
	}
}

// The two messages claim different amounts, and that is the whole point of the
// Verified split: forge must not assert "outside the registry" about an image
// it never compared.
func TestFindingMessageClaimsOnlyWhatWasVerified(t *testing.T) {
	verified := Finding{Owner: "api", Image: "ghcr.io/acme/api", Base: base}
	if !verified.Verified() {
		t.Fatal("a finding with a base is verified")
	}
	msg := verified.Message()
	for _, want := range []string{"outside this org's image push base", base, `image = "api"`, base + "/api"} {
		if !strings.Contains(msg, want) {
			t.Errorf("verified message missing %q:\n%s", want, msg)
		}
	}

	unverified := Finding{Owner: "api", Image: "ghcr.io/acme/api"}
	if unverified.Verified() {
		t.Fatal("a finding with no base is not verified")
	}
	msg = unverified.Message()
	for _, want := range []string{"host-bearing image", "admits only its own registry", "drop the host"} {
		if !strings.Contains(msg, want) {
			t.Errorf("unverified message missing %q:\n%s", want, msg)
		}
	}
	// It must NOT claim the image is outside the base: forge did not look.
	for _, gone := range []string{"outside", base} {
		if strings.Contains(msg, gone) {
			t.Errorf("unverified message asserts %q, which forge never checked:\n%s", gone, msg)
		}
	}

	// Verified narrows to exactly the provable half.
	if got := Verified([]Finding{verified, unverified}); len(got) != 1 || got[0] != verified {
		t.Errorf("Verified() = %+v, want only the finding judged against a base", got)
	}
}

// The scan joins an image declared in workloads.k to a runtime bound in an
// env's main.k, because that split is the shape real projects use. A scan
// that only matched same-literal declarations would see almost nothing.
func TestScanTreeJoinsSplitDeclarations(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "workloads.k", `
import forge.workloads as fw

api = fw.Workload {
    name = "api"
    image = "ghcr.io/acme/api"
}
worker = fw.Workload {
    name = "worker"
    image = "ghcr.io/acme/worker"
}
`)
	write(t, dir, "prod/main.k", `
import forge
import workloads as wl

_bundle = forge.Bundle {
    project = "acme"
    workloads = [
        wl.api | {runtime = forge.OnHosted {}},
        wl.worker | {runtime = forge.OnCluster {cluster = "c"}},
    ]
}
`)
	got := ScanTree(dir)
	if len(got) != 1 || got[0].Owner != "api" || got[0].Image != "ghcr.io/acme/api" {
		t.Fatalf("ScanTree = %+v, want only the OnHosted workload with its declared image", got)
	}
}

// A workload declaring its image and runtime in ONE literal is seen too.
func TestScanTreeReadsASingleLiteral(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "prod/main.k", `
import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        runtime = forge.OnHosted {}
    }]
}
`)
	got := ScanTree(dir)
	if len(got) != 1 || got[0].Owner != "api" {
		t.Fatalf("ScanTree = %+v, want the api workload", got)
	}
}

// Prose that TEACHES the format must not be read as the format. The
// scaffolded workloads.k documents its own shape, including worked examples
// with real-looking images — exactly the trap lint_workload_drift hit.
func TestScanTreeIgnoresCommentsAndDocstrings(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "prod/main.k", `
"""
An example, not a declaration:

    name = "example"
    image = "ghcr.io/example/example"
    runtime = forge.OnHosted {}
"""
import forge

# name = "commented"
# image = "ghcr.io/acme/commented"
# runtime = forge.OnHosted {}

_bundle = forge.Bundle {project = "acme"}
`)
	if got := ScanTree(dir); len(got) != 0 {
		t.Fatalf("ScanTree = %+v, want nothing: every declaration here is prose", got)
	}
}

// A project with no deploy tree yields nothing and no error: a lint must not
// fail over a directory it merely hoped to find.
func TestScanTreeMissingTreeIsEmpty(t *testing.T) {
	if got := ScanTree(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Fatalf("ScanTree(missing) = %+v, want empty", got)
	}
}

// PushBase is the whole resolution rule, so the grammar it produces is
// pinned here: it must match the registry's own `<org>/<project>/...` layout
// (cp's internal/ociregistry), because the address forge pushes to and the
// address the platform admits are the same string or the push is refused.
func TestPushBase(t *testing.T) {
	const org = "4f3c2b1a-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name, host, org, project, want string
	}{
		{"the default host when none is declared", "", org, "shop",
			DefaultRegistryHost + "/" + org + "/shop"},
		{"a declared host wins", "registry.example.com", org, "shop",
			"registry.example.com/" + org + "/shop"},
		{"a trailing slash is not a difference", "registry.example.com/", org, "shop",
			"registry.example.com/" + org + "/shop"},
		// Each missing segment composes NOTHING rather than an address with
		// a hole in it. `<host>//shop` is a reference that looks resolved
		// and cannot be pushed.
		{"no organization composes nothing", "registry.example.com", "", "shop", ""},
		{"no project composes nothing", "registry.example.com", org, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PushBase(tc.host, tc.org, tc.project); got != tc.want {
				t.Errorf("PushBase(%q, %q, %q) = %q, want %q", tc.host, tc.org, tc.project, got, tc.want)
			}
		})
	}
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
