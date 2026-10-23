//go:build e2e

package cli

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"
)

// TestE2EFreshScaffoldLintExitsZero is the acceptance gate for the
// self-consistency invariant: `forge lint` MUST exit 0 on a
// freshly-scaffolded project. Forge's own output has to clear forge's own
// bar — the forge-one-shot workflow gates phases on `forge lint`, so a
// scaffold that fails its own linter makes the scaffold phase structurally
// unpassable.
//
// Scenario (mirrors TestE2EPbThroughCrudPlusCustomRpc): a fresh project
// with a dashboard frontend and, in one service, BOTH a `// forge:entity`
// CRUD entity (with an enum field and an optional field) AND a genuine
// custom RPC. new → author proto → `forge scaffold` → build green →
// `forge lint --no-fix` and asserts:
//
//   - exit 0 with ZERO error findings (no "❌" anywhere) — covering the
//     three defect lanes this test was born RED on:
//     golangci on emitted internal/app wiring (errcheck db.Close /
//     noctx db.Ping / revive blank-imports in providers.go, ST1005
//     capitalized error strings in compose.go); contractlint on
//     generated `<Entity>Columns` + `Inventory` exports and on the
//     internal/app composition seam; scaffold-not-customized firing at
//     error severity on the scaffold's own expected markers;
//   - the scaffold-not-customized findings are still SURFACED, as
//     warnings (fresh scaffold always carries FORGE_SCAFFOLD markers —
//     pending work, not a defect);
//   - the golangci and contractlint lanes actually RAN (a silently
//     skipped lane would make the exit-0 assertion vacuous). The
//     contractlint binary is built from THIS tree and prepended to PATH
//     so a stale globally-installed contractlint can never shadow it.
//
// --no-fix keeps the run read-only: the emitted code must be lint-clean
// as written, not rescued by the auto-fix pre-pass.
func TestE2EFreshScaffoldLintExitsZero(t *testing.T) {
	t.Parallel()
	requireTool(t, "golangci-lint")
	forgeBin := buildforgeBinary(t)
	contractlintDir := buildContractlintBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "lintapp", "--mod", "example.com/lintapp", "--service", "orders", "--frontend", "dashboard")
	projectDir := filepath.Join(dir, "lintapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author, in the service proto: a custom (non-CRUD) RPC AND a
	// `// forge:entity` CRUD entity with an enum field and an optional field.
	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	proto = strings.Replace(proto, "  // TODO: Add your RPC methods here.",
		// Custom mutations declare their activity event (or none), as
		// forgeconv-activity-declared requires.
		"  rpc ArchiveOrder(ArchiveOrderRequest) returns (ArchiveOrderResponse) {\n"+
			"    option (forge.v1.method) = { activity: { verb: \"archive\", target: { kind: \"order\", id: \"id\" } } };\n"+
			"  }\n"+
			"  rpc CreateApiKey(CreateApiKeyRequest) returns (CreateApiKeyResponse) {\n"+
			"    option (forge.v1.method) = { activity: { none: \"fixture: the key is not persisted\" } };\n"+
			"  }", 1)
	proto += `
// forge:entity
message Order {
  string id = 1;
  string customer_name = 2;
  OrderStatus status = 3;
  optional string note = 4;
}

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_PENDING = 1;
  ORDER_STATUS_SHIPPED = 2;
}

message ArchiveOrderRequest {
  string id = 1;
}

message ArchiveOrderResponse {
  bool archived = 1;
}

message CreateApiKeyRequest {}
message CreateApiKeyResponse {}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author orders proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")
	runCmd(t, projectDir, "go", "build", "./...")

	// THE INVARIANT: `forge lint` exits 0 on the fresh scaffold. PATH is
	// prepended with the tree-built contractlint so the contract lane runs
	// against THIS tree's analyzers.
	out, err := runLintE2E(t, projectDir, contractlintDir, forgeBin, "lint", "--no-fix")
	if err != nil {
		t.Fatalf("forge lint must exit 0 on a freshly-scaffolded project, got: %v\n%s", err, out)
	}

	// No error findings at all — every lane clean.
	if strings.Contains(out, "❌") {
		t.Errorf("forge lint printed error findings on a fresh scaffold:\n%s", out)
	}

	// The lanes that were RED before the fix actually ran (not skipped).
	for _, banned := range []string{
		"golangci-lint not found",
		"contractlint not available",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("lint lane was skipped (%q) — the exit-0 assertion is vacuous:\n%s", banned, out)
		}
	}
	if !strings.Contains(out, "✓ golangci-lint") {
		t.Errorf("golangci-lint lane did not run clean:\n%s", out)
	}
	if !strings.Contains(out, "✓ contract linter") {
		t.Errorf("contractlint lane did not run clean:\n%s", out)
	}
	// The scaffold's frontend is a static export (`output: static`) bound to
	// forge.OnHosted in staging and prod, so the static-export lane JUDGES it
	// — and must find nothing: the generated CRUD pages are query-param
	// routes, next/image is unoptimized, and the dev log route is POST-only
	// and dev-guarded. A scaffold this lane flagged would be one whose
	// staging/prod `npm run build` fails, or whose hosted site lacks pages.
	if !strings.Contains(out, "static-export clean — 1 static frontend(s)") {
		t.Errorf("static-export lane did not judge the scaffolded frontend clean:\n%s", out)
	}

	// The scaffold's expected FORGE_SCAFFOLD markers are surfaced as a
	// WARNING — visible, but not gating.
	if !strings.Contains(out, "[scaffold-not-customized]") {
		t.Errorf("scaffold-not-customized warning missing from lint output:\n%s", out)
	}
	if strings.Contains(out, "❌ [scaffold-not-customized]") {
		t.Errorf("scaffold-not-customized fired at error severity:\n%s", out)
	}
}

// TestE2EFreshScaffoldLintExitsZeroForEveryKind holds the same invariant for
// the project kinds TestE2EFreshScaffoldLintExitsZero does not scaffold:
// `--kind cli` and `--kind library`. Neither runs generate, so nothing
// reformats what the templates emit — the cli kind's cmd/<name>/version.go
// once shipped a doc-comment code block indented with spaces, and every new
// CLI project's first `forge lint` failed its format lane. The unit-tier
// tripwire for that shape is internal/generator's
// TestFreshScaffold_GoIsFormattedAndTakesContextFirst; this is the real
// `forge lint` over the real scaffold.
func TestE2EFreshScaffoldLintExitsZeroForEveryKind(t *testing.T) {
	t.Parallel()
	requireTool(t, "golangci-lint")
	forgeBin := buildforgeBinary(t)
	contractlintDir := buildContractlintBinary(t)
	for _, kind := range []string{"cli", "library"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			name := kind + "lintapp"
			runCmd(t, dir, forgeBin, "project", "new", name, "--mod", "example.com/"+name, "--kind", kind)
			projectDir := filepath.Join(dir, name)
			addCorpusForgePkgReplace(t, projectDir)
			runCmd(t, projectDir, "go", "build", "./...")

			out, err := runLintE2E(t, projectDir, contractlintDir, forgeBin, "lint", "--no-fix")
			if err != nil {
				t.Fatalf("forge lint must exit 0 on a freshly-scaffolded --kind %s project, got: %v\n%s", kind, err, out)
			}
			if strings.Contains(out, "❌") {
				t.Errorf("forge lint printed error findings on a fresh --kind %s scaffold:\n%s", kind, out)
			}
			if !strings.Contains(out, "✓ golangci-lint") || strings.Contains(out, "golangci-lint not found") {
				t.Errorf("golangci-lint lane did not run — the exit-0 assertion is vacuous:\n%s", out)
			}
		})
	}
}

// TestE2EComponentScaffoldsLintClean extends the fresh-scaffold-lint
// invariant from the BARE project to the three COMPONENT scaffolds
// TestE2EFreshScaffoldLintExitsZero never exercises: `scaffold worker`,
// `scaffold package --type adapter` and `scaffold webhook`.
//
// That gap shipped. A dogfood run that deliberately targeted these three
// seams found `forge lint` red with SEVEN findings on a project whose
// author had written no Go at all — every one of them in a file forge
// emitted:
//
//	internal/<adapter>/adapter.go             errcheck: defer resp.Body.Close()
//	internal/handlers/<svc>/webhook_x_test.go noctx ×3: httptest.NewRequest
//	internal/handlers/<svc>/webhook_x.go      staticcheck SA4023 ×2
//	internal/workers/<name>/worker.go         requirecontract: no contract.go
//
// The first `forge lint` a user runs teaching them that forge's lint is
// noise is the worst possible day-one lesson: it destroys the signal for
// every real finding afterwards. It also makes the forge-one-shot
// workflow's lint-gated scaffold phase structurally unpassable.
//
// This drives the REAL `forge lint` over a REAL scaffold rather than
// asserting on template strings, which is the property that matters: the
// analyzers' own exemptions apply, so a read that is genuinely exempt
// stays exempt without this test restating an allowlist that would drift.
// Unit-tier tripwires for the same defects live in
// internal/generator/component_scaffold_lint_test.go (source shapes) and
// internal/linter/contract/analyzer_test.go (the requirecontract lane).
//
// --no-fix keeps the run read-only: the emitted code must be lint-clean as
// WRITTEN, not rescued by the auto-fix pre-pass.
func TestE2EComponentScaffoldsLintClean(t *testing.T) {
	t.Parallel()
	requireTool(t, "golangci-lint")
	forgeBin := buildforgeBinary(t)
	contractlintDir := buildContractlintBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "complintapp",
		"--mod", "example.com/complintapp", "--service", "documents")
	projectDir := filepath.Join(dir, "complintapp")
	addCorpusForgePkgReplace(t, projectDir)

	// The three nouns, each of which was independently red. The adapter is
	// scaffolded through both of its spellings: each composes its own config
	// block on AppConfig, so the second must take the next free field number.
	runCmd(t, projectDir, forgeBin, "scaffold", "worker", "share_expiry")
	// The webhook reads the current service descriptor. Add it before the
	// adapters change config.proto (and make that descriptor stale until generate).
	runCmd(t, projectDir, forgeBin, "scaffold", "webhook", "scanner", "--service", "documents")
	runCmd(t, projectDir, forgeBin, "scaffold", "package", "blobstore", "--type", "adapter")
	runCmd(t, projectDir, forgeBin, "scaffold", "adapter", "pricing")

	// A full generate, then build. The generate matters for more than
	// tidiness: it is what writes internal/<adapter>/middleware_gen.go,
	// whose ABSENCE leaves both compose.go's
	// blobstore.NewServiceWithForgeMiddleware call undefined and the owned
	// observe_chain.go seam unreferenced (an `unused` finding). Both
	// resolve here, which is why the scaffold→generate→build order is part
	// of the assertion rather than incidental setup.
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "test", "./internal/blobstore/", "./internal/pricing/")

	out, err := runLintE2E(t, projectDir, contractlintDir, forgeBin, "lint", "--no-fix")
	if err != nil {
		t.Fatalf("forge lint must exit 0 after `scaffold worker` + `scaffold package --type adapter` + "+
			"`scaffold webhook` on a project with ZERO hand-written Go, got: %v\n%s", err, out)
	}
	if strings.Contains(out, "❌") {
		t.Errorf("forge lint printed error findings on freshly scaffolded components:\n%s", out)
	}

	// An adapter's base URL is a leaf of its own config block, not a scalar
	// Deps field (#748): the scaffold used to declare `BaseURL string`, which
	// forge's own forge-config-deps lint reported on every fresh adapter while
	// compose.go wired it to `BaseURL: "", // TODO`. Zero findings, and both
	// adapters take their block from the loaded config by type.
	if !strings.Contains(out, "✓ config-deps lint") || strings.Contains(out, "[forge-config-deps]") {
		t.Errorf("a freshly scaffolded adapter must not trip forge-config-deps:\n%s", out)
	}
	compose := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "compose.go"))
	for _, want := range []string{"g.Cfg.GetBlobstore()", "g.Cfg.GetPricing()"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose.go must fill the adapter's config block from the loaded config (%s):\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "TODO") {
		t.Errorf("compose.go carries a TODO for a freshly scaffolded component:\n%s", compose)
	}

	// Each lane that was RED actually ran — a skipped lane makes exit-0
	// vacuous.
	for _, banned := range []string{"golangci-lint not found", "contractlint not available"} {
		if strings.Contains(out, banned) {
			t.Errorf("lint lane was skipped (%q) — the exit-0 assertion is vacuous:\n%s", banned, out)
		}
	}
	for _, marker := range []string{
		"✓ golangci-lint",
		"✓ contract linter",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("lint lane %q did not run:\n%s", marker, out)
		}
	}

	// The webhook's signature verification must still FAIL CLOSED. The
	// SA4023 repair has an obvious wrong answer — make the verifier return
	// nil — that would silence the linter by scaffolding a webhook which
	// accepts unsigned payloads. Pin the rejection, not just the lint.
	webhook := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "documents", "webhook_scanner.go"))
	if !strings.Contains(webhook, "VerifyHMACSHA256") {
		t.Errorf("the scaffolded webhook no longer verifies signatures — lint-clean must not have "+
			"been bought by accepting unsigned payloads:\n%s", webhook)
	}
}

