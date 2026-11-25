package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shippedTrimpathPlaybook(t *testing.T) migrationMeta {
	t.Helper()
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	for _, m := range metas {
		if m.ID == trimpathPlaybookID {
			return m
		}
	}
	t.Fatalf("no shipped playbook %s", trimpathPlaybookID)
	return migrationMeta{}
}

// The playbook is listed with its codemod as its automation, and its
// detection is the codemod's own decision (forge-detect), so it applies
// exactly where `forge fix trimpath-default` has something to do or say.
func TestV1110TrimpathPlaybook_DetectionIsTheCodemods(t *testing.T) {
	m := shippedTrimpathPlaybook(t)
	if m.Version != "v1.11.0" || m.SkillPath != "migrations/"+trimpathPlaybookID {
		t.Errorf("meta = %+v", m)
	}
	if len(m.Automation) != 1 || m.Automation[0] != "fix:trimpath-default" {
		t.Errorf("automation = %v, want [fix:trimpath-default]", m.Automation)
	}
	const trimmed = "version: '3'\nenv:\n  GOFLAGS: -trimpath\n"
	for name, tc := range map[string]struct {
		taskfile string // "" writes none
		kcl      string // deploy/kcl/workloads.k; "" writes none
		want     bool
	}{
		"nothing to read":                {"", "", false},
		"a Taskfile without GOFLAGS":     {"version: '3'\ntasks:\n  build:\n    cmds: [go build -trimpath ./...]\n", "", true},
		"GOFLAGS with -trimpath":         {trimmed, "", false},
		"GOFLAGS set without -trimpath":  {"version: '3'\nenv:\n  GOFLAGS: -mod=mod\n", "", true},
		"GOFLAGS with -trimpath=false":   {"version: '3'\nenv:\n  GOFLAGS: -trimpath=false\n", "", false},
		"a Taskfile forge cannot edit":   {"version: '3'\nenv: {A: b}\n", "", true},
		"GOFLAGS only in one task's env": {"version: '3'\ntasks:\n  a:\n    env: {GOFLAGS: -trimpath}\n    cmds: [echo]\n", "", true},
		"a GoBuild without flags":        {trimmed, "import forge\n\nb = forge.GoBuild {cmd = \"./cmd/app\"}\n", true},
		"a GoBuild with -trimpath":       {trimmed, "import forge\n\nb = forge.GoBuild {cmd = \"./cmd/app\", flags = [\"-trimpath\"]}\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.taskfile != "" {
				if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte(tc.taskfile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.kcl != "" {
				writeProjectFile(t, dir, "deploy/kcl/workloads.k", tc.kcl)
			}
			if got := migrationApplies(m, "v1.10.0", dir); got != tc.want {
				t.Errorf("applies = %v, want %v", got, tc.want)
			}
			if migrationApplies(m, "v1.11.0", dir) {
				t.Error("applies to a project already at v1.11.0")
			}
		})
	}
}

// `forge project upgrade list` offers it to an older project whose Taskfile
// has no GOFLAGS entry.
func TestV1110TrimpathPlaybook_UpgradeListSurfacesIt(t *testing.T) {
	dir := newTestProjectWithVersion(t, "v1.10.0")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\n\ntasks:\n  test:\n    cmds: [go test ./...]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var pending []pendingMigration
	var err error
	withCwd(t, dir, func() { pending, err = computePendingMigrations() })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writePendingMigrationsHuman(&out, pending, len(pending)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{trimpathPlaybookID, "skill load migrations/" + trimpathPlaybookID, "upgrade apply " + trimpathPlaybookID} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("upgrade list does not show %q:\n%s", want, out.String())
		}
	}
}
