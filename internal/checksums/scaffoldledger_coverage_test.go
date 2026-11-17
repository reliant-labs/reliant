// The SCOPE guard for the scaffold-once birth ledger.
//
// The behavioral tests beside this file prove one writer honours a
// deletion. This one proves there is no SECOND writer that doesn't —
// because "fixing one instance of a general bug" is exactly how this
// defect shipped: handlers_crud_test.go was caught in a real run, but the
// same `os.Stat` + skip shape was independently authored at a dozen sites,
// each one silently resurrecting a file its own banner promised not to.
//
// DERIVATION. The set of scaffold-once write sites is derived by PARSING
// the Go source of forge's generation packages (go/ast) and finding calls
// to the known scaffold-once writers. It is not a hand-maintained list: a
// list would go stale the moment someone adds a writer, which is the
// failure mode this guard exists to prevent. The assertion then checks
// that each such site's enclosing function also consults the ledger.
//
// EMPTY SETS FAIL LOUDLY. If the AST walk finds zero call sites, the walk
// itself has broken (a moved package, a renamed helper) and every
// assertion below would be vacuously true. That is reported as a failure,
// not as a pass.
package checksums

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// scaffoldOnceChokepoints are the two write-once helpers that own the
// birth decision. Callers that route through these inherit the ledger for
// free and are NOT the hazard — the hazard is a site that skips them.
var scaffoldOnceChokepoints = map[string]bool{
	"WriteScaffoldIfMissing":    true,
	"writeUserScaffoldIfAbsent": true,
}

// rawScaffoldWriters write a scaffold file WITHOUT going through a
// chokepoint: they take already-rendered bytes and put them on disk. A
// function that calls one of these makes the birth decision itself.
//
// Calling one is NOT by itself a defect. Two shapes use these writers and
// only one is scaffold-once:
//
//   - RECONCILERS (handlers.go, handlers_crud.go, compose.go,
//     lifecycle.go, providers.go's ORM retrofit) write in BOTH arms —
//     create when absent, splice additively when present. Forge is a
//     steward of part of their content by design, so an absent file is
//     genuinely a file to create.
//   - SCAFFOLD-ONCE writers write in ONE arm only: absent → render, present
//     → return. That single-armed shape IS the defect, because its only
//     question is "is it here?" and a deleted file answers the same as a
//     never-written one.
//
// So the guard below flags a function only when it both calls a raw writer
// and has a presence check whose exists-branch returns without writing.
// Deliberately forge's OWN raw writer only, not os.WriteFile.
//
// An earlier version of this guard also matched os.WriteFile, on the
// reasoning that some scaffold emitters render and write bytes directly.
// That did find three real defects (the frontend nav/dashboard scaffolds,
// the hooks starter test, the middleware contract test — all now fixed),
// but os.WriteFile is forge's most common call and the great majority of
// its Stat-gated uses are not scaffolds at all: temp probes, buf tool
// configs, workspace package.json files, `forge new` bootstrap. Keeping it
// meant an allowance list that grew without bound, and an allowance list
// that grows without bound is a guard being negotiated down one entry at a
// time until it means nothing.
//
// So this matches the precise signal — forge's own user-scaffold writer —
// and the os.WriteFile emitters are covered by TestScaffoldOnceBannerFiles
// below, which derives its subject from the BANNER rather than the call.
var rawScaffoldWriters = map[string]bool{
	"writeUserScaffold": true,
}

// birthGates are the calls a single-armed scaffold-once writer uses to
// decide "should I write?". BOTH forms are listed on purpose:
//
//   - os.Stat is the DEFECTIVE form (presence, a two-state answer);
//   - ScaffoldOnceDecision is the CORRECT one (the ledger, three-state).
//
// Recognizing both is what keeps this guard's subject non-empty after the
// fix. If only os.Stat counted, repairing every site would empty the
// derived set and the guard would go quietly inert — passing forever while
// checking nothing, which is the exact failure mode the empty-set alarm
// below exists to catch.
var birthGates = map[string]bool{
	"Stat":                 true,
	"ScaffoldOnceDecision": true,
}

// ledgerConsulters are the ledger entry points. A scaffold-once write site
// is compliant when its enclosing function calls one of these — either the
// combined decision helper or the raw predicate.
var ledgerConsulters = map[string]bool{
	"ScaffoldOnceDecision": true,
	"ScaffoldRecorded":     true,
	"RecordScaffold":       true,
	"SplitScaffoldPath":    true,
}