// TestE2EFreshScaffoldFrontendLintClean extends the fresh-scaffold-lint
// invariant to the FRONTEND lane, which TestE2EFreshScaffoldLintExitsZero
// leaves UNCOVERED: that test never `npm install`s, so lintFrontendDir sees
// no node_modules and silently skips eslint/stylelint/tsc. This test does the
// install, so `forge lint` actually runs the frontend lane, and pins three
// defects that made `forge lint` red — or made a follow-up `forge generate`
// demand --force — on a brand-new `--frontend dashboard` project:
//
//	A. globals.css (theme tokens) must pass the scaffold's own stylelint
//	   as-emitted — no blank line between consecutive custom properties
//	   (custom-property-empty-line-before) and short hex (color-hex-length) —
//	   with NO --fix needed.
//	B. `forge lint`'s eslint --fix must be a NO-OP on EVERY Tier-1 generated
//	   TS/TSX file (their imports are already in eslint's canonical order), so
//	   the bytes are unchanged and a subsequent `forge generate` does NOT trip
//	   the Tier-1 file-stomp guard. The set is discovered from the DO NOT EDIT
//	   banner rather than named, so it survives files moving between the
//	   scaffold and the web-runtime package.
//	C. an entity field written in the protovalidate braced AGGREGATE form
//	   (`(buf.validate.field).int64 = {gte: 1, lte: 12}`) births the same DB
//	   CHECK the dotted form would — the raw birth scan must agree with the
//	   descriptor/drift reading.
//	D. the scaffolded style-lint AUTOFIX (`npm run lint:styles:fix`) must
//	   emit CSS that still parses. Before the stylelint floor was raised to
//	   17.2.0 it rewrote `oklch(0.58 0.09 196)` to `oklch(58.% 0.09 196deg)`
//	   — an invalid <number>, so the browser drops the whole declaration and
//	   the token resolves to nothing — then exited 0, and a subsequent
//	   non-fix run passed too. Nothing in the lane could see it.
func TestE2EFreshScaffoldFrontendLintClean(t *testing.T) {
	t.Parallel()
	requireTool(t, "node", "npm")
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "felintapp", "--mod", "example.com/felintapp", "--service", "catalog", "--frontend", "dashboard")
	projectDir := filepath.Join(dir, "felintapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author a `// forge:entity` carrying an enum (drives the dashboard's
	// status filter), a *_cents field with a DOT-form gte 0, and a units
	// field with the braced AGGREGATE form — plus a min_len string.
	protoPath := filepath.Join(projectDir, "proto", "services", "catalog", "v1", "catalog.proto")
	proto := readFileE2E(t, protoPath)
	if !strings.Contains(proto, `import "buf/validate/validate.proto";`) {
		proto = strings.Replace(proto, `syntax = "proto3";`,
			`syntax = "proto3";`+"\n"+`import "buf/validate/validate.proto";`, 1)
	}
	proto += `
// forge:entity
message Product {
  string id = 1;
  string name = 2 [(buf.validate.field).string.min_len = 1];
  ProductStatus status = 3;
  int64 price_cents = 4 [(buf.validate.field).int64.gte = 0];
  int64 units = 5 [(buf.validate.field).int64 = {gte: 1, lte: 12}];
}

enum ProductStatus {
  PRODUCT_STATUS_UNSPECIFIED = 0;
  PRODUCT_STATUS_ACTIVE = 1;
  PRODUCT_STATUS_ARCHIVED = 2;
}
`
	writeFileE2E(t, protoPath, proto)

	runCmd(t, projectDir, forgeBin, "scaffold")
	runCmd(t, projectDir, "go", "build", "./...")

	// ── Defect C: the AGGREGATE-form field births the SAME CHECK the dotted
	//    form does. Both spellings must land in the birth migration. ──
	upMatches, _ := filepath.Glob(filepath.Join(projectDir, "db", "migrations", "*create_products.up.sql"))
	if len(upMatches) != 1 {
		t.Fatalf("expected exactly one products birth migration, got %v", upMatches)
	}
	up := readFileE2E(t, upMatches[0])
	for _, want := range []string{
		"CHECK (price_cents >= 0)", // dot form
		"CHECK (units >= 1)",       // aggregate form, lower bound
		"CHECK (units <= 12)",      // aggregate form, upper bound
	} {
		if !strings.Contains(up, want) {
			t.Errorf("born migration missing %q (aggregate/dot validate CHECK):\n%s", want, up)
		}
	}

	// The frontend lane needs a populated node_modules or lintFrontendDir
	// skips it — install so eslint/stylelint/tsc actually gate.
	feDir := filepath.Join(projectDir, "frontends", "dashboard")
	// The install resolves @reliantlabs/forge-web-runtime over a file: link
	// into the shared repo checkout and runs its prepare script there; build
	// it once up front so parallel tests do not race that bootstrap.
	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, feDir, 5*time.Minute,
		"npm", "install", "--no-audit", "--no-fund", "--prefer-offline")

	// ── Defect D: the scaffolded style-lint autofix must not corrupt CSS.
	//    This drives the scaffold's OWN npm scripts and restores globals.css
	//    before returning, so it neither depends on nor disturbs the
	//    `forge lint` / `forge generate` assertions below. It runs first
	//    because it only needs a populated node_modules. ──
	assertStyleLintAutofixEmitsValidCSS(t, feDir)

	// ── Defect E: the scaffold must be eslint-clean AS EMITTED — zero
	//    problems, warnings included, with no --fix.
	//
	//    `forge lint` runs `eslint --fix`, which silently repaired the
	//    scaffold's own violations, so the lane looked clean while the
	//    emitted code was not. Five accumulated that way: four components
	//    default-exporting under a "prefer named exports" rule that already
	//    exempted the two directories it was written for, and four
	//    import-order slips in the auth components.
	//
	//    Warnings count. A wall of warnings on a brand-new project is a
	//    triage cost the developer pays before writing a line, and a warning
	//    that is always there is a warning nobody reads — which is how a real
	//    one gets missed. The stylelint lane has had this check (Defect A);
	//    eslint never did, which is precisely why these piled up. ──
	assertScaffoldIsEslintCleanAsEmitted(t, feDir)

	// ── Defect B (root cause): snapshot EVERY Tier-1 generated TS/TSX file
	//    BEFORE lint. `forge lint` (default) runs eslint --fix; a canonical
	//    emission makes that a no-op, leaving the bytes untouched.
	//
	//    Snapshot the whole set rather than one named file. This assertion
	//    used to hardcode src/lib/runtime/interceptors.ts, which later moved
	//    into the @reliantlabs/forge-web-runtime package — readFileE2E then
	//    t.Fatal'd on a path that no longer exists, and because this lane is
	//    behind `-tags e2e` nobody saw it. The invariant was never about that
	//    file: eslint --fix must not mutate ANY file forge regenerates, or the
	//    next `forge generate` trips the file-stomp guard. Discovering the set
	//    keeps the lane alive across renames and widens the net for free. ──
	generatedTSBefore := snapshotGeneratedTSE2E(t, feDir)

	// THE INVARIANT: `forge lint` exits 0 on the fresh scaffold, frontend
	// lane included. --fix (not --no-fix), because the eslint --fix pre-pass
	// is what this lane exercises and CI=true would otherwise make the run
	// report-only.
	lintOut, lintErr := runFrontendLintE2E(t, projectDir, forgeBin, "lint", "--fix")
	if lintErr != nil {
		t.Fatalf("forge lint must exit 0 on a freshly-scaffolded project (frontend lane), got: %v\n%s", lintErr, lintOut)
	}
	if strings.Contains(lintOut, "❌") {
		t.Errorf("forge lint printed error findings on a fresh scaffold:\n%s", lintOut)
	}

	// The frontend lane actually RAN (a skipped lane makes exit-0 vacuous):
	// the linters were invoked and each passed, and node_modules was found.
	if strings.Contains(lintOut, "node_modules not found") {
		t.Errorf("frontend lane skipped for missing node_modules — the exit-0 assertion is vacuous:\n%s", lintOut)
	}
	for _, marker := range []string{
		"✓ frontend lint",
		"dashboard: npm run lint passed",        // eslint (Defect B lane)
		"dashboard: typecheck passed",           // tsc
		"dashboard: npm run lint:styles passed", // stylelint (Defect A lane)
	} {
		if !strings.Contains(lintOut, marker) {
			t.Errorf("frontend lane marker %q missing — lane did not fully run:\n%s", marker, lintOut)
		}
	}

	// ── Defect B: eslint --fix left every Tier-1 generated file byte-identical. ──
	for _, rel := range sortedKeysE2E(generatedTSBefore) {
		got, err := os.ReadFile(filepath.Join(feDir, rel))
		if err != nil {
			t.Errorf("Tier-1 generated file %s disappeared across forge lint: %v", rel, err)
			continue
		}
		if string(got) != generatedTSBefore[rel] {
			t.Errorf("forge lint's eslint --fix mutated the Tier-1 generated %s (import order not canonical at emission):\n--- before ---\n%s\n--- after ---\n%s", rel, generatedTSBefore[rel], got)
		}
	}

	// ── Defect B (downstream): a subsequent `forge generate` must NOT trip the
	//    Tier-1 file-stomp guard (no "hand-edited Tier-1 file(s)", no --force). ──
	genOut, genErr := runFrontendLintE2E(t, projectDir, forgeBin, "generate")
	if genErr != nil {
		t.Fatalf("forge generate after forge lint must exit 0 (no file-stomp on the linted runtime), got: %v\n%s", genErr, genOut)
	}
	if strings.Contains(genOut, "hand-edited Tier-1") || strings.Contains(genOut, "file-stomp guard") {
		t.Errorf("forge generate tripped the Tier-1 file-stomp guard after forge lint — eslint --fix mutated a generated file:\n%s", genOut)
	}
}

