package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestBufTemplatesRunEachPluginOnce holds every buf template forge writes — the
// scaffolded project and frontend files, the defaults generate writes when a
// project has none, the throwaway descriptor and OpenAPI templates, and
// forge's own buf.gen.yaml — to `strategy: all` on each local plugin.
//
// buf's default strategy, `directory`, runs a plugin once per proto
// directory, every one of those processes at once up to NumCPU. Each is a Go
// runtime (or node) with its own threads, so one generate on a 31-CPU machine
// reached the 2000-thread cgroup pids.max of the shell running it and failed
// with `pthread_create: Resource temporarily unavailable`. With `all` it is
// one process per plugin, and the plugins forge wires write one file per
// proto either way. A plugin whose output genuinely depends on the
// per-directory split goes in perDirectoryBufPlugins with the reason.
//
// It reads every file itself, so Go's test cache sees each one.
func TestBufTemplatesRunEachPluginOnce(t *testing.T) {
	t.Parallel()

	templates := map[string]string{}
	read := func(name, path string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		templates[name] = string(data)
	}
	read("buf.gen.yaml (forge's own)", filepath.Join("..", "..", "buf.gen.yaml"))
	read("internal/templates/project/buf.gen.yaml", filepath.Join("..", "templates", "project", "buf.gen.yaml"))

	const feTmplPath = "internal/templates/frontend/shared/buf.gen.yaml.tmpl"
	feTmpl, err := os.ReadFile(filepath.Join("..", "templates", "frontend", "shared", "buf.gen.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read %s: %v", feTmplPath, err)
	}
	parsed, err := template.New("fe").Option("missingkey=error").Parse(string(feTmpl))
	if err != nil {
		t.Fatalf("parse %s: %v", feTmplPath, err)
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, map[string]string{"FrontendName": "web"}); err != nil {
		t.Fatalf("render %s: %v", feTmplPath, err)
	}
	templates[feTmplPath] = rendered.String()

	dir := t.TempDir()
	if err := writeDefaultBufGenYaml(dir); err != nil {
		t.Fatalf("writeDefaultBufGenYaml: %v", err)
	}
	read("writeDefaultBufGenYaml", filepath.Join(dir, "buf.gen.yaml"))
	templates["frontendBufGenYaml"] = frontendBufGenYaml("frontends/web")
	templates["workspaceBufGenYaml"] = workspaceBufGenYaml("frontends/web")
	templates["descriptorBufTemplate"] = descriptorBufTemplate(`["forge", "protoc-gen-forge"]`, "/work/out", "/work")
	templates["openapiBufTemplate"] = openapiBufTemplate("openapi")

	for _, problem := range checkBufPluginStrategies(templates, perDirectoryBufPlugins) {
		t.Error(problem)
	}
}

