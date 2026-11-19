package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestReplayPerFrontendMigration_Detection runs the SHIPPED v1.4.0 detection
// against env KCL it has to tell apart: an env scaffolded with _session_replay
// before v1.4.0 (signals.replay plus an explicit runtime_config OTEL_REPLAY,
// which outranks the new per-frontend switch) is offered the move; one
// scaffolded today, on telemetry.replay alone, is not.
func TestReplayPerFrontendMigration_Detection(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, candidate := range metas {
		if candidate.SkillPath == "migrations/v1.4.0" {
			m = candidate
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped migration at migrations/v1.4.0")
	}

	current, err := templates.DeployTemplates().Render("kcl/cloud/main.k.tmpl", templates.EnvTemplateData{
		ProjectName: "shop", EnvName: "prod", HasFrontend: true, SessionReplay: true,
		FrontendName: "web", FrontendIdent: "web", FrontendType: "nextjs",
		FrontendPath: ".", FrontendPublicDir: "out", PrimaryWorkload: "shop",
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"pre-v1.4.0 _session_replay scaffold": {`_session_replay = True
_web_frontend = forge.Frontend {
    name = "web"
    runtime_config = {OTEL_REPLAY = "true" if _session_replay else "false"}
}
_bundle = forge.Bundle {
    observability = forge.Observability {signals = forge.ObservabilitySignals {replay = _session_replay}}
}
`, true},
		"env-wide signals.replay alone":    {"observability = forge.Observability {signals = forge.ObservabilitySignals {browser = True, replay = True}}\n", true},
		"runtime_config OTEL_REPLAY alone": {"runtime_config = {OTEL_REPLAY = \"false\"}\n", true},
		"today's scaffold":                 {string(current), false},
		"no replay at all":                 {"observability = forge.Observability {signals = forge.ObservabilitySignals {logs = False}}\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := migrationApplies(m, "v1.3.0", dir); got != tc.want {
				t.Errorf("migrationApplies = %v, want %v", got, tc.want)
			}
		})
	}
}
