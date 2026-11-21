package scaffold

import (
	"strings"
	"testing"
)

// A worker or library named main is Go package main, which internal/app (or
// any caller) cannot import, so the project could never build. Both are
// refused before anything is written, saying why.
func TestScaffold_RefusesComponentNamedMain(t *testing.T) {
	for kind, run := range map[string]func() error{
		"worker":  func() error { return runWorker(nil, "main", "", "", true) },
		"library": func() error { return runLibrary("main", "", false, false) },
	} {
		err := run()
		if err == nil {
			t.Errorf("forge scaffold %s main: want an error, got nil", kind)
			continue
		}
		if !strings.Contains(err.Error(), "package main") {
			t.Errorf("forge scaffold %s main: error does not name package main: %v", kind, err)
		}
	}
}
