package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CaseCollision reports a file already in dir whose name equals fileName
// case-insensitively but not exactly — `Button.tsx` when forge is about to
// write `button.tsx`.
//
// Such a pair is not two files on the filesystems that matter. macOS (APFS,
// HFS+) and Windows fold case, so the second write replaces the first, and
// the desktop app built on a Mac ships whichever landed last. On Linux the
// pair does coexist, and TypeScript refuses it instead: TS1261 "Already
// included file name differs from file name only in casing". reliant's web
// app has Button/Modal/Input/Card/Badge.tsx; forge's starter set installed
// button/modal/input/card/badge.tsx beside them, five TS1261s on the first
// generate. Every forge write of a component goes through this check.
//
// Returns the colliding name, or "" when there is none (an unreadable dir
// has no entries to collide with).
func CaseCollision(dir, fileName string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Name() != fileName && strings.EqualFold(e.Name(), fileName) {
			return e.Name()
		}
	}
	return ""
}

// CaseCollisionError is the refusal for one colliding component write.
func CaseCollisionError(dir, fileName, existing string) error {
	return fmt.Errorf("%s: not writing %s — %s is already there, and the two names differ only in case "+
		"(one file on macOS/Windows, TS1261 on Linux). Rename or remove %s, or install forge's component "+
		"into another directory (package.json \"forge\": {\"components_dir\": \"...\"})",
		dir, fileName, existing, existing)
}

// componentsDirFor resolves a frontend's components directory from its
// path relative to the frontend ("" = src/components/ui).
func componentsDirFor(frontendDir, componentsRel string) string {
	if componentsRel == "" {
		return filepath.Join(frontendDir, "src", "components", "ui")
	}
	return filepath.Join(frontendDir, filepath.FromSlash(componentsRel))
}
