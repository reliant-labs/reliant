// File: internal/contractcheck/internal_pkg_contract.go
//
// The forgeconv-internal-package-contract-names rule asserts that every
// internal-package contract.go declares the shape the injector wires:
//
//   - a contract interface — `Service`, or any name carrying
//     `//forge:service` / `//forge:contract`
//   - `type Deps struct { ... }`
//   - a constructor `func(Deps) <Contract>` or `func(Deps) (<Contract>, error)`
//     — `New`, or any name carrying `// forge:constructor`
//
// The injector (internal/codegen/inject_gen.go) emits
// `<pkg>.<Constructor>(<pkg>.Deps{...})` and types the field off the
// contract interface, resolving BOTH names through the same detectors this
// rule uses (codegen.DetectServiceInterfaceName, codegen.IsComponentConstructor
// / codegen.DetectConstructorName). Lint and codegen therefore cannot
// disagree: whatever this rule accepts, generate can wire.
//
// The names are free because forcing them is an antipattern — `Service` and
// `New` are poor names for a mailer or a store, and forge is meant to promote
// good practice, not conscript packages into bad naming. What is NOT free is
// the signature: a constructor that doesn't take `Deps` and return the
// contract has nothing for the injector to bind, so `forge generate` would
// write a compile-broken internal/app/compose.go pointing the user at
// generated code they don't own. This rule surfaces that before generate runs.
//
// The two-result form `(Service, error)` was introduced in Day-5 polish
// alongside `validateDeps()` so the bootstrap can surface required-Deps
// gaps once at startup instead of forcing per-RPC nil-checks. The
// scaffold templates emit the two-result form for new packages; the
// single-result form remains accepted so pre-Day-5 packages continue to
// pass lint until they're refactored.
//
// Migrated from internal/linter/forgeconv/internal_pkg_contract.go on
// 2026-06-04 as part of collapsing forge's three contract-check entry
// points onto a shared engine. The detection logic is byte-for-byte
// preserved; only the surrounding API (export name → lowercase, return
// type uses forgeconv.Result) changed.

package contractcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// internalPackageDirs walks rootDir/internal/ once and returns every
// package directory the two canonical-shape rules consider, split by
// whether it declares a contract.go. The split is the ONLY difference
// between the two rules' inputs: everything else — the testdata/ skip, the
// forge.yaml `contracts.exclude` list, the per-package
// `//forge:exclude-contract` directive — is applied here, so a package can
// never be in scope for one rule and out of scope for the other. Both
// slices are sorted, so findings come out in deterministic order.
//
// The excludes argument carries module-relative directory paths (matching
// forge.yaml `contracts.exclude`) that the walk must skip wholesale: those
// packages are not bootstrap-managed (analyzer sub-packages, embed-only
// packages, the cli surface itself), and their contract.go files are
// allowed to declare alternate shapes.
//
// withoutContract is narrower than withContract in one further way, and the
// reason is the verb, not the shape. `forge scaffold package <name>` creates
// `internal/<name>/` and its name validator rejects "/", so an IMMEDIATE
// child of internal/ is precisely the set of directories that verb owns.
// Everything deeper belongs to another role — `internal/handlers/<svc>/`,
// `internal/workers/<pkg>/`, `internal/operators/<pkg>/` are components of
// their own kind, scaffolded by their own verbs, and they deliberately never
// carry a contract.go. Measured on control-plane, recursing produced five
// findings against `internal/handlers/<svc>/service.go` — forge telling the
// author to run `forge scaffold package` on a file `forge scaffold service`
// wrote. The day the package verb accepts a nested name, this widens with
// it. (withContract stays fully recursive: nested contract packages like
// "mcp/database" ARE discovered and wired by codegen, so the shape rule has
// to keep judging them.)
//
// A directory lands in withoutContract only if it actually holds Go source
// the shape scan can read — a parent directory that only holds
// subdirectories (internal/, internal/handlers/) is not a package.
//
// A missing rootDir/internal is not an error: CLI and library projects
// legitimately have none.
func internalPackageDirs(rootDir string, excludes []string) (withContract, withoutContract []string, err error) {
	internalDir := filepath.Join(rootDir, "internal")
	if _, statErr := os.Stat(internalDir); os.IsNotExist(statErr) {
		return nil, nil, nil
	}

	// Delegate to the canonical [config.MatchExclude]. Pre-2026-06 this
	// analyzer hand-rolled the equality / "/"-suffix / substring rule
	// inline to avoid depending on internal/config. The three copies
	// (config, contract/exclude, this file) drifted over time on
	// empty-pattern handling and slash normalisation, which produced
	// the exact bug class the lint exists to prevent: a path that
	// matched under `forge lint --contract` but not under the
	// pre-codegen check (or vice versa). One shared helper, one
	// behaviour.
	isExcluded := func(relSlash string) bool {
		return config.MatchExclude(excludes, relSlash)
	}

	walkErr := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		// Skip testdata/ subtrees — fixture contracts, not real packages.
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		// Honor excludes (matched on the module-relative slash path).
		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			return relErr
		}
		if isExcluded(filepath.ToSlash(rel)) {
			return filepath.SkipDir
		}
		// notExcludedByDirective is the per-package opt-out:
		// `//forge:exclude-contract` in the package's source is the
		// local-header equivalent of a forge.yaml contracts.exclude entry.
		// Union with the central excludes above: either source opts the
		// package out of the canonical shape. It is evaluated only once a
		// directory is otherwise a candidate — it parses every .go file in
		// the package, and the walk visits directories that are neither. Do
		// NOT SkipDir on a hit: descendants may still be components and carry
		// their own directive. This MUST agree with the generate-time walks
		// (generate_middleware.go's mock walk and generate_bootstrap.go's
		// discoverPackages) so a header-only exclude is honored consistently
		// across lint AND codegen — otherwise a package excluded for codegen
		// would still fail the pre-codegen shape check.
		notExcludedByDirective := func() bool { return !codegen.HasExcludeContractDirective(path) }

		_, statErr := os.Stat(filepath.Join(path, "contract.go"))
		switch {
		case statErr == nil:
			if notExcludedByDirective() {
				withContract = append(withContract, path)
			}
		case os.IsNotExist(statErr):
			if filepath.Dir(path) == internalDir && hasContractScannableSource(path) && notExcludedByDirective() {
				withoutContract = append(withoutContract, path)
			}
		default:
			return statErr
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, fmt.Errorf("walk %s: %w", internalDir, walkErr)
	}

	sort.Strings(withContract)
	sort.Strings(withoutContract)
	return withContract, withoutContract, nil
}

