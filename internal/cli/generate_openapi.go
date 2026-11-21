package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/forge/internal/config"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// openAPIPluginBinary is the name of the protoc-gen-connect-openapi
// binary. Exposed as a constant so the error path can include the install
// command and tests can assert against it.
const openAPIPluginBinary = "protoc-gen-connect-openapi"

// openAPIPluginPinnedVersion is the protoc-gen-connect-openapi release forge
// runs for api.openapi: true. The spec is committed output, so — like the Go
// codegen plugins go.mod pins — one version on every machine keeps a
// regenerate byte-identical. A PATH build of exactly this version is used
// as is; anything else is replaced, for forge's child processes only, by
// this version installed into forge's tool cache.
const openAPIPluginPinnedVersion = "v0.30.0"

// openAPITool is the plugin's module. It moved from
// github.com/sudorandom/protoc-gen-connect-openapi to connectrpc.com in
// v0.18; `go install` of the old path now fails on the module-path
// mismatch.
var openAPITool = protoTool{
	Binary:        openAPIPluginBinary,
	Module:        "connectrpc.com/protoc-gen-connect-openapi",
	VersionModule: "connectrpc.com/protoc-gen-connect-openapi",
}

// openAPIPluginInstallCmd is the install instruction printed when forge
// cannot provision the plugin itself (no network, no go toolchain).
const openAPIPluginInstallCmd = "go install connectrpc.com/protoc-gen-connect-openapi@" + openAPIPluginPinnedVersion

// Seams for tests: where a plugin on PATH is found, and how the pinned one is
// installed into forge's tool cache (returns the directory holding it).
var (
	openAPILookPath         = exec.LookPath
	installOpenAPIPluginFn  = installPinnedOpenAPIPlugin
	openAPIPluginVersionFor = func(ctx context.Context, binPath string) (string, bool) {
		return toolMatches(ctx, binPath, openAPITool, openAPIPluginPinnedVersion)
	}
)

func installPinnedOpenAPIPlugin(ctx context.Context, have string) (string, error) {
	dir, err := toolCacheDir(openAPITool, openAPIPluginPinnedVersion)
	if err != nil {
		return "", err
	}
	if _, err := installPinnedTool(ctx, openAPITool, openAPIPluginPinnedVersion, dir, have, func() {
		fmt.Fprintf(os.Stderr, "📌 installing %s %s (%s) into forge's tool cache (one time) → %s\n",
			openAPIPluginBinary, openAPIPluginPinnedVersion, have, dir)
	}); err != nil {
		return "", err
	}
	return dir, nil
}

