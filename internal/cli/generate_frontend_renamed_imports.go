package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The frontend Tier-1 `_gen` rename, from the importer's side.
//
// Renaming a generated Go file is invisible to the rest of the package: the
// package is the unit, and nothing names the file. In TypeScript the FILENAME
// IS THE MODULE SPECIFIER, so the same rename breaks every import of it — and
// the importers here are the ones forge does not own:
//
//	src/lib/connect.ts     — scaffold-once; requires "@/lib/mock-transport"
//	src/app/providers.tsx  — scaffold-once; imports "@/lib/otel"
//	src/mocks/scenarios/*  — the user's own scenarios import "../scenario-types"
//	src/hooks/*.test.tsx   — scaffold-once; imports "./<svc>-service-hooks"
//	src/app/**/page.tsx    — scaffold-once; imports "@/hooks/<svc>-service-hooks"
//
// Retiring the old generated FILE without repointing these would leave a
// project that does not compile: the module the import names is simply gone.
// So the retirement sweep (RetireRenamedGenerated) handles forge's own files
// and this handles the references to them.
//
// WHAT IT WILL AND WILL NOT TOUCH. It rewrites exactly one thing: the
// specifier STRING of an import that names a module forge just renamed. It
// does not reformat, does not touch imports of anything else, and does not
// look at a file that does not contain one of these specifiers. A user who
// rewrote their connect.ts entirely still gets their `require()` repointed,
// because the alternative — leaving it dangling — is not a preservation of
// their work, it is a broken build.
//
// It is idempotent by construction: after the rewrite no old specifier
// remains, so a second run finds nothing and writes nothing. That is what
// keeps `forge generate` twice-in-a-row clean.

// renamedFrontendModule is one old→new module specifier pair.
type renamedFrontendModule struct {
	old string
	new string
}

// staticRenamedFrontendModules are the renames that are the same in every
// project. The per-service hooks modules are derived per frontend, since
// their names come from the service set.
var staticRenamedFrontendModules = []renamedFrontendModule{
	{old: "@/lib/mock-transport", new: "@/lib/mock-transport_gen"},
	{old: "@/lib/otel", new: "@/lib/otel_gen"},
	{old: "../scenario-types", new: "../scenario-types_gen"},
	{old: "./scenario-types", new: "./scenario-types_gen"},
	{old: "../mocks/scenarios", new: "../mocks/scenarios/index_gen"},
	{old: "@/mocks/scenarios", new: "@/mocks/scenarios/index_gen"},
}

// rewriteRenamedFrontendImports repoints imports of the frontend Tier-1
// modules that gained a `_gen` suffix, for every frontend the project
// declares.
//
// Best-effort and non-fatal: a frontend directory that cannot be walked is
// skipped. A project scaffolded after the rename has no old specifiers, so
// this is a no-op walk — the common case.
func rewriteRenamedFrontendImports(cfg *config.ProjectConfig, projectDir string, hookModules []renamedFrontendModule) {
	if cfg == nil {
		return
	}
	renames := append(append([]renamedFrontendModule(nil), staticRenamedFrontendModules...), hookModules...)

	var touched []string
	for _, fe := range cfg.Frontends {
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			// No directory in this repository — a cross-repo
			// source pin, or a path outside the project root.
			continue
		}
		srcDir := filepath.Join(projectDir, feDir, "src")
		if _, err := os.Stat(srcDir); err != nil {
			continue
		}
		_ = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // an unreadable subtree is skipped, not fatal
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "gen" {
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(path) {
			case ".ts", ".tsx":
			default:
				return nil
			}
			if rewriteRenamedImportsInFile(path, renames) {
				rel, relErr := filepath.Rel(projectDir, path)
				if relErr != nil {
					rel = path
				}
				touched = append(touched, filepath.ToSlash(rel))
			}
			return nil
		})
	}

	if len(touched) > 0 {
		fmt.Printf("  ♻️  repointed %d import(s) to the renamed *_gen frontend modules\n", len(touched))
		for _, rel := range touched {
			fmt.Printf("      - %s\n", rel)
		}
	}
}