// hasContractScannableSource reports whether dir holds at least one .go
// file the shape scan would actually read. The filter matches
// scanPackageContractShape exactly (non-test, non-generated), so "there is
// something to judge" and "here is the judgement" can never disagree.
func hasContractScannableSource(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") ||
			strings.HasSuffix(n, "_test.go") || strings.HasSuffix(n, "_gen.go") {
			continue
		}
		return true
	}
	return false
}

// lintMissingContract is the complement of [lintInternalContracts]: that
// rule asks whether a contract.go declares the canonical shape, this one
// asks whether a package that HAS the shape declares a contract.go at all.
//
// The gap it closes is real and was measured. `forge scaffold package`
// writes contract.go, service.go, contract_test.go and mock_gen.go
// together; a component created with a raw file write instead has none of
// them, and every downstream mechanism keys on contract.go —
// codegen.DiscoverInternalPackages (bootstrap wiring), the mock walk, the
// observe decorator, `forge project map|graph|audit`. So the package is not
// half-wired, it is invisible: `forge lint` passed on a hand-written
// component in a dogfood run and named nothing.
//
// # What "looks like a component" means here
//
// internal/ legitimately holds packages that are not components —
// constants, domain errors, pure helpers, an interface plus the one type
// that implements it — and telling every one of them to run a scaffold verb
// is how a rule gets muted. So the trigger is not "has behaviour" or "has an
// interface". It is that the package is already speaking forge's contract
// VOCABULARY, resolved by the same detector codegen and the shape rule use:
//
//   - an interface named `Service`, or one carrying `//forge:service` /
//     `//forge:contract` (codegen.DetectServiceInterfaceName)
//   - `type Deps struct`
//
// Nobody writes `type Deps struct`, names an interface `Service`, or marks
// one `//forge:service` by accident. Each is an unambiguous claim to be the
// thing `forge scaffold package` produces — made without running it.
//
// The injector's constructor signature `func(Deps) <Contract>` is NOT a
// third disjunct, because in any package that compiles it implies a `Deps`
// type declaration and the second claim has already fired. A branch no
// realistic package can reach on its own is a branch no test can pin.
//
// [shouldSkipContractShapeCheck] is deliberately NOT the gate, and the
// measurement is why. Its interface-count early-outs are calibrated for a
// package that already declared itself a contract by WRITING a contract.go;
// applied to packages that never did, its residue is one shape —
// `ifaces=1, no Deps, no constructor` — and swept across four real trees
// (forge, control-plane, peptides-dogfood-r2, reliant) that shape produced
// 13 findings, every one of them an abstraction seam that correctly wants no
// contract: localfs.FS, validation.Collector, devautograntplan.PlanAssigner.
// The existing early-out's own comment gives the reason — the >= 2 threshold
// exists because "a single interface declaration is more likely an
// incomplete contract than a deliberate catalogue", and "incomplete
// contract" is exactly the reading that does not apply to a package with no
// contract.go. The vocabulary trigger above fires 1 time across those same
// 95 contract-less packages: reliant's internal/worktree, which declares
// `type Service interface`, `type service struct` and
// `NewService(...) (Service, error)` — a hand-built component, correctly
// named.
//
// The same sweep is why the packages from the dogfood run stay silent: both
// are constants-plus-pure-functions packages (domain reason codes and a
// ReasonError value type; a handful of stateless policy helpers). Neither
// speaks any of forge's contract vocabulary, and neither wanted a Service, a
// Deps or a mock.
//
// Severity is WARNING. The rule reasons about intent where the sibling shape
// rule reasons about a declaration the author already made; warning is also
// what deps-are-interfaces shipped as until its false-positive rate had been
// measured on real trees and it was promoted. The same bar applies here.
func lintMissingContract(rootDir string, excludes []string) (forgeconv.Result, error) {
	_, pkgDirs, err := internalPackageDirs(rootDir, excludes)
	if err != nil {
		return forgeconv.Result{}, err
	}

	var result forgeconv.Result
	for _, dir := range pkgDirs {
		facts, scanErr := scanPackageContractShape(dir)
		if scanErr != nil {
			// A package that does not parse is reported by the compiler and
			// by the shape rule's own parse-error finding path; guessing at
			// its shape here would just add noise.
			continue
		}
		if !speaksContractVocabulary(facts) {
			continue
		}
		result.Findings = append(result.Findings, missingContractFinding(rootDir, dir, facts))
	}
	return result, nil
}

