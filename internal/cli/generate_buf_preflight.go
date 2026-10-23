package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/cliutil"
)

// Pre-flight for the `local:` plugins of a buf template, run immediately
// before forge hands that template to `buf generate`.
//
// WHY. buf resolves a local plugin only when it reaches it, and a plugin that
// cannot start fails in its own words. reliant adopted forge in place, keeping
// its own buf.gen.yaml, which names
//
//	local: [node, tools/protoc-gen-es/node_modules/protoc-gen-es-v2.15.0/bin/protoc-gen-es]
//
// `npm ci` had never run in tools/protoc-gen-es, so generate printed a Node
// MODULE_NOT_FOUND stack trace from inside buf and rolled back. Nothing in it
// named the template, the plugin, or the one command that fixes it.
//
// WHEN. Right before buf runs, with the environment buf gets — not as an early
// pipeline step. Forge supplies some plugins itself between the start of a run
// and that moment: the go.mod-pinned protoc-gen-go / protoc-gen-connect-go are
// put on PATH by withPinnedTools, and a frontend's protoc-gen-es is
// npm-installed and retargeted by runBufGenerateTypeScript. An earlier check
// would have to predict both, and each misprediction is a false refusal. A
// refusal here still precedes everything buf would write, and the run's
// journal restores what earlier steps wrote.
//
// WHAT. Only what can be decided statically, mirroring buf, which runs
// exec.LookPath(argv[0]) from its working directory (the project root, for
// every invocation forge makes):
//
//   - argv[0]: a bare name must be on PATH; a name with a separator is a path
//     relative to the project root, and must be an executable file.
//   - `go tool <name>`: go.mod (or a module of the go.work in effect) must
//     declare a tool directive for <name>.
//   - `node <script>`: the script must exist, relative to the project root.
//
// Any other form — `go run pkg@v`, npx, an interpreter flag before the script
// — is resolved by that program at run time. Only its argv[0] is checked; the
// rest is not second-guessed.

// bufLocalPlugin is one `local:` plugin entry of a buf.gen.yaml.
type bufLocalPlugin struct {
	argv []string
	out  string
}

// bufTemplateLocalPlugins returns the v2 `local:` plugins a buf.gen.yaml body
// declares, `local:` being either a program (string) or an argv (list). Any
// other version, or a body that does not parse, yields nil: buf reports a
// malformed template better than a guess here would.
func bufTemplateLocalPlugins(body []byte) []bufLocalPlugin {
	var tmpl struct {
		Version string `yaml:"version"`
		Plugins []struct {
			Local any    `yaml:"local"`
			Out   string `yaml:"out"`
		} `yaml:"plugins"`
	}
	if yaml.Unmarshal(body, &tmpl) != nil || strings.TrimSpace(tmpl.Version) != "v2" {
		return nil
	}
	var out []bufLocalPlugin
	for _, p := range tmpl.Plugins {
		if argv := stringArgv(p.Local); len(argv) > 0 {
			out = append(out, bufLocalPlugin{argv: argv, out: p.Out})
		}
	}
	return out
}

// stringArgv reads a `local:` value: a string is a one-element argv, a list
// of strings an argv. Anything else is nil.
func stringArgv(v any) []string {
	switch v := v.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []any:
		argv := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil
			}
			argv = append(argv, s)
		}
		if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
			return nil
		}
		return argv
	}
	return nil
}

// bufPluginProblem is one local plugin that cannot run.
type bufPluginProblem struct {
	plugin bufLocalPlugin
	what   string
	fix    string
}

