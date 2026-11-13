package cli

import (
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// TestEnvUpLaunchesInTheProjectDirNotTheCallersCwd pins `forge -C <dir> env
// up` run from ANOTHER checkout: every host process and frontend dev server
// starts in <dir>, where the relative paths it declares (an air config,
// ./bin/<name>, a frontend's package.json) were written from. They used to
// inherit the caller's cwd, so air failed to open .air.<svc>.toml that sat in
// <dir> (forge bug report 298ec35f).
func TestEnvUpLaunchesInTheProjectDirNotTheCallersCwd(t *testing.T) {
	clearProjectDir(t)
	project := writeProjectDirFixture(t, "target-project")
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(elsewhere)
	if err := cmdutil.SetProjectDir(project); err != nil {
		t.Fatal(err)
	}

	for _, runner := range []string{"air", "go-run", "binary"} {
		w := hostWL("api", func(w *WorkloadEntity) {
			w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/api"}}
			w.Runtime.Host.Runner = runner
			w.Runtime.Host.AirConfig = ".air.api.toml"
		})
		cmd, _, err := buildHostServiceCmd(t.Context(), nil, w, nil, "dev")
		if err != nil {
			t.Fatalf("runner %s: %v", runner, err)
		}
		if cmd.Dir != project {
			t.Errorf("runner %s: cmd.Dir = %q, want the -C project %q (not the caller's cwd %q)", runner, cmd.Dir, project, elsewhere)
		}
	}

	e := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "web", Path: "frontends/web"},
		{Name: "abs", Path: filepath.Join(elsewhere, "abs")},
		{Name: "sourced"},
	}}
	anchorFrontendPaths(projectDirForKCL(), e)
	want := []string{filepath.Join(project, "frontends", "web"), filepath.Join(elsewhere, "abs"), ""}
	for i, fe := range e.Frontends {
		if fe.Path != want[i] {
			t.Errorf("frontend %s: Path = %q, want %q", fe.Name, fe.Path, want[i])
		}
	}
}
