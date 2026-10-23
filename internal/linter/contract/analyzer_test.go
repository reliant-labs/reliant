package contract_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/reliant-labs/forge/internal/linter/contract"
)

func TestAnalyzer_Good(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "good")
}

func TestAnalyzer_Bad(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "bad")
}

func TestAnalyzer_NoMethods(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "nomethods")
}

func TestAnalyzer_MultipleInterfaces(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "multiface")
}

func TestAnalyzer_PointerReceivers(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "pointer")
}

func TestAnalyzer_NoContract(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "nocontract")
}

func TestAnalyzer_EmbeddedInterface(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "embedded")
}

// Bug #20 regression: one impl struct satisfying two disjoint interfaces
// (Reader, Writer with no method overlap) should be accepted. Methods on
// the impl are valid as long as they live in the union of interfaces.
func TestAnalyzer_DisjointInterfaces(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "disjoint")
}

// A type satisfying only interfaces the package's Deps consumes is not the
// package's contract implementation, so its other exported methods are its
// own API (connauth's *Brokers: Get through a Deps seam, Register for the
// composition root). The provided Service still binds, including when a
// Deps field names it, and so does an interface Deps names only inside a
// func signature.
func TestAnalyzer_DepsSeamsDoNotBind(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "depsseam")
}

// A //forge:contract-marked interface is the provided contract under another
// name, and binds when a Deps field names it exactly as Service does.
func TestAnalyzer_MarkedContractInDepsStillBinds(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "depsseammarked")
}

// RequireContractAnalyzer tests

func TestRequireContract_WithContract(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/requiregood")
}

func TestRequireContract_NoExportedMethods(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/requirenoexported")
}

func TestRequireContract_MissingContract(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/requirebad")
}

func TestRequireContract_NotInternal(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "notinternal")
}

// Regression: a package whose only exported methods satisfy STANDARD-LIBRARY
// interfaces — String() (fmt.Stringer), Error() (error), MarshalJSON/
// MarshalText — has no behavioral seam to mock. Those are rendering and
// marshalling conventions on a data record, and Go requires the exact
// exported name, so "unexport it" is not an available repair.
//
// Measured on forge's own tree, this was 6 of 8 requirecontract findings
// (deadcodeguard, vacuousguard, tierguard, pkgguard, devpg) —
// every one a `func (X) String() string` on a Finding/Verdict/Report value.
// A rule whose only repair is "add an interface nobody consumes" trains
// people to add exclusions, so the conventions are skipped instead.
func TestRequireContract_StdlibConventionMethodsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/requirestringer")
}

// Regression: external test packages (`package <name>_test`) host black-box
// tests and helper structs that are not part of the package's API surface.
// Previously the analyzer flagged `package <name>_test` with exported test
// helpers as "has exported methods but no contract.go", forcing users into
// the internal-test form and losing API-boundary discipline. The fix is to
// skip packages whose name ends with `_test`. See friction item
// contractlint-flags-external-test-packages.
func TestRequireContract_ExternalTestPackageSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/requireexttest")
}

// Regression: a package whose ONLY exported methods come from a forge-generated
// file (e.g. mock_gen.go's mock methods) must NOT
// be required to add a contract.go — generated code is the output OF a contract,
// not a hand-authored behavioral surface. The fixture declares exported methods
// solely in a `// Code generated by forge. DO NOT EDIT.`-headed *_gen.go file
// with NO `// want` comment: zero findings expected.
func TestRequireContract_GeneratedMethodsOnly(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/requiregenerated")
}

// Regression: proto-service handler packages (internal/handlers/<svc>) are thin
// Connect handlers whose exported methods are the proto RPC surface. Their
// contract is the proto service, not a Go contract.go, so the require-contract
// rule must skip them. The fixture has hand-written exported RPC methods and NO
// contract.go, with NO `// want` comment: zero findings expected.
func TestRequireContract_HandlerPackageSkipped(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/handlers/user")
}

