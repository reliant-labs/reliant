package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/templates"
)

// runTaskfileTrimpathFix runs the whole codemod over a project with only a
// Taskfile, so its report is the Taskfile step's.
func runTaskfileTrimpathFix(t *testing.T, dir string, write bool) codemod.Report {
	t.Helper()
	return runTrimpathFix(t, dir, write)
}

func writeTaskfile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// entry is forge's GOFLAGS entry at indent, as the codemod writes it.
func entry(indent int) string { return taskfileTrimpathEntry(indent, "\n") + "\n" }

// stripTrimpathBlock removes forge's env block from a rendered Taskfile: the
// file a project scaffolded before the block existed has.
func stripTrimpathBlock(t *testing.T, rendered string) string {
	t.Helper()
	block := "env:\n" + templates.TaskfileTrimpathGOFLAGS + "\n\n"
	if strings.Count(rendered, block) != 1 {
		t.Fatalf("the render does not hold forge's env block exactly once:\n%s", rendered)
	}
	return strings.Replace(rendered, block, "", 1)
}

// A Taskfile scaffolded before the block existed is the template's render
// without it. The codemod puts it back byte for byte, in every Taskfile kind
// forge scaffolds, and a new project's Taskfile is left alone.
func TestTaskfileTrimpath_RestoresTheScaffoldedBlock(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "templates", "testdata", "golden", "Taskfile.yml.golden"))
	if err != nil {
		t.Fatal(err)
	}
	renders := map[string]string{"service": string(golden)}
	for _, kind := range []string{"cli", "library"} {
		out, err := templates.ProjectTemplates().Render("Taskfile."+kind+".yml.tmpl", map[string]any{"Name": "demo", "Module": "example.com/demo"})
		if err != nil {
			t.Fatal(err)
		}
		renders[kind] = string(out)
	}
	for kind, current := range renders {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			p := writeTaskfile(t, dir, "Taskfile.yml", current)
			if rep := runTaskfileTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 || projectNeedsTrimpathDefault(dir) {
				t.Fatalf("a current Taskfile got %+v", rep)
			}
			old := stripTrimpathBlock(t, current)
			writeTaskfile(t, dir, "Taskfile.yml", old)
			if !projectNeedsTrimpathDefault(dir) {
				t.Fatal("the detection does not see a Taskfile without the block")
			}
			if rep := runTaskfileTrimpathFix(t, dir, false); len(rep.Rewrites) != 1 || readString(t, p) != old {
				t.Fatalf("dry run = %+v, or it wrote the file", rep)
			}
			rep := runTaskfileTrimpathFix(t, dir, true)
			if len(rep.Rewrites) != 1 || len(rep.Next) != 0 || !strings.Contains(rep.Rewrites[0], "before `vars:`") {
				t.Fatalf("run = %+v", rep)
			}
			if got := readString(t, p); got != current {
				t.Fatalf("the codemod's Taskfile is not forge's render:\n%s", got)
			}
			if rep := runTaskfileTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 || projectNeedsTrimpathDefault(dir) {
				t.Errorf("second run = %+v, want nothing", rep)
			}
		})
	}
}

