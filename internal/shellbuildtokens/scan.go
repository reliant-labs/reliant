package shellbuildtokens

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ScanKCLTree walks dir for `.k` files and reports retired tokens in every
// ShellBuild `cmd` it can read. Findings carry the file and line so `forge
// lint` can point at the declaration.
//
// Scanning SOURCE rather than a render is the right surface here, not a
// compromise. In an ordinary KCL string `${TARGETARCH}` is already a compile
// error ("name 'TARGETARCH' is not defined"), so KCL itself refuses it and
// there is nothing for this rule to add. A token can only survive to the shell
// from a RAW string (r"...") or a backslash-escaped one, where KCL passes it
// through deliberately. Both of those are visible in the text, and a render
// would have to succeed first to show them — which is the wrong ordering for a
// lint that wants to explain the problem rather than report a KCL error.
//
// Directories that hold no hand-written project KCL are skipped: a vendored
// module copy would otherwise report findings the user cannot edit.
//
// A missing dir yields no findings and no error — a CLI or library project has
// no deploy/kcl tree and has nothing to check, which is not a failure.
func ScanKCLTree(dir string) ([]FileFinding, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	var out []FileFinding
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".forge", "vendor", ".forge-pkg":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".k" {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			rel = path
		}
		out = append(out, ScanSource(rel, string(raw))...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Token < out[j].Token
	})
	return out, nil
}

// FileFinding is a Finding located in a source file.
type FileFinding struct {
	Finding
	File string
	Line int
}

var (
	// shellBuildLiteral matches the opening of a ShellBuild literal, however
	// the module was imported (`forge.ShellBuild {`, `f.ShellBuild {`).
	shellBuildLiteral = regexp.MustCompile(`\bShellBuild\s*\{`)
	// cmdField captures a ShellBuild's cmd string, including the raw-string
	// (r"...") and triple-quoted forms — which are the ones that can carry a
	// token through KCL, so they are the ones that must be read.
	cmdField = regexp.MustCompile(`(?s)\bcmd\s*=\s*(r?"""(.*?)"""|r?"((?:[^"\\]|\\.)*)")`)
	// envKeyField captures the keys a build's env map declares, so a token the
	// user wired up deliberately is not reported.
	envKeyField = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_]*)"\s*=`)
	// nameField captures the enclosing workload's name, for the message.
	nameField = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
	// cmdIdentField captures a `cmd = <identifier>` — the command bound to a
	// variable rather than written inline. Real projects share one command
	// string across several workloads and envs this way, so this is the common
	// shape, not an exotic one.
	cmdIdentField = regexp.MustCompile(`\bcmd\s*=\s*([A-Za-z_][A-Za-z0-9_]*)\b`)
	// topLevelString captures `<ident> = "<string>"` at the top level of a file,
	// including the raw and triple-quoted forms — the declaration a
	// `cmd = <ident>` refers to.
	topLevelString = regexp.MustCompile(`(?ms)^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(r?"""(.*?)"""|r?"((?:[^"\\]|\\.)*)")`)
	// cmdNamedDeclaration captures any declaration whose IDENTIFIER says it
	// holds a build command — `_reliant_image_build_cmd = r"""…"""`, or a
	// `build_cmd = lambda -> str { r"""…""" }`.
	//
	// Matching on the name is what reaches the two shapes no brace-scoped scan
	// can: a command returned from a lambda, and one handed to a config-struct
	// field in a DIFFERENT file from the ShellBuild that eventually runs it.
	// Both are how control-plane writes every one of its `GOARCH=${TARGETARCH}`
	// builds — the single token that fails silently — so leaving them
	// unreachable would mean the rule covered only the findings that were going
	// to fail loudly anyway.
	//
	// It is deliberately keyed on `cmd` rather than on "any raw string": an
	// identifier ending in _cmd (or containing build_cmd) is the project
	// declaring what the string is FOR, and a rule that flagged every raw string
	// holding ${TAG} would hit SQL and docstrings and get ignored.
	cmdNamedDeclaration = regexp.MustCompile(`(?ms)^\s*([A-Za-z_][A-Za-z0-9_]*(?:_cmd|Cmd))\s*=\s*(?:lambda[^{]*\{\s*)?(r?"""(.*?)"""|r?"((?:[^"\\]|\\.)*)")`)
)

