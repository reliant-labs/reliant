package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/forgecompat"
)

// TestGoSubprocesses_DoNotInheritCallerModMod runs the `go` invocations
// `forge generate` (and the `forge tools install` its verify-generated CI job
// runs first) makes against a stand-in toolchain that records the GOFLAGS it
// was handed.
//
// The caller exports GOFLAGS=-mod=mod, as control-plane's pin-sibling.sh did
// for its own `go get`s. Under it, forge's go list / go build / go/packages
// calls rewrite go.sum as a side effect — ~350 lines on control-plane that no
// CI regenerate produces. Each subprocess must arrive WITHOUT -mod=mod and
// WITH the rest of the caller's GOFLAGS (-tags here), which is a legitimate
// statement about how to build.
func TestGoSubprocesses_DoNotInheritCallerModMod(t *testing.T) {
	if testing.Short() {
		t.Skip("execs stand-in go/goimports scripts from every go subprocess call site; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("stand-in toolchain is a POSIX shell script")
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	// Records "<argv0> <args>\t<GOFLAGS>" and fails, which every call
	// site below already tolerates — only what it was handed matters.
	recorder := "#!/bin/sh\nprintf '%s %s\\t%s\\n' \"$(basename \"$0\")\" \"$*\" \"${GOFLAGS-<unset>}\" >> \"$FAKE_TOOL_LOG\"\nexit 1\n"
	for _, name := range []string{"go", "goimports"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(recorder), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_TOOL_LOG", logPath)
	t.Setenv("GOFLAGS", "-tags=probe -mod=mod")

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/p\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "p.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := []struct {
		name string
		run  func()
	}{
		{"forge version compatibility (go list -m)", func() { forgecompat.ResolveProjectForge(project) }},
		// go list -export -test, then (go list failing here) the go build
		// fallback: every call is recorded, and both must arrive scrubbed.
		{"compile validate", func() { _ = runGoBuildValidate(project) }},
		{"post-rollback compile check", func() { _ = goBuildRestoredTree(project) }},
		{"bridged-module tidy probe", func() { _, _ = runGoOffWorkspace(project, "mod", "tidy", "-diff") }},
		{"tools install version resolve (go list -m)", func() {
			resolveToolVersion(context.Background(), project, requiredProtoTools[0], "")
		}},
		{"goimports on generated Go", func() {
			checksums.ResetPerRunState()
			t.Cleanup(checksums.ResetPerRunState)
			checksums.MarkWrittenThisRun("p.go")
			_ = runGoimportsOnGenerated(project, "example.com/p")
		}},
	}
	for _, c := range calls {
		_ = os.Remove(logPath)
		c.run()
		data, err := os.ReadFile(logPath)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			t.Errorf("%s: no go-toolchain subprocess was recorded, so this case proves nothing", c.name)
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			cmd, goflags, _ := strings.Cut(line, "\t")
			if strings.Contains(goflags, "-mod=mod") {
				t.Errorf("%s: `%s` inherited the caller's -mod=mod (GOFLAGS=%q), so it may rewrite go.mod/go.sum", c.name, cmd, goflags)
			}
			if !strings.Contains(goflags, "-tags=probe") {
				t.Errorf("%s: `%s` lost the rest of the caller's GOFLAGS (got %q, want -tags=probe kept)", c.name, cmd, goflags)
			}
		}
	}
}