// snapshotGeneratedTSE2E returns every forge-regenerated TS/TSX file under
// feDir, keyed by path relative to feDir. "Regenerated" is read off the file
// itself — the DO NOT EDIT banner forge stamps on Tier-1 output — so the set
// tracks whatever the emitters currently produce instead of a list that has to
// be maintained in lockstep with them.
//
// Fails when the set is EMPTY. That is the failure mode this helper exists to
// prevent: the caller's assertion is a loop, and a loop over nothing passes.
// A scaffold with no generated frontend TS means the scaffold step regressed,
// which must be loud rather than silently reducing the test to a no-op.
func snapshotGeneratedTSE2E(t *testing.T, feDir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	srcDir := filepath.Join(feDir, "src")
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// node_modules is dependency code and .next is build output;
			// neither is forge's to regenerate.
			if name := d.Name(); name == "node_modules" || strings.HasPrefix(name, ".next") {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".ts" && ext != ".tsx" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "DO NOT EDIT") {
			return nil
		}
		rel, err := filepath.Rel(feDir, path)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s for generated TS: %v", srcDir, err)
	}
	if len(out) == 0 {
		t.Fatalf("no forge-generated TS/TSX found under %s — the byte-identical assertion "+
			"would be vacuous. Either the scaffold emitted no Tier-1 frontend files, or the "+
			"DO NOT EDIT banner changed.", srcDir)
	}
	// Report the breadth so a shrinking set is visible in the log rather than
	// only at the zero cliff.
	t.Logf("Defect B covers %d Tier-1 generated frontend file(s): %s", len(out), strings.Join(sortedKeysE2E(out), ", "))
	return out
}