// preflightBufTemplate refuses, before buf runs, a template naming a local
// plugin that cannot start. label is the template as the user knows it
// (project-relative); body is the template buf will be handed — nil reads
// label from disk. env is the environment buf runs with (nil: this process's).
func preflightBufTemplate(projectDir, label string, body []byte, env []string) error {
	if body == nil {
		data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(label)))
		if err != nil {
			return nil // no template to check; buf reports a missing one itself
		}
		body = data
	}
	if env == nil {
		env = os.Environ()
	}
	var problems []bufPluginProblem
	for _, p := range bufTemplateLocalPlugins(body) {
		if prob := checkBufLocalPlugin(projectDir, p, env); prob != nil {
			problems = append(problems, *prob)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	var what, fix strings.Builder
	fmt.Fprintf(&what, "%s names %d local plugin(s) that cannot run, so `buf generate` was not started:", label, len(problems))
	for i, p := range problems {
		fmt.Fprintf(&what, "\n  - local: [%s] (out: %s): %s", strings.Join(p.plugin.argv, ", "), p.plugin.out, p.what)
		if i > 0 {
			fix.WriteString("\n  ")
		}
		fix.WriteString(p.fix)
	}
	fix.WriteString("\n  then re-run generate")
	return cliutil.UserErr(Name()+" generate (buf plugin pre-flight)", what.String(), "", fix.String())
}

// checkBufLocalPlugin resolves one plugin the way buf will, returning nil when
// it can start (or when its form cannot be decided statically).
func checkBufLocalPlugin(projectDir string, p bufLocalPlugin, env []string) *bufPluginProblem {
	prog := p.argv[0]
	if what, fix := checkProgram(projectDir, prog, bufEnvValue(env, "PATH")); what != "" {
		return &bufPluginProblem{plugin: p, what: what, fix: fix}
	}
	switch base := strings.TrimSuffix(filepath.Base(prog), ".exe"); {
	case base == "go" && len(p.argv) >= 3 && p.argv[1] == "tool" && !strings.HasPrefix(p.argv[2], "-"):
		if declared, known := goToolDeclared(projectDir, p.argv[2], env); known && !declared {
			return &bufPluginProblem{plugin: p,
				what: fmt.Sprintf("`go tool %s` finds no tool directive for %s in go.mod", p.argv[2], p.argv[2]),
				fix:  fmt.Sprintf("declare it: go get -tool %s", goToolPackageHint(p.argv[2])),
			}
		}
	case base == "node" && len(p.argv) >= 2 && !strings.HasPrefix(p.argv[1], "-"):
		script := p.argv[1]
		if _, err := os.Stat(projectPath(projectDir, script)); err != nil {
			return &bufPluginProblem{plugin: p,
				what: fmt.Sprintf("node script %s does not exist", script) + nodeModulesNote(projectDir, script),
				fix:  missingPathFix(projectDir, script),
			}
		}
	}
	return nil
}

// checkProgram resolves argv[0] as exec.LookPath would from projectDir with
// pathList as PATH, returning what is wrong and how to fix it ("" when it
// resolves). Windows is skipped: PATHEXT resolution is not reproduced here.
func checkProgram(projectDir, prog, pathList string) (what, fix string) {
	if runtime.GOOS == "windows" {
		return "", ""
	}
	if strings.ContainsRune(prog, '/') {
		p := projectPath(projectDir, prog)
		info, err := os.Stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Sprintf("%s does not exist", prog) + nodeModulesNote(projectDir, prog), missingPathFix(projectDir, prog)
		case err != nil:
			return fmt.Sprintf("%s cannot be read: %v", prog, err), fmt.Sprintf("make %s readable", prog)
		case info.IsDir():
			return fmt.Sprintf("%s is a directory, not a program", prog), "point local: at the plugin executable"
		case info.Mode().Perm()&0o111 == 0:
			return fmt.Sprintf("%s is not executable", prog), fmt.Sprintf("chmod +x %s", prog)
		}
		return "", ""
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			dir = "."
		}
		info, err := os.Stat(filepath.Join(projectPath(projectDir, dir), prog))
		if err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return "", ""
		}
	}
	return fmt.Sprintf("%s is not on PATH", prog), programInstallHint(prog)
}

// programInstallHint is the fix for a program missing from PATH.
func programInstallHint(prog string) string {
	for _, t := range requiredProtoTools {
		if t.Binary == prog {
			return fmt.Sprintf("run `%s tools install` (installs %s at the version go.mod pins)", Name(), prog)
		}
	}
	switch prog {
	case "go":
		return "install Go (https://go.dev/dl) and put it on PATH"
	case "node":
		return "install Node.js (https://nodejs.org) and put it on PATH"
	}
	return fmt.Sprintf("install %s and put it on PATH, or point local: at where it is", prog)
}

// projectPath resolves a path from a buf template against the project root,
// which is buf's working directory for every invocation forge makes.
func projectPath(projectDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projectDir, filepath.FromSlash(p))
}

// nodeModulesDir returns the package directory whose node_modules p is
// inside (project-relative, slash-separated; "." for the root), or ok=false
// when p is not under a node_modules directory.
func nodeModulesDir(p string) (string, bool) {
	clean := path.Clean(filepath.ToSlash(p))
	if path.IsAbs(clean) {
		return "", false
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if part == "node_modules" {
			if i == 0 {
				return ".", true
			}
			return strings.Join(parts[:i], "/"), true
		}
	}
	return "", false
}

// nodeModulesNote explains a missing path that lives in a node_modules tree.
func nodeModulesNote(projectDir, p string) string {
	pkg, ok := nodeModulesDir(p)
	if !ok {
		return ""
	}
	if _, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(pkg), "node_modules")); err != nil {
		return fmt.Sprintf(" — it is inside %s/node_modules, which is not installed", pkg)
	}
	return fmt.Sprintf(" — %s/node_modules is installed but does not contain it (a stale install, or a version the path does not name)", pkg)
}

