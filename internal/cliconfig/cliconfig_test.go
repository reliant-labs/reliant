// Copyright (c) 2025 Reliant Labs
package cliconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func testConfig() *Config {
	return &Config{
		CurrentContext: "prod",
		Contexts: map[string]*Context{
			"prod":    {Server: "https://reliantapi.com", Token: "rlnt_pat_prod"},
			"staging": {Server: "https://staging.reliantapi.com", Token: "rlnt_pat_staging"},
			"dev": {
				Server: "http://localhost:3110",
				Hooks:  []HookSpec{{On: "workflow_failed", Cmd: "notify.sh"}},
			},
		},
	}
}

func TestResolvePrecedence(t *testing.T) {
	cfg := testConfig()

	cases := []struct {
		name        string
		flag, env   string
		wantName    string
		wantSource  string
		wantErr     bool
		emptyResult bool
	}{
		{name: "flag wins over env and current", flag: "dev", env: "staging", wantName: "dev", wantSource: "flag"},
		{name: "env wins over current", env: "staging", wantName: "staging", wantSource: "env"},
		{name: "current_context fallback", wantName: "prod", wantSource: "current_context"},
		{name: "flag naming missing context errors", flag: "nope", wantErr: true},
		{name: "env naming missing context errors", env: "nope", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(cfg, tc.flag, tc.env)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
			if got.Context != cfg.Contexts[tc.wantName] {
				t.Errorf("Context pointer mismatch for %q", tc.wantName)
			}
		})
	}
}

func TestResolveLegacyMode(t *testing.T) {
	// No contexts and no current_context: legacy mode, no error.
	got, err := Resolve(&Config{Contexts: map[string]*Context{}}, "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "" || got.Context != nil {
		t.Errorf("expected empty resolution, got %+v", got)
	}
}

func TestResolveDanglingCurrentContextErrors(t *testing.T) {
	cfg := &Config{CurrentContext: "gone", Contexts: map[string]*Context{}}
	if _, err := Resolve(cfg, "", ""); err == nil {
		t.Fatal("expected dangling current_context to error")
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "cli-config.json")

	cfg := testConfig()
	if err := SaveTo(path, cfg); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	// Config holds tokens: file must be 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file perm = %o, want 600", perm)
	}

	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got.CurrentContext != "prod" {
		t.Errorf("CurrentContext = %q", got.CurrentContext)
	}
	if len(got.Contexts) != 3 {
		t.Fatalf("contexts = %d, want 3", len(got.Contexts))
	}
	if got.Contexts["dev"].Hooks[0].On != "workflow_failed" || got.Contexts["dev"].Hooks[0].Cmd != "notify.sh" {
		t.Errorf("hooks did not round-trip: %+v", got.Contexts["dev"].Hooks)
	}
	if got.Contexts["prod"].Token != "rlnt_pat_prod" {
		t.Errorf("token did not round-trip")
	}
}

func TestLoadMissingFileYieldsEmptyConfig(t *testing.T) {
	got, err := LoadFrom(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got == nil || got.Contexts == nil || len(got.Contexts) != 0 {
		t.Errorf("expected empty config, got %+v", got)
	}
}

func TestLoadCorruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli-config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(path); err == nil {
		t.Fatal("expected corrupt config to error")
	}
}
