package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The merge invariant, as a REGISTRY every generate step must be in.
//
// The invariant: two branches that change independent inputs (different
// services, entities, migrations, protos, interfaces) merge with plain `git
// merge` — no merge driver, exactly as GitHub's server-side merge does — into
// exactly what `forge generate` writes for the merged inputs.
// internal/mergeproperty states it for one output; TestE2EMergeProperty for a
// whole scaffolded project.
//
// This test makes it a rule for every step: each one names how it holds the
// invariant, and a step with no entry — a new generator — fails CI until its
// author decides, in this table, how its output merges. That is the point: the
// cheap moment to make an output merge-friendly is when it is written. A row
// that cites a test is checked, below, against the test functions that exist,
// so a renamed or deleted proof fails here too.

// mergeKind is how one step's output keeps the invariant.
type mergeKind int

const (
	// mergeProved: a merge-property test proves the step's output (cases
	// names it).
	mergeProved mergeKind = iota + 1
	// mergePerInput: the step writes one file per input (a service's
	// handlers, an entity's ORM file, a proto's stubs), so independent
	// inputs touch different files. why says which.
	mergePerInput
	// mergeNoCommittedOutput: the step writes nothing a project commits
	// (it validates, or writes under .forge/ or another ignored path).
	mergeNoCommittedOutput
	// mergeScaffoldOnce: the step writes user-owned files once (never
	// rewritten), so merging them is merging the user's own edits.
	mergeScaffoldOnce
	// mergeExternalTool: the output is another tool's (go mod tidy,
	// goimports, buf), merged as that tool's files always are.
	mergeExternalTool
	// mergeKnownGap: the step's output does NOT yet keep the invariant.
	// why names what conflicts and gapPaths where
	// (mergeCase{kind: mergeKnownGap, why: …, gapPaths: …}). Allowed, so the
	// table tells the truth; never the default for a new step. None is
	// declared today: TestE2EMergeProperty merges every case without a
	// conflict.
	mergeKnownGap
)

type mergeCase struct {
	kind mergeKind
	// tests names the test functions proving the case (mergeProved; any
	// kind may cite them).
	tests []string
	why   string
	// gapPaths (mergeKnownGap) are the slash-separated path.Match globs of
	// the files that still conflict. TestE2EMergeProperty tolerates a
	// conflict, or a post-merge regeneration diff, in these paths and in no
	// others, so a gap cannot quietly widen.
	gapPaths []string
}

func proved(why string, tests ...string) mergeCase {
	return mergeCase{kind: mergeProved, tests: tests, why: why}
}
func perInput(why string) mergeCase     { return mergeCase{kind: mergePerInput, why: why} }
func noOutput(why string) mergeCase     { return mergeCase{kind: mergeNoCommittedOutput, why: why} }
func scaffoldOnce(why string) mergeCase { return mergeCase{kind: mergeScaffoldOnce, why: why} }
func external(why string) mergeCase     { return mergeCase{kind: mergeExternalTool, why: why} }