// TestEveryBufTemplateEmitterIsGuarded fails when a file under internal/
// spells a `- local:` buf plugin line but is not one of the sources
// TestBufTemplatesRunEachPluginOnce checks, so a new template cannot dodge
// the guard by living somewhere it does not look.
func TestEveryBufTemplateEmitterIsGuarded(t *testing.T) {
	testkit.Slow(t, "execs go")
	t.Parallel()

	guarded := map[string]bool{
		"internal/cli/generate_buf.go":                         true, // writeDefaultBufGenYaml, frontendBufGenYaml, workspaceBufGenYaml
		"internal/cli/generate_orm.go":                         true, // descriptorBufTemplate
		"internal/cli/generate_openapi.go":                     true, // openapiBufTemplate
		"internal/templates/project/buf.gen.yaml":              true,
		"internal/templates/frontend/shared/buf.gen.yaml.tmpl": true,
	}
	localLine := regexp.MustCompile(`(?m)^\s*-\s*local:`)
	root := filepath.Join("..", "..")
	var unguarded []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !localLine.Match(data) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); !guarded[rel] {
			unguarded = append(unguarded, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	for _, rel := range unguarded {
		t.Errorf("%s writes a buf template with a `local:` plugin, but TestBufTemplatesRunEachPluginOnce "+
			"does not check it: add the template it writes there (and the file to this test's guarded set)", rel)
	}
}

// TestCheckBufPluginStrategies proves the checker catches each failure mode,
// so the guard above cannot pass by decoding nothing.
func TestCheckBufPluginStrategies(t *testing.T) {
	testkit.Slow(t, "execs go")
	t.Parallel()

	cases := []struct {
		name         string
		template     string
		perDirectory map[string]string
		want         []string // substrings; empty means no problems
	}{
		{
			name:     "local plugins with strategy all",
			template: "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n    strategy: all\n  - local: [node, x/protoc-gen-es]\n    out: web\n    strategy: all\n",
		},
		{
			name:     "remote plugin needs no strategy",
			template: "version: v2\nplugins:\n  - remote: buf.build/protocolbuffers/go\n    out: gen\n",
		},
		{
			name:     "strategy unset",
			template: "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n",
			want:     []string{"t: local plugin protoc-gen-go has strategy unset (buf's default: directory)"},
		},
		{
			name:     "strategy directory",
			template: "version: v2\nplugins:\n  - local: [go, tool, protoc-gen-go]\n    out: gen\n    strategy: directory\n",
			want:     []string{"t: local plugin [go tool protoc-gen-go] has strategy directory"},
		},
		{
			name:     "strategy on a remote plugin",
			template: "version: v2\nplugins:\n  - remote: buf.build/protocolbuffers/go\n    out: gen\n    strategy: all\n",
			want:     []string{"cannot specify strategy for remote plugin"},
		},
		{
			name:         "allowlisted per-directory plugin",
			template:     "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n",
			perDirectory: map[string]string{"t: protoc-gen-go": "fixture reason"},
		},
		{
			name:         "stale allowlist entry",
			template:     "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n    strategy: all\n",
			perDirectory: map[string]string{"t: protoc-gen-go": "fixture reason"},
			want:         []string{`perDirectoryBufPlugins entry "t: protoc-gen-go" matches no per-directory plugin`},
		},
		{
			name:     "no plugins decoded",
			template: "version: v2\nplugin:\n  - local: protoc-gen-go\n",
			want:     []string{"t: declares no plugins"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			problems := checkBufPluginStrategies(map[string]string{"t": tc.template}, tc.perDirectory)
			joined := strings.Join(problems, "\n")
			if len(tc.want) == 0 && len(problems) > 0 {
				t.Fatalf("want no problems, got:\n%s", joined)
			}
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("want a problem containing %q, got:\n%s", want, joined)
				}
			}
		})
	}
}

// perDirectoryBufPlugins lists the local plugins allowed to keep buf's
// per-directory strategy, keyed "<template>: <local>", each with the reason
// its output differs under `strategy: all`. Empty: none does.
var perDirectoryBufPlugins = map[string]string{}

// checkBufPluginStrategies returns one problem per local plugin (across
// templates, keyed by a display name) that is not `strategy: all` and not in
// perDirectory, per remote plugin that sets a strategy (buf refuses it), per
// template that decodes no plugins, and per stale perDirectory entry.
func checkBufPluginStrategies(templates map[string]string, perDirectory map[string]string) []string {
	type plugin struct {
		Local    any    `yaml:"local"`
		Remote   string `yaml:"remote"`
		Strategy string `yaml:"strategy"`
	}
	var body struct {
		Plugins []plugin `yaml:"plugins"`
	}

	names := make([]string, 0, len(templates))
	for name := range templates {
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []string
	used := map[string]bool{}
	for _, name := range names {
		body.Plugins = nil
		if err := yaml.Unmarshal([]byte(templates[name]), &body); err != nil {
			problems = append(problems, fmt.Sprintf("%s: not valid YAML: %v", name, err))
			continue
		}
		if len(body.Plugins) == 0 {
			problems = append(problems, fmt.Sprintf("%s: declares no plugins; the guard cannot see its plugin list", name))
			continue
		}
		for _, p := range body.Plugins {
			if p.Local == nil {
				if p.Strategy != "" {
					problems = append(problems, fmt.Sprintf("%s: remote plugin %s sets strategy %q; buf refuses it "+
						"(cannot specify strategy for remote plugin)", name, p.Remote, p.Strategy))
				}
				continue
			}
			if p.Strategy == "all" {
				continue
			}
			local := fmt.Sprint(p.Local)
			key := name + ": " + local
			if reason, ok := perDirectory[key]; ok && strings.TrimSpace(reason) != "" {
				used[key] = true
				continue
			}
			shown := "strategy " + p.Strategy
			if p.Strategy == "" {
				shown = "strategy unset (buf's default: directory)"
			}
			problems = append(problems, fmt.Sprintf("%s: local plugin %s has %s, so buf runs it once per proto "+
				"directory, every process at once. Add `strategy: all`, or, if its output differs that way, "+
				"add %q to perDirectoryBufPlugins with the reason.", name, local, shown, key))
		}
	}
	var stale []string
	for key := range perDirectory {
		if !used[key] {
			stale = append(stale, fmt.Sprintf("perDirectoryBufPlugins entry %q matches no per-directory plugin; delete it", key))
		}
	}
	sort.Strings(stale)
	return append(problems, stale...)
}
