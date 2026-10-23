package tierguard

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cli"
	"github.com/reliant-labs/forge/internal/linter/scaffolds"
)

// TestMain doubles as the protoc-gen-forge plugin.
//
// The generate pipeline resolves its own plugin path via os.Executable(),
// which under `go test` is THIS test binary. buf therefore spawns
// `<tierguard.test> protoc-gen-forge` and, without this dispatch, reads
// `go test` output where it expected a CodeGeneratorResponse — the
// pipeline fails at descriptor extraction with "wire unmarshal: proto:
// cannot parse invalid wire-format data" and every subsequent step is
// skipped. That failure mode is quiet in the worst way: the run "passes"
// having rendered nothing.
//
// Routing that argv into cli.Execute() makes the test binary a faithful
// stand-in for the real forge binary, which is what lets the pipeline run
// inside a test process at all — the requirement for reading
// checksums.Tier1TargetSet, which lives in that process's memory.
//
// The second dispatch, renderChildArgv, re-executes this binary as a
// single-fixture renderer so the four fixtures can render concurrently
// in separate processes. See render_child_test.go for why concurrency
// has to be bought with processes rather than goroutines.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "protoc-gen-forge" {
		if err := cli.Execute(); err != nil {
			fmt.Fprintf(os.Stderr, "protoc-gen-forge: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == renderChildArgv {
		runRenderChild()
		return
	}
	exitCode := m.Run()
	// The fixtures are shared across tests, so only the parent test process
	// can remove them after the entire suite, including failed tests, finishes.
	if renderBase != "" {
		if err := os.RemoveAll(renderBase); err != nil {
			fmt.Fprintf(os.Stderr, "remove tierguard fixtures: %v\n", err)
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

// maxConcurrentRenders bounds how many fixture render children run at once.
//
// Each child is a whole forge pipeline — project new, scaffold, generate,
// buf, go mod tidy — plus the go toolchain processes it spawns, so a
// render's memory is the price of a real project build, and the guard's peak
// is this number times that price. Leaving it at len(specs) made the peak
// grow with every fixture added and co-reside with whatever else the runner
// was doing; with all four at once the package alone peaked at 6.7 GB RSS
// on a 16 GB CI runner, which is how the Test job came to be OOM-killed
// ("The runner has received a shutdown signal", exit 143).
//
// It is a FIXED number rather than GOMAXPROCS on purpose: the renders are
// memory-bound, not CPU-bound, and a bigger machine does not make a render
// smaller. Two keeps most of #167's wall-clock win (the renders are ~30s
// each, so two slots cost roughly one extra render over four).
const maxConcurrentRenders = 2

// renderOnce memoizes the fixture renders. Each costs ~20s of real
// codegen (buf, go mod tidy), and every test in this package wants
// the same set, so they are built once per binary.
var (
	renderOnce   sync.Once
	renderBase   string
	renderInputs []*renderResult
	renderC      *renderResult
	renderErr    error
)

// renders returns the INPUT-VARIED fixtures and the identity-varied
// control (C).
//
// The input set is A, B and D. All three share one project identity and
// differ only in what the user DECLARED, so any byte difference among
// them is attributable to a declaration. C is returned separately and
// never participates in a verdict — it varies the module path, which is
// not a derivation input (see the package doc's "held constant"
// section), and is used only to decide the REMEDY for a constant file.
func renders(t *testing.T) (inputs []*renderResult, identity *renderResult) {
	t.Helper()
	// Every test in this package that needs a render comes through here, so
	// this is the one gate: the renders are four whole forge projects (project
	// new, generate, buf, go mod tidy) and cost minutes, not seconds.
	if testing.Short() {
		t.Skip("renders four full forge projects in child processes (minutes); runs in task test")
	}
	renderOnce.Do(func() {
		// Not t.TempDir: the trees outlive the first test that asks for
		// them. TestMain removes the shared directory after all tests finish.
		base, err := os.MkdirTemp("", "tierguard-*")
		if err != nil {
			renderErr = err
			return
		}
		renderBase = base

		// The four fixtures are independent trees, but render() cannot
		// run concurrently IN THIS PROCESS: forgeIn drives the pipeline
		// via os.Chdir + os.Args, and the Tier-1 inventory is read from
		// checksums.Tier1TargetSet — all three are process-global, so
		// two concurrent renders would interleave working directories
		// and attribute one fixture's targets to another.
		//
		// So each render runs in its own CHILD of this test binary,
		// where those globals are private again, and reports its result
		// back as JSON. The children are the same binary (os.Args[0]),
		// dispatched by TestMain, which is the trick the protoc-gen-forge
		// dispatch above already relies on.
		specs := []struct {
			dir string
			fx  fixture
		}{
			{"a", projectA()},
			{"b", projectB()},
			{"d", projectD()},
			{"c", projectC()},
		}

		results := make([]*renderResult, len(specs))
		errs := make([]error, len(specs))
		// At most maxConcurrentRenders children at once: see its doc.
		slots := make(chan struct{}, maxConcurrentRenders)
		var wg sync.WaitGroup
		for i, spec := range specs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				slots <- struct{}{}
				defer func() { <-slots }()
				r, rerr := renderInChild(filepath.Join(base, spec.dir), spec.fx)
				if rerr != nil {
					errs[i] = fmt.Errorf("fixture %s: %w", spec.fx.Label, rerr)
					return
				}
				results[i] = r
			}()
		}
		wg.Wait()

		for _, e := range errs {
			if e != nil {
				renderErr = e
				return
			}
		}
		// Order is fixed by the specs slice, not by completion order, so
		// the inventory and every failure message stay deterministic.
		renderInputs = results[:3]
		renderC = results[3]
	})
	if renderErr != nil {
		t.Fatalf("rendering the fixtures failed: %v\n\n"+
			"This guard cannot classify anything without rendered projects. It fails here rather "+
			"than reporting an empty inventory as success.", renderErr)
	}
	return renderInputs, renderC
}

// TestTier1FilesAreDerivedFromUserInput is the guard.
//
// It renders two projects whose user inputs differ in all four
// derivation channels (protos, migrations, contract.go, forge.yaml) and
// fails for every Tier-1 file whose bytes are identical across both. A
// constant Tier-1 file is library code in the user's tree: forge gains
// nothing by regenerating it, and the user who edits it falls off the
// upgrade path permanently (see the package doc).
func TestTier1FilesAreDerivedFromUserInput(t *testing.T) {
	inputs, identity := renders(t)

	assertInventoryIsReal(t, inputs)

	classifications := Classify(inputs, identity)
	if len(classifications) == 0 {
		t.Fatal("classified zero files: the Tier-1 inventory is empty, so every assertion below " +
			"would hold vacuously. See assertInventoryIsReal — this should be unreachable.")
	}

	exempt := allowed()

	var offenders, unreadable []Classification
	counts := map[Verdict]int{}
	for _, c := range classifications {
		counts[c.Verdict]++
		switch c.Verdict {
		case Constant:
			if _, ok := exempt[c.Path]; ok {
				continue
			}
			offenders = append(offenders, c)
		case Unreadable:
			unreadable = append(unreadable, c)
		}
	}

	var perFixture []string
	for _, r := range inputs {
		perFixture = append(perFixture, fmt.Sprintf("%s=%d", r.Label, len(r.Tier1)))
	}
	t.Logf("Tier-1 inventory: %d paths across %d input-varied fixtures (%s) — "+
		"derived(content)=%d derived(presence)=%d constant=%d unreadable=%d",
		len(classifications), len(inputs), strings.Join(perFixture, ", "),
		counts[Derived], counts[OnlyInOne], counts[Constant], counts[Unreadable])

	// Under -v, print the whole table. The pass/fail verdict only needs
	// the offenders, but a reader auditing the guard needs to see what it
	// judged DERIVED too — that is where a false negative would hide.
	if testing.Verbose() {
		var table strings.Builder
		table.WriteString("full classification table (path — verdict — evidence):\n")
		for _, c := range classifications {
			fmt.Fprintf(&table, "  %-56s %-18s %s\n", c.Path, c.Verdict, c.Evidence)
		}
		t.Log(table.String())
	}

	for _, c := range unreadable {
		t.Errorf("%s: %s", c.Path, c.Evidence)
	}

	if len(offenders) == 0 {
		return
	}

	// Rank by size: the biggest constant file is the most library code
	// sitting in a user tree behind a "do not edit" banner.
	sort.Slice(offenders, func(i, j int) bool {
		if offenders[i].SizeBytes != offenders[j].SizeBytes {
			return offenders[i].SizeBytes > offenders[j].SizeBytes
		}
		return offenders[i].Path < offenders[j].Path
	})

	var labels []string
	for _, r := range inputs {
		labels = append(labels, r.Label)
	}
	var msg strings.Builder
	fmt.Fprintf(&msg, "%d Tier-1 file(s) are NOT derived from user input.\n\n", len(offenders))
	fmt.Fprintf(&msg, "Each was rendered %d times against materially different user inputs — %s — "+
		"and came out byte-identical.\n", len(inputs), strings.Join(labels, " vs "))
	msg.WriteString(
		"A file that does not respond to the user's protos, migrations, contract.go or forge.yaml " +
			"is not being re-derived from anything;\nit is library code in the user's tree behind a " +
			"\"do not edit\" banner. Regenerating it cannot keep it correct (it was never a function of " +
			"the inputs)\nand it carries a real cost: a user who edits it puts it in permanent drift, " +
			"so every future forge improvement silently stops arriving.\n\n" +
			"Fix: move the content into forge/pkg and leave a short user-owned scaffold that calls it. " +
			"If a file is constant only\nBECAUSE these fixtures do not happen to move it, extend a " +
			"fixture in fixtures.go to exercise the input it tracks\n(preferred — it becomes real " +
			"evidence), or add a documented allowList entry in allowlist_test.go.\n\n")
	fmt.Fprintf(&msg, "Ranked by size (largest = most library code misplaced):\n")
	for i, c := range offenders {
		kind := "pure library code — can move to forge/pkg verbatim"
		if c.IdentityDependent {
			kind = "names the user's module — forge/pkg library + a small user-owned scaffold"
		}
		fmt.Fprintf(&msg, "  %2d. %-52s %6d bytes / %4d lines  [%s]\n",
			i+1, c.Path, c.SizeBytes, c.Lines, kind)
		fmt.Fprintf(&msg, "      reason: %s\n", c.Evidence)
		if tp := templatePathFor(c.Path); tp != "" {
			fmt.Fprintf(&msg, "      template: %s\n", tp)
		}
	}
	msg.WriteString("\nRendered trees (removed when the test suite finishes):\n")
	for _, r := range inputs {
		fmt.Fprintf(&msg, "  %s: %s\n", r.Label, r.Root)
	}
	fmt.Fprintf(&msg, "  %s: %s\n", identity.Label, identity.Root)
	t.Error(msg.String())
}

// assertInventoryIsReal fails loudly when the derived inventory is empty
// or implausibly thin.
//
// This is the anti-vacuity check, and it is the reason this guard is
// worth having. A differential render that renders nothing classifies
// nothing and reports success — green output that looks like evidence
// and is not. The in-process pipeline has a real failure mode that lands
// exactly there (a broken protoc-gen-forge dispatch makes descriptor
// extraction fail, after which every emitter is skipped and
// Tier1TargetSet stays empty), so the empty case must be a loud failure
// and not a quiet pass.
func assertInventoryIsReal(t *testing.T, inputs []*renderResult) {
	t.Helper()

	if len(inputs) < 2 {
		t.Fatalf("the differential method needs at least two input-varied fixtures; got %d. "+
			"A single render cannot distinguish a derived file from a constant one.", len(inputs))
	}

	for _, r := range inputs {
		if len(r.Tier1) == 0 {
			t.Fatalf("fixture %s targeted ZERO Tier-1 files.\n\n"+
				"checksums.Tier1TargetSet is populated by every Tier-1 write, so an empty set means the "+
				"pipeline never reached its emitters\n(most likely descriptor extraction failed — check "+
				"the TestMain protoc-gen-forge dispatch). Failing loudly: a guard that\nrenders zero files "+
				"passes vacuously and is worse than no guard.\nProject: %s", r.Label, r.Root)
		}
		if len(r.Bodies) == 0 {
			t.Fatalf("fixture %s targeted %d Tier-1 files but none were readable on disk — "+
				"nothing can be compared.\nProject: %s", r.Label, len(r.Tier1), r.Root)
		}
	}

	// Independent of the count, the inventory must contain files that
	// EVERY project shape has. Their absence means the walk found a
	// partial run, not a real one.
	//
	// Deliberately chosen to span several distinct emitters, so a single
	// broken step cannot satisfy the whole check: config codegen, the
	// cmd tree, app composition, and the middleware procedure set.
	// NOTE: the per-service test harness is deliberately NOT in this list.
	// It is emitted per service, at internal/handlers/<svc>/helpers_gen_test.go,
	// so its path differs between fixture A (task), fixture B
	// (billing/reporting) and fixture D (alpha/version) — it cannot be a
	// shape-invariant anchor. Its
	// predecessor, the project-wide pkg/app/testing.go, was retired because
	// being a non-test .go file in pkg/app put package `testing` into every
	// scaffolded project's production binary.
	mustContain := []string{
		"pkg/config/config_gen.go",
		"pkg/middleware/procedures_gen.go",
		// NOT a cmd-tree file: there is no longer any Tier-1 file under
		// cmd/<bin>/cmd/. root_gen.go was the last one, and its facts either
		// stopped being derived (ServiceName; the db command, now added by
		// the db.go that defines it) or moved to the package they describe.
		// db/source_gen.go is where the surviving one lives, and it is
		// shape-invariant — every service project emits it, in both the
		// with-SQL and without-SQL states.
		"db/source_gen.go",
	}
	for _, r := range inputs {
		for _, want := range mustContain {
			if !r.Tier1[filepath.ToSlash(want)] {
				t.Fatalf("fixture %s has %d Tier-1 targets but not %q.\n\n"+
					"That file is emitted for every project shape, so its absence means this run captured a "+
					"PARTIAL pipeline\nand the classification below would be drawn from an incomplete "+
					"inventory. Failing rather than under-reporting.\nProject: %s",
					r.Label, len(r.Tier1), want, r.Root)
			}
		}
	}

	for _, r := range inputs {
		if len(r.Missing) > 0 {
			t.Logf("fixture %s: %d Tier-1 target(s) not readable on disk: %v",
				r.Label, len(r.Missing), r.Missing)
		}
	}
}

// TestClassifierAgreesWithKnownDerivedFiles pins the classifier against
// files whose Tier-1 status is not in question: each is per-service or
// per-entity by construction, so a classifier that calls any of them
// constant is broken and its verdicts elsewhere cannot be trusted.
//
// This is the control on the guard itself. Without it, a bug that made
// every comparison return "identical" would present as a long list of
// confident mis-tier findings.
func TestClassifierAgreesWithKnownDerivedFiles(t *testing.T) {
	inputs, identity := renders(t)

	byPath := map[string]Classification{}
	for _, c := range Classify(inputs, identity) {
		byPath[c.Path] = c
	}

	// Known-derived, from the schema and the protos respectively. Each
	// is per-service or per-entity BY CONSTRUCTION, so its content
	// cannot be constant unless the comparison itself is broken.
	//
	// pkg/config/config.go is deliberately NOT in this list, though it
	// is often cited as per-project. Its per-project element is the
	// `type Config = configv1.AppConfig` alias — and an alias to a
	// varying type is itself invariant text. Adding a component config
	// block to proto/config/v1/config.proto (fixture B's ConfigBlocks)
	// demonstrably moves deploy/kcl/config_gen.k and the
	// generated gen/config/v1/config.pb.go while leaving config.go
	// byte-identical, so the file tracks the config proto's EXISTENCE,
	// not its content. It is reported as a mis-tier candidate on that
	// evidence; see the ranked findings.
	// byPath is the union over both fixtures, so a path only fixture A
	// emits is a valid control. The test harness and the entity factories
	// both moved beside the service they serve
	// (internal/handlers/<svc>/{helpers,factories}_gen_test.go), so that
	// `cmd/<proj>` no longer links package `testing`.
	known := []string{
		"pkg/middleware/procedures_gen.go",
		"internal/handlers/task/factories_gen_test.go",
		"internal/handlers/task/helpers_gen_test.go",
	}
	checked := 0
	for _, p := range known {
		c, ok := byPath[p]
		if !ok {
			t.Errorf("known-derived file %q is not a Tier-1 target in either fixture — "+
				"the control has nothing to check; has the emitter moved?", p)
			continue
		}
		checked++
		if c.Verdict == Constant || c.Verdict == Unreadable {
			t.Errorf("classifier says %q is %s, but it is per-project by construction.\n"+
				"The classifier is wrong, which makes every other verdict in this package suspect.\n"+
				"evidence: %s", p, c.Verdict, c.Evidence)
		}
	}
	if checked == 0 {
		t.Fatal("checked zero known-derived files — this control passed vacuously")
	}
	t.Logf("classifier control: %d known-derived files all classified derived", checked)
}

// templatePathFor maps a generated project path to the template that
// renders it, so a mis-tier finding points at the file to change.
// Best-effort: an empty result just omits the hint.
func templatePathFor(rel string) string {
	base := filepath.Base(rel)
	candidates := map[string]string{
		"root.go":       "internal/templates/project/cmd-tree-root.go.tmpl",
		"config_gen.go": "internal/templates/project/config.go.tmpl",
		// source_gen.go is emitted from Go, not a template — the hint names
		// the emitter so a finding still points at the file to change.
		"source_gen.go":       "internal/codegen/app_scaffolds.go",
		"server.go":           "internal/templates/project/cmd-tree-server.go.tmpl",
		"version.go":          "internal/templates/project/cmd-tree-version.go.tmpl",
		"db.go":               "internal/templates/project/cmd-tree-db.go.tmpl",
		"migrate.go":          "internal/templates/project/app-migrate.go.tmpl",
		"helpers_gen_test.go": "internal/templates/project/component_test_helpers.go.tmpl",
		"compose.go":          "internal/templates/project/app-compose.go.tmpl",
		"lifecycle.go":        "internal/templates/project/app-lifecycle.go.tmpl",
		"repo.go":             "internal/templates/project/repo.go.tmpl",
		"embed.go":            "internal/templates/project/db-embed.go.tmpl",
	}
	tp, ok := candidates[base]
	if !ok {
		return ""
	}
	// Only report the hint when the template actually exists in this
	// tree — a stale guess is worse than no hint.
	if _, err := os.Stat(filepath.Join("..", "..", tp)); err != nil {
		return ""
	}
	return tp
}

// TestForgeOwnedFilesRouteThroughTheTier1Chokepoint reports files that
// carry the forge-owned banner but never pass through
// checksums.WriteGeneratedFile, and are therefore invisible to
// Tier1TargetSet — the channel this whole guard reads.
//
// This is the completeness check on the INVENTORY, and it exists because
// the gap is real rather than hypothetical:
// internal/handlers/mocks/<svc>_mock.go is written with a plain
// os.Create in internal/codegen/generator.go (writeServiceMock), so it
// is stamped forge-owned, is regenerated every run, and is absent from
// the producer's own Tier-1 set. It also carries no forge:hash marker,
// which is why reconstructing the inventory from disk markers instead
// would ALSO miss it.
//
// Such a file gets the worst of both tiers: forge overwrites it every
// run, but the drift detection and disown machinery that make Tier-1
// survivable do not apply to it. Reported as a failure so the count
// cannot grow quietly.
func TestForgeOwnedFilesRouteThroughTheTier1Chokepoint(t *testing.T) {
	inputs, _ := renders(t)
	a := inputs[0]

	banner := []byte("forge-owned:")

	var offenders []string
	err := filepath.Walk(a.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		switch {
		case strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)),
			strings.Contains(path, "node_modules"):
			return nil
		}
		// Head-bounded read: the banner lives at the top of the file.
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		head := make([]byte, 4096)
		n, _ := f.Read(head)
		if n <= 0 || !bytes.Contains(head[:n], banner) {
			return nil
		}
		rel, rerr := filepath.Rel(a.Root, path)
		if rerr != nil {
			return nil
		}
		if !a.Tier1[normalizeKey(rel, a.Name)] {
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", a.Root, err)
	}

	// The walk must have had something to judge, or it proved nothing.
	if len(a.Tier1) == 0 {
		t.Fatal("no Tier-1 targets — the scan has no set to check membership against")
	}

	sort.Strings(offenders)
	for _, p := range offenders {
		t.Errorf("%s carries the forge-owned banner but is NOT in checksums.Tier1TargetSet.\n"+
			"It is regenerated every run without passing through checksums.WriteGeneratedFile, so it "+
			"gets neither drift detection\nnor `forge project disown` — and it is invisible both to "+
			"this guard's inventory and to a disk-marker scan.\nRoute the write through the Tier-1 "+
			"writer (or stop stamping it forge-owned).", p)
	}
}