// commandStrings maps every top-level `<ident> = "<string>"` in a file to its
// body and the line it starts on.
//
// Only identifiers a ShellBuild actually names as its `cmd` are ever consulted
// (see ScanSource), which is what keeps this from reading every string in the
// file: a raw string holding SQL, a docstring example or a Dockerfile heredoc is
// collected here and then never looked at, because no build points at it. That
// distinction matters — a scanner that flagged any raw string containing ${TAG}
// would produce findings nobody can act on and teach people to ignore the rule.
func commandStrings(src string) map[string]struct {
	body string
	line int
} {
	out := map[string]struct {
		body string
		line int
	}{}
	for _, m := range topLevelString.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		body := ""
		if m[6] >= 0 {
			body = src[m[6]:m[7]] // triple-quoted
		} else if m[8] >= 0 {
			body = src[m[8]:m[9]] // single-quoted
		}
		out[name] = struct {
			body string
			line int
		}{body: body, line: 1 + strings.Count(src[:m[0]], "\n")}
	}
	return out
}

// ScanSource reports retired tokens in every ShellBuild literal in one KCL
// source file. file is used only for the finding's File field.
//
// The workload name is taken from the nearest `name = "..."` field BEFORE the
// ShellBuild literal, which is where it sits in every real declaration. An
// unnamed build reports as "<unnamed>" rather than being skipped: the file and
// line still locate it.
func ScanSource(file, src string) []FileFinding {
	var out []FileFinding
	declared := commandStrings(src)
	seenLine := map[int]bool{}

	// Declarations that NAME themselves a build command, wherever they sit. This
	// runs first and independently of any ShellBuild literal, because the
	// declaration and the build are routinely in different files.
	for _, m := range cmdNamedDeclaration.FindAllStringSubmatchIndex(src, -1) {
		cmd := ""
		if m[6] >= 0 {
			cmd = src[m[6]:m[7]]
		} else if m[8] >= 0 {
			cmd = src[m[8]:m[9]]
		}
		ident := src[m[2]:m[3]]
		line := 1 + strings.Count(src[:m[0]], "\n")
		seenLine[line] = true
		for _, f := range Check(ident, cmd, nil) {
			out = append(out, FileFinding{Finding: f, File: file, Line: line})
		}
	}
	for _, loc := range shellBuildLiteral.FindAllStringIndex(src, -1) {
		body, ok := kclBlockBody(src, loc[1])
		if !ok {
			continue
		}
		// The line to report: the ShellBuild literal for an inline cmd, or the
		// command string's own line when the cmd is bound to a variable — that
		// is where the edit has to happen, and one shared string may serve
		// several builds.
		line := 1 + strings.Count(src[:loc[0]], "\n")
		var cmd string
		if m := cmdField.FindStringSubmatch(body); m != nil {
			// Group 2 is the triple-quoted body, group 3 the single-quoted one.
			cmd = m[2]
			if cmd == "" {
				cmd = m[3]
			}
		} else if m := cmdIdentField.FindStringSubmatch(body); m != nil {
			// `cmd = <ident>`: resolve the identifier to its declaration.
			// Unresolvable (imported from another file, or built by an
			// expression) simply yields nothing rather than a guess.
			d, found := declared[m[1]]
			if !found {
				continue
			}
			cmd, line = d.body, d.line
		} else {
			continue
		}
		env := map[string]string{}
		if e := envMapBody(body); e != "" {
			for _, k := range envKeyField.FindAllStringSubmatch(e, -1) {
				env[k[1]] = ""
			}
		}
		name := "<unnamed>"
		if n := nameField.FindAllStringSubmatch(src[:loc[0]], -1); len(n) > 0 {
			name = n[len(n)-1][1]
		}
		// A cmd-named declaration already reported at this line — don't report it
		// twice just because a ShellBuild also points at it.
		if seenLine[line] {
			continue
		}
		for _, f := range Check(name, cmd, env) {
			out = append(out, FileFinding{Finding: f, File: file, Line: line})
		}
	}
	return out
}

// envMapBody returns the text of a ShellBuild's `env = { ... }` map, or "".
func envMapBody(body string) string {
	idx := regexp.MustCompile(`\benv\s*=\s*\{`).FindStringIndex(body)
	if idx == nil {
		return ""
	}
	inner, ok := kclBlockBody(body, idx[1])
	if !ok {
		return ""
	}
	return inner
}

// kclBlockBody returns the text between the `{` ending at open and its
// matching `}`, skipping braces inside string literals.
func kclBlockBody(src string, open int) (string, bool) {
	depth := 1
	inString := false
	for i := open; i < len(src); i++ {
		c := src[i]
		switch {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return src[open:i], true
			}
		}
	}
	return "", false
}
