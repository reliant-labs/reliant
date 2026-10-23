package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/forge/internal/config"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// openAPIPluginBinary is the name of the protoc-gen-connect-openapi
// binary on PATH. Exposed as a constant so the error path can include
// the install command and tests can assert against it.
const openAPIPluginBinary = "protoc-gen-connect-openapi"

// openAPIPluginInstallCmd is the canonical install instruction printed
// when the plugin is missing. We pin @latest rather than a specific
// version: the plugin is a stable opt-in projection of the user's
// proto and we don't want forge to lag behind upstream fixes.
const openAPIPluginInstallCmd = "go install github.com/sudorandom/protoc-gen-connect-openapi@latest"

// runOpenAPIGenerate runs `buf generate` with a synthesized
// buf.gen.openapi.yaml that invokes protoc-gen-connect-openapi for each
// service under proto/services/. Emits one yaml spec per service to
// `openapi/<service>.yaml` at the project root.
//
// Always uses a synthesized template (written + removed per run) so
// the user's hand-edited buf.gen.yaml stays the source of truth for
// Go stubs. This keeps the openapi step composable with `forge project upgrade`
// flipping the flag on for an existing project — no need to touch the
// main buf.gen.yaml in that case.
//
// Best-effort policy: the plugin is opt-in user infrastructure. Failure
// here is logged via the returned error but the caller (the pipeline
// step) keeps going so a missing plugin doesn't brick the rest of
// `forge generate`.
func runOpenAPIGenerate(projectDir string, cfg *config.ProjectConfig) error {
	if cfg == nil || !cfg.API.OpenAPI {
		return nil
	}

	// Pre-flight: the plugin is a user-installed Go binary. If it's not
	// on PATH, surface a clear remediation message rather than letting
	// buf emit its native "fork/exec: no such file" error which doesn't
	// mention the install command.
	if !isPluginAvailable(openAPIPluginBinary) {
		return fmt.Errorf("%s not found on PATH (required when api.openapi: true). "+
			"Install with: %s",
			openAPIPluginBinary, openAPIPluginInstallCmd)
	}

	// Discover service proto paths. The plugin emits one spec per proto
	// file by default; we scope each invocation to a single service dir
	// so the output filename matches the service.
	serviceDirs := discoverServiceProtoDirs(projectDir)
	if len(serviceDirs) == 0 {
		// Nothing to project — silent no-op. The flag may be on in a
		// project that hasn't added services yet.
		return nil
	}

	outDir := filepath.Join(projectDir, "openapi")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create openapi output dir: %w", err)
	}

	fmt.Println("🔨 Generating OpenAPI specs (protoc-gen-connect-openapi)...")

	// One buf invocation per service: protoc-gen-connect-openapi writes
	// the output yaml using the proto file's own base name, so scoping
	// the input via --path keeps each spec to its own service.
	for _, svc := range serviceDirs {
		if err := runOpenAPIForService(projectDir, svc); err != nil {
			// Surface as a warning per-service rather than aborting the
			// whole run — one bad service shouldn't block specs for the
			// others.
			fmt.Fprintf(os.Stderr, "  ⚠️  OpenAPI generation for %s failed: %v\n", svc, err)
			continue
		}
	}

	fmt.Println("  ✅ OpenAPI specs written to openapi/")
	return nil
}

// runOpenAPIForService invokes buf generate scoped to one service proto
// directory. Writes/removes a per-service ephemeral buf.gen.openapi.yaml
// — the template is small enough that re-writing per service is cheaper
// than threading state through a shared template.
func runOpenAPIForService(projectDir, serviceProtoDir string) error {
	tmpl := openapiBufTemplate("openapi")
	tmpPath := filepath.Join(projectDir, "buf.gen.openapi.yaml")
	if err := atomicfile.WriteFile(tmpPath, []byte(tmpl), 0o644); err != nil {
		return fmt.Errorf("write ephemeral openapi template: %w", err)
	}
	defer func() { _ = os.Remove(tmpPath) }()

	args := []string{"generate", "--template", "buf.gen.openapi.yaml", "--path", serviceProtoDir}
	cmd := exec.Command("buf", args...)
	cmd.Dir = projectDir
	if err := withPinnedTools(cmd, projectDir); err != nil {
		return err
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buf generate: %w", err)
	}
	return nil
}

// openapiBufTemplate returns the buf.gen.yaml v2 content that wires
// protoc-gen-connect-openapi. Kept as a function so the out: directory
// is parameterised (tests pin it; future call-sites might want a
// per-service subdirectory). The plugin emits a yaml per input proto.
func openapiBufTemplate(outDir string) string {
	return fmt.Sprintf(`version: v2
# Synthesized by forge when api.openapi: true. Removed after each
# `+"`forge generate`"+` run. Edit forge.yaml (api.openapi) to toggle;
# do not commit this file.
plugins:
  - local: %s
    out: %s
    opt:
      - format=yaml
      - base=connect
`, openAPIPluginBinary, outDir)
}

// discoverServiceProtoDirs returns project-relative paths of every
// immediate subdirectory under proto/services/ that holds at least one
// .proto file (at any nesting depth — hasProtoFilesInDir walks
// recursively, so both flat `proto/services/<svc>/<svc>.proto` and
// canonical `proto/services/<svc>/v<n>/<svc>.proto` layouts surface at
// the service-dir level). Sorted for determinism. Returns nil when
// proto/services is absent — that's the canonical "no services" signal
// and the caller no-ops on an empty result.
//
// We scope each buf invocation to the service-dir (not the nested
// version dir) because buf's --path is recursive and the plugin names
// its output after the proto file's base name — `users.proto` becomes
// `openapi/users.yaml` whether it lives at `proto/services/users/` or
// `proto/services/users/v1/`.
func discoverServiceProtoDirs(projectDir string) []string {
	root := filepath.Join(projectDir, "proto", "services")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join("proto", "services", e.Name())
		has, err := hasProtoFilesInDir(filepath.Join(projectDir, sub))
		if err != nil || !has {
			continue
		}
		dirs = append(dirs, filepath.ToSlash(sub))
	}
	sort.Strings(dirs)
	return dirs
}
