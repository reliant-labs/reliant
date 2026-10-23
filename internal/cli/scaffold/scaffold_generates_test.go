package scaffold

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/configproto"
)

// A scaffold whose writes are generate INPUTS must end with a generate run, or
// the project it hands back is lint-red until the user thinks to run one.
// `forge scaffold frontend` wrote proto/config/v1/<fe>_config.proto and stopped,
// so gen/forge_descriptor.json went stale and `forge lint`'s convention and
// graph lanes refused it; `scaffold package` / `scaffold adapter` stopped
// before middleware_gen.go existed (golangci `unused` on the owned
// observe_chain.go seam) and before an adapter's config.<Name>Config was
// compiled. These pin the tail; TestE2EScaffoldNounsLeaveLintGreen runs the
// real `forge lint` after every noun.

func TestRunFrontend_EndsWithGenerate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noGenerate bool
		wantRuns   int
	}{
		{name: "generates", wantRuns: 1},
		{name: "no-generate", noGenerate: true, wantRuns: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skipNpmInstall(t)
			dir := withTempProject(t, freshServiceForgeYAML)
			markServiceProject(t, dir)

			f := testFactory()
			runs := 0
			f.Gen.RunPipeline = func(root string) error {
				runs++
				// Generate reads the config proto the scaffold wrote, so it
				// must run after that write, not before it.
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(configproto.ForProject(root).Dir()), "web_config.proto")); err != nil {
					t.Errorf("generate ran before the frontend's config proto was written: %v", err)
				}
				return nil
			}
			if err := runFrontend(context.Background(), f, frontendArgs{name: "web", noGenerate: tc.noGenerate}); err != nil {
				t.Fatalf("runFrontend: %v", err)
			}
			if runs != tc.wantRuns {
				t.Errorf("generate pipeline ran %d time(s), want %d", runs, tc.wantRuns)
			}
		})
	}
}

// A failed generate keeps what the scaffold wrote — the files are the user's
// from birth — and fails the command, because a project that does not
// generate is not green.
func TestRunFrontend_GenerateFailureKeepsTheFilesAndFails(t *testing.T) {
	skipNpmInstall(t)
	dir := withTempProject(t, freshServiceForgeYAML)
	markServiceProject(t, dir)

	f := testFactory()
	f.Gen.RunPipeline = func(string) error { return errors.New("buf: boom") }
	err := runFrontend(context.Background(), f, frontendArgs{name: "web"})
	if err == nil || !strings.Contains(err.Error(), "buf: boom") || !strings.Contains(err.Error(), "forge generate") {
		t.Fatalf("runFrontend = %v, want the pipeline error and the re-run advice", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "frontends", "web", "package.json")); statErr != nil {
		t.Errorf("a failed generate must keep the scaffolded frontend: %v", statErr)
	}
}

func TestPackageAndAdapterScaffoldsEndWithGenerate(t *testing.T) {
	for _, noun := range []struct {
		name string
		cmd  func(f *factory.Factory) *cobra.Command
	}{
		{name: "package", cmd: newPackageCmd},
		{name: "adapter", cmd: newAdapterCmd},
	} {
		for _, tc := range []struct {
			flag     string
			wantRuns int
		}{
			{wantRuns: 1},
			{flag: "--no-generate", wantRuns: 0},
		} {
			t.Run(noun.name+tc.flag, func(t *testing.T) {
				dir := withTempProject(t, freshServiceForgeYAML)
				f := testFactory()
				scaffolded := false
				f.Gen.RunPackageNew = func(*cobra.Command, []string) error {
					scaffolded = true
					return nil
				}
				var roots []string
				f.Gen.RunPipeline = func(root string) error {
					if !scaffolded {
						t.Error("generate ran before the package was scaffolded")
					}
					roots = append(roots, root)
					return nil
				}
				cmd := noun.cmd(f)
				args := []string{"pricing"}
				if tc.flag != "" {
					args = append(args, tc.flag)
				}
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != nil {
					t.Fatalf("scaffold %s: %v", noun.name, err)
				}
				if len(roots) != tc.wantRuns {
					t.Fatalf("generate pipeline ran %d time(s), want %d", len(roots), tc.wantRuns)
				}
				if tc.wantRuns > 0 && !sameDir(t, roots[0], dir) {
					t.Errorf("generate ran in %s, want the project root %s", roots[0], dir)
				}
			})
		}
	}
}

// A scaffold that fails validation writes nothing, so it must not generate.
func TestPackageScaffoldFailureSkipsGenerate(t *testing.T) {
	withTempProject(t, freshServiceForgeYAML)
	f := testFactory()
	f.Gen.RunPackageNew = func(*cobra.Command, []string) error { return errors.New("invalid package name") }
	f.Gen.RunPipeline = func(string) error {
		t.Error("generate ran after a scaffold that failed")
		return nil
	}
	cmd := newPackageCmd(f)
	cmd.SetArgs([]string{"Bad"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("want the scaffold's error")
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
