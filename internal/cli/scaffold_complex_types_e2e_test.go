//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EScaffoldConfigNaming verifies that the generated config struct
// field names match what the templates reference. This is a regression test
// for the DatabaseURL vs DatabaseUrl naming mismatch.
func TestE2EScaffoldConfigNaming(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin,
		"project", "new", "cfgtest",
		"--mod", "example.com/cfgtest",
		"--service", "api",
	)

	projectDir := filepath.Join(dir, "cfgtest")
	addCorpusForgePkgReplace(t, projectDir)

	// Generate code (produces pkg/config/config.go from config.proto)
	runCmd(t, projectDir, forgeBin, "generate")

	// Read the generated config and all templates that reference config fields.
	configGo := readFileE2E(t, filepath.Join(projectDir, "pkg", "config", "config_gen.go"))

	// Find all config field names referenced in cmd/ files.
	cmdFiles := []string{"cmd/server.go", "cmd/db.go"}
	for _, rel := range cmdFiles {
		cmdPath := filepath.Join(projectDir, rel)
		if !fileExists(cmdPath) {
			continue
		}
		cmdContent := readFileE2E(t, cmdPath)

		// Extract cfg.Xxx references from template-generated code.
		// If the template uses cfg.DatabaseURL but config.go defines
		// cfg.DatabaseUrl, the build will fail — but this test catches
		// the mismatch with a clear error message before build.
		for _, field := range extractConfigFieldRefs(cmdContent) {
			if !strings.Contains(configGo, field) {
				t.Errorf("%s references cfg.%s but pkg/config/config.go does not define it.\n"+
					"This is likely a naming mismatch between the template and the config generator.\n"+
					"Config struct fields:\n%s",
					rel, field, extractStructFields(configGo, "Config"))
			}
		}
	}

	// Also verify the setup.go file if it exists.
	setupPath := filepath.Join(projectDir, "pkg", "app", "setup.go")
	if fileExists(setupPath) {
		setupContent := readFileE2E(t, setupPath)
		for _, field := range extractConfigFieldRefs(setupContent) {
			if !strings.Contains(configGo, field) {
				t.Errorf("pkg/app/setup.go references cfg.%s but pkg/config/config.go does not define it",
					field)
			}
		}
	}

	// go mod tidy + build as a final assertion.
	runCmd(t, projectDir, "go", "mod", "tidy")
	runCmd(t, filepath.Join(projectDir, "gen"), "go", "mod", "tidy")
	runCmd(t, projectDir, "go", "build", "./...")
}

// TestE2EScaffoldNoConflictingProtos verifies that `forge project new` does not
// scaffold both proto/forge/v1/forge.proto AND proto/forge/options/v1/*.proto,
// which would produce conflicting extension tag numbers and break buf generate.
func TestE2EScaffoldNoConflictingProtos(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin,
		"project", "new", "protocheck",
		"--mod", "example.com/protocheck",
		"--service", "api",
	)

	projectDir := filepath.Join(dir, "protocheck")
	addCorpusForgePkgReplace(t, projectDir)

	// Check which proto annotation format was scaffolded.
	hasOptionsV1 := fileExists(filepath.Join(projectDir, "proto", "forge", "options", "v1", "entity.proto"))
	hasForgeV1 := fileExists(filepath.Join(projectDir, "proto", "forge", "v1", "forge.proto"))

	if hasOptionsV1 && hasForgeV1 {
		t.Fatal("scaffold created BOTH proto/forge/options/v1/*.proto AND proto/forge/v1/forge.proto — " +
			"these have conflicting extension tag numbers and will break buf generate. " +
			"Only one annotation format should be scaffolded.")
	}

	if !hasOptionsV1 && !hasForgeV1 {
		t.Fatal("scaffold created neither proto/forge/options/v1/ nor proto/forge/v1/ — " +
			"at least one annotation format must be scaffolded for forge generate to work")
	}

	// Whichever format was scaffolded, verify generate + build works.
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "mod", "tidy")
	runCmd(t, filepath.Join(projectDir, "gen"), "go", "mod", "tidy")
	runCmd(t, projectDir, "go", "build", "./...")
}

// ── Helpers ────────────────────────────────────────────────────────────────

// extractConfigFieldRefs finds all `cfg.XxxYyy` references in Go source
// and returns the field names (e.g. "DatabaseURL", "Port").
func extractConfigFieldRefs(content string) []string {
	var fields []string
	seen := make(map[string]bool)

	// Simple scanner: find "cfg." followed by an uppercase identifier.
	for i := 0; i < len(content)-4; i++ {
		if content[i:i+4] != "cfg." {
			continue
		}
		j := i + 4
		if j >= len(content) || content[j] < 'A' || content[j] > 'Z' {
			continue
		}
		end := j
		for end < len(content) && (content[end] >= 'a' && content[end] <= 'z' ||
			content[end] >= 'A' && content[end] <= 'Z' ||
			content[end] >= '0' && content[end] <= '9' ||
			content[end] == '_') {
			end++
		}
		field := content[j:end]
		if !seen[field] {
			seen[field] = true
			fields = append(fields, field)
		}
	}
	return fields
}

// extractStructFields extracts the field declaration block from a Go struct
// for diagnostic output in test failure messages.
func extractStructFields(content string, structName string) string {
	marker := "type " + structName + " struct {"
	idx := strings.Index(content, marker)
	if idx < 0 {
		return "(struct not found)"
	}
	// Find the closing brace.
	depth := 0
	start := idx
	for i := idx; i < len(content); i++ {
		if content[i] == '{' {
			depth++
		} else if content[i] == '}' {
			depth--
			if depth == 0 {
				return content[start : i+1]
			}
		}
	}
	// Truncate if we can't find the end.
	end := start + 500
	if end > len(content) {
		end = len(content)
	}
	return content[start:end] + "…"
}
