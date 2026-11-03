package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStreamDrainMigration_Detection runs the SHIPPED v0.1.46 detection
// against the serve.go shapes its stream-drain section has to tell apart: an
// owned serve.go that builds the interceptor chain without
// StreamDrainInterceptor (offered — its open streams still hold every
// shutdown), and one that already has it (not offered). Nothing else in the
// fixture matches the release's other sections.
func TestStreamDrainMigration_Detection(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, candidate := range metas {
		if candidate.SkillPath == "migrations/v0.1.46" {
			m = candidate
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped migration at migrations/v0.1.46")
	}

	write := func(t *testing.T, dir, body string) {
		t.Helper()
		path := filepath.Join(dir, "cmd", "app", "cmd", "serve.go")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	legacy := t.TempDir()
	write(t, legacy, "opts := connect.WithInterceptors(observe.Chain(chainDeps)...)\n")
	if !migrationApplies(m, "v0.1.45", legacy) {
		t.Error("a serve.go building observe.Chain without StreamDrainInterceptor was not offered the migration")
	}

	current := t.TempDir()
	write(t, current, "chainDeps.Extras = append(chainDeps.Extras, serverkit.StreamDrainInterceptor())\n"+
		"opts := connect.WithInterceptors(observe.Chain(chainDeps)...)\n")
	if migrationApplies(m, "v0.1.45", current) {
		t.Error("a serve.go that already binds streaming RPCs to the drain was offered the migration")
	}
}
