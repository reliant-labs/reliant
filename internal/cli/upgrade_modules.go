package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/checksums"
)

// upgradeForgeModules moves the project's own requirements, not its workspace
// build list. GOWORK=off keeps external workspace members untouched; each
// module's replace directives remain authoritative. Never run `go work sync`.
// This phase precedes template/codemod writes, so a failed subprocess can be
// rolled back without leaving a partially upgraded project behind.
func upgradeForgeModules(root, target string, check bool, run goOffWorkspaceRunner) (retErr error) {
	if target == "" || target == "dev" || target == "(devel)" {
		return nil
	}
	target = "v" + strings.TrimPrefix(target, "v")
	if !semver.IsValid(target) {
		return fmt.Errorf("upgrade forge modules: target %q is not a Go module version", target)
	}
	modules, err := projectForgeModules(root)
	if err != nil {
		return err
	}
	var pending []string
	for _, m := range modules {
		if m.version != target {
			fmt.Printf("  %s: %s → %s\n", filepath.ToSlash(filepath.Join(m.dir, "go.mod")), m.version, target)
			pending = append(pending, m.dir)
		}
	}
	if check || len(pending) == 0 {
		return nil
	}
	if checksums.RollbackEnabled() {
		return fmt.Errorf("upgrade forge modules: another write journal is active")
	}
	// Capture ALL discovered modules: get/tidy may update a local dependency's
	// sum file, including a module whose forge requirement was already current.
	checksums.BeginRollbackJournal(root)
	defer func() {
		if retErr == nil {
			checksums.CommitRollback()
			return
		}
		_, failed := checksums.RestoreRollbackReport(root)
		if len(failed) > 0 {
			retErr = fmt.Errorf("%w\nRollback incomplete; inspect: %s", retErr, strings.Join(failed, ", "))
		} else {
			retErr = fmt.Errorf("%w\nModule files restored; forge.yaml and templates were not upgraded", retErr)
		}
		retErr = fmt.Errorf("%w\n%s", retErr, upgradeModulesNext(pending, target))
	}()
	for _, m := range modules {
		for _, file := range []string{"go.mod", "go.sum"} {
			rel := filepath.Join(m.dir, file)
			// The shared journal is best-effort; refuse unreadable/non-regular files
			// here rather than allowing its fallback to delete an unreadable input.
			if err := upgradeModuleFileReadable(filepath.Join(root, rel)); err != nil {
				return err
			}
			checksums.CaptureExternalWrites(root, rel)
		}
	}
	// Get EVERY pin first: root and gen may depend on one another through local
	// replaces, and tidy must see their final requirements, not an old neighbor.
	for _, args := range [][]string{{"get", forgeModuleRequirePath + "@" + target}, {"mod", "tidy"}} {
		for _, dir := range pending {
			out, err := run(filepath.Join(root, dir), args...)
			if err != nil {
				return fmt.Errorf("upgrade incomplete: %s in %s: %w\n%s", strings.Join(append([]string{"go"}, args...), " "), dir, err, strings.TrimSpace(string(out)))
			}
		}
	}
	// MVS can select a newer version than requested; never announce an exact
	// upgrade that did not happen. Tidy may legitimately remove an unused require.
	after, err := projectForgeModules(root)
	if err != nil {
		return err
	}
	for _, m := range after {
		if m.version != target {
			return fmt.Errorf("upgrade incomplete: %s still requires forge %s, wanted %s", filepath.Join(m.dir, "go.mod"), m.version, target)
		}
	}
	fmt.Printf("Forge module requirements synchronized to %s.\n", target)
	return nil
}

type upgradeForgeModule struct{ dir, version string }

// Read manifests directly: module graph resolution would need the very network
// access whose failure --check must be able to diagnose. Stay inside the project,
// never follow symlinks or edit external go.work members, dependencies or fixtures.
func projectForgeModules(root string) ([]upgradeForgeModule, error) {
	var modules []upgradeForgeModule
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root {
				switch d.Name() {
				case "vendor", "node_modules", "testdata":
					return filepath.SkipDir
				}
				if strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() != "go.mod" {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("upgrade modules: %s is not a regular file", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := modfile.Parse(path, data, nil)
		if err != nil {
			return err
		}
		dir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		for _, r := range f.Require {
			if r.Mod.Path == forgeModuleRequirePath {
				modules = append(modules, upgradeForgeModule{dir, r.Mod.Version})
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover project forge requirements: %w", err)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].dir < modules[j].dir })
	return modules, nil
}

func upgradeModuleFileReadable(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("upgrade modules: %s is not a regular file", path)
	}
	_, err = os.ReadFile(path)
	return err
}

func upgradeModulesNext(dirs []string, target string) string {
	var next strings.Builder
	next.WriteString("Next: resolve the Go error, then re-run project upgrade; or run from the project root:")
	for _, dir := range dirs {
		fmt.Fprintf(&next, "\n  (cd %s && GOWORK=off go get %s && GOWORK=off go mod tidy)", shellQuote(dir), shellQuote(forgeModuleRequirePath+"@"+target))
	}
	if module.IsPseudoVersion(target) {
		next.WriteString("\nThis pseudo-version is fetchable only after its commit is pushed. For local development, bridge to its source instead (keep go.work machine-local):\n  go work use '<path-to-your-forge-checkout>'\n  (if no workspace exists, first run: go work init .)")
	}
	return next.String()
}
