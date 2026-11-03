package cli

import (
	"strings"
	"testing"
)

// ADR-0003 F1. A bare image on a hosted item resolves to the env's image push
// base; everything else is untouched.
//
// The table is the contract, row by row, because each row is a different
// REASON rather than a different input:
//
//   - bare + hosted      → the platform owns the registry, so forge supplies it
//   - host-bearing       → the author named one and meant it; verbatim
//   - bare + non-hosted  → nobody owns that registry; unchanged (and KCL still
//     refuses it at render for a cluster workload)
//   - bare + no base     → nothing to resolve under; refused, not guessed
func TestResolveHostedImageBase(t *testing.T) {
	const base = "registry.reliant.dev/org-7"
	for _, tc := range []struct {
		name, pushBase, image, want string
	}{
		{"bare hosted resolves under the push base", base, "hounders", base + "/hounders"},
		{"a multi-segment bare name keeps its path", base, "team/api", base + "/team/api"},
		{"a host-bearing image is verbatim", base, "ghcr.io/acme/api", "ghcr.io/acme/api"},
		{"an image already under the base is verbatim", base, base + "/api", base + "/api"},
		{"a localhost registry is host-bearing", base, "localhost:5051/api", "localhost:5051/api"},
		{"a port-bearing host is host-bearing", base, "127.0.0.1:5000/api", "127.0.0.1:5000/api"},
		{"no push base leaves a bare name bare", "", "hounders", "hounders"},
		{"a trailing slash on the base is not a difference", base + "/", "api", base + "/api"},
		{"an empty image stays empty", base, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveHostedImageBase(tc.pushBase, tc.image); got != tc.want {
				t.Errorf("resolveHostedImageBase(%q, %q) = %q, want %q", tc.pushBase, tc.image, got, tc.want)
			}
		})
	}
}

// The push plan is where build and deploy agree, so the resolution has to
// happen THERE and not only in the helper. A bare hosted workload becomes a
// real push destination; a bare CLUSTER workload does not, exactly as before.
func TestDeclaredImageDestinations_BareHostedResolvesBareClusterDoesNot(t *testing.T) {
	const base = "registry.reliant.dev/org-7"
	ents := &KCLEntities{Workloads: []WorkloadEntity{
		hostedWL("api", func(w *WorkloadEntity) {
			w.Image = "api"
			w.Spec.Image = "api"
			w.Build.Type = "go"
		}),
		clusterWL("worker", "c", "n", func(w *WorkloadEntity) {
			w.Image = "worker"
			w.Build.Type = "go"
		}),
	}}
	got := declaredImageDestinationsWithBase(ents, base)
	if len(got) != 1 {
		t.Fatalf("destinations = %+v, want exactly the hosted one", got)
	}
	if got[0].repository != base+"/api" {
		t.Errorf("hosted destination = %q, want %q", got[0].repository, base+"/api")
	}
	// The cluster workload is absent, and that is not an oversight: no
	// platform owns its registry, so a base borrowed from the control plane
	// would push it somewhere nobody declared.
	for _, d := range got {
		if d.workload == "worker" {
			t.Errorf("a bare CLUSTER image became a destination at %q", d.repository)
		}
	}
}

// A hosted FRONTEND's bare image resolves the same way, and the platform's
// static.v1 layout is appended to the RESULT — so the release key is the
// address the site was pushed to, which is the invariant
// hosted_static_release_ref_test.go pins on the deploy side.
func TestDeclaredImageDestinations_BareHostedFrontendGetsBaseThenStaticLayout(t *testing.T) {
	const base = "registry.reliant.dev/org-7"
	ents := &KCLEntities{Frontends: []FrontendEntity{{
		Name: "web", Image: "web", Runtime: FrontendRuntime{Type: RuntimeHosted},
	}}}
	got := declaredImageDestinationsWithBase(ents, base)
	if len(got) != 1 || got[0].repository != base+"/web/static.v1" {
		t.Fatalf("destinations = %+v, want %s/web/static.v1", got, base)
	}
}

// With no push base there is nothing to resolve a bare hosted image under.
// forge refuses rather than inventing a default, and the message is the
// pre-ADR-0003 remedy, because declaring the reference is the only thing the
// author can do from here.
func TestCheckHostedImagesResolve_NoPushBaseNamesTheFullReferenceRemedy(t *testing.T) {
	// Spec.Image is set as well as Image, because that is what the render
	// produces: the resolved reference a runtime pulls lands on the spec,
	// and for a BARE hosted image the render keeps it bare rather than
	// inventing a host (positive_hosted_image_bare_name.k). Setting only
	// Image would describe a workload no render emits.
	ents := &KCLEntities{Workloads: []WorkloadEntity{
		hostedWL("api", func(w *WorkloadEntity) {
			w.Image = "api"
			w.Spec.Image = "api"
			w.Build.Type = "go"
		}),
	}}
	err := checkHostedImagesResolve("prod", ents, "")
	if err == nil {
		t.Fatal("a bare hosted image with no push base must be refused")
	}
	// The remedy is to AUTHENTICATE, not to write a full reference or declare an
	// org: a hosted author's registry is the platform's, and the org is the
	// credential's.
	for _, want := range []string{"names no registry host", "could not learn your org", "forge login", `"api"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%v", want, err)
		}
	}
	// With a base, the same declaration is fine.
	if err := checkHostedImagesResolve("prod", ents, "registry.reliant.dev/org-7"); err != nil {
		t.Errorf("a bare hosted image WITH a push base must be accepted, got: %v", err)
	}
	// A bare image on a cluster workload is not this check's business: KCL
	// refuses it at render, and reporting it here would give one mistake two
	// different messages.
	cluster := &KCLEntities{Workloads: []WorkloadEntity{
		clusterWL("worker", "c", "n", func(w *WorkloadEntity) {
			w.Image = "worker"
			w.Build.Type = "go"
		}),
	}}
	if err := checkHostedImagesResolve("prod", cluster, ""); err != nil {
		t.Errorf("a bare CLUSTER image is KCL's refusal, not this one, got: %v", err)
	}
}