// TestTier1GenFilesPassScaffoldOwnershipLint holds every Tier-1 _gen file
// forge renders to the rule `forge lint` holds the user's tree to: the
// canonical "Code generated by forge" header AND a "// Source:" line.
//
// Each emitter hand-writes its own header, and nothing checked them
// against that rule — so the ORM projection, the service mocks and
// db/embed_gen.go all shipped without a Source line, and every freshly
// scaffolded project reported gen-missing-source on forge's own output.
// That is noise the user cannot clear: the file says do not edit it. A
// per-emitter test pins the three that were caught; this pins the
// INVENTORY, so the next emitter that forgets fails here rather than in a
// user's first `forge lint`.
func TestTier1GenFilesPassScaffoldOwnershipLint(t *testing.T) {
	inputs, identity := renders(t)

	checked := 0
	for _, r := range append(append([]*renderResult{}, inputs...), identity) {
		paths := make([]string, 0, len(r.Bodies))
		for p := range r.Bodies {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fs := scaffolds.LintGeneratedHeader(p, r.Bodies[p])
			if strings.HasSuffix(p, "_gen.go") || strings.HasSuffix(p, "_gen_test.go") {
				checked++
			}
			for _, f := range fs {
				t.Errorf("fixture %s: %s fails forge's own scaffold-ownership lint [%s]: %s\n"+
					"Stamp the emitter's header with a \"// Source: <input>\" line naming what the "+
					"file is derived from.", r.Label, p, f.Rule, f.Message)
			}
		}
	}
	// A guard that judged nothing proved nothing.
	if checked == 0 {
		t.Fatal("no Tier-1 _gen file in any render — the inventory channel this guard reads has changed")
	}
}

