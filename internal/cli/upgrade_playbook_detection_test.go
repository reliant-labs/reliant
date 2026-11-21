package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/shellrun"
)

// A detection runs in forge's in-process shell (runDetection), where grep and
// rg are portable built-ins. One they cannot run exits 2, and that reads as
// "this project does not have the old shape" for every project, so the
// playbook is never listed. v1.10.0/pgo-default-on's `grep -lqs` was one.
func TestShippedPlaybookDetectionsRunInProcess(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	checked := 0
	for _, m := range metas {
		if strings.TrimSpace(m.Detection) == "" {
			continue
		}
		checked++
		if err := shellrun.CheckPortable(m.Detection); err != nil {
			t.Errorf("%s: its detection cannot run in process: %v\n\tdetection: %s", m.ID, err, m.Detection)
		}
	}
	if checked == 0 {
		t.Fatal("no shipped playbook declares a detection: the check checked nothing")
	}
}

// v1.10.0/pgo-default-on applies to a project with a hosted env, no committed
// default.pgo and no opt-out. Its detection exited 2 in process until the
// portable grep learned -l, so it applied to none.
func TestV1100PGOPlaybook_Detection(t *testing.T) {
	var m migrationMeta
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	for _, c := range metas {
		if c.ID == "v1.10.0/pgo-default-on" {
			m = c
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped playbook v1.10.0/pgo-default-on")
	}
	const hosted = "app = forge.OnHosted {}\n"
	for name, tc := range map[string]struct {
		files map[string]string
		want  bool
	}{
		"a hosted env":           {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\n"}, true},
		"no hosted env":          {map[string]string{"deploy/kcl/prod/main.k": "app = forge.OnCluster {}\n", "forge.yaml": "name: x\n"}, false},
		"declined in forge.yaml": {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\nci:\n  workflows:\n    pgo-refresh: false\n"}, false},
		"a committed profile":    {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\n", "cmd/x/default.pgo": "pgo"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.files {
				p := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := migrationApplies(m, "v1.9.0", dir); got != tc.want {
				t.Errorf("applies = %v, want %v", got, tc.want)
			}
		})
	}
}
