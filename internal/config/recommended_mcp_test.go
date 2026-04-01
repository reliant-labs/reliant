// Copyright (c) 2025 Reliant Labs
package config

import (
	"slices"
	"testing"
)

func TestLoadRecommendedMCPs(t *testing.T) {
	cfg, err := LoadRecommendedMCPs()
	if err != nil {
		t.Fatalf("Failed to load recommended MCPs: %v", err)
	}

	if len(cfg.Recommended) == 0 {
		t.Fatal("Expected at least one recommended MCP, got 0")
	}

	t.Logf("Loaded %d recommended MCPs:", len(cfg.Recommended))
	for _, rec := range cfg.Recommended {
		t.Logf("  - %s (%s): %s", rec.Name, rec.Category, rec.DisplayName)

		// Verify required fields
		if rec.Name == "" {
			t.Errorf("MCP has empty name")
		}
		if rec.DisplayName == "" {
			t.Errorf("MCP %s has empty display name", rec.Name)
		}
		if rec.Config.Type == "stdio" && rec.Config.Command == "" {
			t.Errorf("MCP %s has empty command for stdio transport", rec.Name)
		}
		if (rec.Config.Type == "sse" || rec.Config.Type == "http") && rec.Config.URL == "" {
			t.Errorf("MCP %s has empty URL for %s transport", rec.Name, rec.Config.Type)
		}

		// Test conversion to MCPServer
		server := rec.Config.ToMCPServer()
		if server.Command != rec.Config.Command {
			t.Errorf("Conversion failed for %s: command mismatch", rec.Name)
		}
	}
}

func TestRecommendedMCPs_NoSharedContext7Key(t *testing.T) {
	cfg, err := LoadRecommendedMCPs()
	if err != nil {
		t.Fatalf("Failed to load recommended MCPs: %v", err)
	}

	var context7 *RecommendedMCP
	for i := range cfg.Recommended {
		if cfg.Recommended[i].Name == "context7" {
			context7 = &cfg.Recommended[i]
			break
		}
	}
	if context7 == nil {
		t.Fatal("context7 recommended MCP not found")
	}

	if !context7.SetupRequired {
		t.Fatal("context7 should require setup")
	}

	fieldRequired := false
	for _, field := range context7.ConfigFields {
		if field.Key == "CONTEXT7_API_KEY" {
			fieldRequired = field.Required
		}
	}
	if !fieldRequired {
		t.Fatal("CONTEXT7_API_KEY field should be required")
	}

	if val := context7.Config.Env["CONTEXT7_API_KEY"]; val != "" {
		t.Fatalf("expected empty CONTEXT7_API_KEY default, got %q", val)
	}
}

func TestRecommendedMCPs_SerenaHasNoYFlag(t *testing.T) {
	cfg, err := LoadRecommendedMCPs()
	if err != nil {
		t.Fatalf("Failed to load recommended MCPs: %v", err)
	}

	for _, rec := range cfg.Recommended {
		if rec.Name != "serena" {
			continue
		}
		for _, arg := range rec.Config.Args {
			if arg == "-y" {
				t.Fatal("serena recommended config should not include -y for uvx")
			}
		}
		return
	}

	t.Fatal("serena recommended MCP not found")
}

func TestRecommendedMCPs_BundledSkillsSchema(t *testing.T) {
	cfg, err := LoadRecommendedMCPs()
	if err != nil {
		t.Fatalf("Failed to load recommended MCPs: %v", err)
	}

	var githubRec *RecommendedMCP
	for i := range cfg.Recommended {
		if cfg.Recommended[i].Name == "github" {
			githubRec = &cfg.Recommended[i]
			break
		}
	}
	if githubRec == nil {
		t.Fatal("github recommended MCP not found")
	}
	if githubRec.Bundles == nil || len(githubRec.Bundles.Skills) == 0 {
		t.Fatal("github recommended MCP should include bundled skills")
	}

	skill := githubRec.Bundles.Skills[0]
	if skill.Name == "" {
		t.Fatal("bundled skill should have a name")
	}
	if len(skill.Files) == 0 {
		t.Fatal("bundled skill should include files")
	}
	if skill.Files[0].Path == "" || skill.Files[0].Content == "" {
		t.Fatal("bundled skill file path/content must be non-empty")
	}
}

