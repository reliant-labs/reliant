package cli

// generate_frontend_hooks_imported.go — React Query hooks for the services of
// an IMPORTED proto module: a buf module outside the project's own proto/
// tree, named by a frontend's package.json `forge.imported_hooks`
// (internal/config/frontend_codegen.go).
//
// The case is a frontend that calls another repository's API through a
// vendored copy of its public contract. reliant's web app calls ~30
// control-plane services whose protos are a `buf export` under
// proto-vendor/controlplane/ — deliberately not a module of reliant's
// buf.yaml, so reliant's lint and breaking checks never judge another repo's
// contract. Their hooks were hand-written, ~200 of them, because the hook
// generator only saw the project's own descriptor.
//
// The module's services are extracted with the same protoc-gen-forge
// descriptor pass as the project's (stageDescriptorFragments), in the
// module's own directory so its own buf.yaml governs, and read IN MEMORY.
// They never reach gen/forge_descriptor/: handlers, mocks, mounts, CRUD,
// procedures_gen and audit must keep seeing only services this project
// serves. What differs from a project service's hooks:
//
//   - imports are rooted under the app's gen_dir (`@/gen/<gen_dir>/…_pb`),
//     where the app's own buf template writes the module's messages — forge
//     does not generate them;
//   - the file lands in src/hooks/<gen_dir>/, outside the src/hooks/index.ts
//     barrel, so an imported DaemonService's useGetDaemon collides with
//     neither the project's own DaemonService file nor its barrel exports;
//   - the query keys are rooted at the fully-qualified service name, so the
//     two DaemonServices never share a cache entry;
//   - no Unimplemented table is read: the backend lives in another
//     repository, and a same-named handler package here is a different
//     service.
//
// Routing an imported service to the origin that serves it is the app's
// connectClient (src/lib/connect.ts), which every generated hook calls.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// hasImportedHooks reports whether any frontend declares imported_hooks.
func hasImportedHooks(cfg *config.ProjectConfig) bool {
	if cfg == nil {
		return false
	}
	for _, fe := range cfg.Frontends {
		if len(fe.ImportedHooks) > 0 {
			return true
		}
	}
	return false
}

// generateImportedFrontendHooks writes the hooks of every frontend's
// imported_hooks entries. Each distinct proto_root is extracted once per run,
// however many frontends name it.
func generateImportedFrontendHooks(cfg *config.ProjectConfig, projectDir string, cs *checksums.FileChecksums) error {
	if !hasImportedHooks(cfg) {
		return nil
	}
	if cfg.IsFrontendWorkspacesEnabled() {
		return fmt.Errorf("package.json forge.imported_hooks is not supported with frontend workspaces " +
			"(the shared packages/hooks package): remove imported_hooks, or turn the workspaces layout off")
	}
	tmpl, err := parseHooksTemplate()
	if err != nil {
		return err
	}

	modules := map[string][]codegen.ServiceDef{}
	for _, fe := range cfg.Frontends {
		if len(fe.ImportedHooks) == 0 || !isHookFrontendType(fe.Type) {
			continue
		}
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			continue
		}
		hooksRel := filepath.Join(feDir, "src", "hooks")
		if skipExcludedDir(projectDir, hooksRel) {
			continue
		}
		for _, imp := range fe.ImportedHooks {
			services, seen := modules[imp.ProtoRoot]
			if !seen {
				services, err = extractImportedServices(projectDir, imp.ProtoRoot)
				if err != nil {
					return fmt.Errorf("frontend %s: imported_hooks %s: %w", fe.Name, imp.ProtoRoot, err)
				}
				modules[imp.ProtoRoot] = services
			}
			reportUnknownImportedServices(fe, imp, services)

			subRel := filepath.Join(hooksRel, filepath.FromSlash(imp.GenDir))
			if skipExcludedDir(projectDir, subRel) {
				continue
			}
			n, err := writeImportedHookFiles(tmpl, fe, imp, services, projectDir, subRel, cs)
			if err != nil {
				return err
			}
			fmt.Printf("  ✅ Generated %d imported hook file(s) for frontend %s (%s → %s/)\n",
				n, fe.Name, imp.ProtoRoot, filepath.ToSlash(subRel))
		}
	}
	return nil
}

