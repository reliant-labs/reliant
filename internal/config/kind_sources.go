package config

import (
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// deriveProjectKindFromSources determines the project kind by reading the
// project's REAL sources on disk — never a manifest, never an authored
// forge.yaml bit. Every signal is a directory or file the scaffold and the
// add verbs actually write, so the kind cannot drift from the tree:
//
//   - the KCL deploy tree (deploy/kcl/), the service composition root /
//     registry (pkg/app/), the service implementations
//     (internal/handlers/), or the service protos (proto/services/) →
//     service. Any one of them is enough: forge emits them only for service
//     projects, so even a zero-service scaffold reads as a service, which is
//     exactly what it is — a shell waiting for `forge scaffold service`.
//   - otherwise a cmd/<name>/main.go binary → a CLI;
//   - nothing of the above → a pure library (a Go module with no entrypoint).
//
// projectDir is the directory holding forge.yaml.
func deriveProjectKindFromSources(projectDir string) string {
	serviceSources := []string{
		filepath.Join(projectDir, "deploy", "kcl"),        // KCL deploy tree
		filepath.Join(projectDir, "pkg", "app"),           // composition root / registry home
		filepath.Join(projectDir, "internal", "handlers"), // service impls + contract.go
		filepath.Join(projectDir, "proto", "services"),    // service protos
	}
	for _, d := range serviceSources {
		if dirExists(d) {
			return ProjectKindService
		}
	}

	// Not service-shaped. A cmd/<name>/main.go binary is a CLI; anything
	// else is a library.
	if hasCmdBinary(projectDir) {
		return ProjectKindCLI
	}
	return ProjectKindLibrary
}

// hasCmdBinary reports whether projectDir carries a cmd/<name>/main.go — the
// real entrypoint of a CLI (or service) binary. Best-effort: a missing cmd/
// tree yields false.
func hasCmdBinary(projectDir string) bool {
	entries, err := os.ReadDir(filepath.Join(projectDir, "cmd"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(projectDir, "cmd", e.Name(), "main.go")) {
			return true
		}
	}
	return false
}

// sourceProjectDir returns the directory to read real sources from, or "" when
// there is no on-disk project to read (byte-only loads in tests pass a
// synthetic path). It requires the forge.yaml itself to exist so we never
// probe an unrelated cwd for a hand-constructed config.
func sourceProjectDir(forgeYAMLPath string) string {
	if forgeYAMLPath == "" {
		return ""
	}
	if _, err := os.Stat(forgeYAMLPath); err != nil {
		return ""
	}
	return filepath.Dir(forgeYAMLPath)
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// hasProtos reports whether the project has .proto files: under proto/, or
// under any module a buf.yaml declares. This is the evidence for codegen — a
// buf run over a module with no files fails ("had no .proto files"), so a
// project that has none must not be told to run one. With no on-disk project
// the kind stands in.
func (c *ProjectConfig) hasProtos() bool {
	if c.projectDir == "" {
		return c.IsServiceKind()
	}
	for _, rel := range append([]string{"proto"}, bufModulePaths(c.projectDir)...) {
		if dirHasProtoFile(filepath.Join(c.projectDir, filepath.FromSlash(rel))) {
			return true
		}
	}
	return false
}

// hasServerSources reports whether the project carries a server's own
// sources (the composition root, handlers, or service protos) — the evidence
// for the features that only make sense for a long-running server. A tree
// that is only deploy/kcl is not one.
func (c *ProjectConfig) hasServerSources() bool {
	if c.projectDir == "" {
		return c.IsServiceKind()
	}
	for _, rel := range []string{"pkg/app", "internal/handlers", "proto/services"} {
		if dirExists(filepath.Join(c.projectDir, filepath.FromSlash(rel))) {
			return true
		}
	}
	return false
}

// hasDeployTree reports whether deploy/kcl exists: the evidence for deploy.
func (c *ProjectConfig) hasDeployTree() bool {
	if c.projectDir == "" {
		return c.IsServiceKind()
	}
	return dirExists(filepath.Join(c.projectDir, "deploy", "kcl"))
}

// HasGoModule reports whether the project is a Go module: a go.mod at its
// root. A static site — its frontends and their deploy envs, nothing else
// (`forge project new --kind site`) — has none, so every Go step (codegen,
// go mod tidy, goimports, the compile check, golangci-lint, the contract
// linter) has nothing to act on and is skipped, naming this reason, rather
// than failing on `pattern ./...`. A site that grows a backend gains a go.mod
// and every one of those steps with it.
//
// With no on-disk project (a byte-only load in a test) the answer is yes:
// every kind but a site is a Go module, and a site is only ever read from disk.
func (c *ProjectConfig) HasGoModule() bool {
	if c.projectDir == "" {
		return true
	}
	return fileExists(filepath.Join(c.projectDir, "go.mod"))
}

// bufModulePaths returns the module paths declared in buf.yaml (v2
// `modules[].path`), or nil when there is no buf.yaml or it does not parse.
func bufModulePaths(projectDir string) []string {
	data, err := os.ReadFile(filepath.Join(projectDir, "buf.yaml"))
	if err != nil {
		return nil
	}
	var doc struct {
		Modules []struct {
			Path string `yaml:"path"`
		} `yaml:"modules"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []string
	for _, m := range doc.Modules {
		if m.Path != "" {
			out = append(out, m.Path)
		}
	}
	return out
}

// dirHasProtoFile reports whether any .proto file lives under dir.
func dirHasProtoFile(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".proto") {
			found = true
		}
		return nil
	})
	return found
}