func TestRecommendedMCPs_CorrectedPackageDefinitions(t *testing.T) {
	cfg, err := LoadRecommendedMCPs()
	if err != nil {
		t.Fatalf("Failed to load recommended MCPs: %v", err)
	}

	required := map[string]func(t *testing.T, rec RecommendedMCP){
		"fetch": func(t *testing.T, rec RecommendedMCP) {
			t.Helper()
			if rec.Config.Command != "uvx" {
				t.Fatalf("fetch should use uvx, got %q", rec.Config.Command)
			}
			if !slices.Equal(rec.Config.Args, []string{"mcp-server-fetch"}) {
				t.Fatalf("fetch args mismatch: %#v", rec.Config.Args)
			}
		},
		"sqlite": func(t *testing.T, rec RecommendedMCP) {
			t.Helper()
			if rec.Config.Command != "bash" {
				t.Fatalf("sqlite should use bash wrapper, got %q", rec.Config.Command)
			}
			if !slices.Equal(rec.Config.Args, []string{"-lc", "uvx mcp-server-sqlite --db-path \"$SQLITE_DB_PATH\""}) {
				t.Fatalf("sqlite args mismatch: %#v", rec.Config.Args)
			}
			if v, ok := rec.Config.Env["SQLITE_DB_PATH"]; !ok || v != "" {
				t.Fatalf("sqlite should define empty SQLITE_DB_PATH env default, got present=%v val=%q", ok, v)
			}
		},
		"sentry": func(t *testing.T, rec RecommendedMCP) {
			t.Helper()
			if rec.Config.Command != "npx" {
				t.Fatalf("sentry should use npx, got %q", rec.Config.Command)
			}
			if !slices.Equal(rec.Config.Args, []string{"-y", "@sentry/mcp-server"}) {
				t.Fatalf("sentry args mismatch: %#v", rec.Config.Args)
			}
			if v, ok := rec.Config.Env["SENTRY_ACCESS_TOKEN"]; !ok || v != "" {
				t.Fatalf("sentry should define empty SENTRY_ACCESS_TOKEN env default, got present=%v val=%q", ok, v)
			}
		},
		"aws": func(t *testing.T, rec RecommendedMCP) {
			t.Helper()
			if rec.Config.Command != "uvx" {
				t.Fatalf("aws should use uvx, got %q", rec.Config.Command)
			}
			if !slices.Equal(rec.Config.Args, []string{"awslabs.aws-api-mcp-server@latest"}) {
				t.Fatalf("aws args mismatch: %#v", rec.Config.Args)
			}
			if v, ok := rec.Config.Env["AWS_REGION"]; !ok || v != "" {
				t.Fatalf("aws should define empty AWS_REGION env default, got present=%v val=%q", ok, v)
			}
		},
		"docker": func(t *testing.T, rec RecommendedMCP) {
			t.Helper()
			if rec.Config.Command != "uvx" {
				t.Fatalf("docker should use uvx, got %q", rec.Config.Command)
			}
			if !slices.Equal(rec.Config.Args, []string{"mcp-server-docker"}) {
				t.Fatalf("docker args mismatch: %#v", rec.Config.Args)
			}
		},
	}

	for name, assertRec := range required {
		found := false
		for _, rec := range cfg.Recommended {
			if rec.Name != name {
				continue
			}
			found = true
			assertRec(t, rec)
			break
		}
		if !found {
			t.Fatalf("%s recommended MCP not found", name)
		}
	}
}
