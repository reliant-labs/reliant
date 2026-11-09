package cli

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/internal/codegen"
)

// Declared outputs of the pipeline steps that run EXTERNAL tools.
//
// forge's own writers journal every file they touch, but buf, `go mod tidy`
// and protoc-gen-connect-openapi write files forge never sees. A failed
// control-plane generate printed "your tree is back to its pre-run state"
// over 32 buf outputs left modified: some carried a different protoc-gen-go
// version header (the plugin on PATH was not the one that produced the
// committed stubs), others were regenerated from another agent's uncommitted
// proto. Each step below therefore DECLARES what its tool may write, and the
// pipeline captures those paths into the rollback journal just before the
// step runs (GenStep.Writes → checksums.CaptureExternalWrites), so a failed
// run restores them like any forge write.
//
// A declaration is a superset, never a guess at the minimum: a path the tool
// leaves alone is neither rewritten nor reported. What a declaration misses
// is caught by the post-revert verification (generate_rollback_verify.go),
// which withholds the "pre-run state" claim and names the file.

// The module files `go mod tidy` rewrites in the root and gen/ modules.
var (
	rootModuleFiles = []string{"go.mod", "go.sum"}
	genModuleFiles  = []string{"gen/go.mod", "gen/go.sum"}
)

func writesRootTidy(*pipelineContext) []string { return rootModuleFiles }

func writesGenTidy(*pipelineContext) []string { return genModuleFiles }

func writesBothTidies(*pipelineContext) []string {
	return append(append([]string{}, genModuleFiles...), rootModuleFiles...)
}

func writesGenGoMod(*pipelineContext) []string { return []string{"gen/go.mod"} }

// writesBufGo: buf.gen.yaml (written when absent), the vendored annotation
// protos, and every plugin `out` directory the template names.
func writesBufGo(ctx *pipelineContext) []string {
	out := append([]string{"buf.gen.yaml"}, vendoredProtoDirs()...)
	outs := bufTemplateOutDirs(filepath.Join(ctx.ProjectDir, "buf.gen.yaml"))
	if len(outs) == 0 {
		outs = []string{"gen"} // writeDefaultBufGenYaml's template
	}
	return append(out, outs...)
}

func writesDescriptor(*pipelineContext) []string {
	return append([]string{codegen.DescriptorDir, codegen.LegacyDescriptorFile, "gen/" + descriptorStageDir, ".gitignore"}, vendoredProtoDirs()...)
}

func writesOpenAPI(*pipelineContext) []string { return []string{"openapi"} }

// writesFrontendTS: per TypeScript frontend, its buf.gen.yaml and output
// directory, plus package-lock.json (the step runs `npm install` when the
// plugin is missing). Workspace mode shares one template and output under
// packages/api.
func writesFrontendTS(ctx *pipelineContext) []string {
	if ctx.Cfg == nil {
		return nil
	}
	var out []string
	if ctx.Cfg.IsFrontendWorkspacesEnabled() {
		out = append(out, "packages/api/buf.gen.yaml", "packages/api/src/gen")
		out = append(out, bufTemplateOutDirs(filepath.Join(ctx.ProjectDir, "packages", "api", "buf.gen.yaml"))...)
	}
	for _, fe := range ctx.Cfg.Frontends {
		if !generatesTypeScript(fe.Type) {
			continue
		}
		feDir, ok := fe.Dir(ctx.ProjectDir)
		if !ok {
			continue
		}
		feSlash := filepath.ToSlash(feDir)
		out = append(out, feSlash+"/package-lock.json")
		if ctx.Cfg.IsFrontendWorkspacesEnabled() {
			continue
		}
		out = append(out, feSlash+"/buf.gen.yaml", feSlash+"/src/gen")
		out = append(out, bufTemplateOutDirs(filepath.Join(ctx.ProjectDir, feDir, "buf.gen.yaml"))...)
	}
	return dedupeSorted(out)
}

// writesKCLMigration: the image-registry migration (internal/kclmigrate)
// rewrites existing .k files under deploy/kcl in place, through its own
// writes. Declared as the FILES that exist, not the directory, so a file a
// later forge step or another process creates under deploy/kcl is never
// swept up by the restore.
func writesKCLMigration(ctx *pipelineContext) []string {
	kclDir := filepath.Join(ctx.ProjectDir, "deploy", "kcl")
	var out []string
	_ = filepath.WalkDir(kclDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != kclDir {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".k") {
			if rel, relErr := filepath.Rel(ctx.ProjectDir, p); relErr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return out
}

// vendoredProtoDirs are the directories ensureVendoredProtos may write a
// vendored annotation proto into.
func vendoredProtoDirs() []string {
	return []string{
		path.Dir(filepath.ToSlash(assets.ForgeProtoVendorRelPath)),
		path.Dir(filepath.ToSlash(assets.ValidateProtoVendorRelPath)),
	}
}

// bufTemplateOutDirs returns the plugin `out` directories a buf.gen.yaml
// names, project-relative (buf resolves them against the directory it runs
// in, which is the project root for every invocation forge makes). nil when
// the template is absent or unreadable.
func bufTemplateOutDirs(templatePath string) []string {
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return nil
	}
	var tmpl struct {
		Plugins []struct {
			Out string `yaml:"out"`
		} `yaml:"plugins"`
	}
	if yaml.Unmarshal(data, &tmpl) != nil {
		return nil
	}
	var out []string
	for _, p := range tmpl.Plugins {
		if dir := cleanProjectRel(p.Out); dir != "" {
			out = append(out, dir)
		}
	}
	return dedupeSorted(out)
}

// cleanProjectRel normalizes a tool-config path to a project-relative slash
// path, or "" for one that is empty, absolute, the root itself, or escapes
// the project.
func cleanProjectRel(p string) string {
	p = strings.TrimSpace(p)
	// filepath.IsAbs alone misses "/abs" on Windows (no volume, so not
	// absolute there), yet it names the drive root, never the project.
	if p == "" || filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return ""
	}
	p = path.Clean(filepath.ToSlash(p))
	if p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
		return ""
	}
	return p
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
