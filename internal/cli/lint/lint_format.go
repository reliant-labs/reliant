// File: internal/cli/lint/lint_format.go
//
// The deterministic-safe half of `forge lint`'s auto-fix-then-gate default.
//
// Forensics (long overnight builds): the dominant `forge lint` failures were
// all trivially mechanical — goimports/gofmt import-grouping and whitespace,
// re-introduced every time a file was hand-edited after `forge generate`.
// `forge lint --fix` existed but was opt-in and never used; agents instead
// hand-ran `gofmt -w`/`goimports -w` and hand-fixed every finding. Mechanical
// formatting has no business gating a build.
//
// formatGoTree applies the project's Go formatting IN WRITE MODE across its
// owned Go source before the gating pipeline runs, so import grouping and
// gofmt whitespace never surface as a gate. It is invoked by runAllLinters
// unless the caller opted out with `--no-fix` (or is in `--json` detect-only
// mode).
//
// WHICH formatting is the project's gate, read from its golangci-lint config
// (lint_format_standard.go): gofmt always, plus goimports' import grouping
// with exactly the local-prefixes the config declares when it enables
// goimports. A pre-pass that formatted to anything else would rewrite files
// the project's own gate accepts — reliant's incident, in that file's header.
//
// Why the in-process formatter (not the `goimports` binary): CanonicalGoSource
// is goimports' formatting engine in FormatOnly mode — byte-identical to what
// golangci-lint's `goimports` formatter gates on with the same local-prefixes
// (see internal/checksums/goformat.go). So the pre-pass fixes exactly what the
// gate would flag, needs no `goimports` on PATH, and is deterministic and
// hermetic. It is also idempotent: already-canonical files are a no-op and
// are never rewritten.
//
// ── Generated files are OUT of scope ──────────────────────────────────
//
// The pre-pass skips generated Go entirely (see isGeneratedGoPath). It used
// to walk gen/ too, and that made `forge lint` and `forge generate` disagree
// about the bytes of files neither of them gates on.
//
// Incident (control-plane, cutting v0.1.9): a bare `forge lint` left 35 files
// under gen/ modified — every .pb.go and .connect.go in the project. The next
// `forge generate` reverted all 35, because protoc-gen-go writes those files
// from scratch with its own import grouping, which is not goimports'. So the
// tree was dirty after whichever command ran last, and CI's "Verify Generated
// Code" job failed for whoever's local order differed.
//
// Formatting them was never buying anything: the scaffolded .golangci.yml
// excludes generated files from every linter — "(^|/)gen/", ".*_gen\.go$",
// ".*\.pb\.go$", ".*\.connect\.go$" — so there is no gate for the pre-pass to
// pre-empt on those paths. It was pure cost.
//
// This is a path rule, not a directory rule: a hand-owned file living beside
// a mock_gen.go is exactly the drift this pass exists to absorb, so only the
// generated file is skipped and its neighbours are still formatted.

package lint

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// goFormatDirs are the source trees the format pre-pass walks — the same set
// the generate pipeline's post-write goimports pass covers
// (runGoimportsOnGenerated). Walking whole trees (rather than only files
// forge wrote) is the point: it catches the owned/hand-edited files
// (compose.go after `forge project disown`, providers.go, contract bodies) that
// re-introduce formatting drift between generate runs.
// gen/ is deliberately absent: every Go file under it is generated, and
// rewriting generated output only guarantees the next `forge generate`
// reverts it (see the header). The remaining trees are walked because they
// MIX owned and generated files, which isGeneratedGoPath separates per file.
var goFormatDirs = []string{"cmd", "pkg", "internal"}

// isGeneratedGoPath reports whether relPath (slash-separated, project-
// relative) names Go source that a generator owns and rewrites wholesale.
//
// The set mirrors the generated-file exclusions in the scaffolded
// .golangci.yml one-for-one, and that correspondence is the whole argument:
// the pre-pass exists to fix what the gate would otherwise flag, so a path
// the gate never inspects is a path the pre-pass must not touch.
func isGeneratedGoPath(relPath string) bool {
	switch {
	case relPath == "gen" || strings.HasPrefix(relPath, "gen/"),
		strings.Contains(relPath, "/gen/"):
		return true
	case strings.HasSuffix(relPath, "_gen.go"),
		strings.HasSuffix(relPath, ".pb.go"),
		strings.HasSuffix(relPath, ".connect.go"):
		return true
	}
	return false
}

