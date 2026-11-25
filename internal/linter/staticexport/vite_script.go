package staticexport

import (
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// viteBuildArgs recognizes a single statically visible build. It doesn't try
// to decide which of several builds is published, execute shell code, or
// follow subprocesses whose command or working directory isn't knowable.
func viteBuildArgs(script string) ([]string, string) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, "cannot be parsed as a shell command"
	}
	var builds [][]string
	complex := false
	syntax.Walk(file, func(n syntax.Node) bool {
		switch node := n.(type) {
		case nil, *syntax.Comment:
			return false
		case *syntax.File:
			return true
		case *syntax.Stmt:
			complex = complex || node.Background || node.Coprocess || node.Disown || node.Negated || len(node.Redirs) != 0
			return true
		case *syntax.BinaryCmd:
			complex = complex || (node.Op != syntax.AndStmt && node.Op != syntax.OrStmt)
			return true
		case *syntax.CallExpr:
			args, build, unknown := viteCall(node)
			complex = complex || unknown
			if build {
				builds = append(builds, args)
			}
			return false // don't count a Vite command substitution as a top-level build
		default:
			complex = true
			return false
		}
	})
	switch {
	case len(builds) > 1:
		return nil, "runs multiple `vite build` commands; which build supplies the published assets is ambiguous"
	case complex:
		return nil, "uses shell commands or values whose Vite build configuration cannot be resolved"
	case len(builds) == 0:
		return nil, "does not run `vite build` directly or through a supported package-manager wrapper"
	default:
		return builds[0], ""
	}
}

func viteCall(call *syntax.CallExpr) (args []string, build, unknown bool) {
	if len(call.Args) == 0 {
		return nil, false, true // e.g. an assignment that changes subsequent commands
	}
	words := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		word, ok := literalShellWord(arg.Parts, false)
		if !ok {
			return nil, false, true
		}
		words = append(words, word)
	}
	unknown = slices.Contains([]string{"cd", "pushd", "popd", "eval", "source", "."}, words[0])

	// Wrappers without workspace or cwd flags; the latter would change the
	// configuration being linted. Additional wrappers or flags are treated
	// as not determinable, never matched by a token elsewhere in the line.
	switch words[0] {
	case "npx":
		words = words[1:]
		for len(words) > 0 && (words[0] == "--yes" || words[0] == "-y" || words[0] == "--no-install") {
			words = words[1:]
		}
	case "npm":
		if len(words) < 3 || words[1] != "exec" || words[2] != "--" {
			return nil, false, true
		}
		words = words[3:]
	case "pnpm", "yarn":
		words = words[1:]
		if len(words) > 0 && (words[0] == "exec" || words[0] == "run") {
			words = words[1:]
		}
	}
	if len(words) > 0 && words[0] == "--" {
		words = words[1:]
	}
	if len(words) < 2 || path.Base(words[0]) != "vite" || words[1] != "build" {
		return nil, false, unknown
	}
	return words[2:], true, unknown
}

// literalShellWord keeps quoting but rejects expansion, escapes and globs.
// A flags value containing spaces in quotes remains one argument.
func literalShellWord(parts []syntax.WordPart, quoted bool) (string, bool) {
	var out strings.Builder
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if strings.Contains(p.Value, `\`) || (!quoted && strings.ContainsAny(p.Value, "*?[~{}")) {
				return "", false
			}
			out.WriteString(p.Value)
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			out.WriteString(p.Value)
		case *syntax.DblQuoted:
			inner, ok := literalShellWord(p.Parts, true)
			if p.Dollar || !ok {
				return "", false
			}
			out.WriteString(inner)
		default:
			return "", false
		}
	}
	return out.String(), true
}
