package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
)

// A vendored annotation proto (forge/v1/forge.proto, buf/validate/
// validate.proto) is a buf COMPILE input only. Its Go already exists —
// forge/pkg/forgepb, the protovalidate runtime — and both register the
// descriptor file at init, so a project-local Go copy registers it a second
// time and every binary linking both panics before main:
//
//	proto: file "forge/v1/forge.proto" is already registered
//
// The templates forge writes keep it out with `inputs: exclude_paths`. A
// project that adopted forge keeps its OWN buf.gen.yaml, which may target all
// of proto/; before ensureVendoredProtos that was harmless, because nothing
// put forge.proto there. checkGoTemplateSkipsVendoredProtos is the guard for
// that template: it runs before forge hands it to `buf generate` and refuses,
// with the lines to add, a template whose Go plugin would generate one.

// bufGoTemplate is the slice of a buf.gen.yaml v2 this guard reads.
type bufGoTemplate struct {
	Version string           `yaml:"version"`
	Inputs  []map[string]any `yaml:"inputs"`
	Plugins []struct {
		Local        any      `yaml:"local"`
		Remote       string   `yaml:"remote"`
		ExcludeTypes []string `yaml:"exclude_types"`
	} `yaml:"plugins"`
}

// checkGoTemplateSkipsVendoredProtos refuses a buf template that would
// generate Go for a vendored annotation proto present in the project. label
// is the template as the user knows it (project-relative); body nil reads it
// from disk. A template that does not parse, or names no protoc-gen-go, is
// left to buf.
func checkGoTemplateSkipsVendoredProtos(projectDir, label string, body []byte) error {
	if body == nil {
		data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(label)))
		if err != nil {
			return nil
		}
		body = data
	}
	var tmpl bufGoTemplate
	if yaml.Unmarshal(body, &tmpl) != nil || strings.TrimSpace(tmpl.Version) != "v2" {
		return nil
	}

	var problems []string
	for _, v := range vendoredImports() {
		if _, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(v.relPath))); err != nil {
			continue // not vendored here: nothing for the template to generate
		}
		if projectOwnsGoPackage(projectDir, v.relPath) {
			continue // the file's Go home is this module: generating it is the point
		}
		if !templateTargets(tmpl.Inputs, v.relPath) {
			continue
		}
		for _, p := range tmpl.Plugins {
			if !isGoMessagePlugin(p.Local, p.Remote) || excludesPackage(p.ExcludeTypes, v.protoPackage) {
				continue
			}
			problems = append(problems, v.relPath)
			break
		}
	}
	if len(problems) == 0 {
		return nil
	}

	what := fmt.Sprintf("%s generates Go for %s: its protoc-gen-go targets the vendored copy. "+
		"Its Go already ships with forge (forge/pkg/forgepb) or protovalidate, and a second copy "+
		"registers the same descriptor file twice, so every binary would panic at init "+
		"(`proto: file %q is already registered`). `buf generate` was not started.",
		label, strings.Join(problems, " and "), strings.TrimPrefix(problems[0], "proto/"))
	fix := fmt.Sprintf("keep the vendored protos as imports only — target proto/ through an input that excludes them, in %s:\n\n"+
		"    inputs:\n"+
		"      - directory: proto\n"+
		"        exclude_paths:\n"+
		"          - proto/forge/v1\n"+
		"          - proto/buf/validate\n\n"+
		"  (or exclude the directory from whichever `inputs:` entry already covers it), then re-run generate", label)
	return cliutil.UserErr(Name()+" generate (buf template pre-flight)", what, "", fix)
}

// goPackageRE reads a proto file's `option go_package` import path (the part
// before any `;name`).
var goPackageRE = regexp.MustCompile(`(?m)^option go_package = "([^";]*)`)

// projectOwnsGoPackage reports whether the project is the Go module that owns
// the file's go_package — forge's own repository, whose
// proto/forge/v1/forge.proto is the SOURCE of forge/pkg/forgepb rather than a
// copy of it. There, generating its Go is what produces the one registration;
// anywhere else it would be a second.
func projectOwnsGoPackage(projectDir, relPath string) bool {
	body, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(relPath)))
	if err != nil {
		return false
	}
	m := goPackageRE.FindSubmatch(body)
	mod, modErr := codegen.GetModulePath(projectDir)
	if m == nil || modErr != nil || mod == "" {
		return false
	}
	pkg := string(m[1])
	return pkg == mod || strings.HasPrefix(pkg, mod+"/")
}

// templateTargets reports whether a template's inputs make relPath a
// generation target. No inputs means buf targets the whole workspace, which
// holds proto/. A `directory` input targets everything beneath it except its
// exclude_paths; any other input kind (a module, a git repository, a single
// proto_file) is not this project's proto/ tree, and is left alone.
func templateTargets(inputs []map[string]any, relPath string) bool {
	if len(inputs) == 0 {
		return true
	}
	for _, in := range inputs {
		dir, ok := in["directory"].(string)
		if !ok || !pathWithin(relPath, dir) {
			continue
		}
		excluded := false
		if list, ok := in["exclude_paths"].([]any); ok {
			for _, e := range list {
				if s, ok := e.(string); ok && pathWithin(relPath, s) {
					excluded = true
					break
				}
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

// pathWithin reports whether the project-relative file p lies under the
// project-relative directory dir ("." and "" are the project root).
func pathWithin(p, dir string) bool {
	dir = path.Clean(filepath.ToSlash(strings.TrimSpace(dir)))
	if dir == "." || dir == "" {
		return true
	}
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// isGoMessagePlugin reports whether a plugin is protoc-gen-go — the one that
// emits a *.pb.go registering the file's descriptor. protoc-gen-connect-go is
// not: it emits nothing for a file with no service.
func isGoMessagePlugin(local any, remote string) bool {
	for _, a := range stringArgv(local) {
		if strings.TrimSuffix(filepath.Base(a), ".exe") == "protoc-gen-go" {
			return true
		}
	}
	r := strings.TrimSpace(remote)
	if i := strings.LastIndex(r, ":"); i > 0 {
		r = r[:i] // a pinned version: buf.build/protocolbuffers/go:v1.36.0
	}
	return r == "buf.build/protocolbuffers/go"
}

// excludesPackage reports whether a plugin's exclude_types drops every type of
// proto package pkg: the package itself or an enclosing one.
func excludesPackage(excludeTypes []string, pkg string) bool {
	for _, t := range excludeTypes {
		t = strings.TrimSpace(t)
		if t == pkg || strings.HasPrefix(pkg, t+".") {
			return true
		}
	}
	return false
}
