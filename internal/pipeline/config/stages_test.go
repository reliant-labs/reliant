package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// summary renders a stage as "state/source kind command|policy:env" for
// compact table expectations.
func summary(s ResolvedStage) string {
	out := string(s.State) + "/" + string(s.Source)
	switch s.Kind {
	case StageCommand:
		out += " " + s.Command
	case StageBuiltinScan:
		out += " builtin:" + string(s.Scan.Policy)
		if s.Scan.Env != "" {
			out += ":" + s.Scan.Env
		}
	}
	return out
}

func summaries(stages []ResolvedStage) []string {
	out := make([]string, len(stages))
	for i, s := range stages {
		out[i] = summary(s)
	}
	return out
}

func TestStageOrderIsFixed(t *testing.T) {
	if want := []StageName{"lint", "test", "build", "sbom_scan"}; !slices.Equal(StageNames, want) {
		t.Fatalf("StageNames = %v, want %v", StageNames, want)
	}
	// The file's key order means nothing: the resolved list is always in
	// stage order.
	r := mustLoad(t, "version: 1\ngate:\n  stages:\n    sbom_scan: off\n    build: {run: b}\n    test: {run: t}\n    lint: {run: l}\n")
	var names []StageName
	for _, s := range r.Gate.Stages {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, StageNames) {
		t.Errorf("resolved stage order = %v, want %v", names, StageNames)
	}
	for _, n := range StageNames {
		if got, want := n.SettingKey(), "gate.stage."+string(n); got != want {
			t.Errorf("SettingKey(%s) = %q, want %q", n, got, want)
		}
	}
}

