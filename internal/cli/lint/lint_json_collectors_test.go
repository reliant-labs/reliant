package lint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/closedset"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// The --json lanes each map their engine's findings onto lintJSONFinding. These
// pin that mapping (file, line, severity, rule, fix hint) and the gating
// verdict for the lanes that did not have a test of their own.

func TestCollectGoMarkersJSON_GatesOnErrors(t *testing.T) {
	root := t.TempDir()
	writeGoMarkerFixture(t, root, map[string]string{
		"internal/typo/doc.go": "//forge:exclude-contrat: typo\npackage typo\n",
	})
	got, gated, err := collectGoMarkersJSON(root)
	if err != nil {
		t.Fatal(err)
	}
	if !gated || len(got) != 1 || got[0].File != "internal/typo/doc.go" || got[0].Line != 1 || got[0].Severity != lintSevError {
		t.Fatalf("got %+v gated=%v, want one error at internal/typo/doc.go:1 that gates", got, gated)
	}
	if !strings.Contains(got[0].FixHint, "forge:exclude-contract") {
		t.Errorf("fix hint = %q, want the suggestion", got[0].FixHint)
	}

	clean := t.TempDir()
	writeGoMarkerFixture(t, clean, map[string]string{"internal/a/a.go": "package a\n"})
	if got, gated, err := collectGoMarkersJSON(clean); err != nil || gated || len(got) != 0 {
		t.Fatalf("clean tree: got %+v gated=%v err=%v", got, gated, err)
	}
}

func TestGoMarkerGrammarNote(t *testing.T) {
	tests := []struct {
		grammar codegen.GoMarkerGrammar
		want    string
	}{
		{codegen.GoMarkerBare, "takes no argument"},
		{codegen.GoMarkerReasoned, "reason follows a colon"},
		{codegen.GoMarkerReasonRequired, "reason follows a colon"},
		{codegen.GoMarkerWordArgs, "follows a colon or a space"},
		{codegen.GoMarkerOneWord, "one lower-case word"},
		{codegen.GoMarkerGrammar(-1), "forge project annotations --kind go"},
	}
	for _, tc := range tests {
		if got := goMarkerGrammarNote(tc.grammar); !strings.Contains(got, tc.want) {
			t.Errorf("goMarkerGrammarNote(%v) = %q, want it to contain %q", tc.grammar, got, tc.want)
		}
	}
}

func TestCollectIdempotencyJSON_GatesOnErrors(t *testing.T) {
	root := writeIdempotencyProject(t, []codegen.Method{{Name: "CreateOrder", NoSideEffects: true}})
	got, gated, err := collectIdempotencyJSON(root)
	if err != nil {
		t.Fatal(err)
	}
	var sawWriteAsRead bool
	for _, f := range got {
		if f.Rule == ruleIdempotencyWriteAsRead {
			sawWriteAsRead = true
			if f.File != "proto/services/shop/v1/shop.proto" || f.Line == 0 {
				t.Errorf("finding location = %s:%d", f.File, f.Line)
			}
		}
	}
	if !sawWriteAsRead {
		t.Fatalf("a mutation declared NO_SIDE_EFFECTS was not reported: %+v", got)
	}
	if !gated {
		t.Error("an error-severity finding must gate")
	}
}

