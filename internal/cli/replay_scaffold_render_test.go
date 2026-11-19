package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/kclplugin"
)

// Render the real scaffold, not a hand-written approximation: a hosted
// binder must retain the browser choice while the shape admits its records.
func TestReplayScaffoldSitesRenderCaptureAndAdmissionTogether(t *testing.T) {
	if testing.Short() {
		t.Skip("renders real KCL through the native runtime; runs in task test")
	}
	kclplugin.Register()
	for _, kind := range []string{"vite-spa", "nextjs"} {
		for _, enabled := range []bool{true, false} {
			t.Run(kind+"/"+map[bool]string{true: "on", false: "off"}[enabled], func(t *testing.T) {
				dir := t.TempDir()
				fe, _ := generator.SiteStarterFrontend(kind, "web")
				if err := generator.WriteSiteStarter(dir, "shop", fe); err != nil {
					t.Fatal(err)
				}
				if err := generator.WriteSiteScaffold(dir, "shop", generator.Harness("reliant"), fe); err != nil {
					t.Fatal(err)
				}
				for _, env := range []string{"dev", "staging", "prod"} {
					path := filepath.Join(dir, "deploy/kcl", env, "main.k")
					if !enabled {
						b, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), "_session_replay = True", "_session_replay = False")), 0600); err != nil {
							t.Fatal(err)
						}
					}
					raw, err := renderKCLRaw(context.Background(), dir, env)
					if err != nil {
						t.Fatal(err)
					}
					e, err := parseKCLEntities(raw)
					if err != nil {
						t.Fatal(err)
					}
					if len(e.Frontends) != 1 {
						t.Fatalf("frontends: %v", e.Frontends)
					}
					f := e.Frontends[0]
					// The scaffold's switch is the frontend's telemetry.replay:
					// on renders OTEL_REPLAY = "true", off renders no key
					// (unset = off), so nothing explicit outranks the switch.
					if f.Telemetry == nil || f.Telemetry.Replay != enabled {
						t.Fatalf("telemetry.replay: %+v, want %v", f.Telemetry, enabled)
					}
					if env == "dev" {
						if v, ok := f.RuntimeConfig["OTEL_REPLAY"]; ok != enabled || (enabled && v != "true") {
							t.Fatalf("dev replay: %v", f.RuntimeConfig)
						}
					} else {
						v, ok := f.RuntimeConfigSpec["OTEL_REPLAY"]
						if ok != enabled || (enabled && (v.Value == nil || *v.Value != "true")) {
							t.Fatalf("hosted replay: %v", f.RuntimeConfigSpec)
						}
					}
					if e.Observability == nil || e.Observability.Policy == nil || slices.Contains(e.Observability.Policy.OptIn, "replay") != enabled {
						t.Fatalf("ingest policy: %+v", e.Observability)
					}
					if devReplayRequested(e) != enabled {
						t.Fatal("dev grant intent differs from ingest declaration")
					}
					if len(e.Workloads) != 0 {
						t.Fatal("static site acquired a backend or Docker dependency")
					}
				}
			})
		}
	}
}