// speaksContractVocabulary reports whether the package has made one of the
// two unambiguous claims to be a forge component. See [lintMissingContract]
// for why these two and not a shape heuristic.
func speaksContractVocabulary(facts contractShapeFacts) bool {
	return facts.hasServiceInterface || facts.hasDepsStruct
}

// missingContractFinding builds the finding for a package that speaks
// forge's contract vocabulary but has no contract.go. It names WHICH
// declaration earned the verdict and anchors on that declaration's line —
// the one the author reads to agree or disagree — never on the contract.go
// that does not exist, which no editor or LSP could open.
func missingContractFinding(rootDir, pkgDir string, facts contractShapeFacts) forgeconv.Finding {
	relDir, err := filepath.Rel(rootDir, pkgDir)
	if err != nil {
		relDir = pkgDir
	}
	relDir = filepath.ToSlash(relDir)
	name := path.Base(relDir)

	// Report the stronger claim first: the contract interface names the
	// boundary, Deps names the wiring.
	declared, anchor := "a `type Deps struct`", facts.depsPos
	if facts.hasServiceInterface {
		declared = fmt.Sprintf("a contract interface %#q", facts.serviceIfaceName)
		anchor = facts.serviceIfacePos
	}

	file, line := relDir, 1
	if anchor.IsValid() {
		if rel, relErr := filepath.Rel(rootDir, anchor.Filename); relErr == nil {
			file = filepath.ToSlash(rel)
			line = anchor.Line
		}
	}

	return forgeconv.Finding{
		Rule:     string(RuleInternalPackageMissingContract),
		Severity: forgeconv.SeverityWarning,
		File:     file,
		Line:     line,
		Message: fmt.Sprintf(
			"forge convention: %s/ declares %s but no contract.go, so forge does not see it as a component "+
				"at all — no mock is generated for it, the injector does not wire it into "+
				"internal/app/compose.go, no observability decorator wraps it, and `forge project map|graph` "+
				"does not list it. This is what a package created with a raw file write instead of "+
				"`forge scaffold package %s` looks like. See skill: contracts.",
			relDir, declared, name),
		Remediation: fmt.Sprintf(
			"`forge scaffold package %[1]s` writes the shape (contract.go + service.go + contract_test.go + "+
				"mock_gen.go) and refuses to overwrite an existing directory — move %[2]s/ aside, run the verb, "+
				"then move your code into the service.go it writes. If this package is deliberately NOT a "+
				"component (a helper, a constants package, a catalogue of narrow interfaces consumed elsewhere), "+
				"put `//forge:exclude-contract: <why>` at the top of one of its .go files — the same directive that "+
				"takes a package out of bootstrap wiring and mock generation.",
			name, relDir),
	}
}

// lintInternalContracts asserts the canonical Service/Deps/New(Deps)
// Service shape on every internal package that declares a contract.go.
// The package set (and the opt-outs applied to it) comes from
// [internalPackageDirs].
//
// Returns findings in deterministic order (file, then position).
func lintInternalContracts(rootDir string, excludes []string) (forgeconv.Result, error) {
	pkgDirs, _, err := internalPackageDirs(rootDir, excludes)
	if err != nil {
		return forgeconv.Result{}, err
	}

	var result forgeconv.Result
	for _, dir := range pkgDirs {
		contractPath := filepath.Join(dir, "contract.go")
		relContract, relErr := filepath.Rel(rootDir, contractPath)
		if relErr != nil {
			relContract = contractPath
		}
		findings, err := lintInternalContractPackage(relContract, dir)
		if err != nil {
			result.Findings = append(result.Findings, forgeconv.Finding{
				Rule:     string(RuleInternalPackageContractNames),
				Severity: forgeconv.SeverityError,
				File:     relContract,
				Message:  fmt.Sprintf("failed to parse package: %v", err),
				Remediation: "fix the syntax error and re-run; the analyzer needs parseable .go files " +
					"to verify Service/Deps/New(Deps) Service",
			})
			continue
		}
		result.Findings = append(result.Findings, findings...)
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})

	return result, nil
}

