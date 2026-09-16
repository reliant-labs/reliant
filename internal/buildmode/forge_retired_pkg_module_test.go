// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRetiredForgePkgModuleIsGone keeps github.com/reliant-labs/forge/pkg out
// of this module's dependency graph.
//
// It replaced TestForgeModulePinsMatch, which kept forge and forge/pkg pinned
// to the SAME version because they were two modules tagged from one commit, and
// a split pin linked two different builds of one codebase into one binary.
// forge merged them, so there is no second pin left to split — but the merge
// put a sharper hazard in its place.
//
// BOTH modules can serve github.com/reliant-labs/forge/pkg/* import paths: the
// merged forge carries pkg/ as a directory, and the retired submodule was that
// directory. A graph holding both answers every such import with
//
//	ambiguous import: found package github.com/reliant-labs/forge/pkg/observe
//	in multiple modules
//
// once per import, naming no cause and no fix. reliant imports forge/pkg/* from
// shipping code (internal/grpc/server.go's observe, internal/llm/tools'
// components), so this is not hypothetical.
//
// It is easy to reintroduce, which is why this is a test and not a one-time
// migration note: `go mod tidy` can satisfy a forge/pkg/* import from the
// published submodule all by itself, and any dependency that has not yet moved
// to the merged forge drags the retired module back in transitively.
func TestRetiredForgePkgModuleIsGone(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	// The matched text IS the offending line, so quote that rather than
	// locating it again: `^\s*` swallows the preceding newline, which makes an
	// index-based line lookup point at the blank line before the require and
	// print nothing where the evidence should be.
	direct := regexp.MustCompile(`(?m)^[\t ]*(?:require[\t ]+)?github\.com/reliant-labs/forge/pkg[\t ]+v\S+`)
	if line := direct.FindString(string(data)); line != "" {
		t.Errorf("go.mod requires the retired module github.com/reliant-labs/forge/pkg:\n"+
			"    %s\n\n"+
			"It was merged into github.com/reliant-labs/forge, and both provide the same\n"+
			"forge/pkg/* import paths — so this makes every one of them ambiguous and\n"+
			"nothing builds. Import paths did NOT change; only the require line did:\n"+
			"    go mod edit -droprequire=github.com/reliant-labs/forge/pkg\n"+
			"    go mod tidy",
			strings.TrimSpace(line))
	}

	// The transitive case, which the go.mod text cannot show. Only the
	// toolchain knows, and a toolchain that will not answer is not this
	// test's problem — `go build` still fails on the ambiguity, just without
	// naming the cause.
	cmd := exec.Command("go", "list", "-m", "github.com/reliant-labs/forge/pkg")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// "not a known dependency" is exactly the state we want, and the go
		// command reports it as an error — so a failure here is the PASS case.
		return
	}
	if v := strings.TrimSpace(string(out)); v != "" {
		t.Errorf("the retired module is in the build list via a dependency:\n"+
			"    %s\n\n"+
			"Nothing in this repo can resolve that — a `replace` would only hide the\n"+
			"ambiguity. Find who pulls it in and move that dependency to the merged\n"+
			"forge first:\n"+
			"    go mod why -m github.com/reliant-labs/forge/pkg", v)
	}
}
