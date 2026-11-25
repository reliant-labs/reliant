package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/shellrun"
)

// A detection runs in forge's in-process shell (runDetection), where grep and
// rg are portable built-ins. One they cannot run exits 2, and that reads as
// "this project does not have the old shape" for every project, so the
// playbook is never listed. v1.11.0/pgo-default-on's `grep -lqs` was one.
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

// v1.11.0/pgo-default-on applies to a project with a hosted env, no committed
// default.pgo and no opt-out. Its detection exited 2 in process until the
// portable grep learned -l, so it applied to none. pgo-refresh.yml first ships
// in v1.11.0 (v1.10.0 was tagged before it), so a project already pinned at
// v1.10.0 is offered it too, and one at v1.11.0 is not.
func TestV1110PGOPlaybook_Detection(t *testing.T) {
	var m migrationMeta
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	for _, c := range metas {
		if c.ID == "v1.11.0/pgo-default-on" {
			m = c
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped playbook v1.11.0/pgo-default-on")
	}
	const hosted = "app = forge.OnHosted {}\n"
	const goMain = "package main\n\nfunc main() {}\n"
	birth := checksums.ScaffoldEntryPath(".github/workflows/pgo-refresh.yml")
	for name, tc := range map[string]struct {
		files map[string]string
		want  bool
	}{
		"a hosted env":           {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\n", "cmd/x/main.go": goMain}, true},
		"no hosted env":          {map[string]string{"deploy/kcl/prod/main.k": "app = forge.OnCluster {}\n", "forge.yaml": "name: x\n", "cmd/x/main.go": goMain}, false},
		"declined in forge.yaml": {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\nci:\n  workflows:\n    pgo-refresh: false\n", "cmd/x/main.go": goMain}, false},
		"declined in flow style": {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\nci: {workflows: {pgo-refresh: false}}\n", "cmd/x/main.go": goMain}, false},
		"a committed profile":    {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\n", "cmd/x/default.pgo": "pgo", "cmd/x/main.go": goMain}, false},
		// reliant: a hosted static site and no cmd/<bin> Go file at the
		// project root would not get the file.
		"no Go binary": {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\n"}, false},
		// reliant and control-plane deleted it and committed the record.
		"deleted after birth": {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\n", "cmd/x/main.go": goMain, birth: "{}\n"}, false},
		// control-plane refreshes profiles in a Reliant workflow.
		"refreshed by a Reliant workflow": {map[string]string{"deploy/kcl/prod/main.k": hosted, "forge.yaml": "name: x\n", "cmd/x/main.go": goMain, ".reliant/workflows/pgo-refresh.yaml": "name: pgo-refresh\n"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			checksums.ResetScaffoldLedgerCache()
			t.Cleanup(checksums.ResetScaffoldLedgerCache)
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
			for _, baseline := range []string{"v1.9.0", "v1.10.0"} {
				if got := migrationApplies(m, baseline, dir); got != tc.want {
					t.Errorf("at %s: applies = %v, want %v", baseline, got, tc.want)
				}
			}
			if migrationApplies(m, "v1.11.0", dir) {
				t.Error("applies at v1.11.0, a pin that already crossed its release")
			}
		})
	}
}

// v1.11.0/auth-none-public applies to a project nobody signs in to (no
// identity.k, a dev env with no IdP) whose frontend still has the sign-in
// flow's API client, and to no project that declares users.
func TestV1110AuthNonePlaybook_Detection(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, c := range metas {
		if c.ID == "v1.11.0/auth-none-public" {
			m = c
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped playbook v1.11.0/auth-none-public")
	}
	const noIDP = "# no identity provider\nfrontends = []\n"
	const client = "frontends/web/src/lib/auth/native-login.ts"
	for name, tc := range map[string]struct {
		files map[string]string
		want  bool
	}{
		"auth-mode none, old frontend":    {map[string]string{"deploy/kcl/dev/main.k": noIDP, client: "export {};\n"}, true},
		"auth-mode none, already applied": {map[string]string{"deploy/kcl/dev/main.k": noIDP, "frontends/web/src/lib/auth/context.tsx": "x\n"}, false},
		"identity declared":               {map[string]string{"deploy/kcl/dev/main.k": noIDP, "deploy/kcl/identity.k": "X = 1\n", client: "export {};\n"}, false},
		"legacy dev IdP":                  {map[string]string{"deploy/kcl/dev/main.k": "engine = \"zitadel\"\n", client: "export {};\n"}, false},
		"no dev env":                      {map[string]string{client: "export {};\n"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.files {
				p := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := migrationApplies(m, "v1.10.0", dir); got != tc.want {
				t.Errorf("applies = %v, want %v", got, tc.want)
			}
		})
	}
}
