package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// `forge project memory` prints exactly what RenderProjectMemory returns —
// the guide a harness injects — and --json says which forge rendered it.
func TestProjectMemoryCmd_PrintsTheRenderedGuide(t *testing.T) {
	dir := t.TempDir()
	writeForgeYAML(t, dir, "name: my-app\nmodule_path: example.com/my-app\n")
	sub := filepath.Join(dir, "internal")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	run := func(args ...string) (string, error) {
		root := newTestRootCmd(t)
		var out strings.Builder
		root.SetOut(&out)
		root.SetArgs(args)
		err := root.Execute()
		return out.String(), err
	}

	got, err := run("project", "memory")
	// Rendered after the command ran, so both name forge the same way: the
	// command records how forge is mounted, which the guide's commands follow.
	want, rerr := RenderProjectMemory(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err != nil || got != string(want) {
		t.Fatalf("err %v; the printed guide differs from RenderProjectMemory", err)
	}
	raw, err := run("project", "memory", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ForgeVersion string `json:"forge_version"`
		Memory       string `json:"memory"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("%v:\n%s", err, raw)
	}
	if doc.Memory != string(want) || doc.ForgeVersion != buildinfo.Version() {
		t.Errorf("--json: version %q (want %q), memory matches %v", doc.ForgeVersion, buildinfo.Version(), doc.Memory == string(want))
	}

	t.Chdir(t.TempDir())
	if _, err := run("project", "memory"); err == nil || !strings.Contains(err.Error(), "not inside a forge project") {
		t.Errorf("outside a project: %v", err)
	}
}
