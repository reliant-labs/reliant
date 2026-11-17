package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// frontendRenderJSON builds the render shape a real project emits: the
// `output` contract, carrying the applied stream (`manifests`) and the
// frontend declarations. frontends is spliced in verbatim so
// each test states only the declaration under test.
func frontendRenderJSON(frontends string) string {
	return `{"output":{
	  "manifests":[
	    {"apiVersion":"apps/v1","kind":"Deployment",
	     "metadata":{"name":"api","namespace":"ns"},
	     "spec":{"template":{"spec":{"containers":[{"name":"api","image":"api:1"}]}}}}
	  ],
	  "frontends":[` + frontends + `]
	}}`
}

// missingShippingFrontend is the case this check exists for: an IN-REPO
// path, a forge.OnFirebase runtime, and no directory. It claims to
// ship and there is nothing to build.
const missingShippingFrontend = `{"name":"web","type":"vite","path":"frontends/web",
	"runtime":{"type":"firebase","project":"proj-prod","site":"proj-prod"}}`

// missingBuildOnlyFrontend is on forge.BuildOnly, which makes no shipping
// claim: "compile-checked only".
const missingBuildOnlyFrontend = `{"name":"internal-console","type":"nextjs","path":"frontends/internal-console",
	"runtime":{"type":"build-only"}}`

// siblingRepoFrontend is control-plane's reliant-web: a path pointing OUT
// of this repository, at a checkout whose presence is a property of the
// machine rather than of this repository's configuration.
const siblingRepoFrontend = `{"name":"reliant-web","type":"vite","path":"../reliant/web",
	"runtime":{"type":"firebase","project":"reliant-prod","site":"reliant-prod"}}`

// projectWithFrontends writes a forge.yaml declaring the named frontends
// and returns its directory.
func projectWithFrontends(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	body := "name: proj\nmodule_path: example.com/proj\n"
	if len(names) > 0 {
		body += "frontends:\n"
		for _, n := range names {
			body += "    - name: " + n + "\n      type: nextjs\n      path: frontends/" + n + "\n"
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	return dir
}

// withFrontendDir creates the on-disk source tree for a frontend, which
// is the single fact this check reads about the filesystem.
func withFrontendDir(t *testing.T, projectDir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(projectDir, "frontends", n), 0o755); err != nil {
			t.Fatalf("mkdir frontend %s: %v", n, err)
		}
	}
}

// envForDrift wires a pre-seeded render to a project dir, the way the
// deploy checks see it in production after the first render memoises.
func envForDrift(t *testing.T, projectDir string, renders ...envRender) *Environment {
	t.Helper()
	e := envWithRender(renders)
	e.ProjectDir = projectDir
	return e
}

// THE BUG. A frontend with a FirebaseHosting target and no source tree
// is deployable in principle and unbuildable in practice: `npm run
// build` cannot run in a directory that does not exist. Nothing else
// reports it — the sibling forge.yaml→filesystem walk only covers what
// forge.yaml lists, which by construction this does not.
func TestFrontendCode_MissingWithDeployTargetIsAnError(t *testing.T) {
	dir := projectWithFrontends(t)
	env := envForDrift(t, dir,
		renderFromJSON(t, "preprod", frontendRenderJSON(missingShippingFrontend)),
		renderFromJSON(t, "prod", frontendRenderJSON(missingShippingFrontend)))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusFail {
		t.Fatalf("Status = %q, want %q — a frontend that declares a deploy target "+
			"claims to ship and has no code to build; that contradiction is an error.\n"+
			"message: %s\nevidence: %s", got.Status, StatusFail, got.Message, got.Evidence)
	}

	// The finding must name the frontend, both environments declaring it,
	// where forge looked, and the remedy.
	for _, want := range []string{
		"web",
		"deploy/kcl/{preprod,prod}/main.k",
		"frontends/web/ does not exist",
		"forge scaffold frontend web",
	} {
		if !strings.Contains(got.Evidence, want) {
			t.Errorf("evidence does not mention %q — a finding the reader cannot act on "+
				"is the failure mode this check exists to fix.\nevidence: %s", want, got.Evidence)
		}
	}
}

