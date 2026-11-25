package config

import (
	"strings"
	"testing"
)

func TestCIWorkflows_LoadAndDefault(t *testing.T) {
	in := "name: demo\nmodule_path: github.com/example/demo\nci:\n  workflows:\n    deploy: false\n    e2e: true\n"
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("expected clean load, got: %v", err)
	}
	if cfg.CI.WorkflowEnabled("deploy") || !cfg.CI.WorkflowEnabled("e2e") || !cfg.CI.WorkflowEnabled("ci") {
		t.Errorf("WorkflowEnabled: deploy=%v e2e=%v ci=%v", cfg.CI.WorkflowEnabled("deploy"),
			cfg.CI.WorkflowEnabled("e2e"), cfg.CI.WorkflowEnabled("ci"))
	}
}

// The v1.11.0 pgo-default-on playbook and pgo-refresh.yml's own header name
// this opt-out; it was a load error ("no workflow named pgo-refresh").
func TestCIWorkflows_PGORefreshCanBeDeclined(t *testing.T) {
	in := "name: demo\nmodule_path: github.com/example/demo\nci:\n  workflows:\n    pgo-refresh: false\n"
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("expected clean load, got: %v", err)
	}
	if cfg.CI.WorkflowEnabled("pgo-refresh") {
		t.Error("pgo-refresh: false must switch pgo-refresh.yml off")
	}
}

func TestCIWorkflows_UnknownNameIsALoadError(t *testing.T) {
	in := "name: demo\nmodule_path: github.com/example/demo\nci:\n  workflows:\n    deploys: false\n"
	_, err := LoadProject([]byte(in), "forge.yaml")
	if err == nil || !strings.Contains(err.Error(), `no workflow named "deploys"`) || !strings.Contains(err.Error(), "build-images") {
		t.Fatalf("err = %v, want a load error naming the key and the valid names", err)
	}
}

// ci.provider reliant loads, and its workflows switch by name like the
// GitHub ones (review is the reliant provider's alone).
func TestCIProvider_ReliantLoads(t *testing.T) {
	in := "name: demo\nmodule_path: github.com/example/demo\nci:\n  provider: reliant\n  workflows:\n    review: false\n"
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("expected clean load, got: %v", err)
	}
	if got := cfg.CI.EffectiveProvider(); got != CIProviderReliant {
		t.Errorf("EffectiveProvider() = %q, want reliant", got)
	}
	if cfg.CI.WorkflowEnabled("review") || !cfg.CI.WorkflowEnabled("ci") {
		t.Errorf("WorkflowEnabled: review=%v ci=%v", cfg.CI.WorkflowEnabled("review"), cfg.CI.WorkflowEnabled("ci"))
	}
}

func TestCIProvider_EffectiveProvider(t *testing.T) {
	for in, want := range map[string]string{"": CIProviderGitHub, "github": CIProviderGitHub, " Reliant ": CIProviderReliant, "gitlab": "gitlab"} {
		if got := (CIConfig{Provider: in}).EffectiveProvider(); got != want {
			t.Errorf("EffectiveProvider(%q) = %q, want %q", in, got, want)
		}
	}
	cfg, err := LoadProject([]byte("name: demo\nmodule_path: github.com/example/demo\n"), "forge.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.CI.EffectiveProvider(); got != CIProviderGitHub {
		t.Errorf("a forge.yaml with no ci block runs CI on %q, want github", got)
	}
}
