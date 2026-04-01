// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNPMAdapter_Parse_IncludesBuiltinCommands(t *testing.T) {
	// Create a temp directory with a package.json
	tmpDir := t.TempDir()
	packageJSON := `{
		"name": "test-project",
		"scripts": {
			"start": "node index.js",
			"test": "jest"
		}
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageJSON), 0644)
	if err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}

	adapter := NewNPMAdapter()
	commands, err := adapter.Parse(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Should have package.json scripts + built-in commands
	// 2 scripts + 9 built-in commands = 11 total
	if len(commands) < 11 {
		t.Errorf("expected at least 11 commands, got %d", len(commands))
	}

	// Verify package.json scripts are present
	scriptNames := map[string]bool{"start": false, "test": false}
	for _, cmd := range commands {
		if _, ok := scriptNames[cmd.Name]; ok {
			scriptNames[cmd.Name] = true
			if cmd.Source != filepath.Join(tmpDir, "package.json") {
				t.Errorf("script %q should have package.json source, got %q", cmd.Name, cmd.Source)
			}
		}
	}
	for name, found := range scriptNames {
		if !found {
			t.Errorf("expected to find script %q", name)
		}
	}

	// Verify built-in commands are present with correct source
	builtinCommands := map[string]struct {
		command  string
		category string
	}{
		"install":     {command: "npm install", category: "setup"},
		"ci":          {command: "npm ci", category: "setup"},
		"update":      {command: "npm update", category: "maintenance"},
		"outdated":    {command: "npm outdated", category: "maintenance"},
		"prune":       {command: "npm prune", category: "maintenance"},
		"dedupe":      {command: "npm dedupe", category: "maintenance"},
		"audit":       {command: "npm audit", category: "security"},
		"audit fix":   {command: "npm audit fix", category: "security"},
		"cache clean": {command: "npm cache clean --force", category: "cache"},
	}

	for _, cmd := range commands {
		if expected, ok := builtinCommands[cmd.Name]; ok {
			if cmd.Source != "npm (built-in)" {
				t.Errorf("built-in command %q should have source 'npm (built-in)', got %q", cmd.Name, cmd.Source)
			}
			if cmd.Command != expected.command {
				t.Errorf("built-in command %q should have command %q, got %q", cmd.Name, expected.command, cmd.Command)
			}
			if cmd.Category != expected.category {
				t.Errorf("built-in command %q should have category %q, got %q", cmd.Name, expected.category, cmd.Category)
			}
			if cmd.PackageType != PackageTypeNPM {
				t.Errorf("built-in command %q should have PackageType npm, got %q", cmd.Name, cmd.PackageType)
			}
			delete(builtinCommands, cmd.Name)
		}
	}

	if len(builtinCommands) > 0 {
		for name := range builtinCommands {
			t.Errorf("expected to find built-in command %q", name)
		}
	}
}

func TestNPMAdapter_Parse_EmptyScripts(t *testing.T) {
	// Create a temp directory with a package.json with no scripts
	tmpDir := t.TempDir()
	packageJSON := `{
		"name": "test-project"
	}`
	err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageJSON), 0644)
	if err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}

	adapter := NewNPMAdapter()
	commands, err := adapter.Parse(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Should still have built-in commands even with no scripts
	if len(commands) != 9 {
		t.Errorf("expected 9 built-in commands, got %d", len(commands))
	}

	// All commands should be built-in
	for _, cmd := range commands {
		if cmd.Source != "npm (built-in)" {
			t.Errorf("expected all commands to be built-in, got source %q for %q", cmd.Source, cmd.Name)
		}
	}
}

func TestGetBuiltinNPMCommands(t *testing.T) {
	commands := getBuiltinNPMCommands("/some/path/package.json")

	if len(commands) != 9 {
		t.Errorf("expected 9 built-in commands, got %d", len(commands))
	}

	// Verify all commands have required fields
	for _, cmd := range commands {
		if cmd.Name == "" {
			t.Error("command Name should not be empty")
		}
		if cmd.Description == "" {
			t.Errorf("command %q Description should not be empty", cmd.Name)
		}
		if cmd.Command == "" {
			t.Errorf("command %q Command should not be empty", cmd.Name)
		}
		if cmd.PackageType != PackageTypeNPM {
			t.Errorf("command %q PackageType should be npm, got %q", cmd.Name, cmd.PackageType)
		}
		if cmd.Source != "npm (built-in)" {
			t.Errorf("command %q Source should be 'npm (built-in)', got %q", cmd.Name, cmd.Source)
		}
		if cmd.Category == "" {
			t.Errorf("command %q Category should not be empty", cmd.Name)
		}
	}

	// Verify categories are valid
	validCategories := map[string]bool{
		"setup":       true,
		"maintenance": true,
		"security":    true,
		"cache":       true,
	}
	for _, cmd := range commands {
		if !validCategories[cmd.Category] {
			t.Errorf("command %q has invalid category %q", cmd.Name, cmd.Category)
		}
	}
}
