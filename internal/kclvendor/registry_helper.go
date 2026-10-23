package kclvendor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The forge KCL module used to ship `forge.registry(default)` — a lambda that
// returned `option("registry") or default`, so a registry could be overridden
// at render time with `-D registry=`. That made the registry something other
// than a declaration, which is the one thing it must be: the env's KCL states
// it, and nothing outranks the statement. The helper is gone; the registry is
// a literal in deploy/kcl/<env>/main.k.
//
// A project written against the old module calls the helper, and would fail to
// render with KCL's own "attribute 'registry' not found in module 'forge'".
// MigrateRegistryHelper rewrites each call to its literal argument — which is
// exactly what every such call evaluated to, since forge itself never bound
// the option — and CheckRegistryHelper makes a render of an unmigrated project
// fail with the command that fixes it. A project that WANTS a render-time
// override writes `option("registry") or "…"` itself: that is ordinary KCL,
// and forge gives it no special treatment either way.

// registryHelperCallRE matches a forge.registry(...) call whose argument is a
// single string literal (either quote style). Submatch 1 is the literal,
// quotes included.
var registryHelperCallRE = regexp.MustCompile(`\bforge\.registry\(\s*("[^"\\\n]*"|'[^'\\\n]*')\s*\)`)

// registryHelperAnyRE matches any remaining forge.registry( call.
var registryHelperAnyRE = regexp.MustCompile(`\bforge\.registry\(`)

// MigrateRegistryHelper rewrites every `forge.registry("<literal>")` call in
// the project's KCL (deploy/kcl/**/*.k) to `"<literal>"`, and returns the
// project-relative paths it changed. Comments are not code and are left
// alone. Idempotent. A call with a non-literal argument is left in place:
// CheckRegistryHelper reports it.
func MigrateRegistryHelper(projectDir string) ([]string, error) {
	var changed []string
	err := walkProjectKCL(projectDir, func(path string, lines []string) error {
		rewrote := false
		for i, line := range lines {
			code, comment := splitKCLComment(line)
			if !registryHelperCallRE.MatchString(code) {
				continue
			}
			lines[i] = registryHelperCallRE.ReplaceAllString(code, "$1") + comment
			rewrote = true
		}
		if !rewrote {
			return nil
		}
		// Journaled, so a `forge generate` that fails later rolls the edit
		// back instead of reporting an unchanged tree (#271).
		checksums.RecordPreWriteAbs(path)
		if err := atomicfile.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		changed = append(changed, projectRel(projectDir, path))
		return nil
	})
	sort.Strings(changed)
	return changed, err
}

// StaleRegistryHelperError reports KCL files that still call the retired
// forge.registry helper.
type StaleRegistryHelperError struct {
	Paths []string // project-relative
}

func (e *StaleRegistryHelperError) Error() string {
	verb := "calls"
	if len(e.Paths) > 1 {
		verb = "call"
	}
	return fmt.Sprintf(
		"%s still %s forge.registry(...), which the forge KCL module no longer provides.\n"+
			"    expected: the registry declared as a literal, e.g. `_registry = \"<registry>\"` or\n"+
			"              `registry = \"ghcr.io/acme\"` on the env's forge.ClusterTarget\n"+
			"    found:    a forge.registry(...) call (older forge scaffolds wrote one)\n"+
			"  Fix: run `forge generate` once — it rewrites every forge.registry(\"<literal>\") call to\n"+
			"  its literal. A call whose argument is not a string literal must be rewritten by hand.",
		strings.Join(e.Paths, ", "), verb)
}

// CheckRegistryHelper returns a StaleRegistryHelperError when any of the
// project's KCL still calls forge.registry. Every render calls it (beside
// CheckKclMods); it never edits anything.
func CheckRegistryHelper(projectDir string) error {
	var stale []string
	err := walkProjectKCL(projectDir, func(path string, lines []string) error {
		for _, line := range lines {
			if code, _ := splitKCLComment(line); registryHelperAnyRE.MatchString(code) {
				stale = append(stale, projectRel(projectDir, path))
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return &StaleRegistryHelperError{Paths: stale}
	}
	return nil
}

// walkProjectKCL calls fn with the lines of every .k file under
// <projectDir>/deploy/kcl. A project with no deploy/kcl is a no-op.
func walkProjectKCL(projectDir string, fn func(path string, lines []string) error) error {
	root := filepath.Join(projectDir, "deploy", "kcl")
	if _, err := os.Stat(root); err != nil {
		return nil //nolint:nilerr // no KCL tree: nothing to migrate or check
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".k" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return fn(path, strings.Split(string(data), "\n"))
	})
}

// splitKCLComment splits a line at its first `#` that is not inside a string
// literal, returning the code before it and the comment (with the `#`).
func splitKCLComment(line string) (code, comment string) {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return line[:i], line[i:]
		}
	}
	return line, ""
}

func projectRel(projectDir, path string) string {
	if rel, err := filepath.Rel(projectDir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}