// lintInternalContractPackage walks every non-test .go file in pkgDir
// and asserts the canonical Service/Deps/New(Deps) Service shape lives
// somewhere in the package — not necessarily all in contract.go.
// Pre-2026-05-06 the rule required all three in contract.go itself,
// which false-positived on packages that split Service into contract.go
// and Deps + New into a sibling file (the `--kind=client` and
// `--type=adapter` scaffolds both do this). The fix moves
// detection to package scope while keeping findings anchored on
// contract.go (so the user knows where the contract is documented).
//
// Returns one finding per missing/wrong piece so a completely-renamed
// contract surfaces all three at once (Service / Deps / New) rather
// than drip-feeding one violation per re-run.
func lintInternalContractPackage(relContractPath, pkgDir string) ([]forgeconv.Finding, error) {
	// The shape exists so the server composition can construct the
	// package: `<pkg>.<Constructor>(<pkg>.Deps{...})`, typed off the
	// contract. A `//forge:not-a-component` package is one the composition
	// never constructs — something else builds it — so there is nothing
	// for the shape to make bindable. Before this check, such a package
	// passed only by accident of the interface-catalogue early-out, and
	// deleting an unrelated private interface (a scan helper) flipped it
	// to three findings demanding Service/Deps/New.
	if codegen.HasNotAComponentDirective(pkgDir) {
		return nil, nil
	}

	facts, err := scanPackageContractShape(pkgDir)
	if err != nil {
		return nil, err
	}

	if shouldSkipContractShapeCheck(facts) {
		return nil, nil
	}

	// A finding anchors on the declaration it names when there is one
	// (the non-canonical interface, struct or constructor the author
	// wrote), in that declaration's OWN file. Only when nothing was found
	// does it fall back to contract.go:1, the package boundary. Pairing
	// contract.go with a line number taken from another file (the old
	// behavior) pointed at an unrelated line, and a `forge:lint-disable`
	// written above the real declaration could never reach it.
	relPath := relContractPath

	var findings []forgeconv.Finding
	if !facts.hasServiceInterface {
		findings = append(findings, missingServiceFinding(relPath, facts))
	}
	if !facts.hasDepsStruct {
		findings = append(findings, missingDepsFinding(relPath, facts))
	}
	// A SEAM contract is consumed, not produced, so `func(Deps) <Contract>`
	// is not the shape its constructor can have — see contractIsSeam for why
	// demanding it made the multi-impl refusal's own remedy impossible to
	// complete.
	if !facts.hasConstructor && !facts.contractIsSeam() {
		findings = append(findings, missingConstructorFinding(relPath, facts))
	}

	return findings, nil
}

// contractShapeFacts captures everything the canonical-names check needs
// to know about a package: which of the three canonical declarations are
// present, the interface count that drives the two shape early-outs, and
// the first non-canonical interface/struct/constructor found (so findings
// can name what the user actually wrote instead).
type contractShapeFacts struct {
	hasServiceInterface bool
	hasDepsStruct       bool
	hasConstructor      bool
	// interfaceCount counts every interface declaration anywhere in
	// the package. When >= 2 AND no Deps struct AND no New func, the
	// package is recognized as an "interface-catalogue" shape — a
	// collection of narrow interfaces consumed elsewhere, not a
	// Service-shape package the bootstrap template binds to. We
	// skip the canonical-names check in that case so the user
	// doesn't have to add the package to contracts.exclude just to
	// silence false-positive Service/Deps/New findings.
	// FRICTION 2026-06-02: cp-forge layer-3 natsio, layer-4 daemonstate.
	interfaceCount int
	// serviceIfaceName is the interface name this package's contract is
	// keyed on: "Service" for the zero-annotation canonical shape, or a
	// `//forge:service`/`//forge:contract`-marked name (Gateway / Provider
	// / …). Resolved once per package (codegen.DetectServiceInterfaceName)
	// BEFORE the decl walk so the interface presence check AND the New
	// return-type check both accept the same name — an author who marks a
	// role-oriented interface no longer trips the strict Service/New rule.
	serviceIfaceName string
	// Positions of the CANONICAL contract interface and Deps struct, when
	// present. The missing-contract rule anchors its finding on whichever
	// one it fired on, so the author lands on the line that earned the
	// verdict — a real file an editor can open, never the contract.go that
	// does not exist. (firstIfacePos / firstStructPos / firstCtorPos below
	// are the opposite: positions of NON-canonical near misses, for the
	// shape rule's messages.)
	serviceIfacePos token.Position
	depsPos         token.Position
	// Capture the first non-canonical interface and struct names so
	// the error message can be specific ("found 'Sender'/'Config'/'NewSender'").
	firstIfaceName  string
	firstIfacePos   token.Position
	firstStructName string
	firstStructPos  token.Position
	firstCtorName   string
	firstCtorPos    token.Position
	// firstCtorMarked records whether the near-miss constructor already
	// carries `//forge:constructor`. The marker frees a constructor's NAME
	// and has never touched its signature, so prescribing it to an author
	// who has already written it is advice that cannot work — see
	// missingConstructorFinding.
	firstCtorMarked bool
	// depsConstructor is a `func(Deps) …` the package declares whose results
	// are NOT the contract — the seam shape's entry point. Distinct from
	// hasConstructor (which requires the contract return) and from
	// firstCtorName (any near miss): this one positively took Deps, which is
	// what makes it a forge-shaped component entry rather than an arbitrary
	// helper.
	depsConstructor bool
	// ctorReturnBase is that constructor's first result type with pointer
	// stars stripped (`*Handler` → `Handler`), or "" when there is none. Used
	// to tell a SEAM contract (implemented in-package by something the
	// constructor does not return) from the package's OWN surface.
	ctorReturnBase string
	// contractIfaceMethods is the marked/canonical contract interface's
	// method names, and methodsByType is every in-package named type's
	// method set. Together they answer "is this contract implemented here by
	// a type other than the one the constructor returns" — the seam test.
	contractIfaceMethods []string
	methodsByType        map[string]map[string]bool
}