// Generated-file exemption: an exported method emitted by codegen (a file
// carrying the canonical `Code generated ... DO NOT EDIT.` header) is
// forge's responsibility, not a user seam violation — the user cannot
// durably remove a method the next `forge generate` re-emits. The fixture
// pairs an exempt generated method (observe_gen.go, no want) with a
// hand-written rogue method (impl.go, want) to prove the skip is per-file.
func TestAnalyzer_GeneratedMethodsExempt(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "seamgenerated")
}

// Worker exemption: `forge scaffold worker` emits a package under
// internal/workers/ whose exported methods ARE the serverkit.Worker
// lifecycle (Name/Start/Stop, plus Run on the cron variant) and no
// contract.go. That interface is forge's own — the supervisor consumes
// workers polymorphically through it — so a hand-written contract.go would
// only restate it, for a package shape forge itself scaffolds.
//
// Before this exemption a brand-new `forge scaffold worker` left
// `forge lint` red with a requirecontract finding in a file forge wrote,
// which teaches users on day one that forge's own lint is noise. Zero
// findings expected.
func TestRequireContract_WorkerPackageSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/workers/share_expiry")
}

// The worker exemption is STRUCTURAL, not a path blacklist: a package that
// merely lives under internal/workers/ but presents an ordinary exported
// surface (not the Worker lifecycle) is still subject to the rule. Without
// this half, filing any package under internal/workers/ would silently buy
// it a permanent exemption. One finding expected.
func TestRequireContract_NonWorkerUnderWorkersDirStillFlagged(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/workers/notaworker")
}

// Operator exemption, on the worker exemption's structural terms: a package
// under internal/operators/ declaring SetupWithManager is a controller whose
// exported methods are controller-runtime's reconciler surface. Before it, a
// fresh `forge scaffold operator` failed `forge lint --contract`.
func TestRequireContract_OperatorPackageSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/operators/widget")
}

// The operator exemption is structural: no SetupWithManager, no exemption.
func TestRequireContract_NonOperatorUnderOperatorsDirStillFlagged(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/operators/notanoperator")
}

// Composition-seam exemption: the module's internal/app package is forge's
// explicit composition site (compose.go/providers.go/mounts_services.go/
// lifecycle.go). Its exported methods (DefaultClient, the lifecycle
// accessors) are wiring plumbing whose shape forge defines — not a
// behavioral contract — so the require-contract rule must not demand a
// contract.go there. The exemption is structural: it only applies when the
// package actually carries the forge seam files. Zero findings expected.
func TestRequireContract_CompositionSeamSkipped(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/app")
}

// ExportedVarsAnalyzer tests

func TestExportedVars_Good(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsok")
}

func TestExportedVars_Bad(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsbad")
}

// Regression: an immutable lookup table or a compiled regexp is a `var`
// only because Go has no const slice/map/regexp. The rule's premise —
// "mutable global state should be behind a method or getter" — does not
// apply: a getter returning the same slice/map header hides nothing and
// prevents no mutation, it just adds a call.
//
// Measured on forge's own tree this was all 5 exportedvars findings
// (codegen.KnownProtoMarkers/RemovedProtoMarkers, unwired_stub's
// MustCompile'd marker regex, kcloptions.Reserved, kclvendor's Sprintf'd
// dep line) — every one a fixed vocabulary or precompiled pattern that is
// already documented as the single source of truth for several callers.
func TestExportedVars_ImmutableDataAndRegexps(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsimmutable")
}

// Regression: a package carrying the per-package //forge:exclude-contract
// header is opted out of EVERY contract rule — including exported-vars —
// exactly as a forge.yaml contracts.exclude entry would. Without this, a
// project that drops the central exclude list in favour of headers (the
// contract-system conformance migration) would see exported-var findings
// resurface on packages like internal/metrics (global Prometheus
// collectors). The fixture declares exported vars with NO `// want`
// comments: zero findings expected.
// TestExportedVars_SentinelShapes covers every error-sentinel shape forge's
// own skills teach an author to write. `service-layer` and `api` say to
// build domain errors from forge/pkg/svcerr rather than redeclare the
// sentinel set — and while the exception matched on the CALLEE
// (errors.New / fmt.Errorf and nothing else) every one of those shapes was
// reported, so following forge's guidance failed forge's lint. Only
// pkg/svcerr itself passed, by using the two spellings the whitelist knew.
func TestExportedVars_SentinelShapes(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varssentinelshapes")
}