// formatGoTreeVerified is formatGoTreeFiltered with the type-check injected.
func formatGoTreeVerified(root string, keep func(rel string) bool, check goTypecheck) (changed []string, reverted []revertedGoFix, err error) {
	set := newGoFixSet(root)
	var written []string
	err = walkGoFormat(root, keep, func(rel, abs string, src, formatted []byte) error {
		mode := fs.FileMode(0o644)
		if info, statErr := os.Stat(abs); statErr == nil {
			mode = info.Mode().Perm()
		}
		if werr := set.write(abs, formatted, mode); werr != nil {
			return werr
		}
		written = append(written, rel)
		return nil
	})
	reverted = set.settle(check)
	for _, rel := range written {
		if set.applied(filepath.Join(root, filepath.FromSlash(rel))) {
			changed = append(changed, filepath.FromSlash(rel))
		}
	}
	return changed, reverted, err
}

// walkGoFormat visits every owned Go file under the project's source trees
// whose canonical formatting differs from what is on disk, handing visit the
// slash-separated project-relative path, the absolute path, and the current
// and canonical bytes. It writes nothing itself: the fixer rewrites in visit,
// and the report-only check turns the same pair into a diff, so the two can
// never disagree about which files are pending.
func walkGoFormat(root string, keep func(rel string) bool, visit func(rel, abs string, src, formatted []byte) error) error {
	std := projectGoFormatStandard(root)
	for _, d := range goFormatDirs {
		base := filepath.Join(root, d)
		if !dirExists(base) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, de fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if de.IsDir() {
				switch de.Name() {
				case "node_modules", "vendor", ".git", testdataDir:
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			slashRel := filepath.ToSlash(rel)
			if isTestdataPath(slashRel) || isGeneratedGoPath(slashRel) || (keep != nil && !keep(slashRel)) {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			formatted, ferr := std.format(path, src)
			if ferr != nil {
				// Unparseable (broken edit) — not this pass's job to fix;
				// leave it for the compiler / gate to report with a real
				// error.
				return nil //nolint:nilerr // intentional: skip, don't fail the pre-pass
			}
			if bytes.Equal(src, formatted) {
				return nil
			}
			return visit(slashRel, path, src, formatted)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// testdataDir is the directory name every lint rewrite leaves alone.
const testdataDir = "testdata"

// isTestdataPath reports whether the slash-separated path rel has a segment
// named testdata. No fixer — the Go format pass, its report-only diff, or the
// frontend prettier pass — rewrites or reports such a file: the go tool never
// builds it, and its fixtures are written as they are on purpose. An
// analyzer's `// want` lines are line-number sensitive, so regrouping the
// imports of forge's own internal/linter/slowtest/testdata/src/a/a_test.go
// broke the analyzer's tests. The walks also prune the directory; this is
// the per-file rule, so it holds however a path reaches the fixer.
func isTestdataPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == testdataDir {
			return true
		}
	}
	return false
}

// keepForPaths turns the positional Go package patterns of `forge lint
// [paths...]` into the file filter the format pre-pass honours. No paths, or a
// pattern naming the whole module (".", "./...", "..."), is the whole tree
// (nil). Otherwise only files under a named directory, or a named .go file,
// are rewritten: the paths exist to say "this is the part I am working on",
// and the pre-pass used to ignore them and reformat every package.
func keepForPaths(paths []string) func(rel string) bool {
	var prefixes []string
	for _, p := range paths {
		p = strings.TrimSuffix(filepath.ToSlash(filepath.Clean(strings.TrimSpace(p))), "/...")
		p = strings.TrimPrefix(p, "./")
		if p == "" || p == "." || p == "..." {
			return nil
		}
		prefixes = append(prefixes, p)
	}
	if len(prefixes) == 0 {
		return nil
	}
	return func(rel string) bool {
		for _, p := range prefixes {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
		return false
	}
}
