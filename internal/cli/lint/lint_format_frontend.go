// File: internal/cli/lint/lint_format_frontend.go
//
// The frontend half of `forge lint`'s auto-fix-then-gate default: run each
// frontend's OWN prettier, in write mode, over the files the user owns.
//
// Why it exists: the auto-fix pre-pass promised that "mechanical formatting
// never surfaces as a gating error", but for TypeScript the only fixer was
// `eslint --fix` — and the scaffolded eslint config carries no formatting
// rules (formatting is prettier's job; the two fighting is the classic
// eslint+prettier failure). So nothing in `forge lint` ever formatted a
// frontend file. The scaffold's own pre-commit config runs prettier in CI,
// which made forge-scaffolded output (control-plane's
// git-credential-internal-service-hooks.test.tsx) fail a CI check that
// `forge lint` — run precisely to clear mechanical issues — could not fix.
//
// ── Scope: what forge does NOT own ────────────────────────────────────
//
// The Go pre-pass skips generated files because rewriting them only
// guarantees the next `forge generate` reverts them (see lint_format.go).
// The same holds here, for three classes of file:
//
//   - generated output: src/gen/ (buf's protobuf-es output), every *_gen.*
//     module, and any file carrying a "DO NOT EDIT" / @generated banner
//     (the hooks barrel, public/config.js);
//   - scaffold-until-touched files (nav.tsx, dashboard.tsx) while they are
//     still byte-for-byte what forge wrote: forge refreshes those until the
//     first edit, and a formatter rewrite would count as that edit and
//     freeze them;
//   - everything outside src/: tsconfig.json, package.json and the config
//     files are edited in place by forge or npm, and prettier owning them
//     too is a diff every tool fights over.
//
// prettier also honors the frontend's own .prettierignore (and .gitignore),
// so a project can exclude more without touching forge.

package lint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

// prettierExtensions are the source files the scaffolded pre-commit prettier
// hook checks (`files: \.(ts|tsx|js|jsx|json|md|yml|yaml|css)$`) narrowed to
// what lives under a frontend's src/.
var prettierExtensions = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".css": true, ".json": true,
}

// prettierBatch caps argv per prettier invocation, well under any platform's
// command-line limit.
const prettierBatch = 200

// prettierTimeout bounds one batch. Formatting a few hundred files is a
// couple of seconds; the bound keeps a wedged node from hanging `forge lint`.
const prettierTimeout = 2 * time.Minute

// formatFrontendTrees runs prettier --write over the user-owned sources of
// every frontend this project drives a toolchain for. Returns the
// project-relative paths it rewrote. A frontend with no installed prettier is
// skipped silently: eslint and the typecheck lane already report a missing
// node_modules as "could not run", and this pass is a fixer, not a gate.
func formatFrontendTrees(ctx context.Context, root string, cfg *config.ProjectConfig) ([]string, error) {
	var changed []string
	for _, fe := range frontendTypecheckTargets(cfg) {
		feDir := filepath.Join(root, fe.dir)
		bin := resolveLocalBin("prettier", feDir, root)
		if bin == "" {
			continue
		}
		files, err := ownedFrontendSources(root, feDir)
		if err != nil {
			return changed, fmt.Errorf("%s: %w", fe.name, err)
		}
		for start := 0; start < len(files); start += prettierBatch {
			end := min(start+prettierBatch, len(files))
			out, err := runPrettierWrite(ctx, bin, feDir, files[start:end])
			if err != nil {
				return changed, fmt.Errorf("%s: prettier: %w", fe.name, err)
			}
			for _, rel := range out {
				changed = append(changed, filepath.ToSlash(filepath.Join(fe.dir, rel)))
			}
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// ownedFrontendSources lists, relative to feDir, the files under feDir/src
// that the user owns — see the file header for what is excluded and why.
func ownedFrontendSources(root, feDir string) ([]string, error) {
	srcDir := filepath.Join(feDir, "src")
	if !dirExists(srcDir) {
		return nil, nil
	}
	var out []string
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "gen", ".next", testdataDir:
				return filepath.SkipDir
			}
			return nil
		}
		if !prettierExtensions[filepath.Ext(path)] || isGeneratedFrontendPath(path) {
			return nil
		}
		projectRel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // unreachable under a walk rooted inside root; skip rather than fail the fixer
		}
		if isTestdataPath(projectRel) {
			return nil
		}
		if checksums.ScaffoldIsPristine(root, projectRel) {
			return nil
		}
		if hasGeneratedBanner(path) {
			return nil
		}
		feRel, relErr := filepath.Rel(feDir, path)
		if relErr != nil {
			return nil //nolint:nilerr // as above
		}
		out = append(out, feRel)
		return nil
	})
	return out, err
}

// isGeneratedFrontendPath matches forge's generated-module naming: foo_gen.ts,
// foo_gen.test.ts and the like. The banner check catches the rest.
func isGeneratedFrontendPath(path string) bool {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.TrimSuffix(stem, ".test")
	return strings.HasSuffix(stem, "_gen")
}

// hasGeneratedBanner reports whether the file's head carries a
// generated-code marker. Only the first 1 KiB is read: every forge and buf
// banner is on the first lines.
func hasGeneratedBanner(path string) bool {
	f, err := os.Open(path) //nolint:gosec // path comes from a walk under the project root
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 1024)
	n, _ := f.Read(head)
	head = head[:n]
	return bytes.Contains(head, []byte("DO NOT EDIT")) || bytes.Contains(head, []byte("@generated"))
}

// runPrettierWrite formats files (relative to feDir) in place and returns the
// ones prettier rewrote. `--list-different --write` prints exactly those, on
// stdout at prettier's default log level — `--log-level warn` would silence
// that list too, and the pre-pass would format files while reporting none.
// Prettier exits 2 on a file it cannot parse; that is a hand edit the
// typecheck and eslint lanes will report with a real error, so it is not
// this fixer's failure — the files it could format are still formatted.
func runPrettierWrite(ctx context.Context, bin, feDir string, files []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, prettierTimeout)
	defer cancel()
	args := append([]string{"--list-different", "--write"}, files...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = feDir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ctx.Err() != nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			return nil, err
		}
	}
	var out []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// resolveLocalBin returns node_modules/.bin/<name> for feDir, searching feDir
// and each ancestor up to (and including) projectRoot — the same walk as
// resolveLocalTSC, and for the same reasons: never npx (downloads on miss),
// never PATH (a different version than the project pins).
func resolveLocalBin(name, feDir, projectRoot string) string {
	if runtime.GOOS == "windows" {
		name += ".cmd"
	}
	dir, err := filepath.Abs(feDir)
	if err != nil {
		return ""
	}
	root := projectRoot
	if abs, absErr := filepath.Abs(projectRoot); absErr == nil {
		root = abs
	}
	for {
		candidate := filepath.Join(dir, "node_modules", ".bin", name)
		if st, statErr := os.Stat(candidate); statErr == nil && !st.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir || dir == root {
			return ""
		}
		dir = parent
	}
}