// sortedKeysE2E orders map keys so assertion failures report in a stable,
// diffable order rather than Go's randomized map iteration.
func sortedKeysE2E(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// oklchCallE2E captures the three positional arguments of an oklch() call:
// lightness, chroma, hue.
var oklchCallE2E = regexp.MustCompile(`oklch\(\s*([^\s)]+)\s+([^\s)]+)\s+([^\s)]+)\s*\)`)

var (
	// cssPercentageE2E / cssAngleE2E are the CSS Color 4 grammars for the
	// notations stylelint-config-standard's `lightness-notation` and
	// `hue-degree-notation` demand. A <number> requires a digit AFTER the
	// decimal point, which is exactly what the pre-17.2.0 autofix dropped.
	cssPercentageE2E = regexp.MustCompile(`^\d+(\.\d+)?%$`)
	cssAngleE2E      = regexp.MustCompile(`^\d+(\.\d+)?deg$`)

	// danglingDecimalE2E is the corruption's fingerprint: `58.%`.
	danglingDecimalE2E = regexp.MustCompile(`\d+\.%`)
)

// assertStyleLintAutofixEmitsValidCSS drives the scaffold's OWN style-lint
// scripts over theme tokens written the way the `frontend/design` skill
// teaches, and pins two invariants a fresh scaffold must hold:
//
//  1. `npm run lint:styles:fix` never emits an invalid CSS number. The
//     probe values are the eight two-decimal lightnesses where x*100 lands
//     off-integer in binary floating point (.07 .14 .28 .29 .55 .56 .57
//     .58) — the exact inputs the broken autofix mangled into `NN.%`. A
//     corrupted file re-lints CLEAN (`58.%` still reads as a percentage
//     dimension), so asserting on the lint exit code alone is vacuous:
//     the assertion has to read the resulting BYTES.
//  2. every oklch literal the design skill offers as an exemplar passes
//     `npm run lint:styles` as written, with no --fix. The skill and the
//     scaffold's linter must agree, or the first unit to follow the skill
//     burns a retry on 100+ notation errors.
//
// globals.css is restored before returning so the caller's later
// `forge generate` assertions see the scaffold's own bytes.
func assertStyleLintAutofixEmitsValidCSS(t *testing.T, feDir string) {
	t.Helper()

	globalsPath := filepath.Join(feDir, "src", "app", "globals.css")
	pristine := readFileE2E(t, globalsPath)
	defer writeFileE2E(t, globalsPath, pristine)

	anchor := "@theme {\n"
	if !strings.Contains(pristine, anchor) {
		t.Fatalf("globals.css has no `@theme {` block to inject probe tokens into:\n%s", pristine)
	}

	// ── 1. autofix must not corrupt ──
	var probes strings.Builder
	for i, lightness := range []string{"0.07", "0.14", "0.28", "0.29", "0.55", "0.56", "0.57", "0.58"} {
		fmt.Fprintf(&probes, "  --color-probe-%d: oklch(%s 0.09 196);\n", i, lightness)
	}
	withProbes := strings.Replace(pristine, anchor, anchor+probes.String(), 1)
	writeFileE2E(t, globalsPath, withProbes)

	if out, err := runFrontendLintE2E(t, feDir, "npm", "run", "lint:styles:fix"); err != nil {
		t.Fatalf("npm run lint:styles:fix failed on oklch theme tokens: %v\n%s", err, out)
	}
	fixed := readFileE2E(t, globalsPath)

	if hits := danglingDecimalE2E.FindAllString(fixed, -1); len(hits) > 0 {
		t.Errorf("lint:styles:fix emitted %d invalid CSS number(s) %v — a lightness like `58.` has no digit after the decimal "+
			"point, so the browser drops the whole declaration and the custom property resolves to nothing. Raise the "+
			"stylelint floor in the frontend scaffold's package.json.\n%s", len(hits), hits, fixed)
	}
	// Declarations only: the scaffolded header comment quotes the REJECTED
	// spelling on purpose, so that a reader learns which one is which.
	for n, line := range strings.Split(fixed, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		for _, m := range oklchCallE2E.FindAllStringSubmatch(line, -1) {
			if !cssPercentageE2E.MatchString(m[1]) {
				t.Errorf("globals.css:%d: lint:styles:fix produced %q whose lightness %q is not a valid CSS <percentage>", n+1, m[0], m[1])
			}
			if !cssAngleE2E.MatchString(m[3]) {
				t.Errorf("globals.css:%d: lint:styles:fix produced %q whose hue %q is not a valid CSS <angle>", n+1, m[0], m[3])
			}
		}
	}
	if !strings.Contains(fixed, "oklch(58% 0.09 196deg)") {
		t.Errorf("lint:styles:fix did not normalise oklch(0.58 0.09 196) to oklch(58%% 0.09 196deg); got:\n%s", fixed)
	}
	// The corrupted form used to pass this too — kept so a REGRESSION shows
	// up as "bytes bad, exit 0", which is the whole point of the defect.
	if out, err := runFrontendLintE2E(t, feDir, "npm", "run", "lint:styles"); err != nil {
		t.Errorf("lint:styles is red on its own autofix output: %v\n%s", err, out)
	}

	// ── 2. the design skill's exemplars pass the scaffold's linter ──
	skillPath := filepath.Join(findRepoRoot(t), "internal", "templates", "project",
		"skills", "forge", "frontend", "design", "SKILL.md")
	skill := readFileE2E(t, skillPath)

	var exemplars []string
	for _, m := range oklchCallE2E.FindAllStringSubmatch(skill, -1) {
		if cssPercentageE2E.MatchString(m[1]) && cssAngleE2E.MatchString(m[3]) {
			exemplars = append(exemplars, m[0])
		}
	}
	if len(exemplars) == 0 {
		t.Fatalf("the frontend/design skill offers no oklch exemplar in the notation the scaffold's stylelint accepts — "+
			"a unit following it cannot write a passing color token (%s)", skillPath)
	}

	var skillProbes strings.Builder
	for i, literal := range exemplars {
		fmt.Fprintf(&skillProbes, "  --color-skill-%d: %s;\n", i, literal)
	}
	writeFileE2E(t, globalsPath, strings.Replace(pristine, anchor, anchor+skillProbes.String(), 1))

	if out, err := runFrontendLintE2E(t, feDir, "npm", "run", "lint:styles"); err != nil {
		t.Errorf("the frontend/design skill's oklch exemplars %v are RED under the scaffold's own stylelint — the skill and "+
			"the scaffold contradict each other, and the unit pays: %v\n%s", exemplars, err, out)
	}
}

// runFrontendLintE2E runs `forge <args>` in dir and returns combined output +
// the raw error (it does NOT fail the test on a non-zero exit — the caller
// asserts on it, exit code being the thing under test). Unlike runLintE2E it
// does not mutate PATH: the frontend lane resolves its tools through
// node_modules/.bin via `npm run`, and the Go/contract lanes run in-process.
func runFrontendLintE2E(t *testing.T, dir, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// golangciLockIsolationEnv: `forge lint` shells out to golangci-lint, which
	// otherwise contends on one machine-global lock with every other test's lint
	// step. See the block comment in scaffold_e2e_test.go.
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=", golangciLockIsolationEnv(t)))
	output, err := cmd.CombinedOutput()
	return string(output), err
}

var (
	contractlintOnce sync.Once
	contractlintDir  string
	contractlintErr  error
)

// buildContractlintBinary builds cmd/contractlint from this tree into a
// shared temp dir (once per test binary) and returns the DIRECTORY holding
// the `contractlint` binary — callers prepend it to PATH so `forge lint`'s
// LookPath resolution finds the tree-built analyzer ahead of any stale
// globally-installed copy.
func buildContractlintBinary(t *testing.T) string {
	t.Helper()
	contractlintOnce.Do(func() {
		repoRoot := findRepoRoot(t)
		binDir, err := os.MkdirTemp("", "forge-e2e-contractlint-")
		if err != nil {
			contractlintErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, "contractlint"), "./cmd/contractlint")
		cmd.Dir = repoRoot
		if output, berr := cmd.CombinedOutput(); berr != nil {
			contractlintErr = fmt.Errorf("failed to build contractlint: %w\n%s", berr, output)
			return
		}
		contractlintDir = binDir
	})
	if contractlintErr != nil {
		t.Fatalf("%v", contractlintErr)
	}
	return contractlintDir
}

