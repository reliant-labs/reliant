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

func TestCIWorkflows_UnknownNameIsALoadError(t *testing.T) {
	in := "name: demo\nmodule_path: github.com/example/demo\nci:\n  workflows:\n    deploys: false\n"
	_, err := LoadProject([]byte(in), "forge.yaml")
	if err == nil || !strings.Contains(err.Error(), `no workflow named "deploys"`) || !strings.Contains(err.Error(), "build-images") {
		t.Fatalf("err = %v, want a load error naming the key and the valid names", err)
	}
}
