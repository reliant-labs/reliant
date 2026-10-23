package kclmigrate

import (
	"strings"
	"testing"
)

// Two shapes the hounders tree writes that the reader still missed after #325.
// Both were found by running the migration against houndersclub's REAL
// pre-#322 deploy/kcl rather than a fixture, and each one silently cost a
// declaration its reference while the env's registry was removed anyway —
// leaving prod unable to render, which is the failure #325 set out to end.
//
// On the real tree #325 completed 1 of the 3 declarations that needed a
// reference (migrate); with these two fixes it completes all 3, matching what
// the hounders agent had to hand-write.

// TestImageRegistry_HostedFrontendGetsItsOwnReference: a frontend bound to
// forge.OnHosted publishes its static build to a registry, so it needs a
// reference by exactly the same rule as a workload, with the same field name.
//
// Pre-#322 it declared none and took the env's ControlPlane.registry. Nothing
// read frontends at all — frontendStartRe was declared and never used — so the
// registry was removed, the frontend kept no reference, and prod refused at
// render with "image is REQUIRED on forge.OnHosted".
//
// The frontend is declared INLINE in the Bundle's `frontends` list, which is
// what forge scaffolds and what hounders wrote. A reader that required a
// top-level `web = forge.Frontend {…}` assignment found nothing on the shape
// most projects have.
func TestImageRegistry_HostedFrontendGetsItsOwnReference(t *testing.T) {
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k": `import forge
import forge.workloads as fw

api = fw.Workload {
    name = "api"
    image = "ghcr.io/reliant-01/api"
    build = forge.GoBuild {cmd = "./cmd/api"}
}
`,
		"deploy/kcl/prod/main.k": `import forge
import forge.workloads as fw
import ..workloads as wl

_bundle = forge.Bundle {
    project = "hounders"
    control_plane = forge.ControlPlane {
        registry = "ghcr.io/reliant-01"
    }
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        public_dir = "out"
        runtime = forge.OnHosted {}
    }]
    workloads = [wl.api | {runtime = forge.OnHosted {}}]
}

output = forge.render(_bundle)
`,
	})

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused() {
		t.Fatalf("refused: %v %v", res.Ambiguous, res.Unaccounted)
	}
	got := read(t, root, "deploy/kcl/prod/main.k")
	if !strings.Contains(got, `image = "ghcr.io/reliant-01/web"`) {
		t.Errorf("the hosted frontend kept no reference, so this env cannot render:\n%s", got)
	}
	if registryAssignRe.MatchString(got) {
		t.Errorf("the ControlPlane registry survived:\n%s", got)
	}
	// The registry was PLACED (on the frontend), so it must not be reported as
	// dropped: "dropped" means nothing needed it, and something did.
	if len(res.Dropped) != 0 {
		t.Errorf("a registry placed on a frontend was reported dropped: %+v", res.Dropped)
	}
}

// TestImageRegistry_EnvLocalRefinementIsABinding: an env that refines a shared
// workload into a local name and binds THAT name is still binding the same
// workload.
//
//	_membership = wl.membership | {env = … CORS_ORIGINS …}
//	workloads   = [_hosted(_membership)]
//
// hounders' prod does this, to give membership the site origin allowed to call
// it. Neither the call reader nor the pipe reader saw it, because the binder's
// argument is `_membership`, not `wl.membership` — so membership looked bound
// nowhere that pulls, got no registry, and the env's registry was removed
// regardless.
func TestImageRegistry_EnvLocalRefinementIsABinding(t *testing.T) {
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k": `import forge
import forge.workloads as fw

membership = fw.Workload {
    name = "membership"
    kind = "service"
    build = forge.GoBuild {cmd = "./cmd/hounders", output_name = "hounders"}
}
`,
		"deploy/kcl/prod/main.k": `import forge
import forge.workloads as fw
import ..workloads as wl

_hosted = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnHosted {}}
}

_membership = wl.membership | {
    env = wl.membership.env | {CORS_ORIGINS = "https://hounders.club"}
}

output = forge.render(forge.Bundle {
    project = "hounders"
    control_plane = forge.ControlPlane {
        registry = "ghcr.io/reliant-01"
    }
    workloads = [_hosted(_membership)]
})
`,
	})

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused() {
		t.Fatalf("refused: %v %v", res.Ambiguous, res.Unaccounted)
	}
	// The artifact is the build's output_name, as forge derives it.
	if got := read(t, root, "deploy/kcl/workloads.k"); !strings.Contains(got, `image = "ghcr.io/reliant-01/hounders"`) {
		t.Errorf("a workload bound through an env-local refinement got no reference:\n%s", got)
	}
	if len(res.Dropped) != 0 {
		t.Errorf("the registry was placed, so nothing should be dropped: %+v", res.Dropped)
	}
}
