package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeadlineMigration_Detection runs the SHIPPED v0.1.47 detection against
// the owned chain builders its Deadline section has to tell apart: a
// serve.go or providers.go building serverkit.NewChain or observe.Chain with
// no Deadline (offered — every unary RPC now gets the 30s default and its
// declared timeouts reach nothing), and one that already passes
// ProcedureDeadlines (not offered).
func TestDeadlineMigration_Detection(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, candidate := range metas {
		if candidate.SkillPath == "migrations/v0.1.47" {
			m = candidate
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped migration at migrations/v0.1.47")
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
	const legacyChain = "chain, err := serverkit.NewChain(serverkit.ChainDeps{\n\tAuth: auth,\n})\n"
	const currentChain = "chain, err := serverkit.NewChain(serverkit.ChainDeps{\n\tAuth: auth,\n\tDeadline: serverkit.Deadline{Procedures: middleware.ProcedureDeadlines},\n})\n"
	const observeChain = "interceptors := observe.Chain(observe.Deps{\n\tAuth: auth,\n})\n"
	const observeChainDeadline = "interceptors := observe.Chain(observe.Deps{\n\tAuth: auth,\n\tDeadline: serverkit.Deadline{Procedures: middleware.ProcedureDeadlines, Logger: logger},\n})\n"

	for name, tc := range map[string]struct {
		rel, body string
		want      bool
	}{
		"serve.go without a deadline":     {filepath.Join("cmd", "app", "cmd", "serve.go"), legacyChain, true},
		"providers.go without a deadline": {filepath.Join("internal", "app", "providers.go"), legacyChain, true},
		"serve.go with the deadline":      {filepath.Join("cmd", "app", "cmd", "serve.go"), currentChain, false},
		"providers.go with the deadline":  {filepath.Join("internal", "app", "providers.go"), currentChain, false},
		"no chain at all":                 {filepath.Join("cmd", "app", "cmd", "serve.go"), "package cmd\n", false},
		// observe.Chain has the same slot and the same default.
		"observe.Chain without a deadline":              {filepath.Join("cmd", "app", "cmd", "serve.go"), observeChain, true},
		"observe.Chain with a deadline (control-plane)": {filepath.Join("cmd", "app", "cmd", "serve.go"), observeChainDeadline, false},
		// Section 3: the probe surface given the configured paths.
		"Probes without Paths": {filepath.Join("cmd", "app", "cmd", "serve.go"), "g := &app.Graph{Probes: &serverkit.Probes{Drainer: drainer}}\n", true},
		"Probes with Paths":    {filepath.Join("cmd", "app", "cmd", "serve.go"), "g := &app.Graph{Probes: &serverkit.Probes{Drainer: drainer, Paths: skCfg.Probes}}\n", false},
		// Section 2: the legacy composition.
		"the legacy NewComponents": {filepath.Join("internal", "app", "compose.go"), "package app\n\nfunc NewComponents(infra *Infra) (*Components, error) { return nil, nil }\n", true},
		"the legacy Infra":         {filepath.Join("internal", "app", "providers.go"), "package app\n\ntype Infra struct{}\n", true},
		// Section 11: the activity caller is never guessed.
		"activity.Interceptor with ActorFrom (control-plane)": {filepath.Join("cmd", "app", "cmd", "serve.go"),
			"x := activity.Interceptor(activity.Deps{Writer: w, ActorFrom: activitystream.ActorFrom})\n", true},
		"activity.DefaultActor": {filepath.Join("internal", "app", "activity.go"), "var a = activity.DefaultActor(ctx)\n", true},
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
