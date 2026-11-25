package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// relocatePrivateDevLogRoute moves a Next.js frontend's dev-log receiver out
// of the App Router's private-folder namespace.
//
// Frontends scaffolded before the fix carry src/app/__forge/log/route.ts. The
// App Router excludes every `_`-prefixed folder from routing, so that route
// never served: POST /__forge/log answered 404 and no browser console line
// ever reached .forge/logs/<env>/frontend_<name>.log. The working spelling is
// src/app/%5F_forge/log/route.ts (`%5F` is Next's documented escape for a
// leading underscore in a URL segment), which serves the same /__forge/log
// path installDevLogging() posts to.
//
// The file is "yours" (scaffold-once), so this MOVES the user's bytes rather
// than re-rendering, and keeps the scaffold ledger honest across the move:
//
//   - old path absent               → nothing to do (never had it, or deleted
//     it to opt out — either way it stays gone).
//   - new path recorded but absent  → the user deleted the fixed copy to opt
//     out; do not resurrect it from the stale one. Say so and stop.
//   - new path present              → both spellings exist; the user resolved
//     it by hand. Leave both, name the dead one.
//   - otherwise                     → move it, re-key the birth record and
//     any disown record from the old path to the new.
func relocatePrivateDevLogRoute(projectDir, feDir, feName string, cs *checksums.FileChecksums) error {
	legacyDirRel := filepath.Join(feDir, "src", "app", "__forge")
	legacyRel := filepath.Join(legacyDirRel, "log", "route.ts")
	currentRel := filepath.Join(feDir, "src", "app", "%5F_forge", "log", "route.ts")
	legacy := filepath.Join(projectDir, legacyRel)
	current := filepath.Join(projectDir, currentRel)

	if _, err := os.Stat(legacy); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", legacyRel, err)
	}
	if _, err := os.Stat(current); err == nil {
		fmt.Printf("  ⚠️  frontend %s: both src/app/__forge/log/route.ts and src/app/%%5F_forge/log/route.ts exist; the __forge copy is never routed by Next.js — delete it\n", feName)
		return nil
	}
	if checksums.ScaffoldRecorded(projectDir, currentRel) {
		fmt.Printf("  ℹ️  frontend %s: src/app/__forge/log/route.ts is never routed by Next.js (`_` folders are private); you removed its working replacement src/app/%%5F_forge/log/route.ts, so forge leaves it — delete __forge/ to finish opting out\n", feName)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		return fmt.Errorf("mkdir for dev-log route: %w", err)
	}
	checksums.CarryDisownAcrossRename(projectDir, cs, legacyRel, currentRel)
	// Journaled on both ends: a run that fails later puts the route back
	// where it was.
	checksums.RecordPreWriteAbs(legacy)
	checksums.RecordPreWriteAbs(current)
	if err := os.Rename(legacy, current); err != nil {
		return fmt.Errorf("relocate dev-log route: %w", err)
	}
	checksums.RecordScaffold(projectDir, currentRel)
	checksums.ForgetScaffold(projectDir, legacyRel)
	// Remove the private folder only if the move emptied it — anything else
	// the user put under __forge/ is theirs.
	_ = os.Remove(filepath.Dir(legacy))
	_ = os.Remove(filepath.Join(projectDir, legacyDirRel))
	fmt.Printf("  ✅ frontend %s: moved src/app/__forge/log/route.ts → src/app/%%5F_forge/log/route.ts (Next.js never routes `_`-prefixed folders; browser logs now reach .forge/logs)\n", feName)
	return nil
}

// ensureViteQueryResourceHook writes src/hooks/use-query-resource.ts into a
// Vite SPA frontend when it is absent. The hook (the tristate adapter +
// useDebouncedValue) is the app-side adapter for the runtime's <Resource>
// container: it maps a React Query result onto the { status, data, error }
// shape <Resource> consumes. It ships in the shared static scaffold tree for
// new projects, so this only backfills a frontend that predates it.
// Emit-if-missing (never overwrite) so a hand-edited copy survives.
func ensureViteQueryResourceHook(projectDir, feDir string) error {
	destRel := filepath.Join(feDir, "src", "hooks", "use-query-resource.ts")
	dest := filepath.Join(projectDir, destRel)
	if _, err := os.Stat(dest); err == nil {
		return nil // present — leave it alone
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", destRel, err)
	}
	content, err := templates.FrontendTemplates().Get("shared/src/hooks/use-query-resource.ts")
	if err != nil {
		return fmt.Errorf("read use-query-resource: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", destRel, err)
	}
	checksums.RecordPreWriteAbs(dest) // journaled: a failed run removes it
	if err := atomicfile.WriteFile(dest, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", destRel, err)
	}
	return nil
}