// contractIsSeam reports whether the package's contract interface is a SEAM
// the package CONSUMES rather than the behavioral surface it PRODUCES.
//
// The distinction decides whether `func(Deps) <Contract>` is the right demand,
// and the multi-impl refusal is what makes it load-bearing. That rule refuses
// a package's `//forge:exclude-contract` when an exported interface it
// declares has two or more implementations in the module — correctly, because
// interchangeable implementations ARE a contract — and prescribes moving the
// interface into a contract.go. (It no longer refuses an interface the
// package itself consumes — a Deps or struct-field seam — so a seam reaches
// this rule only when its author chose a contract.go for it.) Doing that makes the package a contract
// package, at which point the shape rule used to demand a constructor
// returning that interface.
//
// For a seam that demand is incoherent: control-plane's registryauth declares
// `PushGate` with an in-package default (`AllowAllPushes`) and a second
// implementation at the composition root, and its constructor builds the token
// realm (`*Handler`) TAKING a PushGate in Deps. A constructor returning
// PushGate would mean the quota gate constructs a quota gate. So the remedy
// the gate prescribed could not be completed, the marker the shape rule
// offered in turn was a no-op on a signature gap, and the only exit was a
// forge.yaml exclude that covers one of the two rules.
//
// The test is structural, not a new annotation: the contract is implemented
// in this package by a named type that the constructor does NOT return. When
// the implementor IS the constructor's return type (the ordinary component,
// `New(Deps) (Service, error)` over an unexported `service`), nothing here
// fires and the signature requirement stands.
func (f contractShapeFacts) contractIsSeam() bool {
	if !f.depsConstructor || len(f.contractIfaceMethods) == 0 {
		return false
	}
	for typeName, methods := range f.methodsByType {
		if typeName == f.ctorReturnBase {
			continue
		}
		if coversMethods(methods, f.contractIfaceMethods) {
			return true
		}
	}
	return false
}

// coversMethods reports whether set contains every name in want.
func coversMethods(set map[string]bool, want []string) bool {
	for _, m := range want {
		if !set[m] {
			return false
		}
	}
	return true
}

// scanPackageContractShape walks every non-test, non-gen .go file in
// pkgDir and records which canonical declarations (Service / Deps / New)
// are present, plus the opt-out signals and first non-canonical names.
// Parse errors are surfaced to the caller so the user sees the syntax
// problem rather than a spurious contract-shape finding.
func scanPackageContractShape(pkgDir string) (contractShapeFacts, error) {
	var facts contractShapeFacts

	// Resolve the contract interface name ONCE, up front, from the shared
	// codegen detector (canonical `Service`, else a marked interface, else
	// ""). The decl walk below keys both the interface-presence check and
	// the New return-type check off this so a `//forge:service`-marked
	// interface satisfies BOTH — the same name codegen wires the component's
	// ServiceTypeKey on, so lint and codegen can never disagree.
	if n := codegen.DetectServiceInterfaceName(pkgDir); n != "" {
		facts.serviceIfaceName = n
	} else {
		facts.serviceIfaceName = "Service"
	}

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return facts, fmt.Errorf("read %s: %w", pkgDir, err)
	}

	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Skip _test.go — tests can declare their own helper structs
		// without participating in the package's contract surface.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		// Skip generated files — `mock_gen.go` and friends are the
		// codegen output OF the contract and shouldn't gate it.
		if strings.HasSuffix(e.Name(), "_gen.go") {
			continue
		}
		fp := filepath.Join(pkgDir, e.Name())
		file, parseErr := parser.ParseFile(fset, fp, nil, parser.ParseComments|parser.SkipObjectResolution)
		if parseErr != nil {
			// Surface the parse error, but continue scanning other
			// files in the package so the user sees the full picture.
			return facts, parseErr
		}

		for _, decl := range file.Decls {
			scanContractDecl(decl, fset, &facts)
		}
	}

	return facts, nil
}

