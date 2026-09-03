// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestForgeModulePinsMatch keeps github.com/reliant-labs/forge and
// github.com/reliant-labs/forge/pkg on the SAME version.
//
// The two are separate Go modules but one repository, tagged from one commit,
// so a version that exists for one exists for the other and they are only ever
// released together. Pinning them apart is real skew, not a cosmetic
// inconsistency: the CLI module is imported by shipping code
// (internal/grpc/services/chat_greenfield.go, internal/skills/catalog/forge.go,
// internal/toolexec/daemonruntime/forge_memory.go) while forge/pkg backs the
// runtime (internal/grpc/server.go's observe, internal/llm/tools' components).
// A split pin therefore links two different builds of one codebase into one
// binary, and the failure surfaces as a type or behavior mismatch far from the
// go.mod line that caused it.
//
// It is easy to do by accident: `go get github.com/reliant-labs/forge@vX` bumps
// exactly one of them and leaves the other where it was, with no error.
//
// This is a unit test rather than a CI-only script so it runs in the normal
// suite, where the person who made the split sees it immediately.
func TestForgeModulePinsMatch(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	cli := requireVersion(t, data, `(?m)^\s*github\.com/reliant-labs/forge\s+(v\S+)`, "github.com/reliant-labs/forge")
	pkg := requireVersion(t, data, `(?m)^\s*github\.com/reliant-labs/forge/pkg\s+(v\S+)`, "github.com/reliant-labs/forge/pkg")

	if cli != pkg {
		t.Errorf("forge module pins have split:\n"+
			"    github.com/reliant-labs/forge      %s\n"+
			"    github.com/reliant-labs/forge/pkg  %s\n\n"+
			"Both modules are tagged from the same forge commit and must be pinned to "+
			"the same version. `go get github.com/reliant-labs/forge@vX` bumps only one "+
			"of them — bump both:\n"+
			"    go get github.com/reliant-labs/forge@%s github.com/reliant-labs/forge/pkg@%s",
			cli, pkg, cli, cli)
	}
}

func requireVersion(t *testing.T, goMod []byte, pattern, module string) string {
	t.Helper()

	m := regexp.MustCompile(pattern).FindSubmatch(goMod)
	if m == nil {
		t.Fatalf("go.mod has no require for %s — both forge modules must be pinned "+
			"(TestForgeIsPinnedToAVersion covers why)", module)
	}
	return string(m[1])
}
