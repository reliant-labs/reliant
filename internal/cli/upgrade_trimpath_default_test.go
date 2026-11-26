package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/templates"
)

func runTrimpathFix(t *testing.T, dir string, write bool) codemod.Report {
	t.Helper()
	c, ok := codemod.Lookup("trimpath-default")
	if !ok {
		t.Fatal("trimpath-default is not registered")
	}
	rep, err := c.Run(dir, codemod.Options{Write: write})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

const kclImportForge = "import forge\n\n"

// flagsComment is forge's comment above a GoBuild the step gave its flags,
// at indent.
func flagsComment(indent string) string {
	return indent + "# flags: -trimpath lets every worktree share Go's build cache; forge build,\n" +
		indent + "# go run and air all pass it, and debug builds stay untrimmed. To keep\n" +
		indent + "# absolute source paths, remove it or add \"-trimpath=false\" after it.\n"
}

// Each GoBuild shape the KCL step edits, and the exact file it leaves.
func TestTrimpathDefault_KCLEdits(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"a one-line literal": {
			in:   kclImportForge + "api = forge.Workload {\n    build = forge.GoBuild {cmd = \"./cmd/app\", output_name = \"app\"}\n}\n",
			want: kclImportForge + "api = forge.Workload {\n" + flagsComment("    ") + "    build = forge.GoBuild {cmd = \"./cmd/app\", output_name = \"app\", flags = [\"-trimpath\"]}\n}\n",
		},
		"a one-line literal with a trailing comma": {
			in:   kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\", }\n",
			want: kclImportForge + flagsComment("") + "b = forge.GoBuild {cmd = \"./cmd/app\", flags = [\"-trimpath\"] }\n",
		},
		"a multi-line literal": {
			in:   kclImportForge + "b = forge.GoBuild {\n    cmd = \"./cmd/app\"\n    output_name = \"app\"  # the binary\n}\n",
			want: kclImportForge + "b = forge.GoBuild {\n    cmd = \"./cmd/app\"\n    output_name = \"app\"  # the binary\n" + flagsComment("    ") + "    flags = [\"-trimpath\"]\n}\n",
		},
		"a multi-line literal with commas": {
			in:   kclImportForge + "b = forge.GoBuild {\n        cmd = \"./cmd/app\",\n        env = {\n            CGO_ENABLED = \"0\"\n        },\n}\n",
			want: kclImportForge + "b = forge.GoBuild {\n        cmd = \"./cmd/app\",\n        env = {\n            CGO_ENABLED = \"0\"\n        },\n" + flagsComment("        ") + "        flags = [\"-trimpath\"],\n}\n",
		},
		"an existing flags list": {
			in:   kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\", flags = [\"-race\"]}\n",
			want: kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\", flags = [\"-trimpath\", \"-race\"]}\n",
		},
		"an empty flags list": {
			in:   kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\", flags = []}\n",
			want: kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\", flags = [\"-trimpath\"]}\n",
		},
		"a flags list one element per line": {
			in:   kclImportForge + "b = forge.GoBuild {\n    cmd = \"./cmd/app\"\n    flags = [\n        \"-race\",\n    ]\n}\n",
			want: kclImportForge + "b = forge.GoBuild {\n    cmd = \"./cmd/app\"\n    flags = [\n        \"-trimpath\",\n        \"-race\",\n    ]\n}\n",
		},
		"an aliased submodule import, and two literals": {
			in:   "import forge.workloads as fw\n\na = fw.GoBuild {cmd = \"./cmd/a\"}\nb = fw.GoBuild {cmd = \"./cmd/b\"}\n",
			want: "import forge.workloads as fw\n\n" + flagsComment("") + "a = fw.GoBuild {cmd = \"./cmd/a\", flags = [\"-trimpath\"]}\n" + flagsComment("") + "b = fw.GoBuild {cmd = \"./cmd/b\", flags = [\"-trimpath\"]}\n",
		},
		"a literal inside a lambda": {
			in:   kclImportForge + "_go = lambda c: str {\n    forge.GoBuild {cmd = c}\n}\n",
			want: kclImportForge + "_go = lambda c: str {\n" + flagsComment("    ") + "    forge.GoBuild {cmd = c, flags = [\"-trimpath\"]}\n}\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := writeProjectFile(t, dir, "deploy/kcl/workloads.k", tc.in)
			if rep := runTrimpathFix(t, dir, false); !rep.Changed() || readString(t, p) != tc.in {
				t.Fatalf("dry run = %+v, or it wrote the file", rep)
			}
			rep := runTrimpathFix(t, dir, true)
			if !rep.Changed() || len(rep.Next) != 0 || !strings.HasPrefix(rep.Rewrites[0], "deploy/kcl/workloads.k:") {
				t.Fatalf("report = %+v", rep)
			}
			if got := readString(t, p); got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			if rep := runTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 || projectNeedsTrimpathDefault(dir) {
				t.Errorf("second run = %+v, want nothing", rep)
			}
		})
	}
}