// rewriteRenamedImportsInFile rewrites the specifier strings in one file.
// Returns true when the file changed.
//
// The match is on the QUOTED specifier (`"@/lib/otel"`, `'@/lib/otel'`), not
// on the bare path, which is what keeps it from corrupting a longer specifier
// that merely starts with the same characters: `"@/lib/otel"` must be
// rewritten while `"@/lib/otel-custom"` — a module the user wrote — must not.
// The closing quote in the pattern is the whole guard.
//
// A file that carries a forge:hash marker is RE-CERTIFIED before it is
// written, because this pass edits forge's own Tier-1 output as well as the
// user's files. The importers listed at the top of this file are the
// user-owned ones, but Tier-1 renders import renamed modules too
// (mock-transport_gen.ts names the scenarios barrel; the scenarios barrel
// names scenario-types), and an emitter does not always rewrite them first:
// a pristine render of an OLDER vintage is deliberately left alone
// (checksums.AutoHeal is off by default), and a narrowed `--force` scope
// skips everything the drift guard did not name. In those windows this pass
// is the only writer. Changing the bytes while leaving the old hash in place
// makes the file claim a digest it no longer has, so the NEXT run's Tier-1
// stomp guard reports forge's own output as hand-edited and aborts — the
// state a project upgrading across the `_gen` rename got stuck in, which
// `--force` could not clear because each run re-created it.
//
// Only an already-marked file is stamped. An unmarked file is the user's,
// and inventing a marker there would claim forge authorship of a
// scaffold-once file and hand it to the stomp guard to police.
func rewriteRenamedImportsInFile(path string, renames []renamedFrontendModule) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	updated := body
	for _, r := range renames {
		for _, quote := range []string{`"`, `'`} {
			from := []byte(quote + r.old + quote)
			to := []byte(quote + r.new + quote)
			updated = bytes.ReplaceAll(updated, from, to)
		}
	}
	if bytes.Equal(updated, body) {
		return false
	}
	updated = restampRepointedRender(path, body, updated)
	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	// Journaled: a run that fails later restores the import it repointed.
	checksums.RecordPreWriteAbs(path)
	if err := atomicfile.WriteFile(path, updated, mode); err != nil {
		return false
	}
	return true
}

// restampRepointedRender re-certifies `updated` when `before` was a file
// forge had certified — so the embedded hash covers the bytes this pass is
// about to write rather than the ones it replaced.
//
// It re-stamps ONLY a file whose marker verified BEFORE the repoint. That
// condition is the whole safety argument, and it is why the decision is made
// from `before` rather than from the marker's mere presence:
//
//   - PRISTINE before → the file was forge's own render, this pass made the
//     one edit, and the new hash certifies a render forge still owns.
//   - MODIFIED before → the user hand-edited a Tier-1 file. That drift is the
//     stomp guard's to report, and re-stamping here would erase the evidence
//     and silently bless the edit. Their file is repointed (a dangling import
//     is not a preservation of anyone's work) but left uncertified, so the
//     guard still sees it.
//   - NO MARKER → not forge's file at all. Left alone.
//
// A path whose format cannot carry a marker keeps whatever bytes it had; the
// scoped .forge/hashes.json fallback covers those, and this pass does not
// touch that ledger.
func restampRepointedRender(path string, before, updated []byte) []byte {
	if checksums.Verify(before) != checksums.Pristine {
		return updated
	}
	restamped, ok := checksums.Stamp(filepath.ToSlash(path), updated)
	if !ok {
		return updated
	}
	return restamped
}

// hookModuleRenames derives the per-service hook module renames
// (`@/hooks/<svc>-service-hooks` → `@/hooks/<svc>-service-hooks_gen`, and the
// `./`-relative form the co-located starter test uses) from the service set.
//
// Derived rather than pattern-matched on purpose: only a module forge
// actually renamed may be rewritten, and the service set is what says which
// those are. A `-hooks` module the user wrote themselves is left alone.
func hookModuleRenames(serviceHookFiles []string) []renamedFrontendModule {
	out := make([]renamedFrontendModule, 0, len(serviceHookFiles)*2)
	for _, file := range serviceHookFiles {
		base := strings.TrimSuffix(file, ".ts")
		legacy := strings.TrimSuffix(base, "_gen")
		if legacy == base {
			continue // not a renamed module
		}
		for _, prefix := range []string{"@/hooks/", "./"} {
			out = append(out, renamedFrontendModule{
				old: prefix + legacy,
				new: prefix + base,
			})
		}
	}
	return out
}