// mergeCases is the registry: every generateSteps() name, and how it merges.
var mergeCases = map[string]mergeCase{
	"load project config":                        noOutput("reads forge.yaml"),
	"forge version compatibility":                noOutput("refusal check"),
	"pre-codegen contract check":                 noOutput("refusal check"),
	"retired ShellBuild tokens":                  noOutput("refusal check"),
	"legacy config proto layout":                 noOutput("refusal check"),
	"migrate env registries onto images":         scaffoldOnce("one-time migration of the user's KCL envs"),
	"derive frontend inventory":                  noOutput("renders into .forge-kcl/, ignored"),
	"load checksums":                             noOutput("reads ownership state"),
	"migrate legacy checksums manifest":          scaffoldOnce("one-time migration off .forge/checksums.json"),
	"check Tier-1 file-stomp guard":              noOutput("refusal check"),
	"snapshot Tier-1 exports":                    noOutput("in-memory snapshot"),
	"reconcile forge_version with go.mod":        perInput("one forge.yaml line, a function of the pin: two branches agree or genuinely conflict over the pin"),
	"announce project":                           noOutput("prints"),
	"sync forge KCL module vendor":               noOutput("vendors into .forge-kcl/, ignored"),
	"detect proto directories":                   noOutput("in-memory"),
	"ensure gen/go.mod":                          scaffoldOnce("written when absent"),
	"buf generate (Go stubs)":                    perInput("one stub file per .proto"),
	"descriptor extraction":                      noOutput("gen/forge_descriptor/ is derived on demand and never committed (#1572)"),
	"OpenAPI specs (protoc-gen-connect-openapi)": perInput("one spec per service"),
	"frontend workspaces scaffold":               scaffoldOnce("workspace root written once"),
	"TypeScript stubs (frontends)":               perInput("one _pb.ts per .proto"),
	"config loader (proto/<project>/config/v1)":  perInput("pkg/config/config.go is a function of the config protos alone"),
	"parse services + module path":               noOutput("in-memory"),
	"frontend hooks":                             perInput("one <service>-hooks.ts per service"),
	"ensure frontend components":                 scaffoldOnce("component library copied once"),
	"frontend CRUD pages":                        scaffoldOnce("pages are the user's from birth"),
	"frontend nav + dashboard": proved("nav.tsx and dashboard.tsx are the user's and render the same for every entity set (their birth hash with them); each entity's route and tile is its own generated module (src/lib/nav-routes/, src/components/dashboard-tiles/), gathered by the bundler",
		"TestE2EMergeProperty"),
	"service stubs":                    perInput("one rpc_<name>.go per rpc, in its service's directory"),
	"internal/db/ ORM (entity-driven)": perInput("one <entity>_orm_gen.go and one <entity>_store_gen.go per table; the aggregate store_gen.go names no entity (each accessor is a method in its entity's store file), proved at project scale by TestE2EMergeProperty"),
	"CRUD handlers":                    perInput("one ops file per service"),
	"service mocks": proved("one sectioned mock per interface (#1568)",
		"TestMockMergeProperty_IndependentInterfacesMergeWithPlainGit", "TestStampSections_EveryIndependentPairMergesWithPlainGit"),
	"internal package contracts": proved("one sectioned mock per interface (#1568)",
		"TestMockMergeProperty_IndependentInterfacesMergeWithPlainGit"),
	"open-procedure set (pkg/middleware)":       perInput("procedures_gen.go: one line per rpc; rpcs of different services sort apart"),
	"webhook routes":                            perInput("one routes file per webhook"),
	"go mod tidy (pre-composition)":             external("go.mod / go.sum"),
	"internal/app composition (hybrid DI)":      perInput("compose.go is the user's; forge reconciles one Deps key per component"),
	"root commands without a composition graph": scaffoldOnce("written once"),
	"root env schema (pkg/config/roots_gen.go)": perInput("one root per binary"),
	"go mod tidy (pre-wiring)":                  external("go.mod / go.sum"),
	"cmd/commands.go (user extension point)":    scaffoldOnce("the user's extension point"),
	"per-service test helpers":                  perInput("one helpers_gen_test.go per component"),
	"db/embed.go (embedded migrations)": proved("db/embed.go embeds the db/grants directory (a migration does not change it); the grants are one constant script per role and one row file per table (pkg/dbgrants.Files), so tables born, changed or dropped on different branches are different files",
		"TestMergeProperty_ReadOnlyGrants"),
	"regenerate infra files":                          perInput("infra files are functions of forge.yaml and the pin, not of services"),
	"cmd command groups (services/workers/operators)": perInput("one command file per group"),
	"discover components":                             noOutput("in-memory"),
	"workloads.k (scaffold once)":                     scaffoldOnce("written once"),
	"per-env deploy config": proved("config_gen.k: one section per config block, schema, lambda and env-map block (#1582); each env's config.k is the user's",
		"TestMergeProperty_ConfigKCL"),
	"observability dashboards": proved("one output and one .forge/hashes/ certificate per service (#1568)",
		"TestPlainGitMerge_ObservabilityOutputsAndCertificates"),
	"frontend typed config":             perInput("one schema per frontend, a function of its config proto"),
	"CI workflows":                      scaffoldOnce("the project's CI files are the user's"),
	"ingress k3d ports fragment":        perInput("a function of the env's ports"),
	"CRD KCL manifests (api/ Go types)": perInput("one manifest per CRD"),
	"entity-aware seed data":            perInput("one seed file per table"),
	"frontend mocks + transport": proved("scenario-rpcs_gen.ts is one named section per service (#1582, named headers in v1.0.12); one fixture module and one dispatch-row module (src/mocks/entities/<slug>_gen.ts) per entity, gathered by the bundler, so mock-transport_gen.ts names no entity; the fixture fingerprint is out of git (#1585)",
		"TestMergeProperty_ScenarioRpcs", "TestE2EMergeProperty"),
	"repoint renamed *_gen frontend imports":                    perInput("rewrites import lines in the files that name a renamed module"),
	"retire delivered agent skills":                             noOutput("removes retired files"),
	"go mod tidy (root)":                                        external("go.mod / go.sum"),
	"goimports on generated Go":                                 external("formats files other steps wrote"),
	"cleanup stale codegen":                                     noOutput("removes files no step wrote"),
	"retire obsolete disowns":                                   perInput("one key per disowned path in .forge/disowned.json"),
	"rehash tracked files":                                      noOutput("re-records hashes of files other steps wrote"),
	"Tier-1 file-stomp guard: compare drift with fresh renders": noOutput("refusal check"),
	"generate hooks (forge.yaml)":                               external("the project's own hooks"),
	"go mod tidy (gen/)":                                        external("gen/go.mod / gen/go.sum"),
	"go mod tidy (after generate hooks)":                        external("go.mod / go.sum"),
	"post-gen validation":                                       noOutput("check"),
	"detect renamed Tier-1 exports":                             noOutput("check"),
	"check disowned-sibling dangling refs":                      noOutput("check"),
	"check stale scaffold tests":                                noOutput("check"),
	"go build (validate generated code)":                        noOutput("check"),
	"deploy KCL loads":                                          noOutput("check"),
	"record generating forge build":                             noOutput("writes under .forge/, ignored"),
}