// runLintE2E runs a command with extraPathDir prepended to PATH and returns
// its combined output plus the raw error (it does NOT fail the test on a
// non-zero exit — the caller asserts on it, since exit code IS the thing
// under test here).
func runLintE2E(t *testing.T, dir, extraPathDir, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// os/exec keeps the LAST duplicate of an env key, so appending the
	// modified PATH after os.Environ() makes it win.
	cmd.Env = goexec.WithTrimpath(append(os.Environ(),
		"GOFLAGS=",
		"GOPROXY=https://proxy.golang.org,direct",
		"PATH="+extraPathDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		// `forge lint` shells out to golangci-lint, which otherwise contends on
		// one machine-global lock with every other test's lint step. See the
		// block comment in scaffold_e2e_test.go.
		golangciLockIsolationEnv(t),
	))
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// assertScaffoldIsEslintCleanAsEmitted runs the scaffold's own lint script
// with no --fix and requires zero problems — WARNINGS INCLUDED. eslint exits 0
// on warnings, so the exit code alone passes a scaffold that opens with a wall
// of them (vite-spa once shipped nine react-refresh/only-export-components);
// the summary line ("✖ N problems (0 errors, N warnings)") is what tells.
//
// It drives `npm run lint` rather than eslint directly so the assertion is
// about what the PROJECT is configured to do, not about a flag set this test
// happens to pass — the scaffold's script is what a developer and CI both run.
func assertScaffoldIsEslintCleanAsEmitted(t *testing.T, feDir string) {
	t.Helper()
	cmd := exec.Command("npm", "run", "lint")
	cmd.Dir = feDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("`npm run lint` failed on a freshly-scaffolded frontend: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "problem") || strings.Contains(string(out), " warning ") {
		t.Errorf("the emitted scaffold is not eslint-clean as written — every new project starts "+
			"with findings its author did not create, and `forge lint`'s --fix pre-pass hides "+
			"them by mutating the files:\n%s", out)
	}
}
