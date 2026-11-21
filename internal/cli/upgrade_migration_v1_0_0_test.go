package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestV100Migration_Detection runs the SHIPPED v1.0.0 detection against the
// shapes its two remaining sections have to tell apart: the legacy
// Infra/NewComponents composition (section 1, `forge fix composition-graph`)
// and a hand-built Connect error in a handler (section 2, `forge fix
// connect-errors`). Everything else v1.0.0 changed aged out of the upgrade
// window, so a project showing only those older shapes is not offered it.
func TestV100Migration_Detection(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, candidate := range metas {
		if candidate.SkillPath == "migrations/v1.0.0" {
			m = candidate
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped migration at migrations/v1.0.0")
	}

	write := func(t *testing.T, dir, rel, body string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for name, tc := range map[string]struct {
		rel, body string
		want      bool
	}{
		// Section 1: the legacy composition.
		"the legacy NewComponents": {filepath.Join("internal", "app", "compose.go"), "package app\n\nfunc NewComponents(infra *Infra) (*Components, error) { return nil, nil }\n", true},
		"the legacy Infra":         {filepath.Join("internal", "app", "providers.go"), "package app\n\ntype Infra struct{}\n", true},
		"a graph project":          {filepath.Join("internal", "app", "compose.go"), "package app\n\nfunc (c *Components) Billing() {}\n", false},
		// Section 2: a hand-built Connect error in a handler. The detection
		// spells the paren unescaped, as GNU grep's basic syntax wants it.
		"connect.NewError in a handler": {filepath.Join("internal", "handlers", "billing", "rpc_charge.go"),
			"return nil, connect.NewError(connect.CodeInternal, err)\n", true},
		"connect.NewError only in a test": {filepath.Join("internal", "handlers", "billing", "rpc_charge_test.go"),
			"connect.NewError(connect.CodeInternal, err)\n", false},
		// An aged-out section's shape alone no longer offers the playbook.
		"a chain without a deadline": {filepath.Join("cmd", "app", "cmd", "serve.go"),
			"chain, err := serverkit.NewChain(serverkit.ChainDeps{\n\tAuth: auth,\n})\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tc.rel, tc.body)
			if got := migrationApplies(m, "v0.1.46", dir); got != tc.want {
				t.Errorf("migrationApplies = %v, want %v", got, tc.want)
			}
		})
	}
}