// allowed lists functions that match the single-armed shape but are NOT
// scaffold-once emissions of a user-owned, banner-carrying project file.
// Each entry states WHY, because a silent allowance is how a guard erodes.
// Do NOT add an entry to make a real scaffold pass — fix the scaffold.
var allowed = map[string]string{
	// The chokepoint ITSELF. It resolves birth through the ledger when it
	// can key one (SplitScaffoldPath), and falls back to a presence check
	// only for paths outside any project — a bare temp dir in a unit test,
	// where there is no .forge/ to record into. The fallback is the reason
	// the AST sees a Stat here.
	"writeUserScaffoldIfAbsent": "the chokepoint; ledger-gated, presence only as the no-project fallback",

	// `forge db import` is an explicit one-shot user COMMAND writing
	// migration files the user asked for by name, not a generate-time
	// scaffold. It has --force and --dry-run for its own re-run story, and
	// nothing about it is regenerated on a later `forge generate`.
	"runMigrateImport": "explicit one-shot CLI command, not a generate-time scaffold",

	// Frontend workspace INFRASTRUCTURE (package.json, tsconfig.json, the
	// ui-web barrel). These are npm/tsc contract files, not files carrying
	// forge's "yours: scaffolded once" banner: deleting package.json is a
	// broken workspace, not an ownership decision, and re-creating it is
	// the correct repair rather than an override of user intent.
	"writeIfMissing":   "workspace infrastructure (package.json/tsconfig), not a banner-carrying scaffold",
	"writeUIWebBarrel": "workspace infrastructure (re-export barrel), not a banner-carrying scaffold",

	// A BACKFILL for frontends that predate the shared static scaffold
	// tree. It exists to bring an old project up to the current baseline;
	// its absence in such a project means "never had it", not "removed
	// it". New projects get this file from the static tree, never here.
	"ensureViteQueryResourceHook": "one-time backfill for pre-existing frontends, not a birth emitter",

	// A PID-scoped temp probe (zz_forge_config_probe_<pid>.k) written and
	// deleted within one call to read KCL's config projection. Never a
	// project file the user could own or delete.
	"loadProjectConfigEnvMap": "PID-scoped temp probe file, written and removed in-call",

	// buf.gen.yaml is TOOL CONFIGURATION regenerated to match the current
	// proto/frontend layout, not a "yours" file. Its Stat-gated arms pick
	// which variant to write, not whether the user still wants it.
	"runBufGenerateTypeScript":          "buf tool config, re-derived from project layout",
	"runBufGenerateTypeScriptWorkspace": "buf tool config, re-derived from project layout",

	// The WALKER around the per-package contract scaffolds. Its own
	// Stat-gated arm is the contract.go discovery probe; the scaffold-once
	// decision for contract_test.go inside it IS ledger-gated (see
	// generate_middleware.go).
	"generateInternalPackageContracts": "walker; the contract_test.go birth decision inside it is ledger-gated",
}

// generationPackages are the trees that emit project files. Scoped
// relative to this package's directory so the test is location-independent.
var generationPackages = []string{
	"../codegen",
	"../cli",
	"../generator",
	"../scaffold",
	".",
}

// writeSite is one discovered scaffold-once write.
type writeSite struct {
	file     string
	function string
	writer   string
	// gate is the call the function's write decision branches on.
	gate string
	// consultsLedger is true when that GATE is a ledger entry point.
	consultsLedger bool
}

// TestScaffoldOnceWriteSitesConsultTheLedger walks forge's generation
// packages and asserts every scaffold-once write site resolves birth
// through the ledger rather than through a bare existence check.
func TestScaffoldOnceWriteSitesConsultTheLedger(t *testing.T) {
	sites := discoverScaffoldWriteSites(t)

	// FAIL LOUDLY on an empty derivation: a walk that finds nothing has
	// lost its subject, and passing would be a false all-clear.
	if len(sites) == 0 {
		t.Fatalf("derived scaffold-once write-site set is EMPTY. The AST walk over %v "+
			"found no calls to any of %v — the writers were renamed or the packages "+
			"moved, and this guard is now inert rather than satisfied.",
			generationPackages, keysOf(rawScaffoldWriters))
	}

	var offenders []writeSite
	for _, s := range sites {
		if !s.consultsLedger {
			offenders = append(offenders, s)
		}
	}
	if len(offenders) > 0 {
		var b strings.Builder
		b.WriteString("scaffold-once write sites that never consult the birth ledger:\n")
		for _, o := range offenders {
			b.WriteString("  - " + o.file + ": " + o.function + "() calls " + o.writer +
				"(), gated on " + o.gate + "()\n")
		}
		b.WriteString("\nEach of these decides 'have I already scaffolded this?' from the file's\n")
		b.WriteString("PRESENCE, which cannot distinguish 'never written' from 'written and then\n")
		b.WriteString("deleted by the user'. The second is a deliberate act of ownership and the\n")
		b.WriteString("file's own banner promises forge will not undo it.\n")
		b.WriteString("Fix: gate the write on checksums.ScaffoldOnceDecision(root, relPath) and\n")
		b.WriteString("call checksums.RecordScaffold(root, relPath) after a successful write.")
		t.Fatal(b.String())
	}

	t.Logf("verified %d scaffold-once write site(s) consult the birth ledger", len(sites))
}

