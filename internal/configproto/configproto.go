// Package configproto names where a project's typed config is declared: the
// proto file path, the proto package and the Go package its messages
// generate into.
//
// # Why the project's name is in all three
//
// A protobuf runtime keeps ONE process-wide registry, keyed on the file's
// path relative to its module root AND on every fully-qualified name the
// file declares. Go's registers each generated file at init and panics on a
// conflict ("proto: file config/v1/config.proto is already registered").
// So two forge projects whose config protos share a path or a package
// cannot be linked into one binary — and that is exactly what a project's
// generated pkg/config invites: another repo imports it at a pin and
// validates a rendered env against its Roots() (roots_gen.go).
//
// Every forge project used to declare `package config.v1` at
// proto/config/v1/config.proto, so every pair of them collided. The layout
// here derives the path and the package from the project's own name:
//
//	proto/<project>/config/v1/config.proto   package <project>.config.v1
//	<module>/gen/<project>/config/v1         Go package configv1
//
// <project> is [Slug] of forge.yaml's name — the same identity
// `<PROJECT>_CONFIG` is named from. Env var names, flags and KCL schema
// names do not depend on it: they come from the (forge.v1.config)
// annotations and the message names, never from the package.
//
// # One directory, one package
//
// Every config message of a project — the root config, each binary's, each
// frontend's — lives in [Layout.Dir] and declares [Layout.Package]. buf's
// STANDARD lint enforces PACKAGE_DIRECTORY_MATCH, so one package means one
// directory, and pkg/config/config_gen.go names every config message through
// one generated Go package.
//
//forge:exclude-contract: pure path/name derivations — data and functions, no collaborator to substitute
package configproto

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// RootFile is the base name of the file declaring the project's root config
// (AppConfig).
const RootFile = "config.proto"

// LegacyDir is where every forge project declared its config before the
// layout became project-unique, relative to the project root.
const LegacyDir = "proto/config/v1"

// LegacyPackage is the proto package every forge project's config shared.
const LegacyPackage = "config.v1"

// LegacyCodemod is the `forge fix` codemod that moves a project off the
// legacy layout.
const LegacyCodemod = "config-proto-package"

// Layout is one project's config proto layout.
type Layout struct {
	// Slug is the project's segment of the proto path and package.
	Slug string
}

// For returns the layout of the project named name (forge.yaml's name).
func For(name string) Layout { return Layout{Slug: Slug(name)} }

// ForProject returns the layout of the project at projectDir, named from
// its forge.yaml, or from its go.mod module path when forge.yaml names none
// (see [ProjectName]).
func ForProject(projectDir string) Layout {
	return For(ProjectName(projectDir, ModulePath(projectDir)))
}

// ProjectName is the project's name: forge.yaml's name, else the last
// element of modulePath that is not a major-version suffix ("v2"), else
// modulePath itself.
func ProjectName(projectDir, modulePath string) string {
	if pc, err := config.LoadProjectDir(projectDir); err == nil && pc.Name != "" {
		return pc.Name
	}
	parts := strings.Split(modulePath, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := parts[i]; p != "" && !isMajorVersionSuffix(p) {
			return p
		}
	}
	if modulePath != "" {
		return modulePath
	}
	return filepath.Base(projectDir)
}

// Slug is name as a proto package segment: naming.GoPackage's rule (the one
// forge's handler directories and KCL bindings already follow —
// "control-plane" is control_plane), restricted to [a-z0-9_] and starting
// with a letter.
func Slug(name string) string {
	var b strings.Builder
	for _, r := range naming.GoPackage(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "app" + s
	}
	return s
}

// BufDir is the config directory relative to the buf module root (proto/),
// as protoc and the protobuf registry see it: "<slug>/config/v1".
func (l Layout) BufDir() string { return path.Join(l.Slug, "config", "v1") }

// Dir is the config directory relative to the project root:
// "proto/<slug>/config/v1".
func (l Layout) Dir() string { return path.Join("proto", l.BufDir()) }

// RootPath is the root config file relative to the project root.
func (l Layout) RootPath() string { return path.Join(l.Dir(), RootFile) }

// Package is the proto package every config message declares:
// "<slug>.config.v1".
func (l Layout) Package() string { return l.Slug + ".config.v1" }

// GoImportPath is the Go package the config messages generate into.
func (l Layout) GoImportPath(module string) string {
	return module + "/gen/" + l.BufDir()
}

// GoPackage is the go_package option value every config proto declares.
func (l Layout) GoPackage(module string) string {
	return l.GoImportPath(module) + ";configv1"
}

// TemplateData is what a config proto template (config.proto.tmpl,
// frontend_config.proto.tmpl) needs to declare the layout's package.
type TemplateData struct {
	// ConfigPackage is the proto package ("acme.config.v1").
	ConfigPackage string
	// ConfigGoPackage is the go_package option value.
	ConfigGoPackage string
}

// TemplateData returns the render input naming the layout's package.
func (l Layout) TemplateData(module string) TemplateData {
	return TemplateData{ConfigPackage: l.Package(), ConfigGoPackage: l.GoPackage(module)}
}

// legacyPackageRE matches a proto file's `package config.v1;` declaration.
var legacyPackageRE = regexp.MustCompile(`(?m)^\s*package\s+config\.v1\s*;`)

// LegacyFiles lists the .proto files under projectDir's LegacyDir that
// declare LegacyPackage, relative to projectDir, in lexical order. Empty
// when the project is on the project-unique layout (or has no config).
func LegacyFiles(projectDir string) []string {
	entries, err := os.ReadDir(filepath.Join(projectDir, filepath.FromSlash(LegacyDir)))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".proto") {
			continue
		}
		rel := path.Join(LegacyDir, e.Name())
		data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		if legacyPackageRE.Match(data) {
			out = append(out, rel)
		}
	}
	return out
}

// HasProtos reports whether the layout's directory under projectDir holds a
// .proto file.
func (l Layout) HasProtos(projectDir string) bool {
	entries, err := os.ReadDir(filepath.Join(projectDir, filepath.FromSlash(l.Dir())))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".proto") {
			return true
		}
	}
	return false
}

// ModulePath reads the module path from projectDir's go.mod, "" when it has
// none.
func ModulePath(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// isMajorVersionSuffix reports whether a module path element is a Go
// major-version suffix ("v2", "v10").
func isMajorVersionSuffix(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}