func TestReplayScaffoldOfflineSetupPublishesCaptureOff(t *testing.T) {
	if testing.Short() {
		t.Skip("renders actual scaffold and runtime docs; runs in task test")
	}
	kclplugin.Register()
	oldCredential := devTelemetryCredential
	devTelemetryCredential = func(string, *cloud.Declaration) error { return errors.New("offline") }
	t.Cleanup(func() { devTelemetryCredential = oldCredential })
	for _, tc := range []struct {
		name                   string
		cp, enabled, collector bool
	}{
		{"default offline", true, true, false}, {"no control plane", false, true, false}, {"observability off", false, false, false}, {"local collector", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fe, _ := generator.SiteStarterFrontend("vite-spa", "web")
			if err := generator.WriteSiteStarter(dir, "shop", fe); err != nil {
				t.Fatal(err)
			}
			if err := generator.WriteSiteScaffold(dir, "shop", generator.HarnessReliant, fe); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "deploy/kcl/dev/main.k")
			main, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source := string(main)
			if !tc.cp {
				source = strings.Replace(source, "    control_plane = forge.ControlPlane {}\n", "", 1)
			}
			source = strings.Replace(source, "observability = forge.Observability {}", "observability = forge.Observability {enabled = "+map[bool]string{true: "True", false: "False"}[tc.enabled]+"}", 1)
			if tc.collector {
				source = strings.Replace(source, "enabled = True}", "local_port = 14318, enabled = True}", 1)
			}
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			raw, err := renderKCLRaw(t.Context(), dir, "dev")
			if err != nil {
				t.Fatal(err)
			}
			e, err := parseKCLEntities(raw)
			if err != nil {
				t.Fatal(err)
			}
			var log strings.Builder
			if _, err := setupDevTelemetry(t.Context(), e, devTelemetrySetupIn{ProjectDir: dir, Env: "dev", Flag: "auto", Out: &log, Now: devTelNow}); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadProjectDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := refreshFrontendRuntimeConfigs(cfg, dir, "dev", e); err != nil {
				t.Fatal(err)
			}
			doc, err := os.ReadFile(filepath.Join(dir, "public/config.js"))
			if err != nil {
				t.Fatal(err)
			}
			want := map[bool]string{true: "true", false: "false"}[tc.collector]
			if !strings.Contains(string(doc), `"OTEL_REPLAY": "`+want+`"`) {
				t.Fatalf("capture config %s log %s", doc, log.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != source {
				t.Fatal("runtime decision modified owned declaration")
			}
		})
	}
}

func TestReplayLegacyEnvironmentStaysOff(t *testing.T) {
	if testing.Short() {
		t.Skip("renders real KCL; runs in task test")
	}
	for _, runtime := range []string{`runtime = forge.OnHost {}`, `image = "web", runtime = forge.OnHosted {}`} {
		e := renderFrontendEnv(t, `[forge.Frontend {name = "web", type = "vite", path = ".", `+runtime+`}]`, "control_plane = forge.ControlPlane {}\n observability = forge.Observability {}")
		if devReplayRequested(e) {
			t.Fatal("legacy schema default enabled replay")
		}
		f := e.Frontends[0]
		if _, ok := f.RuntimeConfig["OTEL_REPLAY"]; ok {
			t.Fatal("legacy runtime gained replay key")
		}
		if _, ok := f.RuntimeConfigSpec["OTEL_REPLAY"]; ok {
			t.Fatal("legacy hosted artifact gained replay key")
		}
	}
}

func TestReplayScaffoldAppEnvironmentsRender(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and renders a full app; runs in task test")
	}
	kclplugin.Register()
	dir := t.TempDir()
	g := generator.NewProjectGenerator("shop", dir, "example.com/shop")
	g.Kind = config.ProjectKindService
	g.ApplyKindFeatureDefaults(config.ProjectKindService)
	g.ServiceName = "item"
	g.FrontendName = "web"
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := generator.LoadChecksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := generatePerEnvDeployConfig(dir, cfg, cs, nil); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		raw, err := renderKCLRaw(t.Context(), dir, env)
		if err != nil {
			t.Fatal(err)
		}
		e, err := parseKCLEntities(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !devReplayRequested(e) {
			t.Fatalf("%s not opted in", env)
		}
		f := e.Frontends[0]
		if env == "dev" && f.RuntimeConfig["OTEL_REPLAY"] != "true" {
			t.Fatal("dev capture missing")
		}
		if env != "dev" && (f.RuntimeConfigSpec["OTEL_REPLAY"].Value == nil || *f.RuntimeConfigSpec["OTEL_REPLAY"].Value != "true") {
			t.Fatal("hosted capture missing")
		}
	}
}