func TestResolveStagesPerKind(t *testing.T) {
	forgeOne := Project{Kind: RepoForge, Test: "task ci:full", ForgeEnv: "prod"}
	cases := []struct {
		name    string
		project Project
		doc     string
		want    []string
	}{
		{"forge", forgeOne, "version: 1\ngate: \"task ci:full\"\n", []string{
			"on/default reliant forge lint", "on/override task ci:full", "on/default reliant forge env build prod", "on/default builtin:vuln_gate:prod",
		}},
		{"forge env from the release chain", Project{Kind: RepoForge, ForgeEnv: "dev"}, "version: 1\ngate: \"task test\"\nrelease: {envs: [{name: staging}, {name: prod}]}\n", []string{
			"on/default reliant forge lint", "on/override task test", "on/default reliant forge env build staging", "on/default builtin:vuln_gate:staging",
		}},
		{"forge env unknown", Project{Kind: RepoForge}, "version: 1\ngate: \"task test\"\n", []string{
			"on/default reliant forge lint", "on/override task test", "unavailable/default", "on/default builtin:default",
		}},
		{"go", Project{Kind: RepoGo}, "version: 1\ngate: \"go test ./...\"\n", []string{
			"on/default go vet ./...", "on/override go test ./...", "on/default go build ./...", "on/default builtin:default",
		}},
		{"go with golangci-lint", Project{Kind: RepoGo, GolangciLint: true}, "version: 1\ngate: \"go test ./...\"\n", []string{
			"on/default go vet ./... && golangci-lint run ./...", "on/override go test ./...", "on/default go build ./...", "on/default builtin:default",
		}},
		{"node with lint and build", Project{Kind: RepoNode, LintScript: true, BuildScript: true}, "version: 1\ngate: \"npm test\"\n", []string{
			"on/default npm run lint", "on/override npm test", "on/default npm run build", "on/default builtin:default",
		}},
		{"node without lint or build", Project{Kind: RepoNode}, "version: 1\ngate: \"npm test\"\n", []string{
			"unavailable/default", "on/override npm test", "unavailable/default", "on/default builtin:default",
		}},
		{"unknown kind", Project{}, "version: 1\ngate: \"make check\"\n", []string{
			"unavailable/default", "on/override make check", "unavailable/default", "on/default builtin:default",
		}},
		{"overrides and off", forgeOne, "version: 1\ngate:\n  stages:\n    lint: {run: \"task lint\"}\n    test: {run: \"task test\"}\n    build: off\n    sbom_scan: {run: \"scripts/scan.sh\"}\n", []string{
			"on/override task lint", "on/override task test", "off/override", "on/override scripts/scan.sh",
		}},
		{"test stage off", Project{Kind: RepoGo}, "version: 1\ngate: {stages: {test: off}}\n", []string{
			"on/default go vet ./...", "off/override", "on/default go build ./...", "on/default builtin:default",
		}},
		{"runner checks runs no stage", Project{Kind: RepoGo}, "version: 1\ngate: {checks: [CI]}\n", []string{
			"off/override", "off/override", "off/override", "off/override",
		}},
		{"machine with checks runs the stages", Project{Kind: RepoGo}, "version: 1\ngate: {run: x, checks: [Windows]}\n", []string{
			"on/default go vet ./...", "on/override x", "on/default go build ./...", "on/default builtin:default",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := LoadFor([]byte(tc.doc), tc.project)
			if err != nil {
				t.Fatalf("LoadFor:\n%s\nerror: %v", tc.doc, err)
			}
			if got := summaries(r.Gate.Stages); !slices.Equal(got, tc.want) {
				t.Errorf("stages =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
			for _, s := range r.Gate.Stages {
				if s.Reason == "" {
					t.Errorf("stage %s has no reason", s.Name)
				}
				if (s.State == StageOn) != (s.Kind != "") || (s.Kind == StageCommand) != (s.Command != "") || (s.Kind == StageBuiltinScan) != (s.Scan != nil) {
					t.Errorf("stage %s is inconsistent: %+v", s.Name, s)
				}
			}
			// Run is the test stage's command, as R6a's readers expect.
			if test := r.Gate.Stage(StageTest); (test.State == StageOn && r.Gate.Run != test.Command) || (test.State != StageOn && r.Gate.Run != "") {
				t.Errorf("Gate.Run = %q, test stage %+v", r.Gate.Run, test)
			}
		})
	}
}

// TestGateRunStillSetsTheTestStage: every gate form R6a accepted resolves
// the same Run, which is now the test stage's command.
func TestGateRunStillSetsTheTestStage(t *testing.T) {
	for _, doc := range []string{
		"version: 1\ngate: \"npm test\"\n",
		"version: 1\ngate: {run: \"npm test\"}\n",
		"version: 1\ngate: {run: \"npm test\", pool: big, checks: [Windows]}\n",
		"version: 1\ngate: {stages: {test: {run: \"npm test\"}}}\n",
	} {
		r := mustLoad(t, doc)
		test := r.Gate.Stage(StageTest)
		if r.Gate.Run != "npm test" || test.State != StageOn || test.Source != StageSourceOverride || test.Command != "npm test" {
			t.Errorf("%s: Run = %q, test stage = %+v", doc, r.Gate.Run, test)
		}
	}
	// A Config built in code with only Gate.Run resolves the same way.
	r, err := (&Config{Version: 1, Gate: &Gate{Run: "go test ./..."}}).ResolveFor(Project{Kind: RepoGo})
	if err != nil || r.Gate.Stage(StageTest).Command != "go test ./..." || r.Gate.Stage(StageLint).Command != "go vet ./..." {
		t.Errorf("ResolveFor = %+v, %v", r.Gate.Stages, err)
	}
}

func TestStageRejects(t *testing.T) {
	cases := []struct {
		name, doc, path, want string
	}{
		{"bare command", "version: 1\ngate: {run: x, stages: {lint: \"go vet ./...\"}}\n", "gate.stages.lint", "a command goes in run, e.g. `lint: {run: \"go vet ./...\"}`"},
		{"capitalised off", "version: 1\ngate: {run: x, stages: {build: OFF}}\n", "gate.stages.build", "must be off (lowercase)"},
		{"boolean", "version: 1\ngate: {run: x, stages: {build: false}}\n", "gate.stages.build", "write off to turn the stage off"},
		{"integer", "version: 1\ngate: {run: x, stages: {build: 0}}\n", "gate.stages.build", "must be off or {run: <command>}, got the integer 0"},
		{"null", "version: 1\ngate: {run: x, stages: {build: null}}\n", "gate.stages.build", "is empty: write {run: <command>}"},
		{"empty mapping", "version: 1\ngate: {run: x, stages: {build: {}}}\n", "gate.stages.build", "needs run"},
		{"empty run", "version: 1\ngate: {run: x, stages: {build: {run: \"\"}}}\n", "gate.stages.build.run", "is empty"},
		{"null run", "version: 1\ngate: {run: x, stages: {build: {run: null}}}\n", "gate.stages.build.run", "is empty"},
		{"unknown stage", "version: 1\ngate: {run: x, stages: {fmt: off}}\n", "gate.stages.fmt", `unknown key "fmt": gate.stages takes lint, test, build, sbom_scan`},
		{"unknown stage key", "version: 1\ngate: {run: x, stages: {lint: {run: y, when: z}}}\n", "gate.stages.lint.when", "unknown key"},
		{"gate.run and stages.test", "version: 1\ngate:\n  run: x\n  stages: {test: {run: y}}\n", "gate.stages.test", "and gate.run (line 3) both set the test stage"},
		{"gate.run and test off", "version: 1\ngate:\n  run: x\n  stages: {test: off}\n", "gate.stages.test", "and gate.run (line 3) both set the test stage"},
		{"no test command", "version: 1\ngate: {stages: {lint: off}}\n", "gate", "needs a command for runner machine"},
		{"every stage off", "version: 1\ngate: {stages: {lint: off, test: off, build: off, sbom_scan: off}}\n", "gate.stages", "turns every stage off"},
		{"runner checks", "version: 1\ngate: {runner: checks, checks: [CI], stages: {lint: off}}\n", "gate.stages", "applies only to runner machine"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := parseErrors(t, tc.doc)
			e := findError(errs, tc.path)
			if e == nil {
				t.Fatalf("errors %v, want one at %s", errs, tc.path)
			}
			if !strings.Contains(e.Msg, tc.want) {
				t.Errorf("error at %s = %q, want it to contain %q", tc.path, e.Msg, tc.want)
			}
		})
	}
	// A Config built in code is held to the same rules.
	err := (&Config{Version: 1, Gate: &Gate{Run: "x", Stages: &Stages{Test: &Stage{Run: "y"}}}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "gate.stages.test: and gate.run both set the test stage") {
		t.Errorf("Validate = %v", err)
	}
}

func TestDetectProject(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	cases := []struct {
		name string
		fs   fstest.MapFS
		want Project
	}{
		{"forge with one env", fstest.MapFS{"forge.yaml": file(""), "deploy/kcl/prod/main.k": file(""), "deploy/kcl/kcl.mod": file("")},
			Project{Kind: RepoForge, Test: "task test", ForgeEnv: "prod"}},
		{"forge with several envs", fstest.MapFS{"forge.yaml": file(""), "deploy/kcl/dev/main.k": file(""), "deploy/kcl/prod/main.k": file("")},
			Project{Kind: RepoForge, Test: "task test"}},
		{"forge env dir without main.k", fstest.MapFS{"forge.yaml": file(""), "deploy/kcl/prod/main.k": file(""), "deploy/kcl/lib/x.k": file("")},
			Project{Kind: RepoForge, Test: "task test", ForgeEnv: "prod"}},
		{"go with golangci", fstest.MapFS{"go.mod": file("module x\n"), ".golangci.yml": file("")},
			Project{Kind: RepoGo, Test: "go test ./...", GolangciLint: true}},
		{"node with scripts", fstest.MapFS{"package.json": file(`{"scripts":{"test":"vitest","lint":"eslint .","build":"vite build"}}`)},
			Project{Kind: RepoNode, Test: "npm test", LintScript: true, BuildScript: true}},
		{"node without lint", fstest.MapFS{"package.json": file(`{"scripts":{"test":"vitest"}}`)},
			Project{Kind: RepoNode, Test: "npm test"}},
		{"nothing", fstest.MapFS{"README.md": file("")}, Project{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectProject(tc.fs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DetectProject = %+v, want %+v", got, tc.want)
			}
		})
	}
	// The enrolment screen's defaults for a detected project: test is the
	// proposal the file then writes.
	p := DetectProject(fstest.MapFS{"package.json": file(`{"scripts":{"test":"jest"}}`)})
	if got, want := summaries(p.DefaultStages()), []string{"unavailable/default", "on/default npm test", "unavailable/default", "on/default builtin:default"}; !slices.Equal(got, want) {
		t.Errorf("DefaultStages = %v, want %v", got, want)
	}
}

func TestStageChanges(t *testing.T) {
	load := func(doc string) Resolved { return mustLoad(t, doc) }
	plain := load("version: 1\ngate: \"go test ./...\"\n")
	cases := []struct {
		name     string
		old, new Resolved
		want     []StageChange
	}{
		{"no change", plain, plain, nil},
		{"first config with defaults", Resolved{}, plain, nil},
		{"first config turns a stage off", Resolved{}, load("version: 1\ngate: {run: x, stages: {sbom_scan: off}}\n"),
			[]StageChange{{Stage: StageSBOMScan, Setting: "gate.stage.sbom_scan", Old: StageSettingOn, New: StageSettingOff}}},
		{"turned off", plain, load("version: 1\ngate: {run: x, stages: {lint: off, build: off}}\n"), []StageChange{
			{Stage: StageLint, Setting: "gate.stage.lint", Old: StageSettingOn, New: StageSettingOff},
			{Stage: StageBuild, Setting: "gate.stage.build", Old: StageSettingOn, New: StageSettingOff},
		}},
		{"turned back on", load("version: 1\ngate: {stages: {test: off}}\n"), plain,
			[]StageChange{{Stage: StageTest, Setting: "gate.stage.test", Old: StageSettingOff, New: StageSettingOn}}},
		{"a changed command is not a setting change", plain, load("version: 1\ngate: {run: x, stages: {lint: {run: y}}}\n"), nil},
		{"unavailable is on as a setting", load("version: 1\ngate: x\n"), func() Resolved {
			r, err := LoadFor([]byte("version: 1\ngate: x\n"), Project{Kind: RepoGo})
			if err != nil {
				t.Fatal(err)
			}
			return r
		}(), nil},
		{"runner checks turns every stage off", plain, load("version: 1\ngate: {checks: [CI]}\n"), []StageChange{
			{Stage: StageLint, Setting: "gate.stage.lint", Old: StageSettingOn, New: StageSettingOff},
			{Stage: StageTest, Setting: "gate.stage.test", Old: StageSettingOn, New: StageSettingOff},
			{Stage: StageBuild, Setting: "gate.stage.build", Old: StageSettingOn, New: StageSettingOff},
			{Stage: StageSBOMScan, Setting: "gate.stage.sbom_scan", Old: StageSettingOn, New: StageSettingOff},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StageChanges(tc.old, tc.new); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("StageChanges = %+v, want %+v", got, tc.want)
			}
		})
	}
}
