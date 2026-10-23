//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EComponentsKCLDeclaredFromSources pins that a scaffolded project's
// deploy/kcl/workloads.k ends up declaring every service the project has,
// and that the envs render from the tracked tree with NOTHING generated
// first.
//
// The second half is the whole point of workloads.k being a tracked, scaffolded
// file rather than a generated one: this test deletes every gitignored
// artifact before rendering, which is what a fresh clone actually looks like.
// The predecessor design read its component list from a gitignored generated
// file, and so rendered ZERO manifests SILENTLY at that point.
func TestE2EComponentsKCLDeclaredFromSources(t *testing.T) {
	t.Parallel()

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// Two mounted services on one shared binary — the multi-entity dogfood
	// shape that surfaced the empty-component-list defect.
	runCmd(t, dir, forgeBin,
		"project", "new", "multiapp",
		"--mod", "example.com/multiapp",
		"--binary", "shared",
		"--service", "order",
		"--service", "intake",
	)
	projectDir := filepath.Join(dir, "multiapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Re-run the pipeline explicitly so the assertion covers `forge generate`,
	// not only the one-shot scaffold path.
	runCmd(t, projectDir, forgeBin, "generate")

	assertWorkloadsKCL := func(stage string) {
		raw := readFileE2E(t, filepath.Join(projectDir, "deploy", "kcl", "workloads.k"))
		for _, svc := range []string{"order", "intake"} {
			if !strings.Contains(raw, `name = "`+svc+`"`) {
				t.Fatalf("[%s] workloads.k does not declare the %q service:\n%s", stage, svc, raw)
			}
		}
		// Declared as kind="service" specifically: the kind is what selects
		// the expansion, so a service landing as anything else renders the
		// wrong resources (no Service object, in particular).
		if got := strings.Count(raw, `kind = "service"`); got < 2 {
			t.Fatalf("[%s] workloads.k should declare both services as kind=\"service\"; got %d:\n%s", stage, got, raw)
		}
		// ...and both are named in the aggregation list, or they exist as
		// declarations nothing deploys.
		for _, ident := range []string{"order", "intake"} {
			if !strings.Contains(raw, "ALL: [fw.Workload] = [") || !strings.Contains(raw, ident) {
				t.Fatalf("[%s] workloads.k must name %q in ALL:\n%s", stage, ident, raw)
			}
		}
	}
	assertWorkloadsKCL("forge generate")

	// BUG 2 rides the same scaffold: the KCL-native dev config.k must carry the
	// runtime MODE marker so the env is positively "development" (the seed gate
	// + dev-auth classifier read it). Staging inherits the schema default.
	devConfigK := readFileE2E(t, filepath.Join(projectDir, "deploy", "kcl", "dev", "config.k"))
	if !strings.Contains(devConfigK, `environment = "development"`) {
		t.Fatalf("dev config.k must mark the env development; got:\n%s", devConfigK)
	}
	stagingConfigK := readFileE2E(t, filepath.Join(projectDir, "deploy", "kcl", "staging", "config.k"))
	if strings.Contains(stagingConfigK, `environment = "development"`) {
		t.Fatalf("staging config.k must NOT be marked development; got:\n%s", stagingConfigK)
	}

	// THE FRESH-CLONE ACCEPTANCE TEST.
	//
	// Delete every gitignored artifact, leaving only what a `git clone`
	// would give you, and render. Deploy has to work from the tracked tree
	// alone — that is what it means for KCL to be the source of truth. This
	// is the exact scenario the generated-inventory design failed silently:
	// its input was gitignored, so a clean checkout rendered `app_config`
	// and nothing else, with no error to explain why.
	//
	// kcl is REQUIRED here: skipped by name on a laptop, hard failure in CI.
	requireTool(t, "kcl")
	runCmd(t, projectDir, "git", "clean", "-xdf", "--", "deploy")

	// Render THROUGH FORGE: the project's KCL imports kcl_plugin.forge, a
	// namespace only the forge process registers, so a bare `kcl run` fails
	// with "the plugin package `kcl_plugin.forge` is not found" regardless
	// of the tree's state — which would mask exactly the clean-tree
	// regression this test exists to catch.
	out := runCmdOutput(t, projectDir, forgeBin, "env", "render", "dev")
	// Require the workloads to be BOUND and lowered, not merely named: a
	// render that produced only scalars would still mention them in labels.
	// The scaffolded dev env binds to the host runtime (ADR 0002, #291), so
	// the proof is the host launch line `forge env up` would run, not a
	// Deployment, which a host-bound dev env correctly never renders.
	//
	// The services are served by ONE process (#529): `_api`, the binary's
	// `server`, mounts every service on one Connect mux and runs under air.
	// So the dev render carries one API line, and the migrate job — which
	// dev/main.k binds straight out of workloads.k (`wl.migrate`) — is what
	// proves workloads.k itself was read from the clean tree.
	for _, want := range []string{
		"api (service): air -c .air.toml",
		"migrate (job): go run ./cmd/multiapp db migrate up",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dev KCL render from a clean tree did not bind %q — the workload did not lower:\n%s", want, out)
		}
	}
	// ...and NOT a host process per service. That was the dev loop #529
	// replaced: each service on its own port, the frontend reaching only the
	// first, its sign-in cookie stranded on that one origin.
	for _, svc := range []string{"order", "intake"} {
		if bad := svc + " (service):"; strings.Contains(out, bad) {
			t.Fatalf("dev render runs %q as its own host process; every service belongs in the one `server` process:\n%s", svc, out)
		}
	}
	assertWorkloadsKCL("fresh clone")
}