// What the KCL step leaves alone, byte for byte, and what it says about it.
func TestTrimpathDefault_KCLLeavesAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		rel, in string
		next    string // "" means no report at all
	}{
		"already -trimpath": {in: kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\", flags = [\"-trimpath\"]}\n"},
		"opted out":         {in: kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\", flags = [\"-trimpath=false\"]}\n"},
		"not forge's GoBuild": {
			in: "import other as o\n\nb = o.GoBuild {cmd = \"./cmd/app\"}\n",
		},
		"a generated file":    {rel: "deploy/kcl/dev/wiring_gen.k", in: kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\"}\n"},
		"config_gen":          {rel: "deploy/kcl/config_gen/x.k", in: kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\"}\n"},
		"a generated header":  {in: "# Code generated by forge. DO NOT EDIT.\n" + kclImportForge + "b = forge.GoBuild {cmd = \"./cmd/app\"}\n"},
		"flags is a variable": {in: kclImportForge + "_f = [\"-race\"]\nb = forge.GoBuild {cmd = \"./cmd/app\", flags = _f}\n", next: "workloads.k:4: this GoBuild's flags is a variable or an expression"},
		"flags has a variable element": {
			in: kclImportForge + "_f = \"-race\"\nb = forge.GoBuild {cmd = \"./cmd/app\", flags = [_f]}\n", next: "workloads.k:4: this GoBuild's flags list has an element that is not a plain string",
		},
		"a spread": {
			in: kclImportForge + "_base = {cmd = \"./cmd/app\"}\nb = forge.GoBuild {**_base}\n", next: "workloads.k:4: this GoBuild literal has entries forge cannot read",
		},
		"a schema derived from GoBuild": {
			in: kclImportForge + "schema AppBuild(forge.GoBuild):\n    cmd: str = \"./cmd/app\"\n", next: "workloads.k:3: a schema derived from GoBuild",
		},
		"a file KCL cannot parse": {
			in: kclImportForge + "b = forge.GoBuild {cmd = \n", next: "forge cannot parse it",
		},
	} {
		t.Run(name, func(t *testing.T) {
			rel := tc.rel
			if rel == "" {
				rel = "deploy/kcl/workloads.k"
			}
			dir := t.TempDir()
			p := writeProjectFile(t, dir, rel, tc.in)
			rep := runTrimpathFix(t, dir, true)
			if rep.Changed() || readString(t, p) != tc.in {
				t.Fatalf("the file was rewritten: %+v\n%s", rep, readString(t, p))
			}
			next := strings.Join(rep.Next, "\n")
			if tc.next == "" {
				if next != "" || projectNeedsTrimpathDefault(dir) {
					t.Errorf("want no report, got %q", next)
				}
				return
			}
			if !strings.Contains(next, tc.next) || !strings.Contains(next, "by hand") || !projectNeedsTrimpathDefault(dir) {
				t.Errorf("next = %q, want it to mention %q and the manual edit, and the detection to match", next, tc.next)
			}
		})
	}
}

// A literal the step edits and one it cannot, in one file: the edit is
// written and the other is reported. Both report lines name the line in the
// file as written, below forge's inserted comment.
func TestTrimpathDefault_KCLEditsWhatItCanAndReportsTheRest(t *testing.T) {
	dir := t.TempDir()
	in := kclImportForge + "_f = [\"-race\"]\na = forge.GoBuild {cmd = \"./cmd/a\"}\nb = forge.GoBuild {cmd = \"./cmd/b\", flags = _f}\n"
	p := writeProjectFile(t, dir, "deploy/kcl/workloads.k", in)
	rep := runTrimpathFix(t, dir, true)
	want := kclImportForge + "_f = [\"-race\"]\n" + flagsComment("") + "a = forge.GoBuild {cmd = \"./cmd/a\", flags = [\"-trimpath\"]}\nb = forge.GoBuild {cmd = \"./cmd/b\", flags = _f}\n"
	if got := readString(t, p); got != want || len(rep.Rewrites) != 1 || len(rep.Next) != 1 || !strings.Contains(rep.Next[0], "workloads.k:8:") || !strings.HasPrefix(rep.Rewrites[0], "deploy/kcl/workloads.k:7:") {
		t.Fatalf("report = %+v\n%s", rep, got)
	}
}

