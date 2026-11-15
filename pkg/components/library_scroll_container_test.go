// Copyright (c) 2025 Reliant Labs
package components

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var scrollClassName = regexp.MustCompile(`className=(?:"[^"]*"|\{` + "`" + `[^` + "`" + `]*` + "`" + `\})`)
var scrollUtility = regexp.MustCompile(`(^|[\s"{` + "`" + `])overflow-(y-|x-)?(auto|scroll)\b`)

// TestLayoutScrollContainersArePositioned: an unpositioned scroller is not the
// containing block for absolutely positioned descendants, so every sr-only
// span inside it resolves against an ancestor outside the scroller and
// stretches the page past the viewport. Every layout scroller must be
// `relative`.
func TestLayoutScrollContainersArePositioned(t *testing.T) {
	err := fs.WalkDir(componentsFS, "components/layouts", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tsx") {
			return err
		}
		data, readErr := componentsFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(data)
		for _, loc := range scrollClassName.FindAllStringIndex(src, -1) {
			attr := src[loc[0]:loc[1]]
			if scrollUtility.MatchString(attr) && !regexp.MustCompile(`[\s"{`+"`"+`]relative\b`).MatchString(attr) {
				line := strings.Count(src[:loc[0]], "\n") + 1
				t.Errorf("%s:%d: scroll container is not positioned (add `relative`): %s", path, line, attr)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
