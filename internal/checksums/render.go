// Side-render bookkeeping.
//
// Two consumers remain now that the fork lifecycle is gone:
//
//   - `forge generate --explain-drift` parks transient fresh renders
//     under `.forge/render/<relpath>` so the post-pipeline diff can show
//     "on-disk vs what regeneration would produce" without touching the
//     user's drifted file (WriteSideRenderNoBase).
//   - `forge unfork --merge` — the one-release migration aid for LEGACY
//     forks — three-way-merges ours/base/theirs from
//     `.forge/render/<relpath>` and `.forge/render-base/<relpath>`,
//     renders parked by pre-disown forge versions. Current forge never
//     writes render-base; the constants survive only so the migration
//     tool can find what older versions left behind.
//
// Both directories sit under `.forge/`, which the scaffolded project
// .gitignore already excludes (only disowned.json / hashes.json /
// scaffolded.json are negated back in) — side renders are per-developer
// state, never committed.
package checksums

import (
	"os"
	"path"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Side-render directory roots, project-relative. Exposed as constants
// so the cli layer can print them in messages without re-deriving.
const (
	// RenderDir holds transient fresh renders: written by
	// `--explain-drift` for diffing, and (legacy) the per-run "theirs"
	// renders older forge versions parked for forked paths.
	RenderDir = ".forge/render"
	// RenderBaseDir holds the merge base older forge versions captured
	// when a path was forked. Read-only in current forge — consumed by
	// `forge unfork --merge` (legacy-fork migration aid) only.
	RenderBaseDir = ".forge/render-base"
)

// SideRenderRelPath returns the project-relative location of the latest
// side render for relPath (`.forge/render/<relpath>`).
func SideRenderRelPath(relPath string) string {
	return path.Join(RenderDir, relPath)
}

// WriteSideRenderNoBase writes `.forge/render/<relpath>` — the
// transient render parked by the `--explain-drift` redirect so the
// post-pipeline diff has a "fresh render" side without touching the
// user's drifted file. No merge-base is ever seeded (that was fork-era
// machinery).
func WriteSideRenderNoBase(root, relPath string, content []byte) error {
	renderPath := filepath.Join(root, RenderDir, relPath)
	if err := os.MkdirAll(filepath.Dir(renderPath), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(renderPath, content, 0o644)
}

// CleanSideRenders removes both side-render files for relPath. Called
// when a path is disowned, re-adopted, or migrated off the legacy fork
// state — parked renders are stale once ownership is settled. Missing
// files are fine; any other removal error is returned.
func CleanSideRenders(root, relPath string) error {
	for _, p := range []string{
		filepath.Join(root, RenderDir, relPath),
		filepath.Join(root, RenderBaseDir, relPath),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
