package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// runDescriptorGenerate runs buf generate with the protoc-gen-forge plugin (mode=descriptor)
// to extract service, entity, and config data from all proto files into forge_descriptor.json.
func runDescriptorGenerate(projectDir string) error {
	// Collect every proto/<sub>/ that contains .proto files. This includes
	// the canonical {services,api,db,config} dirs plus any scaffolded
	// proto trees (e.g. proto/audit/). Without
	// the broader walk, those services are invisible to the descriptor
	// and downstream codegen (frontend hooks, mocks, etc).
	protoPaths := discoverProtoSubdirs(projectDir)

	if len(protoPaths) == 0 {
		return nil // Nothing to extract
	}

	// Vendor forge/v1/forge.proto and buf/validate/validate.proto if any
	// proto imports them, so the descriptor buf run resolves the forge
	// annotations and buf.validate rules the extractor reads (config
	// options, fieldConstraintsFromDescriptor). This is the first buf run of
	// a generate, so it is where an adopted project's first config proto
	// gets its forge.proto.
	if err := ensureVendoredProtos(projectDir); err != nil {
		return fmt.Errorf("vendor annotation protos: %w", err)
	}

	// Remove stale descriptor + any leftover staging fragments so the
	// merge step in MergeDescriptorFragments only sees fragments from
	// this invocation.
	_ = os.Remove(filepath.Join(projectDir, "gen", "forge_descriptor.json"))
	_ = os.RemoveAll(filepath.Join(projectDir, "gen", descriptorStageDir))

	forgeCmd, err := forgeExecCommand()
	if err != nil {
		return fmt.Errorf("resolve forge binary: %w", err)
	}

	fmt.Println("🔨 Running protoc-gen-forge (mode=descriptor) to extract proto metadata...")

	pluginArgs := slices.Concat(forgeCmd, []string{"protoc-gen-forge"})
	quoted := make([]string, len(pluginArgs))
	for i, a := range pluginArgs {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	pluginCmd := "[" + strings.Join(quoted, ", ") + "]"

	absProjectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}
	genDir := filepath.Join(absProjectDir, "gen")
	descConfig := fmt.Sprintf(`version: v2
plugins:
  - local: %s
    out: gen
    opt:
      - mode=descriptor
      - descriptor_out=%s
`, pluginCmd, genDir)
	tmpFile := filepath.Join(projectDir, "buf.gen.descriptor.yaml")
	if err := atomicfile.WriteFile(tmpFile, []byte(descConfig), 0644); err != nil {
		return fmt.Errorf("failed to write descriptor buf config: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile) }()

	args := []string{"generate", "--template", "buf.gen.descriptor.yaml"}
	for _, p := range protoPaths {
		args = append(args, "--path", p)
	}

	cmd := exec.Command("buf", args...)
	cmd.Dir = projectDir
	if err := withPinnedTools(cmd, projectDir); err != nil {
		return err
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("descriptor generation failed: %w", err)
	}

	// Each plugin process wrote a per-invocation fragment under
	// gen/.descriptor.d/; merge them into gen/forge_descriptor.json now
	// that buf has finished. This step is what makes parallel buf-plugin
	// invocations safe — the merge happens in a single parent process.
	if err := MergeDescriptorFragments(filepath.Join(projectDir, "gen")); err != nil {
		return fmt.Errorf("merge descriptor fragments: %w", err)
	}

	// Post-condition: proto files exist, so the plugin must have run and
	// the merged descriptor must be on disk (every plugin invocation
	// creates the staging dir even when its fragment is empty, and the
	// merge writes the file whenever the staging dir exists). A missing
	// descriptor here means the self-invoked plugin silently never
	// executed — fail loudly instead of letting downstream codegen
	// quietly emit nothing.
	descPath := filepath.Join(projectDir, "gen", "forge_descriptor.json")
	if _, err := os.Stat(descPath); err != nil {
		return fmt.Errorf("buf generate reported success but %s was not produced — protoc-gen-forge (invoked as %s) did not run", descPath, pluginCmd)
	}

	fmt.Println("  ✅ forge_descriptor.json generated")
	return nil
}

func hasProtoFilesInDir(root string) (bool, error) {
	if !dirExists(root) {
		return false, nil
	}

	hasProtoFiles := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".proto" {
			hasProtoFiles = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return false, err
	}
	return hasProtoFiles, nil
}

// hasSQLMigrations returns true if db/migrations/ contains at least one .sql file.
func hasSQLMigrations(projectDir string) bool {
	migDir := filepath.Join(projectDir, "db", "migrations")
	entries, err := os.ReadDir(migDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			return true
		}
	}
	return false
}