// The remedy must never be "add it to forge.yaml frontends[]". That key
// is the CODEGEN inventory: adding a name to it creates no code, and for
// a frontend owned by another repository it would project this project's
// generated TypeScript into the sibling working tree. The old check
// printed exactly that advice.
func TestFrontendCode_RemedyIsNotForgeYAML(t *testing.T) {
	env := envForDrift(t, projectWithFrontends(t),
		renderFromJSON(t, "prod", frontendRenderJSON(missingShippingFrontend)))

	got := CheckFrontendCode(context.Background(), env)
	if strings.Contains(got.Evidence, "forge.yaml") {
		t.Errorf("the finding tells the reader to edit forge.yaml — that key is the codegen "+
			"inventory and adding a name to it produces no source tree.\nevidence: %s", got.Evidence)
	}
}

// forge.BuildOnly makes no shipping claim. Missing code there is a
// likely oversight — a declaration written ahead of `forge scaffold
// frontend` — rather than a contradiction, so it must not fail the gate.
func TestFrontendCode_BuildOnlyIsAWarningNotAnError(t *testing.T) {
	env := envForDrift(t, projectWithFrontends(t),
		renderFromJSON(t, "dev", frontendRenderJSON(missingBuildOnlyFrontend)))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusWarn {
		t.Fatalf("Status = %q, want %q — forge.BuildOnly makes no shipping claim, so "+
			"missing code must not fail the deployability gate.\nmessage: %s\nevidence: %s",
			got.Status, StatusWarn, got.Message, got.Evidence)
	}
	if !strings.Contains(got.Evidence, "internal-console") {
		t.Errorf("evidence does not name the frontend: %s", got.Evidence)
	}
}

// Code on disk is the steady state and must be silent, whether or not
// forge.yaml lists the frontend. A check that fires on a correct project
// is worse than no check — it trains the reader to ignore the whole
// report.
func TestFrontendCode_PresentDirectoryIsClean(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared []string
	}{
		{"declared in forge.yaml", []string{"web"}},
		{"declared only in KCL", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := projectWithFrontends(t, tc.declared...)
			withFrontendDir(t, dir, "web")
			env := envForDrift(t, dir,
				renderFromJSON(t, "prod", frontendRenderJSON(missingShippingFrontend)))

			got := CheckFrontendCode(context.Background(), env)
			if got.Status != StatusPass {
				t.Fatalf("Status = %q, want %q — the source tree is on disk.\n"+
					"message: %s\nevidence: %s", got.Status, StatusPass, got.Message, got.Evidence)
			}
		})
	}
}