// Each shape the codemod edits, and the exact file it leaves.
func TestTaskfileTrimpath_Edits(t *testing.T) {
	for name, tc := range map[string]struct {
		file, in, want, where string
	}{
		"an env map gets GOFLAGS as its first key": {
			in:    "version: '3'\n\nenv:\n  CGO_ENABLED: \"0\"\n\ntasks:\n  build:\n    cmds: [go build ./...]\n",
			want:  "version: '3'\n\nenv:\n" + entry(2) + "  CGO_ENABLED: \"0\"\n\ntasks:\n  build:\n    cmds: [go build ./...]\n",
			where: "first key of the top-level `env:`",
		},
		"at the map's own indentation": {
			in:    "version: '3'\nenv:\n    # ours\n    FOO: bar\ntasks:\n    a:\n        cmds: [echo]\n",
			want:  "version: '3'\nenv:\n" + entry(4) + "    # ours\n    FOO: bar\ntasks:\n    a:\n        cmds: [echo]\n",
			where: "first key",
		},
		"a bare env gets it as its only key": {
			in:    "version: '3'\nenv:\ntasks:\n  a: {cmds: [echo]}\n",
			want:  "version: '3'\nenv:\n" + entry(2) + "tasks:\n  a: {cmds: [echo]}\n",
			where: "which was empty",
		},
		"a new env goes above the comment of the first of vars and tasks": {
			in:    "version: \"3\"\n\nincludes:\n  x: ./x.yml\n\n# the tasks\n# of this repo\ntasks:\n  a: {cmds: [echo]}\n\nvars:\n  A: b\n",
			want:  "version: \"3\"\n\nincludes:\n  x: ./x.yml\n\nenv:\n" + entry(2) + "\n# the tasks\n# of this repo\ntasks:\n  a: {cmds: [echo]}\n\nvars:\n  A: b\n",
			where: "before `tasks:`",
		},
		"a blank line is added before the new env when none is there": {
			in:    "version: '3'\nvars:\n  A: b\n",
			want:  "version: '3'\n\nenv:\n" + entry(2) + "\nvars:\n  A: b\n",
			where: "before `vars:`",
		},
		"no vars or tasks: after version": {
			in:    "version: '3'\nincludes:\n  x: ./x.yml\n",
			want:  "version: '3'\n\nenv:\n" + entry(2) + "\nincludes:\n  x: ./x.yml\n",
			where: "after `version:`",
		},
		"a version-only file without a final newline": {
			in:    "version: '3'",
			want:  "version: '3'\n\nenv:\n" + entry(2),
			where: "after `version:`",
		},
		"CRLF line endings are kept": {
			in:    "version: '3'\r\n\r\ntasks:\r\n  a:\r\n    cmds: [echo]\r\n",
			want:  "version: '3'\r\n\r\nenv:\r\n" + strings.ReplaceAll(entry(2), "\n", "\r\n") + "\r\ntasks:\r\n  a:\r\n    cmds: [echo]\r\n",
			where: "before `tasks:`",
		},
		"Taskfile.yaml": {
			file:  "Taskfile.yaml",
			in:    "version: '3'\ntasks: {}\n",
			want:  "version: '3'\n\nenv:\n" + entry(2) + "\ntasks: {}\n",
			where: "before `tasks:`",
		},
	} {
		t.Run(name, func(t *testing.T) {
			file := tc.file
			if file == "" {
				file = "Taskfile.yml"
			}
			dir := t.TempDir()
			p := writeTaskfile(t, dir, file, tc.in)
			rep := runTaskfileTrimpathFix(t, dir, true)
			if len(rep.Rewrites) != 1 || len(rep.Next) != 0 || !strings.HasPrefix(rep.Rewrites[0], file+": ") || !strings.Contains(rep.Rewrites[0], tc.where) {
				t.Fatalf("report = %+v", rep)
			}
			if got := readString(t, p); got != tc.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, tc.want)
			}
			if rep := runTaskfileTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 {
				t.Errorf("second run = %+v, want nothing", rep)
			}
		})
	}
}