// scanContractDecl folds a single top-level declaration into facts:
// canonical Service/Deps hits, the New(Deps) Service constructor, and the
// first non-canonical interface/struct/New-prefixed constructor seen.
func scanContractDecl(decl ast.Decl, fset *token.FileSet, facts *contractShapeFacts) {
	switch d := decl.(type) {
	case *ast.GenDecl:
		if d.Tok != token.TYPE {
			return
		}
		for _, spec := range d.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			switch ts.Type.(type) {
			case *ast.InterfaceType:
				facts.interfaceCount++
				if ts.Name.Name == facts.serviceIfaceName {
					facts.contractIfaceMethods = interfaceMethodNames(ts)
				}
				// The package's contract interface is `serviceIfaceName` —
				// "Service" by default, or the marker-freed name. An
				// interface under that name (canonical or marked) satisfies
				// the presence check; any other interface is just a
				// collaborator interface and is recorded only for the
				// "found X, rename or mark it" message.
				if ts.Name.Name == facts.serviceIfaceName {
					facts.hasServiceInterface = true
					facts.serviceIfacePos = fset.Position(ts.Pos())
				} else if facts.firstIfaceName == "" {
					facts.firstIfaceName = ts.Name.Name
					facts.firstIfacePos = fset.Position(ts.Pos())
				}
			case *ast.StructType:
				if ts.Name.Name == "Deps" {
					facts.hasDepsStruct = true
					facts.depsPos = fset.Position(ts.Pos())
				} else if facts.firstStructName == "" {
					facts.firstStructName = ts.Name.Name
					facts.firstStructPos = fset.Position(ts.Pos())
				}
			}
		}
	case *ast.FuncDecl:
		// Only top-level (no receiver) functions count as the
		// constructor candidate; methods on the impl struct are fine —
		// but their receiver type's method set is what tells a seam
		// contract from the package's own surface (contractIsSeam).
		if d.Recv != nil {
			if recv := receiverTypeName(d); recv != "" && d.Name != nil {
				if facts.methodsByType == nil {
					facts.methodsByType = map[string]map[string]bool{}
				}
				if facts.methodsByType[recv] == nil {
					facts.methodsByType[recv] = map[string]bool{}
				}
				facts.methodsByType[recv][d.Name.Name] = true
			}
			return
		}
		// The constructor is identified the way CODEGEN identifies it
		// (codegen.IsComponentConstructor): a `// forge:constructor`-marked
		// func under any name, else an unmarked `New`. What the rule actually
		// requires is the SIGNATURE — `func(Deps) <Contract>` — so a package
		// can call its entry point Open / Connect / NewReadOnly and still wire.
		isCtor := codegen.IsComponentConstructor(d)
		// A `func(Deps) …` entry point, whatever it returns. This is the
		// seam test's precondition: taking Deps is what makes a func the
		// package's forge-shaped entry rather than an arbitrary helper.
		if isCtor && takesOnlyDeps(d.Type) {
			facts.depsConstructor = true
			facts.ctorReturnBase = firstResultBaseType(d.Type)
		}
		if isCtor && isDepsContractSignature(d.Type, facts.serviceIfaceName) {
			facts.hasConstructor = true
		} else if facts.firstCtorName == "" && (isCtor || strings.HasPrefix(d.Name.Name, "New")) {
			// A func that CLAIMS to be the constructor (marked, or New-named)
			// but whose signature is wrong is the near miss worth naming. Before
			// this, a marked `Open` with a bad signature reported "no constructor"
			// and left the author hunting for a func they had already written.
			facts.firstCtorName = d.Name.Name
			facts.firstCtorPos = fset.Position(d.Name.NamePos)
			facts.firstCtorMarked = codegen.HasConstructorMarkerOn(d)
		}
	}
}