// writeImportedHookFiles renders and writes the hooks of every allowlisted
// service of one imported module into subRel (project-relative), returning
// how many files it wrote.
func writeImportedHookFiles(tmpl *template.Template, fe config.FrontendConfig, imp config.ImportedHooks, services []codegen.ServiceDef, projectDir, subRel string, cs *checksums.FileChecksums) (int, error) {
	subDir := filepath.Join(projectDir, subRel)
	n := 0
	for _, svc := range services {
		if !imp.WantsService(qualifiedServiceName(svc)) {
			continue
		}
		data, err := importedServiceHookData(svc, projectDir, imp)
		if err != nil {
			return n, err
		}
		if len(data.Methods) == 0 {
			continue
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return n, fmt.Errorf("render hooks for %s: %w", qualifiedServiceName(svc), err)
		}
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			return n, fmt.Errorf("create hooks directory: %w", err)
		}
		fileName := naming.ServiceHookFile(svc.Name)
		outRel := filepath.Join(subRel, fileName)
		reportHookKindFlips(projectDir, outRel, data)
		if _, err := checksums.WriteGeneratedFile(projectDir, outRel, buf.Bytes(), cs, true); err != nil {
			return n, fmt.Errorf("write hooks file %s: %w", outRel, err)
		}
		if isWebFrontendType(fe.Type) {
			if err := writeHookStarterTest(subDir, fileName, svc, data); err != nil {
				return n, fmt.Errorf("write hook starter test for %s: %w", qualifiedServiceName(svc), err)
			}
		}
		n++
	}
	return n, nil
}

// importedServiceHookData is an imported service's hook template data: the
// proto comment markers read from the module's own source, imports rooted
// under the app's gen_dir, and query keys rooted at the fully-qualified name.
func importedServiceHookData(svc codegen.ServiceDef, projectDir string, imp config.ImportedHooks) (codegen.FrontendHookTemplateData, error) {
	data, err := hookDataWithMarkers(svc, importedProtoSourcePath(projectDir, imp, svc.ProtoFile))
	if err != nil {
		return data, err
	}
	data.PrefixImportPaths(imp.GenDir)
	data.KeyRoot = qualifiedServiceName(svc)
	return data, nil
}

// importedProtoSourcePath is the .proto on disk declaring an imported
// service: its descriptor path is relative to the module root, proto_root.
func importedProtoSourcePath(projectDir string, imp config.ImportedHooks, protoFile string) string {
	return filepath.Join(projectDir, filepath.FromSlash(imp.ProtoRoot), filepath.FromSlash(protoFile))
}

// extractImportedServices extracts the services of the buf module at
// protoRoot (project-relative) with the descriptor pass, in memory: nothing is
// written into the project. Services come in proto-path order.
func extractImportedServices(projectDir, protoRoot string) ([]codegen.ServiceDef, error) {
	// Config validation already refused an escaping root; checked again
	// here, at the moment buf would read it, with the same check the
	// generate cache uses, so extraction and fingerprint share one boundary.
	bufDir, err := config.ImportedProtoRootDir(projectDir, protoRoot)
	if err != nil {
		return nil, err
	}
	fmt.Printf("🔨 Running protoc-gen-forge (mode=descriptor) over %s (imported proto module, for frontend hooks)...\n", protoRoot)
	work, err := stageDescriptorFragments(bufDir, projectDir, nil, true, os.Stdout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	parts, err := readDescriptorFragments(work)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(parts))
	for f := range parts {
		files = append(files, f)
	}
	sort.Strings(files)
	var services []codegen.ServiceDef
	for _, f := range files {
		services = append(services, parts[f].Services...)
	}
	return services, nil
}

// reportUnknownImportedServices warns about imported_hooks services entries
// the module does not declare, as reportUnknownHookAllowlist does for hooks.
func reportUnknownImportedServices(fe config.FrontendConfig, imp config.ImportedHooks, services []codegen.ServiceDef) {
	known := make(map[string]bool, len(services))
	names := make([]string, 0, len(services))
	for _, svc := range services {
		known[qualifiedServiceName(svc)] = true
		names = append(names, qualifiedServiceName(svc))
	}
	if unknown := imp.UnknownServices(known); len(unknown) > 0 {
		sort.Strings(names)
		fmt.Printf("  ⚠️  frontend %s: package.json forge.imported_hooks (%s) services %v name no service of the module (known: %s)\n",
			fe.Name, imp.ProtoRoot, unknown, strings.Join(names, ", "))
	}
}