// What the codemod leaves alone, byte for byte, and what it says about it.
func TestTaskfileTrimpath_LeavesAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		in     string
		script string // scripts/goflags.sh, when not ""
		next   string // "" means no report at all
	}{
		"GOFLAGS already carries -trimpath": {
			in: "version: '3'\nenv:\n  GOFLAGS: -mod=mod -trimpath\n",
		},
		"--trimpath=false, an opt-out": {
			in: "version: '3'\nenv:\n  GOFLAGS: --trimpath=false\n",
		},
		"GOFLAGS from a sh: that names -trimpath": {
			in: "version: '3'\nenv:\n  GOFLAGS:\n    sh: echo \"$(go env GOFLAGS) -trimpath\"\n",
		},
		"GOFLAGS from a script of the project's whose code adds -trimpath": {
			in:     "version: '3'\nenv:\n  GOFLAGS:\n    sh: scripts/goflags.sh\n",
			script: "#!/bin/sh\necho \"$(go env GOFLAGS) -trimpath\"\n",
		},
		"GOFLAGS from a script that names -trimpath only after a # on a code line": {
			in:     "version: '3'\nenv:\n  GOFLAGS:\n    sh: scripts/goflags.sh\n",
			script: "#!/bin/sh\necho -mod=mod # not -trimpath yet\n",
			next:   "whose output forge cannot read",
		},
		// pr-review forge#2685 [B1]: a script that mentions -trimpath only in
		// a comment, and yields flags without it, must not pass.
		"GOFLAGS from a script that mentions -trimpath only in a comment": {
			in:     "version: '3'\nenv:\n  GOFLAGS:\n    sh: scripts/goflags.sh\n",
			script: "#!/bin/sh\n# TODO: add -trimpath\necho -mod=mod\n",
			next:   "whose output forge cannot read",
		},
		"GOFLAGS from a script that does not": {
			in:     "version: '3'\nenv:\n  GOFLAGS:\n    sh: scripts/goflags.sh\n",
			script: "#!/bin/sh\necho -mod=mod\n",
			next:   "whose output forge cannot read",
		},
		"a comment says trimpath, GOFLAGS does not": {
			in:   "version: '3'\nenv:\n  # we build with -trimpath\n  GOFLAGS: -mod=mod\ntasks:\n  b:\n    cmds: [go build -trimpath ./...]\n",
			next: "Merge -trimpath into it by hand",
		},
		"GOFLAGS without -trimpath": {
			in:   "version: '3'\nenv:\n  GOFLAGS: -mod=mod\n",
			next: "Merge -trimpath into it by hand",
		},
		"GOFLAGS merged in from an anchor": {
			in:   "version: '3'\nx: &base\n  GOFLAGS: -mod=mod\nenv:\n  <<: *base\n",
			next: "without -trimpath",
		},
		"a flow-style env": {
			in:   "version: '3'\nenv: {CGO_ENABLED: '0'}\n",
			next: "flow style",
		},
		"an env that is an alias": {
			in:   "version: '3'\nx: &e\n  A: b\nenv: *e\n",
			next: "not a map",
		},
		"an anchored env other keys alias": {
			in:   "version: '3'\nenv: &e\n  A: b\ntasks:\n  a:\n    env: *e\n    cmds: [echo]\n",
			next: "without changing its meaning",
		},
		"invalid YAML": {
			in:   "version: '3'\ntasks:\n  a: [\n",
			next: "does not parse as YAML",
		},
		"a flow-style document": {
			in:   "{version: '3', tasks: {}}\n",
			next: "flow style",
		},
		"no version, vars or tasks": {
			in:   "includes:\n  x: ./x.yml\n",
			next: "has no `version:`",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := writeTaskfile(t, dir, "Taskfile.yml", tc.in)
			if tc.script != "" {
				writeProjectFile(t, dir, "scripts/goflags.sh", tc.script)
			}
			rep := runTaskfileTrimpathFix(t, dir, true)
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
			if !strings.Contains(next, tc.next) || !strings.Contains(next, trimpathPlaybookSkill) || !projectNeedsTrimpathDefault(dir) {
				t.Errorf("next = %q, want it to mention %q and the playbook, and the detection to match", next, tc.next)
			}
		})
	}
	t.Run("no Taskfile", func(t *testing.T) {
		dir := t.TempDir()
		if rep := runTaskfileTrimpathFix(t, dir, true); rep.Changed() || len(rep.Next) != 0 || projectNeedsTrimpathDefault(dir) {
			t.Errorf("a project with no Taskfile got %+v", rep)
		}
	})
}