// discoverScaffoldWriteSites parses every non-test .go file in the
// generation packages and returns each function that calls a scaffold-once
// writer, noting whether it also consults the ledger.
func discoverScaffoldWriteSites(t *testing.T) []writeSite {
	t.Helper()
	var sites []writeSite
	fset := token.NewFileSet()

	for _, pkgDir := range generationPackages {
		entries, err := os.ReadDir(pkgDir)
		if err != nil {
			// A package that isn't there is a broken premise, not a pass.
			t.Fatalf("read generation package %s: %v", pkgDir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(pkgDir, name)
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				t.Fatalf("parse %s: %v", path, perr)
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				writers, _ := scanFuncCalls(fn)
				if len(writers) == 0 {
					continue
				}
				// Only single-armed (scaffold-once) shapes are in scope;
				// reconcilers legitimately write on the absent path.
				gate, gated := birthGateOf(fn)
				if !gated {
					continue
				}
				if _, ok := allowed[fn.Name.Name]; ok {
					continue
				}
				for _, w := range writers {
					sites = append(sites, writeSite{
						file:     path,
						function: fn.Name.Name,
						writer:   w,
						gate:     gate,
						// The DECISION must be the ledger's. Calling the
						// ledger elsewhere in the function does not count.
						consultsLedger: ledgerConsulters[gate],
					})
				}
			}
		}
	}
	return sites
}

// scanFuncCalls returns the scaffold-once writers fn calls, and whether fn
// also calls any ledger entry point. Both plain calls (writeX(...)) and
// selector calls (checksums.WriteX(...)) are recognized, since the same
// helper is reached one way from inside the package and the other from
// outside it.
func scanFuncCalls(fn *ast.FuncDecl) (writers []string, consultsLedger bool) {
	seen := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if name == "" {
			return true
		}
		if rawScaffoldWriters[name] && !seen[name] {
			seen[name] = true
			writers = append(writers, name)
		}
		// A function that routes through a chokepoint has delegated the
		// birth decision to it, and the chokepoint consults the ledger.
		if scaffoldOnceChokepoints[name] {
			consultsLedger = true
		}
		if ledgerConsulters[name] {
			consultsLedger = true
		}
		return true
	})
	return writers, consultsLedger
}

// scaffoldOnceBanner is the promise the linter enforces on Tier-2
// templates. A file carrying it tells its reader, in forge's own words,
// that forge will not write this path again.
const scaffoldOnceBanner = "yours: scaffolded once"