// Cases every state-file writer (not a step) holds; checked like the rest.
var stateMergeProofs = []string{
	"TestScaffoldLedger_PlainGitMergeOfIndependentBirths", // .forge/scaffolded/ (#1584)
	"TestHashCerts_OneFilePerOutput",                      // .forge/hashes/ (#1568)
}

func TestEveryGenerateStepDeclaresItsMergeCase(t *testing.T) {
	steps := map[string]bool{}
	var missing []string
	for _, s := range generateSteps() {
		steps[s.Name] = true
		c, ok := mergeCases[s.Name]
		switch {
		case !ok:
			missing = append(missing, s.Name)
		case c.why == "":
			t.Errorf("step %q: its merge case gives no reason", s.Name)
		case c.kind == mergeProved && len(c.tests) == 0:
			t.Errorf("step %q is marked proved but names no test", s.Name)
		case c.kind == mergeKnownGap && len(c.gapPaths) == 0:
			t.Errorf("step %q is a known gap but names no path that conflicts", s.Name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("generate step(s) with no merge case in mergeCases (internal/cli/merge_property_registry_test.go):\n  %s\n\n"+
			"Every generator must say how its committed output keeps forge's merge invariant: two branches that change "+
			"independent inputs merge with plain `git merge` (no driver) into exactly what `forge generate` writes for both. "+
			"One file per input, sections (checksums.SectionLine) for an aggregate, or no committed output. Prove it with "+
			"internal/mergeproperty and add the row.", strings.Join(missing, "\n  "))
	}
	for name := range mergeCases {
		if !steps[name] {
			t.Errorf("mergeCases names %q, which is not a generate step (renamed or removed?)", name)
		}
	}
}

// TestMergeCasesCiteTestsThatExist: a cited proof must be a test function
// somewhere in the module, so deleting or renaming one fails here.
func TestMergeCasesCiteTestsThatExist(t *testing.T) {
	root := findModuleRootForRegistry(t)
	have := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", ".forge":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				have[fn.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cited := append([]string{"TestE2EMergeProperty"}, stateMergeProofs...)
	for _, c := range mergeCases {
		cited = append(cited, c.tests...)
	}
	sort.Strings(cited)
	for _, name := range cited {
		if !have[name] {
			t.Errorf("merge-property proof %s does not exist (renamed or deleted?)", name)
		}
	}
}

func findModuleRootForRegistry(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "internal", "cli")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("module root not found")
		}
		dir = parent
	}
}