func TestExportedVars_ExcludeContractDirectiveExempt(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsexcludedirective")
}

// Generated-file exemption: forge codegen deliberately exports package
// vars — the documented `<Entity>Columns` allowlist in Tier-1 `_orm.go`
// files, the data-only `Inventory` descriptor in mounts_services.go.
// Those files carry the canonical `Code generated ... DO NOT EDIT.`
// header (or the `// forge:hash=` self-certification marker) and are
// forge's responsibility, so the exported-vars rule must skip them —
// while STILL flagging hand-written vars in the same package (hand.go's
// want proves the skip is per-file).
func TestExportedVars_GeneratedFilesExempt(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsgenerated")
}

// Regression: //go:embed targets must be exported package vars (the embed
// package requires it). The analyzer must exempt them — see scaffold-1 in
// PACK_AUDIT.md for the pristine-scaffold breakage that motivated this.
func TestExportedVars_EmbedTargetsExempt(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsembed")
}

// Regression: kubebuilder/controller-runtime API packages MUST expose
// `GroupVersion`, `SchemeBuilder`, and `AddToScheme` as package-level vars
// because k8s API machinery discovers them by name. Wrapping them in a getter
// would silently break operator scheme registration. The analyzer must exempt
// them — see operator-scaffold dogfood (2026-04-30) for the breakage that
// motivated this.
func TestExportedVars_KubebuilderAPIExempt(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsoperator")
}

// Regression: newer kubebuilder scaffolds emit
// `SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}` from
// sigs.k8s.io/controller-runtime/pkg/scheme rather than the classic
// `runtime.NewSchemeBuilder(...)` form. Both shapes are kubebuilder-blessed
// and must be exempt — see control-plane operator port (2026-04-30).
func TestExportedVars_KubebuilderControllerRuntimeAPIExempt(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsoperatorcr")
}

// TEST FILES ARE NOT A PACKAGE'S API. A _test.go file compiles only into the
// package's test binary, so nothing that imports the package can reach what
// it declares — yet every driver hands test files to the analyzers (`go vet`
// and multichecker analyze each package's test variant, and `forge lint
// --contract` loads with Tests: true). Measured in reliant:
// internal/mcpserver/http_test.go declares memStore, a fake satisfying the
// package's BindingStore contract, and the single-seam rule flagged every
// other exported method on the fake — 14 findings, none about the API.
//
// The fixture mirrors it: a fake satisfying the contract with three extra
// exported methods and an export_test.go-style method on the real type, all
// in store_test.go and none expected; plus a rogue method in store.go that IS
// expected, proving the skip is per file.
func TestAnalyzer_TestFilesAreNotTheAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.Analyzer, "internal/testfake")
}

// An exported var in a test file is no more the package's API than an
// exported method is: the one in vars_test.go is not flagged, while the one
// in vars.go still is.
func TestExportedVars_TestFilesAreNotTheAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.ExportedVarsAnalyzer, "varsintest")
}

// A package whose only exported methods live in a test file (a fake clock)
// has no behavioral surface, so it needs no contract.go. Zero findings.
func TestRequireContract_TestOnlyMethodsNeedNoContract(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/requiretestonly")
}

// The worker exemption reads the package's own surface: a fake worker in a
// test file does not make an ordinary package under internal/workers/ a
// worker. One finding expected.
func TestRequireContract_FakeWorkerInATestIsNotAWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("runs analysistest, which loads packages via go list; runs in task test")
	}
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, contract.RequireContractAnalyzer, "internal/workers/fakeworkerintest")
}