// TestTier1GoFilesCarryGoGeneratedBanner holds every Tier-1 .go file forge
// renders — not only the _gen.go ones, and whether or not it already says
// "Code generated" somewhere — to Go's own definition of a generated file:
// go/ast.IsGenerated, the `^// Code generated .* DO NOT EDIT\.$` comment
// before the package clause that gopls and golangci-lint key on.
//
// TestTier1GenFilesPassScaffoldOwnershipLint above cannot catch a file with
// NO banner: the lint rule only judges a file that claims to be generated.
// The ORM's store_gen.go was exactly that — stamped forge:hash, so forge
// owned it, but bannerless, so golangci-lint treated it as hand-written,
// `forge lint`'s auto-fix pass rewrote it, and forge's own drift gate then
// failed on the rewrite.
func TestTier1GoFilesCarryGoGeneratedBanner(t *testing.T) {
	inputs, identity := renders(t)

	checked := 0
	for _, r := range append(append([]*renderResult{}, inputs...), identity) {
		paths := make([]string, 0, len(r.Bodies))
		for p := range r.Bodies {
			if strings.HasSuffix(p, ".go") {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)
		for _, p := range paths {
			checked++
			f, err := parser.ParseFile(token.NewFileSet(), p, r.Bodies[p], parser.ParseComments|parser.PackageClauseOnly)
			if err != nil {
				t.Errorf("fixture %s: %s does not parse: %v", r.Label, p, err)
				continue
			}
			if !ast.IsGenerated(f) {
				t.Errorf("fixture %s: %s is Tier-1 (forge-owned, regenerated every run) but Go does not "+
					"recognise it as generated. Open the emitter's render with "+
					"`// Code generated by forge. DO NOT EDIT.` before the package clause.", r.Label, p)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no Tier-1 .go file in any render — the inventory channel this guard reads has changed")
	}
}

// TestTier1InventoryIsProducerDerived documents and checks the inventory
// method: the set comes from the producer's own bookkeeping, so an
// emitter added anywhere enters this guard's scope with no list to edit.
//
// It also pins the chokepoint property the method depends on. If a future
// change adds a Tier-1 writer that does NOT route through
// checksums.WriteGeneratedFile, the set silently under-reports and this
// guard silently narrows — the exact failure of the earlier audit that
// read one table in upgrade.go and concluded "exactly 6 Tier-1 files".
func TestTier1InventoryIsProducerDerived(t *testing.T) {
	inputs, _ := renders(t)
	a := inputs[0]

	if len(a.Tier1) == 0 {
		t.Fatal("empty inventory (see assertInventoryIsReal)")
	}

	// The set must be broad rather than one emitter's output: a real
	// pipeline run touches many distinct top-level directories.
	dirs := map[string]bool{}
	for p := range a.Tier1 {
		dirs[strings.SplitN(p, "/", 2)[0]] = true
	}
	if len(dirs) < 4 {
		var got []string
		for d := range dirs {
			got = append(got, d)
		}
		sort.Strings(got)
		t.Errorf("Tier-1 inventory spans only %d top-level dir(s) %v — that looks like a single "+
			"emitter's output, not a full pipeline run", len(dirs), got)
	}

	// The pipeline must have populated the set through the checksums
	// chokepoint; nothing else writes it.
	//
	// Read from the render, not from this process: renders run in child
	// processes, so checksums.Tier1TargetSet HERE is legitimately empty
	// and asserting on it would only prove that the parent never
	// rendered. a.ChokepointTargets is the same measurement taken in the
	// process that did.
	if a.ChokepointTargets == 0 {
		t.Error("checksums.Tier1TargetSet was empty in the rendering process — the inventory channel " +
			"this guard depends on has changed; re-derive the method before trusting any verdict")
	}
}