func TestCollectAuthNoSignInJSON(t *testing.T) {
	root := t.TempDir()
	// A frontend, an auth-mode-none dev env and an authenticated rpc: nobody can sign in.
	for rel, body := range map[string]string{
		"deploy/kcl/dev/main.k":                    "# forge:auth-mode none\n",
		"frontends/web/package.json":               "{}",
		"proto/services/notes/v1/notes.proto":      "service NotesService {\n  rpc ListNotes(Req) returns (Resp);\n}\n",
		"gen/forge_descriptor/fixture.json":        `{"Services":[{"Name":"NotesService","ProtoFile":"services/notes/v1/notes.proto","Methods":[{"Name":"ListNotes","AuthRequired":true}]}]}`,
		"frontends/web/src/placeholder.keep.ts":    "",
		"frontends/web/next.config.ts":             "export default {}\n",
		"frontends/web/node_modules/.placeholder":  "",
		"frontends/web/src/app/page.tsx":           "export default function P() { return null }\n",
		"frontends/web/tsconfig.json":              "{}",
		"frontends/web/public/placeholder.keep.js": "",
	} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := collectAuthNoSignInJSON(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Rule != ruleAuthRequiredNoSignIn || got[0].Severity != lintSevWarning ||
		got[0].File != "proto/services/notes/v1/notes.proto" || got[0].Line != 2 {
		t.Fatalf("got %+v, want one advisory finding on NotesService.ListNotes at its proto line 2", got)
	}

	// With a sign-in declared the same descriptor yields nothing.
	if err := os.WriteFile(filepath.Join(root, "deploy", "kcl", "identity.k"), []byte("identity = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := collectAuthNoSignInJSON(root); err != nil || len(got) != 0 {
		t.Fatalf("project with an identity: got %+v err=%v", got, err)
	}
}

func TestCollectCRUDOwnershipJSON_AndShouldRun(t *testing.T) {
	root := writeGrantsProject(t, map[string]string{"0001_birth.up.sql": ownershipBirthSQL})
	desc := `{"Services":[{"Name":"TaskService","Package":"services.task.v1","GoPackage":"example.com/app/gen/services/task/v1",` +
		`"ProtoFile":"proto/services/task/v1/task.proto","Methods":[` +
		`{"Name":"GetTask","InputType":"GetTaskRequest","OutputType":"GetTaskResponse","AuthRequired":true}]}]}`
	for path, body := range map[string]string{
		"gen/forge_descriptor/fixture.json": desc,
		"go.mod":                            "module example.com/app\n\ngo 1.24\n",
		"internal/handlers/task/handlers_crud_ops_gen.go": "package task\n\n" +
			"func (s *Service) crudGetTaskOp() crud.GetOp[pb.GetTaskRequest, pb.GetTaskResponse, *db.Task] {\n\treturn nil\n}\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.ProjectConfig{}
	got, err := collectCRUDOwnershipJSON(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Rule != ruleCRUDTableUnowned || got[0].Severity != lintSevError ||
		!strings.Contains(got[0].Message, "table tasks") || !strings.Contains(got[0].Message, "TaskService.GetTask") || got[0].FixHint == "" {
		t.Fatalf("got %+v, want one error on the unowned tasks table naming the rpc and a fix", got)
	}

	t.Run("the lane runs only where the project has migrations", func(t *testing.T) {
		t.Chdir(root)
		for name, tc := range map[string]struct {
			rc   *lintRunCtx
			want bool
		}{
			"no cwd":        {&lintRunCtx{cfg: cfg}, false},
			"no config":     {&lintRunCtx{cwd: root}, false},
			"no migrations": {&lintRunCtx{cwd: root, cfg: &config.ProjectConfig{Database: config.DatabaseConfig{MigrationsDir: "nowhere"}}}, false},
			"migrations":    {&lintRunCtx{cwd: root, cfg: cfg}, true},
		} {
			if run, _ := crudOwnershipShouldRun(tc.rc); run != tc.want {
				t.Errorf("%s: shouldRun = %v, want %v", name, run, tc.want)
			}
		}
	})
}

func TestCollectGraphJSON_AnUnreadableProjectIsAWrappedError(t *testing.T) {
	// No go.mod: the graph cannot be assembled, and the lane says which lane failed.
	got, err := collectGraphJSON(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "graph lint failed") || got != nil {
		t.Fatalf("got %+v err=%v, want a 'graph lint failed' error and no findings", got, err)
	}
}

func TestClosedSetFixHint(t *testing.T) {
	t.Run("no fix available points at the declaration", func(t *testing.T) {
		got := closedSetFixHint(closedset.Finding{})
		if !strings.Contains(got, "COMMENT ON CONSTRAINT") || !strings.Contains(got, "forge:closed-set") {
			t.Errorf("hint = %q", got)
		}
	})
	t.Run("a fix names the command and the migration", func(t *testing.T) {
		got := closedSetFixHint(closedset.Finding{Constraint: "tokens_scopes", Fix: &closedset.Fix{Migration: "ALTER TABLE t;\nSELECT 1;"}})
		for _, want := range []string{"forge db migration new --closed-set tokens_scopes", "        ALTER TABLE t;", "        SELECT 1;"} {
			if !strings.Contains(got, want) {
				t.Errorf("hint lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "its own transaction") {
			t.Errorf("a fix with no validate step must not describe one:\n%s", got)
		}
	})
	t.Run("a widening adds the validate migration that must follow it", func(t *testing.T) {
		got := closedSetFixHint(closedset.Finding{Constraint: "c", Fix: &closedset.Fix{
			Migration: "ADD CHECK NOT VALID;", Validate: &closedset.Fix{Migration: "VALIDATE CONSTRAINT c;"},
		}})
		for _, want := range []string{"        ADD CHECK NOT VALID;", "its own transaction", "        VALIDATE CONSTRAINT c;"} {
			if !strings.Contains(got, want) {
				t.Errorf("hint lacks %q:\n%s", want, got)
			}
		}
	})
}

func TestConfigReachFixHintNamesTheLiteralFix(t *testing.T) {
	tests := []struct {
		name string
		f    configReachFinding
		want []string
	}{
		{"missing binary", configReachFinding{Message: "GhostConfig", Field: "ghost_url", Binary: "ghost", Reason: configReachNoSuchBinary},
			[]string{"there is no cmd/ghost", "forge scaffold binary ghost"}},
		{"missing frontend", configReachFinding{Message: "WebConfig", Field: "api_url", Frontend: "web", Reason: configReachNoSuchFrontend},
			[]string{`no forge.yaml frontend is named web`, "frontend_config"}},
		{"orphaned", configReachFinding{Message: "StrayConfig", Field: "stray", Reason: configReachOrphaned},
			[]string{"bound to no binary and no frontend", "compose it as a block", "stray"}},
	}
	for _, tc := range tests {
		got := configReachFixHint(tc.f)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: hint lacks %q:\n%s", tc.name, want, got)
			}
		}
	}
}

func TestDiscoverBinariesAndDeclaredFrontends(t *testing.T) {
	root := t.TempDir()
	if got := discoverBinaries(root); got != nil {
		t.Errorf("no cmd/: got %v, want nil (unknown, not empty)", got)
	}
	for _, rel := range []string{"cmd/server/main.go", "cmd/worker/main.go", "cmd/README.md"} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(discoverBinaries(root), ","); got != "server,worker" {
		t.Errorf("binaries = %q, want server,worker (files under cmd/ are not binaries)", got)
	}

	if got := declaredFrontends(nil); got != nil {
		t.Errorf("nil config: got %v, want nil (unknown, not none)", got)
	}
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{{Name: "web"}, {Name: ""}, {Name: "admin"}}}
	if got := strings.Join(declaredFrontends(cfg), ","); got != "web,admin" {
		t.Errorf("frontends = %q, want web,admin", got)
	}
	if got := declaredFrontends(&config.ProjectConfig{}); got == nil || len(got) != 0 {
		t.Errorf("a config with no frontends: got %#v, want an empty non-nil slice", got)
	}
}

func TestCollectConfigReachFindingsForProject_NoConfigProtosIsSilent(t *testing.T) {
	got, err := collectConfigReachFindingsForProject(t.TempDir(), &config.ProjectConfig{})
	if err != nil || got != nil {
		t.Fatalf("got %+v err=%v, want nothing for a project with no config protos", got, err)
	}
}

func TestProtoScalarType(t *testing.T) {
	tests := map[string]string{
		"int32": "int32", "uint16": "int32", "byte": "int32", "rune": "int32",
		"int": "int64", "int64": "int64", "uint64": "int64", "uintptr": "int64",
		"bool": "bool", "float32": "float", "float64": "double",
		"*int":           "int64", // a pointer maps like its element
		"string":         "string",
		"time.Duration":  "string", // a Go duration is configured as "5s"
		"map[string]int": "string",
	}
	for goType, want := range tests {
		if got := protoScalarType(goType); got != want {
			t.Errorf("protoScalarType(%q) = %q, want %q", goType, got, want)
		}
	}
}

func TestPackageDir(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "proj", "internal", "a")
	tests := []struct {
		name string
		pkg  *packages.Package
		want string
	}{
		{"its own directory, cleaned", &packages.Package{Dir: dir + string(filepath.Separator) + "."}, dir},
		{"else the directory of its first file", &packages.Package{GoFiles: []string{filepath.Join(dir, "a.go")}}, dir},
		{"neither", &packages.Package{}, ""},
	}
	for _, tc := range tests {
		if got := packageDir(tc.pkg); got != tc.want {
			t.Errorf("%s: packageDir = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSetGateWriterInstallsTheWriter(t *testing.T) {
	previous := writeGate
	t.Cleanup(func() { writeGate = previous })

	var got GateRequest
	SetGateWriter(func(req GateRequest) error { got = req; return nil })
	if err := WriteGateForTest(GateRequest{Name: "lint", Status: "pass"}); err != nil {
		t.Fatal(err)
	}
	if got.Name != "lint" || got.Status != "pass" {
		t.Errorf("writer saw %+v", got)
	}

	boom := errors.New("disk full")
	SetGateWriter(func(GateRequest) error { return boom })
	if err := WriteGateForTest(GateRequest{}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the writer's error", err)
	}

	SetGateWriter(nil)
	if err := WriteGateForTest(GateRequest{}); !errors.Is(err, errGateWriterNotInstalled) {
		t.Errorf("err = %v, want errGateWriterNotInstalled", err)
	}
}

func TestContractGateOptions_DowngradesOnlyOutsideAForgeProject(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg    *config.ProjectConfig
		strict bool
		want   bool
	}{
		"not a forge project":      {nil, false, true},
		"--strict keeps it gating": {nil, true, false},
		"a forge project":          {&config.ProjectConfig{}, false, false},
	} {
		if got := contractGateOptions(tc.cfg, tc.strict).CodegenUnavailable; got != tc.want {
			t.Errorf("%s: CodegenUnavailable = %v, want %v", name, got, tc.want)
		}
	}
}

func TestContractDiagnosticString(t *testing.T) {
	d := contractDiagnostic{Analyzer: "forgeconv-deps", Message: "Deps too wide"}
	d.Pos.Filename, d.Pos.Line, d.Pos.Column = "internal/a/a.go", 3, 2
	if got, want := d.String(), "internal/a/a.go:3:2: Deps too wide (forgeconv-deps)"; got != want {
		t.Errorf("without a hint: %q, want %q", got, want)
	}
	d.FixHint = "narrow it"
	if got, want := d.String(), "internal/a/a.go:3:2: Deps too wide (forgeconv-deps)\n    ↳ narrow it"; got != want {
		t.Errorf("with a hint: %q, want %q", got, want)
	}
}

func TestFindingsToJSONFallsBackToPathAndCarriesTheFix(t *testing.T) {
	// anchorProtoConventionFindings: proto-relative convention paths are
	// re-anchored under proto/, absolute and empty ones are left alone.
	abs := filepath.Join(t.TempDir(), "y.proto") // absolute on every OS (a drive letter on Windows)
	fs := []forgeconv.Finding{{File: "services/x/v1/x.proto"}, {File: abs}, {File: ""}}
	anchorProtoConventionFindings(fs)
	if fs[0].File != "proto/services/x/v1/x.proto" || fs[1].File != abs || fs[2].File != "" {
		t.Fatalf("anchored = %+v", fs)
	}
}

func TestRulesStartingToGate_ExportedWrapperUsesTheRolloutTable(t *testing.T) {
	// Forge's own tree and unpinned projects never move; the exported wrapper
	// must agree with the table-driven core on that.
	if got := RulesStartingToGate(&config.ProjectConfig{ModulePath: "github.com/reliant-labs/forge"}, "v1.0.0", "v1.99.0"); got != nil {
		t.Errorf("forge's own tree: got %+v, want nothing", got)
	}
	if got := RulesStartingToGate(&config.ProjectConfig{}, "not-a-version", "v1.99.0"); got != nil {
		t.Errorf("an unparseable pin: got %+v, want nothing", got)
	}
}

func TestInstallIntoGOBIN_ReplacesTheBinaryAtomically(t *testing.T) {
	gobin := t.TempDir()
	old := filepath.Join(gobin, golangciExeName())
	if err := os.WriteFile(old, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stage string
	tc := golangciToolchain{pin: "v2.14.0", install: func(_ context.Context, dir, goVersion, version string) error {
		stage = dir
		if goVersion != "go1.27.0" || version != "v2.14.0" {
			t.Errorf("install(%s, %s): wrong toolchain or pin", goVersion, version)
		}
		return os.WriteFile(filepath.Join(dir, golangciExeName()), []byte("new"), 0o755)
	}}
	if err := installIntoGOBIN(context.Background(), tc, gobin, "go1.27.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(old); string(b) != "new" {
		t.Errorf("binary = %q, want the new build renamed over the old one", b)
	}
	if filepath.Dir(stage) != gobin {
		t.Errorf("staged in %s, want a directory under GOBIN so the rename never crosses a filesystem", stage)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("the staging directory was left behind: %v", err)
	}

	t.Run("a failed build leaves the installed binary alone", func(t *testing.T) {
		before, _ := os.ReadFile(old)
		boom := errors.New("go install failed")
		tc.install = func(context.Context, string, string, string) error { return boom }
		if err := installIntoGOBIN(context.Background(), tc, gobin, "go1.27.0"); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the build's error", err)
		}
		if after, _ := os.ReadFile(old); string(after) != string(before) {
			t.Errorf("a failed install changed the binary: %q → %q", before, after)
		}
	})
}

func TestErrOrInvalidAndReleaseFor(t *testing.T) {
	cause := errors.New("bad")
	if got := errOrInvalid(cause, "x"); !errors.Is(got, cause) {
		t.Errorf("err = %v, want the cause", got)
	}
	if got := errOrInvalid(nil, "v?"); got == nil || !strings.Contains(got.Error(), `unrecognised version "v?"`) {
		t.Errorf("err = %v", got)
	}
	for in, want := range map[string]string{"1.27": "go1.27.0", "1.27.3": "go1.27.3", "1.27.0": "go1.27.0"} {
		if got := releaseFor(in); got != want {
			t.Errorf("releaseFor(%q) = %q, want %q", in, got, want)
		}
	}
}