// interfaceMethodNames is the method names an interface declares directly.
// Embedded interfaces are skipped: they have no name at this level, and a
// contract satisfied only through an embed is not a shape this rule needs to
// recognize.
func interfaceMethodNames(ts *ast.TypeSpec) []string {
	iface, ok := ts.Type.(*ast.InterfaceType)
	if !ok || iface.Methods == nil {
		return nil
	}
	var out []string
	for _, m := range iface.Methods.List {
		if _, isFunc := m.Type.(*ast.FuncType); !isFunc {
			continue
		}
		for _, n := range m.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// baseTypeName strips pointer stars and returns the identifier, or "" when
// expr is not a plain (possibly pointed-to) named type.
func baseTypeName(expr ast.Expr) string {
	for {
		star, ok := expr.(*ast.StarExpr)
		if !ok {
			break
		}
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// takesOnlyDeps reports whether ft's parameter list is exactly one `Deps`.
// The same parameter rule isDepsContractSignature applies, read on its own so
// the seam test can ask about the parameter without the result.
func takesOnlyDeps(ft *ast.FuncType) bool {
	if ft == nil || ft.Params == nil || len(ft.Params.List) != 1 {
		return false
	}
	p := ft.Params.List[0]
	if len(p.Names) > 1 {
		return false
	}
	return isIdent(p.Type, "Deps")
}

// firstResultBaseType is ft's first result type with pointer stars stripped,
// or "" when it declares no results.
func firstResultBaseType(ft *ast.FuncType) string {
	if ft == nil || ft.Results == nil || len(ft.Results.List) == 0 {
		return ""
	}
	return baseTypeName(ft.Results.List[0].Type)
}

// shouldSkipContractShapeCheck reports whether the package cannot be a
// Service-shape package at all, in which case the canonical-names rule has
// nothing to say about it. Both early-outs are read off the interface
// count — they are rule predicates, not package categories, and neither
// needs a name, a marker, or a forge.yaml entry.
//
// A package that genuinely has a Service-ish shape and still wants out
// says so with `//forge:exclude-contract`, the same header directive that
// takes it out of bootstrap wiring and mock generation. That directive is
// what the walk in lintInternalContracts honors before this function is
// ever reached.
func shouldSkipContractShapeCheck(facts contractShapeFacts) bool {
	// Zero interfaces: the package cannot possibly declare a
	// `Service interface`, so it isn't a Service-shape package the
	// bootstrap template binds to. Skip silently.
	//
	// The criterion is deliberately narrow. We do NOT also require "no
	// Deps / no New" — a package that has Deps + New but declares no
	// interface is most likely an incomplete Service, and the canonical
	// "found no interface — name it 'Service'" finding is the right
	// action there.
	//
	// FRICTION 2026-06-02 / 2026-06-03: cp-forge migration shipped
	// internal/config, internal/metrics, internal/billing/provideradapters,
	// internal/db, internal/planlimits, internal/ratelimit (and others)
	// as constants-and-structs packages with no interface surface. Each
	// required a contracts.exclude entry that this early-out makes
	// unnecessary.
	if facts.interfaceCount == 0 {
		return true
	}

	// Interface-catalogue early-out: when the package declares >= 2
	// interfaces AND no Deps struct AND no New func, it's clearly a
	// catalogue of narrow interfaces consumed elsewhere — not a
	// Service-shape package the bootstrap template binds to. Skipping
	// the canonical-names check here means the user doesn't have to
	// add every interface-catalogue package to contracts.exclude just
	// to silence false-positive Service/Deps/New findings.
	//
	// Two-interface threshold (rather than one) avoids a false positive
	// on a package that simply forgot to add Deps/New: a single
	// interface declaration is more likely an incomplete contract than
	// a deliberate catalogue.
	//
	// FRICTION 2026-06-02: cp-forge layer-3 natsio (7 narrow interfaces
	// by design) and layer-4 daemonstate (1 data interface + 1 lifecycle
	// runner) both required `contracts.exclude` entries that this
	// early-out makes unnecessary.
	if facts.interfaceCount >= 2 && !facts.hasDepsStruct && !facts.hasConstructor {
		return true
	}

	return false
}

// anchorFile returns the file a finding should name for a declaration at
// pos: that declaration's own file, made relative the same way as the
// contract.go fallback, or fallback when pos is unset.
func anchorFile(fallback string, pos token.Position) string {
	if !pos.IsValid() || pos.Filename == "" {
		return fallback
	}
	return filepath.Join(filepath.Dir(fallback), filepath.Base(pos.Filename))
}

// canonicalContractMessage is the shared prefix for every
// contract-shape finding. Keep the wording uniform so users can grep the
// codebase for it.
const canonicalContractMessage = "internal-package contracts must declare 'type Service interface', " +
	"'type Deps struct', and 'func New(Deps) Service'"

// missingServiceFinding builds the finding emitted when the package has no
// `type Service interface`, naming the first non-canonical interface found
// (or "no interface" when the package declares none).
func missingServiceFinding(relPath string, facts contractShapeFacts) forgeconv.Finding {
	line := 1
	found := "no interface"
	file := relPath
	if facts.firstIfaceName != "" {
		line = facts.firstIfacePos.Line
		file = anchorFile(relPath, facts.firstIfacePos)
		found = fmt.Sprintf("'%s'", facts.firstIfaceName)
	}
	return forgeconv.Finding{
		Rule:     string(RuleInternalPackageContractNames),
		Severity: forgeconv.SeverityError,
		File:     file,
		Line:     line,
		Message: fmt.Sprintf(
			"forge convention: %s. Found %s — name it 'Service', OR keep the name and add a `//forge:service` marker on the line above it, so the bootstrap template can wire it. See skill: contracts.",
			canonicalContractMessage, found),
		Remediation: "either rename the interface to `type Service interface { ... }`, " +
			"or keep your role-oriented name and mark it: put `//forge:service` (or `//forge:contract`) " +
			"on the line directly above the interface — the codegen then keys the component's type off that " +
			"interface under any name. (`Deps` and `New` stay canonical.) " +
			"Or move it to a non-contract.go file if it's not the package's primary behavioral surface.",
	}
}

// missingDepsFinding builds the finding emitted when the package has no
// `type Deps struct`, naming the first non-canonical struct found (or
// "no struct" when the package declares none).
func missingDepsFinding(relPath string, facts contractShapeFacts) forgeconv.Finding {
	line := 1
	found := "no struct"
	file := relPath
	if facts.firstStructName != "" {
		line = facts.firstStructPos.Line
		file = anchorFile(relPath, facts.firstStructPos)
		found = fmt.Sprintf("'%s'", facts.firstStructName)
	}
	return forgeconv.Finding{
		Rule:     string(RuleInternalPackageContractNames),
		Severity: forgeconv.SeverityError,
		File:     file,
		Line:     line,
		Message: fmt.Sprintf(
			"forge convention: %s. Found %s — rename to 'Deps' (or move out of contract.go) so the bootstrap template can wire it. See skill: contracts.",
			canonicalContractMessage, found),
		Remediation: "rename the dependency-set struct to `type Deps struct { ... }` (use `struct{}` if no deps yet)",
	}
}

// missingConstructorFinding builds the finding emitted when the package declares no
// constructor with the required SIGNATURE, naming the near miss — the
// `// forge:constructor`-marked or New-prefixed func the author already wrote
// (or "no constructor" when the package declares none).
//
// The requirement is the signature, never the name. A package that wants to
// call its entry point Open / Connect / NewReadOnly says so with
// `// forge:constructor` — the same marker codegen reads to emit
// `<pkg>.<name>(<pkg>.Deps{...})`. This mirrors `//forge:service` /
// `//forge:contract` on the interface: forge promotes good practice, it does
// not conscript every component into being `New` returning `Service`.
func missingConstructorFinding(relPath string, facts contractShapeFacts) forgeconv.Finding {
	line := 1
	found := "no constructor"
	file := relPath
	if facts.firstCtorName != "" {
		line = facts.firstCtorPos.Line
		file = anchorFile(relPath, facts.firstCtorPos)
		found = fmt.Sprintf("'%s'", facts.firstCtorName)
	}
	contract := facts.serviceIfaceName
	if contract == "" {
		contract = "Service"
	}
	// The marker is advice about the NAME. Offering it to an author whose
	// constructor already carries it prescribes a line they have written,
	// which reads as "the rule cannot be satisfied" — the reported failure.
	// So the name half is included only when it is still available to take.
	nameNote := " — the NAME is yours (`New` needs no annotation; any other name works with " +
		"`//forge:constructor` on the line directly above it), but the SIGNATURE is what the " +
		"injector binds"
	if facts.firstCtorMarked {
		nameNote = fmt.Sprintf(" — `//forge:constructor` already frees the name %s, and that is all it "+
			"does; what is left is the signature", found)
	}
	return forgeconv.Finding{
		Rule:     string(RuleInternalPackageContractNames),
		Severity: forgeconv.SeverityError,
		File:     file,
		Line:     line,
		Message: fmt.Sprintf(
			"forge convention: the package needs a constructor with the signature `func(Deps) %[1]s` "+
				"or `func(Deps) (%[1]s, error)`. Found %[2]s%[3]s. See skill: contracts.",
			contract, found, nameNote),
		Remediation: fmt.Sprintf(
			"give the constructor the signature `func(Deps) %[1]s` (or `func(Deps) (%[1]s, error)`), "+
				"which is what codegen emits as `<pkg>.<name>(<pkg>.Deps{...})` and types the wired field "+
				"off. If %[1]s is a SEAM this package consumes rather than the surface it produces — an "+
				"interface with other implementations, taken in Deps — then it is not this constructor's "+
				"return type, and the package wants that interface in contract.go with its own concrete "+
				"entry point left as it is.",
			contract),
	}
}

// isDepsContractSignature reports whether ft has either of the canonical
// `func(Deps) <Service>` shapes, where <Service> is ifaceName — "Service"
// for the zero-annotation canonical path, or a `//forge:service`-marked
// interface name:
//
//   - `func New(Deps) <Service>` — pre-Day-5 single-result form
//   - `func New(Deps) (<Service>, error)` — Day-5+ form with validateDeps()
//
// We don't insist on a parameter name; both `func New(Deps) <Service>` and
// `func New(d Deps) <Service>` are accepted. We DO insist on:
//   - exactly one parameter, of type `Deps` (unqualified — same package)
//   - one result of type <Service>, optionally followed by a second
//     result of type `error` (unqualified)
//
// Pointer parameters (`func New(*Deps) <Service>`) are intentionally rejected —
// the bootstrap template emits `<pkg>.New(<pkg>.Deps{...})` (a value), so
// a pointer receiver shape would compile-fail at the call site.
func isDepsContractSignature(ft *ast.FuncType, ifaceName string) bool {
	if ifaceName == "" {
		ifaceName = "Service"
	}
	if ft == nil || ft.Params == nil || ft.Results == nil {
		return false
	}
	// Sum names for parameter count: `func(a, b Deps)` declares two even
	// though they share one Field.
	paramCount := 0
	for _, p := range ft.Params.List {
		n := len(p.Names)
		if n == 0 {
			n = 1 // anonymous parameter still counts as one
		}
		paramCount += n
	}
	if paramCount != 1 {
		return false
	}

	// Flatten results into a slice of types so we can pattern-match
	// (Service) vs (Service, error). `func() (a, b T)` shares one Field
	// for both names; treat each name as a distinct result.
	var resultTypes []ast.Expr
	for _, r := range ft.Results.List {
		n := len(r.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			resultTypes = append(resultTypes, r.Type)
		}
	}
	if len(resultTypes) < 1 || len(resultTypes) > 2 {
		return false
	}

	if !isIdent(ft.Params.List[0].Type, "Deps") {
		return false
	}
	if !isIdent(resultTypes[0], ifaceName) {
		return false
	}
	if len(resultTypes) == 2 && !isIdent(resultTypes[1], "error") {
		return false
	}
	return true
}

// isIdent returns true iff expr is an unqualified identifier with the given name.
func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == name
}
