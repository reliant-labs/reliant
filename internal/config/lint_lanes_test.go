package config

import (
	"strings"
	"testing"
)

func TestLintLanes_Load(t *testing.T) {
	in := "name: demo\nmodule_path: github.com/example/demo\nlint:\n  lanes:\n    frontend: off\n    db-grants: on\n"
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("expected clean load, got: %v", err)
	}
	if !LaneOff(cfg.Lint.Lanes["frontend"]) || LaneOff(cfg.Lint.Lanes["db-grants"]) {
		t.Errorf("lanes = %v", cfg.Lint.Lanes)
	}
}

func TestLintLanes_BadValueIsALoadError(t *testing.T) {
	in := "name: demo\nmodule_path: github.com/example/demo\nlint:\n  lanes:\n    frontend: disabled\n"
	_, err := LoadProject([]byte(in), "forge.yaml")
	if err == nil || !strings.Contains(err.Error(), "lint.lanes.frontend") {
		t.Fatalf("err = %v, want a load error naming lint.lanes.frontend", err)
	}
}
