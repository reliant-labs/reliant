package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/codegen"
)

// runDescriptorGenerate runs buf generate with the protoc-gen-forge plugin
// (mode=descriptor) to extract service and config data from all proto files
// into the descriptor directory, gen/forge_descriptor/ (one file per proto;
// codegen.WriteDescriptor). Progress goes to out: stdout under `forge
// generate`, stderr when a reader extracts on demand (buildDescriptorOnDemand),
// so a command's own stdout (`--json`) stays clean.
//
// Every run stages in a private temporary directory, with a private buf
// template, so concurrent extractions — two on-demand readers, or a reader
// beside `forge generate` — never see each other's fragments.
func runDescriptorGenerate(projectDir string, out io.Writer) error {
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

	// A staging dir an older forge left inside gen/.
	_ = os.RemoveAll(filepath.Join(projectDir, "gen", descriptorStageDir))

	forgeCmd, err := forgeExecCommand()
	if err != nil {
		return fmt.Errorf("resolve forge binary: %w", err)
	}

	_, _ = fmt.Fprintln(out, "🔨 Running protoc-gen-forge (mode=descriptor) to extract proto metadata...")

	pluginArgs := slices.Concat(forgeCmd, []string{"protoc-gen-forge"})
	quoted := make([]string, len(pluginArgs))
	for i, a := range pluginArgs {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	pluginCmd := "[" + strings.Join(quoted, ", ") + "]"

	work, err := os.MkdirTemp("", "forge-descriptor-")
	if err != nil {
		return fmt.Errorf("descriptor staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	descConfig := descriptorBufTemplate(pluginCmd, filepath.Join(work, "out"), work)
	template := filepath.Join(work, "buf.gen.descriptor.yaml")
	if err := atomicfile.WriteFile(template, []byte(descConfig), 0o644); err != nil {
		return fmt.Errorf("failed to write descriptor buf config: %w", err)
	}

	args := bufGenerateArgs("--template", template)
	for _, p := range protoPaths {
		args = append(args, "--path", p)
	}

	cmd := exec.Command("buf", args...)
	cmd.Dir = projectDir
	if err := withPinnedTools(cmd, projectDir); err != nil {
		return err
	}
	cmd.Stdout = out
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("descriptor generation failed: %w", err)
	}

	// Post-condition: proto files exist, so the plugin must have run, and
	// every plugin invocation creates the staging dir even when it stages
	// nothing. A missing one means the self-invoked plugin silently never
	// executed — fail loudly instead of letting downstream codegen quietly
	// emit nothing.
	if _, err := os.Stat(filepath.Join(work, descriptorStageDir)); err != nil {
		return fmt.Errorf("buf generate reported success but protoc-gen-forge (invoked as %s) did not run: no descriptor fragments were staged", pluginCmd)
	}

	// Each plugin process staged one fragment per proto; write them out as
	// gen/forge_descriptor/ now that buf has finished. This step is what
	// makes parallel buf-plugin invocations safe — one process writes.
	if err := MergeDescriptorFragments(work, projectDir); err != nil {
		return fmt.Errorf("merge descriptor fragments: %w", err)
	}

	_, _ = fmt.Fprintln(out, "  ✅ "+codegen.DescriptorDir+"/ generated")
	return nil
}

// descriptorBufTemplate is the throwaway buf.gen.descriptor.yaml that runs
// protoc-gen-forge (pluginCmd, a YAML flow list) in descriptor mode, staging
// fragments under descriptorOut. `strategy: all` makes it one protoc-gen-forge
// process — a whole forge binary — instead of one per proto directory, all at
// once; fragments are per proto file, so the descriptor is the same either
// way. See bufStrategyAllComment.
func descriptorBufTemplate(pluginCmd, out, descriptorOut string) string {
	return fmt.Sprintf(`version: v2
plugins:
  - local: %s
    out: %s
    strategy: all
    opt:
      - mode=descriptor
      - descriptor_out=%s
`, pluginCmd, out, descriptorOut)
}

// buildDescriptorOnDemand is the extractor codegen.LoadDescriptor runs when a
// reader finds the descriptor missing or stale (registered by Execute).
func buildDescriptorOnDemand(projectDir string) error {
	return runDescriptorGenerate(projectDir, os.Stderr)
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