// airTemplate is the current .air.toml forge scaffolds for a binary.
func airTemplate(t *testing.T, name string) string {
	t.Helper()
	b, err := templates.ProjectTemplates().Render("air.toml.tmpl", map[string]any{"Name": name})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// airOld is the .air.toml an older forge scaffolded: the current one with
// the build commands as forge wrote them before $FORGE_GO_BUILD_FLAGS.
func airOld(t *testing.T, name string) string {
	t.Helper()
	cur := airTemplate(t, name)
	unixNew := "CGO_ENABLED=0 go build $FORGE_GO_BUILD_FLAGS -buildvcs=false"
	if strings.Count(cur, unixNew) != 1 {
		t.Fatalf("the template's [build] cmd changed shape:\n%s", cur)
	}
	old := strings.Replace(cur, unixNew, "CGO_ENABLED=0 go build -buildvcs=false", 1)
	winNew := "$f = @(\"$env:FORGE_GO_BUILD_FLAGS\" -split \" \" | Where-Object { $_ }); go build @f -buildvcs=false"
	if strings.Count(old, winNew) != 1 {
		t.Fatalf("the template's [build.windows] cmd changed shape:\n%s", cur)
	}
	return strings.Replace(old, winNew, "go build -buildvcs=false", 1)
}

// An older forge's .air.toml becomes the current template, byte for byte.
func TestTrimpathDefault_AirOldScaffoldBecomesTheTemplate(t *testing.T) {
	dir := t.TempDir()
	old := airOld(t, "app")
	p := writeProjectFile(t, dir, ".air.toml", old)
	rep := runTrimpathFix(t, dir, true)
	if len(rep.Rewrites) != 2 || len(rep.Next) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if got := readString(t, p); got != airTemplate(t, "app") {
		t.Fatalf("the rewritten .air.toml is not the template:\n%s", got)
	}
	if rep := runTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 || projectNeedsTrimpathDefault(dir) {
		t.Errorf("second run = %+v, want nothing", rep)
	}
}

// What the air step does with edited, current and debug configs.
func TestTrimpathDefault_AirCases(t *testing.T) {
	t.Run("an edited windows command is the manual step", func(t *testing.T) {
		dir := t.TempDir()
		in := "[build]\n  cmd = \"go build -o ./tmp/app ./cmd/app\"\n\n[build.windows]\n  cmd = 'go build -o ./tmp/app.exe -tags x ./cmd/app'\n"
		p := writeProjectFile(t, dir, ".air.toml", in)
		rep := runTrimpathFix(t, dir, true)
		want := "[build]\n  cmd = \"go build $FORGE_GO_BUILD_FLAGS -o ./tmp/app ./cmd/app\"\n\n[build.windows]\n  cmd = 'go build -o ./tmp/app.exe -tags x ./cmd/app'\n"
		if got := readString(t, p); got != want || len(rep.Rewrites) != 1 || len(rep.Next) != 1 || !strings.Contains(rep.Next[0], ".air.toml:5: [build.windows]") {
			t.Fatalf("report = %+v\n%s", rep, got)
		}
		if !projectNeedsTrimpathDefault(dir) {
			t.Error("the detection does not see the manual step")
		}
	})
	t.Run("a current config is left alone", func(t *testing.T) {
		dir := t.TempDir()
		in := airTemplate(t, "app")
		writeProjectFile(t, dir, ".air.toml", in)
		if rep := runTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 {
			t.Fatalf("report = %+v", rep)
		}
	})
	t.Run("an air_config the KCL names is edited; a debug config never", func(t *testing.T) {
		dir := t.TempDir()
		debug := "[build]\n  cmd = \"go build -gcflags='all=-N -l' -o ./tmp/app ./cmd/app\"\n"
		dp := writeProjectFile(t, dir, ".air-debug.toml", debug)
		api := writeProjectFile(t, dir, "air/api.toml", "[build]\n  cmd = \"go build -o ./tmp/api ./cmd/api\"\n")
		writeProjectFile(t, dir, "deploy/kcl/dev/main.k", "r = {runner = \"air\", air_config = \"./air/api.toml\"}\nd = {runner = \"air\", air_config = \".air-debug.toml\"}\n")
		rep := runTrimpathFix(t, dir, true)
		if readString(t, dp) != debug {
			t.Fatalf("the debug config was rewritten: %+v", rep)
		}
		if got := readString(t, api); got != "[build]\n  cmd = \"go build $FORGE_GO_BUILD_FLAGS -o ./tmp/api ./cmd/api\"\n" || len(rep.Rewrites) != 1 {
			t.Fatalf("report = %+v\n%s", rep, got)
		}
	})
	t.Run("a command that says trimpath is left alone", func(t *testing.T) {
		dir := t.TempDir()
		in := "[build]\n  cmd = \"go build -trimpath=false -o ./tmp/app ./cmd/app\"\n"
		writeProjectFile(t, dir, ".air.toml", in)
		if rep := runTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 {
			t.Fatalf("report = %+v", rep)
		}
	})
}

// The three steps together, on an old-style project: every file is edited
// in one run, the second run is a no-op, and the detection follows.
func TestTrimpathDefault_AnOldProject(t *testing.T) {
	dir := t.TempDir()
	writeProjectFile(t, dir, "Taskfile.yml", "version: '3'\n\ntasks:\n  test:\n    cmds: [go test ./...]\n")
	writeProjectFile(t, dir, "deploy/kcl/workloads.k", kclImportForge+"b = forge.GoBuild {cmd = \"./cmd/app\", output_name = \"app\"}\n")
	writeProjectFile(t, dir, ".air.toml", airOld(t, "app"))
	if !projectNeedsTrimpathDefault(dir) {
		t.Fatal("the detection does not see an old project")
	}
	rep := runTrimpathFix(t, dir, true)
	if len(rep.Rewrites) != 4 || len(rep.Next) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if rep := runTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 || projectNeedsTrimpathDefault(dir) {
		t.Errorf("second run = %+v, want nothing", rep)
	}
}
