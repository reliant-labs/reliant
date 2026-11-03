package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `forge project frontends` prints the in-repo frontends any env's KCL
// declares, at whatever path, one per line: what the scaffolded Taskfile
// loops over. A frontend outside the repository (a sibling checkout) is not
// listed — nothing here can install or build it.
func TestProjectFrontends_PrintsKCLDeclaredInRepoPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\nmodule_path: github.com/example/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeKCLEnvs(t, root, "dev")
	useKCLFixture(t, `{
	  "frontends": [
	    {"name": "web", "type": "vite", "path": "web"},
	    {"name": "admin", "type": "nextjs", "path": "frontends/admin"},
	    {"name": "sibling", "type": "vite", "path": "../other/web"}
	  ]
	}`)
	t.Chdir(root)

	run := func(args ...string) string {
		t.Helper()
		cmd := newProjectFrontendsCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("project frontends %v: %v", args, err)
		}
		return out.String()
	}
	if got, want := run(), "frontends/admin\nweb\n"; got != want {
		t.Errorf("project frontends = %q, want %q", got, want)
	}
	if got := run("--json"); !strings.Contains(got, `"path": "web"`) || strings.Contains(got, "sibling") {
		t.Errorf("--json = %s", got)
	}
}