// THE SECOND BUG. A cross-repo `source:` pin is precisely how a project
// declares "this frontend's code lives elsewhere and forge materializes
// it at build time". `forge build prod --target reliant-web` resolves
// the pin into a local cache and builds it, so reporting it as
// unbuildable describes a failure that does not happen.
func TestFrontendCode_SourcePinnedFrontendIsBuildable(t *testing.T) {
	dir := projectWithFrontends(t) // declares no frontends at all
	pinned := `{"name":"reliant-web","type":"vite","path":"",
		"source":{"repo":"github.com/reliant-labs/reliant","ref":"v1.7.11"},
		"runtime":{"type":"firebase","site":"reliant-prod"}}`
	env := envForDrift(t, dir, renderFromJSON(t, "prod", frontendRenderJSON(pinned)))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("Status = %q, want %q — a source-pinned frontend's code is materialized "+
			"from the pin at build time, so forge can build it.\nmessage: %s\nevidence: %s",
			got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// A path pointing OUT of the tree names a checkout whose presence is a
// property of the machine, not of this repository's configuration.
// Reporting it would fail on a bare CI checkout and pass on every
// developer's machine — and every deployability check is required to be
// answerable in CI. A verdict that flips with the working copy is worse
// than no verdict.
func TestFrontendCode_SiblingRepoPathIsNotReported(t *testing.T) {
	env := envForDrift(t, projectWithFrontends(t),
		renderFromJSON(t, "prod", frontendRenderJSON(siblingRepoFrontend)))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("Status = %q, want %q — ../reliant/web is another repository's checkout; "+
			"its presence is not a fact about this repo.\nmessage: %s\nevidence: %s",
			got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// The opposite direction is not this check's business. A frontend in
// forge.yaml that no environment's KCL declares is the ordinary shape of
// a dev-only frontend.
func TestFrontendCode_ForgeYAMLOnlyIsNotReported(t *testing.T) {
	env := envForDrift(t, projectWithFrontends(t, "docs-site"),
		renderFromJSON(t, "prod", frontendRenderJSON("")))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("Status = %q, want %q — a dev-only frontend declared in forge.yaml and "+
			"in no environment is legitimate.\nmessage: %s\nevidence: %s",
			got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// One frontend, four environments, ONE finding. The fix is a single
// directory, so a per-environment finding would print the same
// instruction four times.
func TestFrontendCode_OneFindingPerFrontendAcrossEnvs(t *testing.T) {
	var renders []envRender
	for _, e := range []string{"dev", "staging", "preprod", "prod"} {
		renders = append(renders, renderFromJSON(t, e, frontendRenderJSON(missingShippingFrontend)))
	}
	env := envForDrift(t, projectWithFrontends(t), renders...)

	got := CheckFrontendCode(context.Background(), env)
	if n := strings.Count(got.Evidence, "frontends[name=web]"); n != 1 {
		t.Fatalf("frontend reported %d times, want 1 — the remedy is one directory.\nevidence: %s",
			n, got.Evidence)
	}
	if !strings.Contains(got.Evidence, "deploy/kcl/{dev,preprod,prod,staging}/main.k") {
		t.Errorf("the finding does not name every environment declaring it: %s", got.Evidence)
	}
}

// A project mixing both cases rolls up to the worse one: a shipping
// frontend with no code fails the gate even when a build-only one is
// also missing.
func TestFrontendCode_ShippingDriftDominatesBuildOnly(t *testing.T) {
	env := envForDrift(t, projectWithFrontends(t),
		renderFromJSON(t, "prod", frontendRenderJSON(missingShippingFrontend+","+missingBuildOnlyFrontend)))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusFail {
		t.Fatalf("Status = %q, want %q — one of the two claims to ship.\nevidence: %s",
			got.Status, StatusFail, got.Evidence)
	}
	for _, want := range []string{"frontends[name=web]", "internal-console"} {
		if !strings.Contains(got.Evidence, want) {
			t.Errorf("evidence drops %q — findings are batched so one round-trip fixes all of them.\n%s",
				want, got.Evidence)
		}
	}
}

// A hosted frontend ships too — the platform publishes its build. The
// runtime is named in the finding so the reader knows what the project
// believes it is shipping. A dev server ships nothing and only warns.
func TestFrontendCode_HostedRuntimeAlsoShips(t *testing.T) {
	const hostedFrontend = `{"name":"site","type":"vite","path":"frontends/site","runtime":{"type":"hosted"}}`
	env := envForDrift(t, projectWithFrontends(t),
		renderFromJSON(t, "prod", frontendRenderJSON(hostedFrontend)))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusFail {
		t.Fatalf("Status = %q, want %q — a hosted frontend publishes its build; it ships.\nevidence: %s",
			got.Status, StatusFail, got.Evidence)
	}
	if !strings.Contains(got.Evidence, "the hosted runtime") {
		t.Errorf("the finding does not name the runtime: %s", got.Evidence)
	}

	const devServer = `{"name":"site","type":"vite","path":"frontends/site","runtime":{"type":"host"}}`
	env = envForDrift(t, projectWithFrontends(t), renderFromJSON(t, "dev", frontendRenderJSON(devServer)))
	if got := CheckFrontendCode(context.Background(), env); got.Status != StatusWarn {
		t.Errorf("a dev-served frontend with no code: Status = %q, want %q", got.Status, StatusWarn)
	}
}

// A declaration with no explicit path falls back to the
// frontends/<name> convention every emitter uses, and is judged there.
func TestFrontendCode_EmptyPathUsesTheConvention(t *testing.T) {
	noPath := `{"name":"web","type":"vite","path":"","runtime":{"type":"firebase"}}`
	dir := projectWithFrontends(t)
	env := envForDrift(t, dir, renderFromJSON(t, "prod", frontendRenderJSON(noPath)))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusFail {
		t.Fatalf("Status = %q, want %q — an absent path means frontends/web, which is missing.\n%s",
			got.Status, StatusFail, got.Evidence)
	}
	if !strings.Contains(got.Evidence, "frontends/web/ does not exist") {
		t.Errorf("the finding does not name the conventional path forge looked at: %s", got.Evidence)
	}

	withFrontendDir(t, dir, "web")
	if got := CheckFrontendCode(context.Background(), env); got.Status != StatusPass {
		t.Fatalf("Status = %q, want %q once frontends/web exists.\n%s", got.Status, StatusPass, got.Evidence)
	}
}

// A project with no environments is a --kind cli / library project. It
// SKIPs rather than passing: the check never asked its question.
func TestFrontendCode_NoEnvironmentsSkips(t *testing.T) {
	env := envForDrift(t, projectWithFrontends(t))
	if got := CheckFrontendCode(context.Background(), env); got.Status != StatusSkip {
		t.Fatalf("Status = %q, want %q for a project that declares no environments", got.Status, StatusSkip)
	}
}

// The check no longer needs a forge.yaml at all: the deploy graph and
// the filesystem are its two sources. A project with a KCL frontend and
// no readable config still has a real, answerable question — is the code
// there — and the old Skip would have hidden it.
func TestFrontendCode_NoForgeYAMLStillChecks(t *testing.T) {
	env := envForDrift(t, t.TempDir(),
		renderFromJSON(t, "prod", frontendRenderJSON(missingShippingFrontend)))

	got := CheckFrontendCode(context.Background(), env)
	if got.Status != StatusFail {
		t.Fatalf("Status = %q, want %q — forge.yaml is not an input to this question.\nmessage: %s",
			got.Status, StatusFail, got.Message)
	}
}

// A render that carries no `output` contract at all (a project whose
// output carries only `manifests`, no frontends) must degrade quietly instead of
// erroring the whole check.
func TestFrontendCode_ManifestsOnlyRenderIsQuiet(t *testing.T) {
	body := `{"output":{"manifests":[{"apiVersion":"v1","kind":"Service","metadata":{"name":"s"}}]}}`
	env := envForDrift(t, projectWithFrontends(t, "web"), renderFromJSON(t, "prod", body))

	if got := CheckFrontendCode(context.Background(), env); got.Status != StatusPass {
		t.Fatalf("Status = %q, want %q — a render carrying only `manifests` has no "+
			"frontend contract to check.\nmessage: %s", got.Status, StatusPass, got.Message)
	}
}

// The check belongs to the deployability arm — the one `forge doctor
// --signal deploy` runs in CI. That is the whole point of the placement:
// a frontend with no code must be caught on a checkout, before a deploy
// is attempted.
func TestFrontendCode_IsInTheDeployabilityArm(t *testing.T) {
	for _, c := range deployabilityChecks() {
		if c.name == "Frontend Code" {
			return
		}
	}
	t.Fatal("Frontend Code is not in deployabilityChecks() — it would not run under " +
		"`forge doctor --signal deploy`, the arm CI calls")
}