// missingPathFix is the fix for a plugin path (or node script) that does not
// exist: install the package that provides it when it lives in a
// node_modules tree, else point the template at the real path.
func missingPathFix(projectDir, p string) string {
	pkg, ok := nodeModulesDir(p)
	if !ok {
		return fmt.Sprintf("create %s, or point the template at where the plugin actually is", p)
	}
	abs := filepath.Join(projectDir, filepath.FromSlash(pkg))
	if _, err := os.Stat(filepath.Join(abs, "package.json")); err != nil {
		return fmt.Sprintf("install the dependencies that provide %s, or point the template at where the plugin actually is", p)
	}
	return fmt.Sprintf("run %#q in %s", jsInstallCommand(abs), pkg)
}

// jsInstallCommand is the lockfile-honouring install for a package directory.
func jsInstallCommand(dir string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	switch {
	case has("package-lock.json"), has("npm-shrinkwrap.json"):
		return "npm ci"
	case has("pnpm-lock.yaml"):
		return "pnpm install --frozen-lockfile"
	case has("yarn.lock"):
		return "yarn install --frozen-lockfile"
	case has("bun.lockb"), has("bun.lock"):
		return "bun install --frozen-lockfile"
	}
	return "npm install"
}

// goBuiltinTools are the tools `go tool` runs from the toolchain itself,
// with no tool directive.
var goBuiltinTools = map[string]bool{
	"addr2line": true, "asm": true, "buildid": true, "cgo": true, "compile": true, "covdata": true,
	"cover": true, "dist": true, "distpack": true, "doc": true, "fix": true, "link": true, "nm": true,
	"objdump": true, "pack": true, "pprof": true, "preprofile": true, "test2json": true, "trace": true,
	"vet": true,
}

// goToolDeclared reports whether `go tool name`, run in dir with env, has a
// tool directive to run. known is false when that cannot be decided
// statically (-modfile in GOFLAGS, an unreadable go.mod or go.work), and the
// caller then leaves it to buf.
//
// In workspace mode the tools of every workspace module are candidates, so
// the go.work in effect (GOWORK, else the nearest go.work above dir) is read
// too.
func goToolDeclared(dir, name string, env []string) (declared, known bool) {
	if goBuiltinTools[name] {
		return true, true
	}
	if strings.Contains(bufEnvValue(env, "GOFLAGS"), "-modfile") {
		return false, false
	}
	tools, ok := goModTools(filepath.Join(dir, "go.mod"))
	if !ok {
		return false, false
	}
	if toolListMatches(tools, name) {
		return true, true
	}
	work := bufEnvValue(env, "GOWORK")
	switch {
	case work == "off":
		return false, true
	case work == "":
		work = findGoWork(dir)
		if work == "" {
			return false, true
		}
	}
	data, err := os.ReadFile(work)
	if err != nil {
		return false, false
	}
	wf, err := modfile.ParseWork(work, data, nil)
	if err != nil {
		return false, false
	}
	for _, u := range wf.Use {
		modDir := u.Path
		if !filepath.IsAbs(modDir) {
			modDir = filepath.Join(filepath.Dir(work), modDir)
		}
		if t, ok := goModTools(filepath.Join(modDir, "go.mod")); ok && toolListMatches(t, name) {
			return true, true
		}
	}
	return false, true
}

// goModTools returns the tool directives of a go.mod.
func goModTools(path string) ([]string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		return nil, false
	}
	out := make([]string, 0, len(f.Tool))
	for _, t := range f.Tool {
		out = append(out, t.Path)
	}
	return out, true
}

// majorVersionElem matches a module major-version path element (v2, v3, …).
var majorVersionElem = regexp.MustCompile(`^v[0-9]+$`)

// toolListMatches reports whether `go tool name` selects one of tools: by
// full package path, or by the binary name `go install` would give it (the
// last path element, skipping a major-version suffix).
func toolListMatches(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
		elems := strings.Split(t, "/")
		last := elems[len(elems)-1]
		if majorVersionElem.MatchString(last) && len(elems) > 1 {
			last = elems[len(elems)-2]
		}
		if last == name {
			return true
		}
	}
	return false
}

// goToolPackageHint names the package to `go get -tool` for a missing tool.
func goToolPackageHint(name string) string {
	for _, t := range requiredProtoTools {
		if t.Binary == name {
			return t.Module
		}
	}
	return "<module>/cmd/" + name
}

// findGoWork returns the go.work the go command would use from dir — the
// nearest one in dir or above — or "".
func findGoWork(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		p := filepath.Join(abs, "go.work")
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}

// bufEnvValue returns key's value in env (the last assignment wins, as it does
// for exec.Cmd), or "".
func bufEnvValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if rest, ok := strings.CutPrefix(kv, key+"="); ok {
			v = rest
		}
	}
	return v
}