// resolveOpenAPIPlugin returns the directory to put first on the buf child's
// PATH so it runs the pinned plugin, or "" when the PATH build already is the
// pinned version. When the pinned build cannot be installed, a plugin of
// another version on PATH is used with a warning (its spec may differ from
// the pinned one's); with none on PATH it is an error naming the install
// command.
func resolveOpenAPIPlugin(ctx context.Context) (string, error) {
	have := "not found on PATH"
	pathBin, lookErr := openAPILookPath(openAPIPluginBinary)
	if lookErr == nil {
		v, ok := openAPIPluginVersionFor(ctx, pathBin)
		if ok {
			return "", nil
		}
		have = "PATH has " + v
	}
	dir, err := installOpenAPIPluginFn(ctx, have)
	if err == nil {
		return dir, nil
	}
	if lookErr == nil {
		fmt.Fprintf(os.Stderr, "  ⚠️  could not install %s %s (%s); using %s from PATH (%s)\n",
			openAPIPluginBinary, openAPIPluginPinnedVersion, firstLine(err.Error()), pathBin, have)
		return "", nil
	}
	return "", fmt.Errorf("%s is required when api.openapi: true; it is not on PATH and forge could not install %s: %w\n"+
		"  fix: %s (then make sure $(go env GOBIN), default $HOME/go/bin, is on PATH)",
		openAPIPluginBinary, openAPIPluginPinnedVersion, err, openAPIPluginInstallCmd)
}

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
// The project opted in, so a spec that cannot be written fails the step
// (and generate) like any other generate step. Every service is still
// attempted, and the error names each one that failed.
func runOpenAPIGenerate(projectDir string, cfg *config.ProjectConfig) error {
	if cfg == nil || !cfg.API.OpenAPI {
		return nil
	}

	// Discover service proto paths. Each invocation is scoped to a single
	// service dir, and writes one merged spec named after it.
	serviceDirs := discoverServiceProtoDirs(projectDir)
	if len(serviceDirs) == 0 {
		// Nothing to project — silent no-op. The flag may be on in a
		// project that hasn't added services yet.
		return nil
	}

	pluginDir, err := resolveOpenAPIPlugin(context.Background())
	if err != nil {
		return err
	}

	outDir := filepath.Join(projectDir, "openapi")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create openapi output dir: %w", err)
	}

	fmt.Println("🔨 Generating OpenAPI specs (protoc-gen-connect-openapi)...")

	var errs []error
	for _, svc := range serviceDirs {
		if err := runOpenAPIForService(projectDir, svc, pluginDir); err != nil {
			fmt.Fprintf(os.Stderr, "  ❌ OpenAPI generation for %s failed: %v\n", svc, err)
			errs = append(errs, fmt.Errorf("%s: %w", svc, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d of %d OpenAPI specs not written: %w", len(errs), len(serviceDirs), errors.Join(errs...))
	}

	fmt.Println("  ✅ OpenAPI specs written to openapi/")
	return nil
}

// openAPISpecName is the file, relative to openapi/, that the spec of the
// service under serviceProtoDir (proto/services/<svc>) is written to.
func openAPISpecName(serviceProtoDir string) string {
	return path.Base(filepath.ToSlash(serviceProtoDir)) + ".yaml"
}

// runOpenAPIForService invokes buf generate scoped to one service proto
// directory. Writes/removes a per-service ephemeral buf.gen.openapi.yaml
// — the template is small enough that re-writing per service is cheaper
// than threading state through a shared template.
func runOpenAPIForService(projectDir, serviceProtoDir, pluginDir string) error {
	spec := openAPISpecName(serviceProtoDir)
	tmpl := openapiBufTemplate("openapi", spec)
	tmpPath := filepath.Join(projectDir, "buf.gen.openapi.yaml")
	if err := atomicfile.WriteFile(tmpPath, []byte(tmpl), 0o644); err != nil {
		return fmt.Errorf("write ephemeral openapi template: %w", err)
	}
	defer func() { _ = os.Remove(tmpPath) }()

	args := bufGenerateArgs("--template", "buf.gen.openapi.yaml", "--path", serviceProtoDir)
	cmd := exec.Command("buf", args...)
	cmd.Dir = projectDir
	if err := withPinnedTools(cmd, projectDir); err != nil {
		return err
	}
	if pluginDir != "" {
		cmd.Env = pathPrepended(cmd.Env, []string{pluginDir})
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buf generate: %w", err)
	}
	// buf exits 0 when the plugin writes nothing; the spec is what was asked for.
	if _, err := os.Stat(filepath.Join(projectDir, "openapi", spec)); err != nil {
		return fmt.Errorf("buf generate succeeded but wrote no openapi/%s: %w", spec, err)
	}
	return nil
}

// openapiBufTemplate returns the buf.gen.yaml v2 content that wires
// protoc-gen-connect-openapi. `path=` merges every proto of the scoped
// service into one spec, written to <outDir>/<spec>; without it the plugin
// writes one <file>.openapi.yaml per proto, nested under its package path.
// (`base=` names a base-spec FILE to merge in, never a protocol.)
func openapiBufTemplate(outDir, spec string) string {
	return fmt.Sprintf(`version: v2
# Synthesized by forge when api.openapi: true. Removed after each
# `+"`forge generate`"+` run. Edit forge.yaml (api.openapi) to toggle;
# do not commit this file.
plugins:
  - local: %s
    out: %s
    strategy: all
    opt:
      - format=yaml
      - path=%s
`, openAPIPluginBinary, outDir, spec)
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
