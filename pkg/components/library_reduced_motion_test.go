// Copyright (c) 2025 Reliant Labs
package components

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// unguardedAnimation matches an animate-* utility that is not itself a
// variant-prefixed class (e.g. the `motion-reduce:animate-none` counterpart).
var unguardedAnimation = regexp.MustCompile(`(^|[^:\w-])animate-[a-z]+`)

// TestLibraryAnimationsHonourReducedMotion: the library ships into every
// scaffolded frontend, where it cannot be patched downstream. Every animate-*
// class needs a motion-reduce: counterpart inside the same string literal
// (WCAG 2.3.3).
func TestLibraryAnimationsHonourReducedMotion(t *testing.T) {
	err := fs.WalkDir(componentsFS, "components", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tsx") {
			return err
		}
		data, readErr := componentsFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(data)
		for _, loc := range unguardedAnimation.FindAllStringIndex(src, -1) {
			start := strings.LastIndexAny(src[:loc[0]+1], "\"'`") + 1
			end := loc[1] + strings.IndexAny(src[loc[1]:], "\"'`")
			if !strings.Contains(src[start:end], "motion-reduce:") {
				line := strings.Count(src[:loc[0]], "\n") + 1
				t.Errorf("%s:%d: %q has no motion-reduce: counterpart in the same className string", path, line, strings.Trim(src[loc[0]:loc[1]], " \"'`"))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
