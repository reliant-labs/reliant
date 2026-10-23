package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// scaffoldFormatTimeout bounds one prettier invocation on a single file. A
// cold node start is well under a second; the bound exists so a wedged
// toolchain can never hang `forge generate`.
const scaffoldFormatTimeout = 30 * time.Second

// formatScaffoldedFrontendFile runs the frontend's OWN prettier over one file
// forge has just scaffolded, so a user-owned file is born in the shape the
// project's formatter — and therefore its CI's prettier check — expects.
//
// Scope is deliberately one file, at birth. A scaffold-once file is the
// user's from the moment it lands, so formatting it here is formatting
// forge's own output, never the user's edits; forge-regenerated (Tier-1)
// files are never passed in, because the generator owns their bytes and a
// formatter rewrite would only be reverted by the next `forge generate`.
//
// Best-effort and silent by design:
//
//   - The binary is resolved from node_modules/.bin, walking from the file's
//     directory up to (and including) the project root — the frontend's own
//     install, or a hoisted workspace one. Never npx and never PATH: npx
//     downloads on a miss, and a global prettier formats with a version the
//     project does not pin, which is worse than not formatting.
//   - No installed prettier (a fresh scaffold before `npm install`), a
//     failing run, or a timeout all leave the template's output in place.
//     That output is already prettier-clean at the scaffold's own printWidth
//     for ordinary names (TestHookStarterTest_PrettierClean); this pass
//     covers what a template cannot know — long names and a project's own
//     prettier config.
//   - prettier resolves the project's config and .prettierignore from the
//     file's location, so a project that ignores the path keeps it as
//     rendered.
func formatScaffoldedFrontendFile(path string) {
	bin := resolveFrontendPrettier(path)
	if bin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), scaffoldFormatTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--write", "--log-level", "silent", filepath.Base(path))
	cmd.Dir = filepath.Dir(path)
	_ = cmd.Run()
}

// resolveFrontendPrettier finds node_modules/.bin/prettier for the file at
// path, searching its directory and each ancestor up to the project root
// (the nearest directory holding forge.yaml). Returns "" when none is
// installed or the file is not inside a forge project.
func resolveFrontendPrettier(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	root := nearestProjectRoot(filepath.Dir(abs))
	if root == "" {
		return ""
	}
	bin := "prettier"
	if runtime.GOOS == "windows" {
		bin = "prettier.cmd"
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "node_modules", ".bin", bin)
		if st, statErr := os.Stat(candidate); statErr == nil && !st.IsDir() {
			return candidate
		}
		// Never above the project root: a prettier in some unrelated
		// ancestor of the checkout is not this project's formatter.
		if dir == root || filepath.Dir(dir) == dir {
			return ""
		}
	}
}

func nearestProjectRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, "forge.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// finalizeScaffoldTSX normalizes what no template can guarantee: the file
// ends in exactly one newline (a trailing {{end}} or blank line otherwise
// leaves extras that end-of-file-fixer and prettier both strip).
func finalizeScaffoldTSX(src []byte) []byte {
	return append(bytes.TrimRight(src, " \t\r\n"), '\n')
}
