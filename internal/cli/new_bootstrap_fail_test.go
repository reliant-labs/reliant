package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/config"
)

// `forge project new` over a project whose own first generate failed exits
// non-zero, says so, and keeps the files: a re-run of generate completes
// them. It used to print "✅ created successfully" and exit 0, so a script, a
// CI step or an agent carried on over a project that did not generate — how
// the whole --disable codegen shape stayed broken with nothing noticing.
func TestRunNew_FailedBootstrapGenerateExitsNonZeroAndKeepsTheFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds a real service project twice (~15s each); runs in task test. TestNewBootstrapFailedErr covers the message under -short")
	}
	for _, tc := range []struct {
		name     string
		err      error
		wantWhat string
	}{
		{name: "aborted", err: errors.New("buf generate (Go) failed: boom"), wantWhat: "does not build"},
		{name: "steps failed", err: &factory.StepsFailedError{Steps: []string{"descriptor extraction"}}, wantWhat: "the output of the failed step(s) is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := bootstrapGenerate
			bootstrapGenerate = func(string, bool) error { return tc.err }
			t.Cleanup(func() { bootstrapGenerate = prev })
			// The tail of `project new` is best-effort and must stay off the
			// network: go mod tidy fails fast and is only warned about.
			t.Setenv("GOPROXY", "off")
			t.Setenv("GOFLAGS", "-mod=mod")
			// --link-forge to this checkout, so the scaffold can resolve forge
			// whatever version this test binary reports.
			prevRoot := buildinfo.DevForgeRoot
			forgeRoot, err := filepath.Abs(filepath.Join("..", ".."))
			if err != nil {
				t.Fatal(err)
			}
			buildinfo.DevForgeRoot = forgeRoot
			t.Cleanup(func() { buildinfo.DevForgeRoot = prevRoot })

			parent := t.TempDir()
			err = runNew(t.Context(), "demo", parent, "example.com/demo", config.ProjectKindService,
				nil, nil, "", false, false, nil, "", true, "local", "", false, true, false, false, newFrontendOptions{})
			if err == nil {
				t.Fatal("project new returned nil (exit 0) over a project whose initial generate failed")
			}
			msg := err.Error()
			for _, want := range []string{"initial code generation failed", tc.wantWhat, "files are kept", "forge generate", tc.err.Error()} {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not say %q:\n%s", want, msg)
				}
			}
			if !errors.Is(err, tc.err) {
				t.Errorf("the generate failure is not wrapped: %v", err)
			}
			// The project stays, finished as far as the scaffold goes: its
			// manifest is there and the in-progress marker (whose presence
			// makes a later run undo the directory) is gone.
			project := filepath.Join(parent, "demo")
			if _, err := os.Stat(filepath.Join(project, "forge.yaml")); err != nil {
				t.Errorf("the scaffolded project was not kept: %v", err)
			}
			if _, err := os.Stat(filepath.Join(project, ".forge", ".scaffold-in-progress")); !os.IsNotExist(err) {
				t.Errorf("in-progress scaffold marker left behind (stat: %v)", err)
			}
		})
	}
}

// The error names the project, says it is incomplete and why, keeps the
// generate failure wrapped, and tells the user the one command that completes
// it — with the cd only when the project is not the current directory.
func TestNewBootstrapFailedErr(t *testing.T) {
	cause := errors.New("buf generate (Go) failed: boom")
	err := newBootstrapFailedErr("demo", "/work/demo", false, cause)
	msg := err.Error()
	for _, want := range []string{`project "demo" was scaffolded at /work/demo`, "initial code generation failed", "does not build", "files are kept", "`cd /work/demo && " + Name() + " generate`", "boom"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not say %q:\n%s", want, msg)
		}
	}
	if !errors.Is(err, cause) {
		t.Errorf("the generate failure is not wrapped: %v", err)
	}
	stepsFailed := &factory.StepsFailedError{Steps: []string{"descriptor extraction"}}
	inPlace := newBootstrapFailedErr("demo", "/work/demo", true, stepsFailed).Error()
	if !strings.Contains(inPlace, "the output of the failed step(s) is missing") || strings.Contains(inPlace, "cd /work/demo") {
		t.Errorf("in-place, steps-failed error:\n%s", inPlace)
	}
}
