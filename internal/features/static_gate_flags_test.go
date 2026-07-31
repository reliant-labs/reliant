// Copyright (c) 2025 Reliant Labs
package features

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStaticGateFlagsHaveAReader holds the static provider's GATE flags to the
// one thing that makes a gate real: something has to evaluate it.
//
// A flag named `<subsystem>_enabled` / `<subsystem>_disabled` is not a note —
// it is a claim about what the product does. `"skills_enabled": false` sat in
// the defaults with no evaluator anywhere in the repo while skills were wired
// unconditionally: the tool was registered, the preload seeded bodies into
// every workflow turn, and the settings APIs answered. So the declaration said
// OFF, the product was ON, and two docs repeated the declaration. A reader who
// trusts a flag over the code is behaving correctly; the flag was the defect.
//
// The remedy for a gate nobody reads is to DELETE it, never to add a reader
// that switches off a shipped feature. Both halves of this guard are derived:
// the gate keys come from the defaults map itself (a rename or a new flag is
// picked up with no edit here), and a reader is any evaluation of that key in
// the module's own Go sources outside the package that declares it.
func TestStaticGateFlagsHaveAReader(t *testing.T) {
	// A gate key is one whose NAME promises a switch. `new_chat_ui` and
	// friends are inert defaults — they assert nothing about a subsystem
	// that exists — and are deliberately out of scope.
	var gates []string
	for key := range staticFlagDefaults {
		if strings.HasSuffix(key, "_enabled") || strings.HasSuffix(key, "_disabled") {
			gates = append(gates, key)
		}
	}
	if len(gates) == 0 {
		t.Fatal("derived NO gate flags from the static defaults map — either every flag was " +
			"renamed out of the `_enabled`/`_disabled` shape or the map moved, and this guard " +
			"is now asserting about nothing")
	}

	root := moduleRoot(t)
	selfDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve package dir: %v", err)
	}

	for _, key := range gates {
		if readers := goFilesMentioning(t, root, selfDir, strconv.Quote(key)); len(readers) == 0 {
			t.Errorf("feature flag %q is declared in the static defaults and evaluated NOWHERE. "+
				"A gate flag with no reader states that a subsystem is switched by it when nothing "+
				"switches — delete the declaration (and every doc line repeating it) rather than "+
				"writing a reader that turns a shipped feature off", key)
		}
	}
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod. Derived rather than hardcoded so the scan below cannot
// quietly start looking at nothing when a package moves.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked to the filesystem root without finding go.mod — the reader scan " +
				"below would have searched nothing")
		}
		dir = parent
	}
}

// goFilesMentioning returns the module's Go files (excluding skipDir, the
// package that DECLARES the flags) that contain needle.
func goFilesMentioning(t *testing.T, root, skipDir, needle string) []string {
	t.Helper()
	var hits []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "gen", "dist", "build":
				return filepath.SkipDir
			}
			if path == skipDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		scanned++
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), needle) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan module sources: %v", err)
	}
	if scanned == 0 {
		t.Fatalf("scanned NO Go files under %s — a zero-readers verdict from an empty scan is "+
			"not evidence", root)
	}
	return hits
}