// TestScaffoldOnceBannerFilesAreLedgerGated checks the BANNER's truth
// rather than any particular call shape.
//
// This is the task the call-shape guard above cannot do: an emitter that
// renders a banner-carrying template and writes it with a bare
// os.WriteFile is invisible to a writer-name scan, but it makes exactly
// the same false promise. So the subject here is derived from the
// TEMPLATE CORPUS — every template whose own bytes carry the banner — and
// the assertion is that the Go code emitting it is ledger-aware.
//
// The mapping from template to emitter is derived by searching the
// generation packages for the template's filename, so adding a
// banner-carrying template with a fresh emitter is caught rather than
// silently uncovered.
func TestScaffoldOnceBannerFilesAreLedgerGated(t *testing.T) {
	if testing.Short() {
		t.Skip("scans every template and generator source in the repository; runs in task test")
	}
	bannered := bannerCarryingTemplates(t)
	if len(bannered) == 0 {
		t.Fatalf("derived banner-carrying template set is EMPTY — the walk over "+
			"the template corpus found no file containing %q. The banner text "+
			"changed or the corpus moved, and this guard is inert.", scaffoldOnceBanner)
	}

	sources := generationSources(t)
	if len(sources) == 0 {
		t.Fatal("derived generation-source set is EMPTY — no Go sources to check")
	}

	var unreferenced []string
	var notLedgerAware []string
	for _, tmplName := range bannered {
		emitters := sourcesReferencing(sources, tmplName)
		if len(emitters) == 0 {
			// Nothing renders it by name (static-tree file, or referenced
			// through a directory walk). Not a defect on its own.
			unreferenced = append(unreferenced, tmplName)
			continue
		}
		// Only GENERATE-TIME emitters can exhibit the defect. `forge
		// package new`, `forge scaffold binary` and friends run once, at an
		// explicit user request, and write into a directory they just
		// created — there is no later run to resurrect anything, and their
		// re-run story is their own (they refuse or --force). The defect is
		// specifically a file that comes BACK on the next `forge generate`.
		generateTime := emitters[:0:0]
		for _, src := range emitters {
			if isGenerateTimeEmitter(src, sources[src]) {
				generateTime = append(generateTime, src)
			}
		}
		if len(generateTime) == 0 {
			unreferenced = append(unreferenced, tmplName)
			continue
		}
		ledgerAware := false
		for _, src := range generateTime {
			if strings.Contains(sources[src], "ScaffoldOnceDecision") ||
				strings.Contains(sources[src], "WriteScaffoldIfMissing") ||
				strings.Contains(sources[src], "writeUserScaffoldIfAbsent") ||
				strings.Contains(sources[src], "writeForgeScaffoldOnce") ||
				strings.Contains(sources[src], "RecordScaffold") {
				ledgerAware = true
				break
			}
		}
		if !ledgerAware {
			notLedgerAware = append(notLedgerAware, tmplName+" (emitted by "+strings.Join(generateTime, ", ")+")")
		}
	}

	if len(notLedgerAware) > 0 {
		sort.Strings(notLedgerAware)
		t.Fatalf("template(s) carrying the %q banner whose emitter never consults "+
			"the scaffold ledger:\n  - %s\n\n"+
			"The banner is a PROMISE forge makes to the file's reader. An emitter "+
			"that decides on presence alone breaks it the moment the user deletes "+
			"the file: the deletion is read as an absence and the file comes back.",
			scaffoldOnceBanner, strings.Join(notLedgerAware, "\n  - "))
	}

	t.Logf("verified %d banner-carrying template(s); %d not referenced by name (static tree)",
		len(bannered)-len(unreferenced), len(unreferenced))
}

// isGenerateTimeEmitter reports whether a source file participates in the
// `forge generate` pipeline, as opposed to being a one-shot `forge
// package new` / `forge scaffold ...` / `forge new` command.
//
// The signal is the pipeline's own plumbing: generate-time emitters are
// reached from the pipeline and thread its context or checksums state,
// while one-shot commands are cobra entry points that build their target
// directory from scratch.
func isGenerateTimeEmitter(path, body string) bool {
	base := filepath.Base(path)
	// Explicit one-shot command entry points.
	switch base {
	case "package.go", "new.go", "binary_gen.go", "skill.go", "skills.go", "e2e.go":
		return false
	}
	if strings.HasPrefix(base, "upgrade") {
		return false // `forge project upgrade`, its own lifecycle
	}
	// Generate-pipeline participants carry its plumbing.
	return strings.Contains(body, "pipelineContext") ||
		strings.Contains(body, "*checksums.FileChecksums") ||
		strings.Contains(body, "cs *FileChecksums") ||
		strings.HasPrefix(base, "generate_")
}

// bannerCarryingTemplates walks the template corpus and returns the base
// names of every file whose bytes carry the scaffold-once banner.
func bannerCarryingTemplates(t *testing.T) []string {
	t.Helper()
	root := "../templates"
	var out []string
	seen := map[string]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		// Skip forge's own Go tests and the golden fixtures — neither is a
		// template forge renders into a project.
		if strings.HasSuffix(path, "_test.go") || strings.Contains(path, "testdata") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil || !carriesScaffoldOnceBanner(string(data)) {
			return nil
		}
		base := filepath.Base(path)
		if !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk template corpus: %v", err)
	}
	sort.Strings(out)
	return out
}

