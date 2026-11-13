package templates

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"mvdan.cc/sh/v3/syntax"
)

// Task runs every `cmds` entry through mvdan.cc/sh's interpreter on every OS.
// Shell syntax and interpreter builtins work on Windows; any other command is
// an EXTERNAL program and is absent on a stock Windows machine. From Task
// v3.45.3 (2025-09-15) Task also ships in-process Go implementations of the
// "core utils" below, enabled by default on Windows (TASK_CORE_UTILS), which is
// why rm/mkdir/cp/mv/cat/gunzip are safe while grep/sed/dirname/tr are not.
// This guard fails when a Taskfile template starts calling an unlisted tool.

var taskBuiltinCommands = setOf(
	// mvdan interpreter builtins.
	"echo", "printf", "exit", "command", "cd", "test", "[", "true", "false", "read",
	"export", "unset", "set", "shift", "type", "pwd", "wait", "eval", "source", ".",
	"return", "break", "continue", ":", "local", "declare", "let", "trap", "umask",
	"unalias", "alias", "getopts", "mapfile", "readarray", "hash", "dirs", "pushd", "popd",
	"fg", "bg", "builtin", "readonly", "typeset", "exec", "time", "kill",
	// Task core utils (mvdan.cc/sh/x/coreutils, Task >= v3.45.3).
	"base64", "cat", "chmod", "cp", "find", "gzcat", "gzip", "gunzip", "ls", "mkdir",
	"mktemp", "mv", "rm", "shasum", "tar", "touch", "xargs",
)

var taskAllowedExternalCommands = setOf(
	// Tools the scaffold's prerequisites install or that CI provides on every OS.
	"go", "forge", "buf", "npm", "npx", "node", "docker", "golangci-lint", "goimports",
	"task", "git", "migrate", "psql", "pg_dump", "pg_restore", "createdb", "dropdb",
	"protoc-gen-go", "protoc-gen-connect-go", "dlv", "air",
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

var taskTemplateExpr = regexp.MustCompile(`\{\{.*?\}\}`)

func taskfileShellSnippets(t *testing.T, rendered []byte) []string {
	t.Helper()
	var doc struct {
		Vars  map[string]any `yaml:"vars"`
		Tasks map[string]struct {
			Cmds []any          `yaml:"cmds"`
			Vars map[string]any `yaml:"vars"`
		} `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(rendered, &doc); err != nil {
		t.Fatalf("rendered Taskfile is not valid YAML: %v", err)
	}
	var out []string
	addVars := func(vars map[string]any) {
		for _, v := range vars {
			if m, ok := v.(map[string]any); ok {
				if sh, ok := m["sh"].(string); ok {
					out = append(out, sh)
				}
			}
		}
	}
	addVars(doc.Vars)
	for _, task := range doc.Tasks {
		addVars(task.Vars)
		for _, c := range task.Cmds {
			switch v := c.(type) {
			case string:
				out = append(out, v)
			case map[string]any:
				if s, ok := v["cmd"].(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func externalCommands(t *testing.T, snippet string) []string {
	t.Helper()
	// Task template expressions ({{.CLI_ARGS}}, {{exeExt}}) are resolved by Task
	// before the shell sees the text; neutralise them for parsing. {{.SLOT}}
	// is a command PREFIX (`forge slot run --`, or nothing), so the command
	// after it is the one to check: it neutralises to nothing.
	text := strings.ReplaceAll(snippet, "{{.SLOT}}", "")
	// {{.FORGE}} is the pinned forge, `go run …/cmd/forge@<pin>`: go is what
	// the shell runs. {{.SCRIPT}} is the npm command line the `frontends`
	// task runs in each frontend.
	text = strings.ReplaceAll(text, "{{.FORGE}}", "go")
	text = strings.ReplaceAll(text, "{{.SCRIPT}}", "npm")
	text = taskTemplateExpr.ReplaceAllString(text, "x")
	// Task parses with mvdan's bash dialect.
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(text), "")
	if err != nil {
		t.Errorf("cmd does not parse as shell: %v\n%s", err, snippet)
		return nil
	}
	var names []string
	syntax.Walk(file, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if name := call.Args[0].Lit(); name != "" {
			names = append(names, name)
		}
		return true
	})
	return names
}

func TestTaskfileTemplatesArePortableToWindows(t *testing.T) {
	data := projectData()
	for _, name := range []string{"Taskfile.yml.tmpl", "Taskfile.cli.yml.tmpl", "Taskfile.library.yml.tmpl"} {
		t.Run(name, func(t *testing.T) {
			rendered := renderProject(t, name, data)
			snippets := taskfileShellSnippets(t, rendered)
			if len(snippets) == 0 {
				t.Fatal("found no cmds to check — extraction is broken")
			}
			bad := map[string]bool{}
			for _, snippet := range snippets {
				for _, cmd := range externalCommands(t, snippet) {
					if !taskBuiltinCommands[cmd] && !taskAllowedExternalCommands[cmd] {
						bad[cmd] = true
					}
				}
			}
			if len(bad) > 0 {
				names := make([]string, 0, len(bad))
				for n := range bad {
					names = append(names, n)
				}
				sort.Strings(names)
				t.Errorf("%s invokes commands that are neither interpreter builtins, Task core utils, nor forge-provisioned tools (absent on stock Windows): %v", name, names)
			}
		})
	}
}

func TestTaskfileBuiltBinaryUsesExeExt(t *testing.T) {
	out := string(renderProject(t, "Taskfile.cli.yml.tmpl", projectData()))
	if !strings.Contains(out, "-o bin/demo{{exeExt}}") {
		t.Errorf("CLI Taskfile must build bin/<name>{{exeExt}} so Windows gets demo.exe:\n%s", out)
	}
}
