package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// GenerateConfig is the `generate:` block: what `forge generate` runs on the
// project's behalf beyond its own code generation.
type GenerateConfig struct {
	// Hooks are project-declared commands run in declaration order. See
	// [GenerateHook].
	Hooks []GenerateHook `yaml:"hooks,omitempty"`
}

// GenerateHook is one command `forge generate` runs, after forge's own code
// generation (proto stubs, ORM, mocks, composition) and before the `go mod
// tidy` and `go build` that validate the result. Code a hook emits is
// therefore tidied and compiled in the same run that produced it.
//
// It is the escape hatch for a generator forge does not own: a query
// generator kept while a project ports onto the ORM (sqlc), a project's own
// codegen tool, a second buf template, a schema or docs dump. forge does not
// interpret the command; it runs it, streams its output, and fails the
// generate if it exits non-zero. The command runs with the codegen tools
// go.mod pins (buf, protoc-gen-go, protoc-gen-connect-go) first on its PATH,
// the same ones forge's own buf step runs.
//
//	generate:
//	  hooks:
//	    - name: sqlc
//	      command: [sqlc, generate]
//	      writes: [internal/db/postgres/generated]
type GenerateHook struct {
	// Name labels the hook in generate's output and in errors. Required and
	// unique.
	Name string `yaml:"name"`
	// Command is the argv to execute: no shell, so no quoting rules differ
	// between platforms. Spell a shell pipeline as [sh, -c, "..."]. A
	// relative command[0] with a path separator resolves against Dir.
	Command []string `yaml:"command"`
	// Dir is the project-relative working directory. Empty is the project
	// root.
	Dir string `yaml:"dir,omitempty"`
	// Writes are the project-relative files or directories the command may
	// write. They are captured into generate's rollback journal before the
	// hook runs, so a failed generate restores them like forge's own output.
	// An undeclared write is not restored; the post-rollback verification
	// names it instead of claiming the tree is back to its pre-run state.
	Writes []string `yaml:"writes,omitempty"`
}

// validateGenerateHooks checks the `generate.hooks` list: every hook needs a
// unique name and a command, and its paths must stay inside the project.
// It also carries the one-version migration hint for sqlc: forge used to
// run a root sqlc.yaml implicitly, and a project that relied on that must
// now declare it, or its sqlc output silently stops being regenerated.
func validateGenerateHooks(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue
	seen := map[string]bool{}
	for i, h := range cfg.Generate.Hooks {
		at := func(key string) (int, int) {
			segs := []string{"generate", "hooks", fmt.Sprintf("[%d]", i)}
			if key != "" {
				segs = append(segs, key)
			}
			return findNodePos(root, segs)
		}
		label := fmt.Sprintf("generate.hooks[%d]", i)
		name := strings.TrimSpace(h.Name)
		switch {
		case name == "":
			line, col := at("")
			out = append(out, validationIssue{line: line, column: col,
				msg: label + " has no name",
				fix: "add `name:`, the label generate prints and errors cite."})
		case seen[name]:
			line, col := at("name")
			out = append(out, validationIssue{line: line, column: col,
				msg: fmt.Sprintf("%s: hook name %q is declared twice", label, name),
				fix: "give each hook a unique name."})
		default:
			seen[name] = true
		}
		if len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
			line, col := at("command")
			if line == 0 {
				line, col = at("")
			}
			out = append(out, validationIssue{line: line, column: col,
				msg: label + " has no command",
				fix: "set `command:` to the argv to run, e.g. [sqlc, generate]; a shell line is [sh, -c, \"...\"]."})
		}
		if h.Dir != "" && !isProjectRelative(h.Dir, true) {
			line, col := at("dir")
			out = append(out, validationIssue{line: line, column: col,
				msg: fmt.Sprintf("%s.dir %q is not inside the project", label, h.Dir),
				fix: "use a project-relative directory, or omit `dir:` to run at the project root."})
		}
		for j, w := range h.Writes {
			if isProjectRelative(w, false) {
				continue
			}
			line, col := findNodePos(root, []string{"generate", "hooks", fmt.Sprintf("[%d]", i), "writes", fmt.Sprintf("[%d]", j)})
			out = append(out, validationIssue{line: line, column: col,
				msg: fmt.Sprintf("%s.writes[%d] %q is not a path inside the project", label, j, w),
				fix: "name the project-relative files or directories the command writes (not the project root itself)."})
		}
	}
	out = append(out, sqlcHookHint(cfg, root)...)
	return out
}

// sqlcHookHint warns when a root sqlc.yaml exists but no hook runs sqlc.
// forge generate stopped running sqlc implicitly; without this the project's
// sqlc output would go stale with nothing saying why.
func sqlcHookHint(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	if cfg.projectDir == "" {
		return nil
	}
	var found string
	for _, name := range []string{"sqlc.yaml", "sqlc.yml"} {
		if fileExists(filepath.Join(cfg.projectDir, name)) {
			found = name
			break
		}
	}
	if found == "" {
		return nil
	}
	for _, h := range cfg.Generate.Hooks {
		for _, arg := range h.Command {
			if strings.Contains(arg, "sqlc") {
				return nil
			}
		}
	}
	line, col := findNodePos(root, []string{"generate"})
	return []validationIssue{{
		line:    line,
		column:  col,
		warning: true,
		msg:     fmt.Sprintf("%s is no longer run by `forge generate`", found),
		fix: "declare it as a hook so generate keeps running it:\n" +
			"  generate:\n    hooks:\n      - name: sqlc\n        command: [sqlc, generate]\n" +
			"        writes: [<the gen.go.out directory from " + found + ">]\n" +
			"forge's data-access path is the ORM plus internal/db/<entity>_repo_ext.go (skill: db); the hook keeps sqlc running while you port onto it.",
	}}
}

// isProjectRelative reports whether p names a location inside the project:
// relative, and not climbing out of it. allowRoot admits "." (the project
// root itself), which is a valid working directory but not a write
// declaration — capturing the whole project would journal every file.
func isProjectRelative(p string, allowRoot bool) bool {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	if clean == "." {
		return allowRoot
	}
	return true
}