// carriesScaffoldOnceBanner reports whether a file DECLARES itself
// scaffold-once, as opposed to merely mentioning the banner in prose.
//
// The distinction is load-bearing. The shipped skills (architecture/,
// ci/, …) are Tier-1 files — correctly forge-owned and regenerated every
// run — that TEACH the banner convention by quoting it. Treating a
// documentation mention as a declaration reported those skills as broken
// promises when the promise was never theirs to make.
//
// A declaration lives in the file's HEAD, where a banner goes; prose
// quoting it appears further down, in a sentence.
func carriesScaffoldOnceBanner(body string) bool {
	lines := strings.Split(body, "\n")
	if len(lines) > 8 {
		lines = lines[:8]
	}
	for _, line := range lines {
		if !strings.Contains(line, scaffoldOnceBanner) {
			continue
		}
		// A banner is a whole-line comment, not a clause inside a sentence.
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimLeft(trimmed, "/#-<!* \t")
		if strings.HasPrefix(trimmed, scaffoldOnceBanner) {
			return true
		}
	}
	return false
}

// generationSources reads every non-test Go file in the generation
// packages, keyed by path.
func generationSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range generationPackages {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			out[path] = string(data)
		}
	}
	return out
}

// sourcesReferencing returns the sorted paths of sources mentioning name.
func sourcesReferencing(sources map[string]string, name string) []string {
	var out []string
	for path, body := range sources {
		if strings.Contains(body, name) {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// birthGateOf finds fn's single-armed "should I write?" guard and reports
// WHICH gate it uses. A reconciler's exists-branch does real work
// (splicing, appending) and calls a writer, so it is not a birth gate and
// yields ("", false).
//
// Returning the gate NAME — rather than a bare "is it guarded?" — is what
// makes the assertion meaningful: a function may call the ledger somewhere
// (e.g. RecordScaffold after writing) while still DECIDING on os.Stat, and
// that function is exactly as broken as one that never mentions the ledger.
// The decision is what must be ledger-based.
func birthGateOf(fn *ast.FuncDecl) (gate string, found bool) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		g, ok := gatesOnBirth(ifStmt)
		if !ok {
			return true
		}
		// The taken branch must bail WITHOUT writing.
		if branchWrites(ifStmt.Body) || !endsInReturn(ifStmt.Body) {
			return true
		}
		gate, found = g, true
		return false
	})
	return gate, found
}

// gatesOnBirth matches the two single-armed "should I write?" shapes:
//
//	if _, err := os.Stat(p); err == nil {          // defective (presence)
//	if !checksums.ScaffoldOnceDecision(root, r) {  // correct (ledger)
//
// Both are a guard whose taken branch means "do not write".
// It handles both the init form (`if _, err := os.Stat(p); err == nil`)
// and the guard-clause form (`if !ScaffoldOnceDecision(...)`), including
// compound conditions such as `if haveLedger && !ScaffoldOnceDecision(...)`
// — a real call site shape, since some emitters can only key a ledger when
// the path resolves to a project root.
//
// When a condition mentions BOTH kinds of gate, the ledger wins: a guard
// that consults the ledger has made the three-state decision, and any
// os.Stat alongside it is doing something else (pruning a stale sibling,
// distinguishing an error from an absence).
func gatesOnBirth(ifStmt *ast.IfStmt) (gate string, ok bool) {
	var found []string
	collect := func(n ast.Node) {
		if n == nil {
			return
		}
		ast.Inspect(n, func(x ast.Node) bool {
			call, isCall := x.(*ast.CallExpr)
			if !isCall {
				return true
			}
			if name := calleeName(call.Fun); birthGates[name] {
				found = append(found, name)
			}
			return true
		})
	}
	collect(ifStmt.Init)
	collect(ifStmt.Cond)
	if len(found) == 0 {
		return "", false
	}
	for _, g := range found {
		if ledgerConsulters[g] {
			return g, true
		}
	}
	return found[0], true
}

// branchWrites reports whether a block calls any scaffold writer — the
// signature of a reconciler's exists-arm rather than a bare skip.
func branchWrites(b *ast.BlockStmt) bool {
	wrote := false
	ast.Inspect(b, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if rawScaffoldWriters[name] || scaffoldOnceChokepoints[name] || name == "WriteFile" {
			wrote = true
			return false
		}
		return true
	})
	return wrote
}

// endsInReturn reports whether the block's last statement is a return —
// the "leave it alone" bail.
func endsInReturn(b *ast.BlockStmt) bool {
	if len(b.List) == 0 {
		return false
	}
	_, ok := b.List[len(b.List)-1].(*ast.ReturnStmt)
	return ok
}

// calleeName extracts the called identifier from either `f(...)` or
// `pkg.F(...)`.
func calleeName(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
