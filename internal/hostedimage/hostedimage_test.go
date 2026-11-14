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
			name:  "an UNBUILT third-party image is mirrored, not refused",
			items: []Item{{Owner: "whoami", Image: "docker.io/traefik/whoami:v1.10.3", Unbuilt: true}},
			base:  base, want: nil,
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

// The mirror address keeps the source's whole path under the base, so two
// sources sharing a last segment cannot collide in one repository, and there
// is nothing to mirror for an image the platform can already pull.
func TestMirrorRepository(t *testing.T) {
	for _, tc := range []struct{ name, base, image, want string }{
		{"a Docker Hub image keeps its path", base, "docker.io/traefik/whoami:v1.10.3", base + "/traefik/whoami"},
		{"a library image keeps library/", base, "docker.io/library/nginx@sha256:abc", base + "/library/nginx"},
		{"Docker Hub's implicit library/ is written out", base, "docker.io/nginx:1.27", base + "/library/nginx"},
		{"index.docker.io is Docker Hub too", base, "index.docker.io/nginx", base + "/library/nginx"},
		{"another registry's one-segment path is verbatim", base, "ghcr.io/nginx", base + "/nginx"},
		{"another registry's path", base, "ghcr.io/acme/api", base + "/acme/api"},
		{"a port-bearing host is dropped too", base, "localhost:5000/team/api:dev", base + "/team/api"},
		{"a bare image resolves, it is not mirrored", base, "whoami", ""},
		{"an image already under the base is not mirrored", base, base + "/api:v1", ""},
		{"no base mirrors nothing", "", "docker.io/traefik/whoami", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MirrorRepository(tc.base, tc.image); got != tc.want {
				t.Errorf("MirrorRepository(%q, %q) = %q, want %q", tc.base, tc.image, got, tc.want)
			}
		})
	}
}

// A destination the registry would refuse is caught before anything is copied:
// a path component outside the distribution grammar, or a name too long.
func TestValidateMirror(t *testing.T) {
	for _, tc := range []struct {
		name, dest, wantErr string
	}{
		{"a normal mirror", base + "/traefik/whoami", ""},
		{"separators the grammar allows", base + "/a.b_c__d--e/f", ""},
		{"an uppercase component", base + "/Acme/api", `"Acme"`},
		{"a leading separator", base + "/-api", `"-api"`},
		{"an empty component", base + "//api", `""`},
		{"too long", base + "/" + strings.Repeat("a", 255), "over the 255"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMirror(tc.dest)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateMirror(%q) = %v, want nil", tc.dest, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateMirror(%q) = %v, want an error containing %s", tc.dest, err, tc.wantErr)
			}
		})
	}
}

// Every spelling of one Docker Hub repository is one source; another
// registry's same path is not.
func TestCanonicalSource(t *testing.T) {
	for _, in := range []string{"docker.io/nginx:1", "index.docker.io/library/nginx", "registry-1.docker.io/nginx@sha256:abc"} {
		if got := CanonicalSource(in); got != "docker.io/library/nginx" {
			t.Errorf("CanonicalSource(%q) = %q, want docker.io/library/nginx", in, got)
		}
	}
	if got := CanonicalSource("ghcr.io/nginx"); got != "ghcr.io/nginx" {
		t.Errorf("CanonicalSource(ghcr.io/nginx) = %q", got)
	}
}

// A workload literal that declares a build is BUILT; one that does not is a
// third-party image, which the scan marks so OffBase does not report it.
func TestScanTreeMarksUnbuiltWorkloads(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "prod/main.k", `
import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        build = forge.DockerBuild {dockerfile = "Dockerfile"}
        runtime = forge.OnHosted {}
    }, fw.Workload {
        name = "whoami"
        image = "docker.io/traefik/whoami:v1.10.3"
        runtime = forge.OnHosted {}
    }]
}
`)
	got := ScanTree(dir)
	if len(got) != 2 || got[0].Owner != "api" || got[0].Unbuilt || got[1].Owner != "whoami" || !got[1].Unbuilt {
		t.Fatalf("ScanTree = %+v, want api built and whoami unbuilt", got)
	}
	if f := OffBase(got, ""); len(f) != 1 || f[0].Owner != "api" {
		t.Errorf("OffBase = %+v, want only the BUILT api", f)
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
