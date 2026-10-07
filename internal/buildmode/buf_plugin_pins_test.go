// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// protocGenESDir is the npm package that pins every protoc-gen-es a buf
// template runs. Its devDependencies are aliases named for their version.
const protocGenESDir = "tools/protoc-gen-es"

// TestBufPluginsAreLocalAndPinned keeps `make generate-all` reproducible: the
// same inputs must produce the same bytes on every machine and in CI, or the
// "Generated code is up to date" gate cannot tell a real change from noise.
//
// Every plugin a buf.gen*.yaml template runs must therefore be local and
// pinned by a file in this repo. The two shapes that are not, and what each
// cost:
//
//   - `remote: buf.build/...` runs on the Buf Schema Registry, which
//     rate-limits (`resource_exhausted: too many requests`) and failed
//     `make generate-go` for every agent at once. Unpinned, a remote also
//     rewrote committed code whenever upstream released (protoc-gen-es
//     v2.14.1 -> v2.15.0 on forge_pb.ts).
//   - `local: protoc-gen-go` is a PATH lookup: the version is whatever this
//     machine installed, built by whatever Go it had. protoc-gen-go v1.36.12
//     built with go1.25 and with go1.27 write different bytes.
//
// The accepted shapes are `[go, tool, <name>]`, where <name> is a `tool`
// directive in go.mod, and `[node, tools/protoc-gen-es/node_modules/<alias>/bin/<bin>]`,
// where <alias> is an exact `npm:` pin in tools/protoc-gen-es/package.json.
//
// It also keeps templates that write the SAME directory on the same plugin
// version, or each `buf generate` would undo the other.
func TestBufPluginsAreLocalAndPinned(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	templates, err := filepath.Glob(filepath.Join(root, "buf.gen*.y*ml"))
	if err != nil {
		t.Fatalf("glob buf.gen templates: %v", err)
	}
	// A glob that silently matched nothing would pass every check below.
	// buf.gen.yaml is the one template `buf generate` reads by default, so its
	// absence means the glob, not the repo, is wrong.
	if !containsBase(templates, "buf.gen.yaml") {
		t.Fatalf("found no buf.gen.yaml under %s (matched %v); the glob no longer sees the templates", root, templates)
	}

	sources := make(map[string][]byte, len(templates))
	for _, path := range templates {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		sources[filepath.Base(path)] = data
	}

	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	pkgJSON, err := os.ReadFile(filepath.Join(root, protocGenESDir, "package.json"))
	if err != nil {
		t.Fatalf("read %s/package.json: %v", protocGenESDir, err)
	}
	esPins, err := parseNPMAliasPins(pkgJSON)
	if err != nil {
		t.Fatalf("%s/package.json: %v", protocGenESDir, err)
	}

	for _, problem := range checkBufPluginPins(sources, goModTools(goMod), esPins) {
		t.Error(problem)
	}
}

// TestCheckBufPluginPins proves the checker itself catches each failure mode,
// so the repo-level test above cannot pass by accident (for example, if the
// YAML shape changes and no plugin is decoded at all).
func TestCheckBufPluginPins(t *testing.T) {
	t.Parallel()

	goTools := goModTools([]byte("module x\n\ngo 1.27.0\n\ntool (\n" +
		"\tconnectrpc.com/connect/cmd/protoc-gen-connect-go\n" +
		"\tgoogle.golang.org/protobuf/cmd/protoc-gen-go\n)\n"))
	esPins, err := parseNPMAliasPins([]byte(`{"devDependencies": {
		"protoc-gen-es-v2.12.0": "npm:@bufbuild/protoc-gen-es@2.12.0",
		"protoc-gen-es-v2.15.0": "npm:@bufbuild/protoc-gen-es@2.15.0",
		"protoc-gen-es-loose": "npm:@bufbuild/protoc-gen-es@^2.15.0"
	}}`))
	if err != nil {
		t.Fatalf("parse fixture package.json: %v", err)
	}

	const es215 = "[node, tools/protoc-gen-es/node_modules/protoc-gen-es-v2.15.0/bin/protoc-gen-es]"
	const es212 = "[node, tools/protoc-gen-es/node_modules/protoc-gen-es-v2.12.0/bin/protoc-gen-es]"

	cases := []struct {
		name      string
		templates map[string]string
		wantFound []string // substrings; empty means "no problems"
	}{
		{
			name: "all local and pinned",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n" +
					"  - local: [go, tool, protoc-gen-go]\n    out: gen\n" +
					"  - local: [go, tool, protoc-gen-connect-go]\n    out: gen\n" +
					"  - local: " + es215 + "\n    out: web/src/gen\n",
			},
		},
		{
			name: "remote plugin",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - remote: buf.build/protocolbuffers/go:v1.36.12\n    out: gen\n",
			},
			wantFound: []string{`buf.gen.yaml: remote plugin "buf.build/protocolbuffers/go:v1.36.12" runs on the Buf Schema Registry`},
		},
		{
			name: "bare PATH lookup",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n",
			},
			wantFound: []string{`buf.gen.yaml: local plugin "protoc-gen-go" is a PATH lookup`},
		},
		{
			name: "go tool not declared in go.mod",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - local: [go, tool, protoc-gen-go-grpc]\n    out: gen\n",
			},
			wantFound: []string{`runs "protoc-gen-go-grpc", which is not a tool directive in go.mod`},
		},
		{
			name: "go run is not go tool",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - local: [go, run, google.golang.org/protobuf/cmd/protoc-gen-go@latest]\n    out: gen\n",
			},
			wantFound: []string{`is neither [go, tool, <name>] nor [node, tools/protoc-gen-es/node_modules/<alias>/bin/<bin>]`},
		},
		{
			name: "node plugin outside the pinned package",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - local: [node, web/node_modules/@bufbuild/protoc-gen-es/bin/protoc-gen-es]\n    out: web/src/gen\n",
			},
			wantFound: []string{`is neither [go, tool, <name>] nor`},
		},
		{
			name: "node plugin alias missing from package.json",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - local: [node, tools/protoc-gen-es/node_modules/protoc-gen-es-v9.9.9/bin/protoc-gen-es]\n    out: web/src/gen\n",
			},
			wantFound: []string{`runs "protoc-gen-es-v9.9.9", which is not a devDependency of tools/protoc-gen-es/package.json`},
		},
		{
			name: "node plugin alias with a range",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - local: [node, tools/protoc-gen-es/node_modules/protoc-gen-es-loose/bin/protoc-gen-es]\n    out: web/src/gen\n",
			},
			wantFound: []string{`"protoc-gen-es-loose" is "npm:@bufbuild/protoc-gen-es@^2.15.0", not an exact`},
		},
		{
			name: "same plugin and out dir, different versions",
			templates: map[string]string{
				"buf.gen.yaml":       "version: v2\nplugins:\n  - local: " + es215 + "\n    out: web/src/gen\n",
				"buf.gen-other.yaml": "version: v2\nplugins:\n  - local: " + es212 + "\n    out: web/src/gen\n",
			},
			wantFound: []string{`@bufbuild/protoc-gen-es writes web/src/gen/ at different versions`},
		},
		{
			name: "same plugin, different out dirs may differ",
			templates: map[string]string{
				"buf.gen.yaml":              "version: v2\nplugins:\n  - local: " + es215 + "\n    out: web/src/gen\n",
				"buf.gen.controlplane.yaml": "version: v2\nplugins:\n  - local: " + es212 + "\n    out: web/src/gen/controlplane\n",
			},
		},
		{
			name: "template with no plugins is itself a problem",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugin:\n  - local: [go, tool, protoc-gen-go]\n",
			},
			wantFound: []string{"buf.gen.yaml: declares no plugins"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sources := make(map[string][]byte, len(tc.templates))
			for name, body := range tc.templates {
				sources[name] = []byte(body)
			}
			problems := checkBufPluginPins(sources, goTools, esPins)

			if len(tc.wantFound) == 0 && len(problems) > 0 {
				t.Fatalf("want no problems, got:\n%s", strings.Join(problems, "\n"))
			}
			joined := strings.Join(problems, "\n")
			for _, want := range tc.wantFound {
				if !strings.Contains(joined, want) {
					t.Errorf("want a problem containing %q, got:\n%s", want, joined)
				}
			}
		})
	}
}

// esPluginPath is the only node plugin shape accepted: a bin script inside one
// alias of the pinned tools/protoc-gen-es package.
var esPluginPath = regexp.MustCompile(`^` + regexp.QuoteMeta(protocGenESDir) + `/node_modules/([^/]+)/bin/[^/]+$`)

// exactNPMAlias is `npm:<package>@<exact semver>`, e.g.
// npm:@bufbuild/protoc-gen-es@2.15.0. A range (^, ~, x) is not a pin.
var exactNPMAlias = regexp.MustCompile(`^npm:((?:@[^/@\s]+/)?[^@\s]+)@(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)$`)

// majorVersionSuffix is the /vN element `go tool` drops from a short name.
var majorVersionSuffix = regexp.MustCompile(`^v\d+$`)

// checkBufPluginPins returns one human-readable problem per violation across
// the given templates (keyed by file name). goTools holds go.mod's tool
// directives; esPins maps each tools/protoc-gen-es alias to its `npm:` spec.
// An empty result means every plugin is local and pinned, and plugins that
// write the same directory agree on a version.
func checkBufPluginPins(templates map[string][]byte, goTools []string, esPins map[string]string) []string {
	type pluginRef struct {
		Remote string `yaml:"remote"`
		Local  any    `yaml:"local"`
		Out    string `yaml:"out"`
	}
	type template struct {
		Plugins []pluginRef `yaml:"plugins"`
	}

	names := make([]string, 0, len(templates))
	for name := range templates {
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []string
	// plugin identity + out dir -> version -> templates using it
	seen := map[string]map[string][]string{}
	record := func(identity, version, out, template string) {
		key := identity + " writes " + filepath.Clean(out) + "/"
		if seen[key] == nil {
			seen[key] = map[string][]string{}
		}
		seen[key][version] = append(seen[key][version], template)
	}

	for _, name := range names {
		var tmpl template
		if err := yaml.Unmarshal(templates[name], &tmpl); err != nil {
			problems = append(problems, fmt.Sprintf("%s: not valid YAML: %v", name, err))
			continue
		}
		if len(tmpl.Plugins) == 0 {
			problems = append(problems, fmt.Sprintf("%s: declares no plugins; the checker cannot see its plugin list", name))
			continue
		}
		for _, plugin := range tmpl.Plugins {
			if plugin.Remote != "" {
				problems = append(problems, fmt.Sprintf(
					"%s: remote plugin %q runs on the Buf Schema Registry, which rate-limits "+
						"(`resource_exhausted: too many requests`) and needs the network. Run it "+
						"locally: [go, tool, <name>] with a `tool` directive in go.mod, or "+
						"[node, %s/node_modules/<alias>/bin/<bin>] with an exact pin in %s/package.json.",
					name, plugin.Remote, protocGenESDir, protocGenESDir))
				continue
			}
			switch local := plugin.Local.(type) {
			case string:
				problems = append(problems, fmt.Sprintf(
					"%s: local plugin %q is a PATH lookup, so its version (and the Go that built it) "+
						"is whatever this machine has installed. Use [go, tool, %s] with a `tool` "+
						"directive in go.mod instead.",
					name, local, local))
			case []any:
				argv := make([]string, 0, len(local))
				for _, arg := range local {
					argv = append(argv, fmt.Sprint(arg))
				}
				identity, version, problem := resolveLocalPlugin(argv, goTools, esPins)
				if problem != "" {
					problems = append(problems, fmt.Sprintf("%s: local plugin %v %s", name, argv, problem))
					continue
				}
				record(identity, version, plugin.Out, name)
			default:
				problems = append(problems, fmt.Sprintf("%s: plugin writing %q is neither remote nor local; the checker cannot see what it runs", name, plugin.Out))
			}
		}
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		versions := seen[key]
		if len(versions) < 2 {
			continue
		}
		var detail []string
		for version, users := range versions {
			detail = append(detail, fmt.Sprintf("%s in %s", version, strings.Join(users, ", ")))
		}
		sort.Strings(detail)
		problems = append(problems, fmt.Sprintf(
			"%s at different versions (%s). Templates that write the same directory must pin the "+
				"same version, or each `buf generate` undoes the other.",
			key, strings.Join(detail, "; ")))
	}
	return problems
}

// resolveLocalPlugin maps a `local:` argv onto the pin that fixes its version.
// It returns the plugin's identity and version, or a problem describing why
// the argv is not one of the two pinned shapes.
func resolveLocalPlugin(argv []string, goTools []string, esPins map[string]string) (identity, version, problem string) {
	switch {
	case len(argv) == 3 && argv[0] == "go" && argv[1] == "tool":
		for _, tool := range goTools {
			if argv[2] == tool || argv[2] == goToolName(tool) {
				// go.mod has exactly one version of the module providing it.
				return tool, "go.mod", ""
			}
		}
		return "", "", fmt.Sprintf("runs %q, which is not a tool directive in go.mod (have %v). "+
			"Add it with `go get -tool <package>@<version the module already requires>`.", argv[2], goTools)
	case len(argv) == 2 && argv[0] == "node" && esPluginPath.MatchString(argv[1]):
		alias := esPluginPath.FindStringSubmatch(argv[1])[1]
		spec, ok := esPins[alias]
		if !ok {
			return "", "", fmt.Sprintf("runs %q, which is not a devDependency of %s/package.json.", alias, protocGenESDir)
		}
		match := exactNPMAlias.FindStringSubmatch(spec)
		if match == nil {
			return "", "", fmt.Sprintf("runs %q, but %q is %q, not an exact `npm:<package>@X.Y.Z` pin.", alias, alias, spec)
		}
		return match[1], match[2], ""
	default:
		return "", "", fmt.Sprintf("is neither [go, tool, <name>] nor [node, %s/node_modules/<alias>/bin/<bin>], "+
			"so nothing in this repo pins its version.", protocGenESDir)
	}
}

// goModTools returns the package paths of go.mod's `tool` directives, in both
// the single-line and the block form.
func goModTools(goMod []byte) []string {
	var tools []string
	inBlock := false
	for _, line := range strings.Split(string(goMod), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			tools = append(tools, line)
		case line == "tool (":
			inBlock = true
		case strings.HasPrefix(line, "tool "):
			tools = append(tools, strings.TrimSpace(strings.TrimPrefix(line, "tool ")))
		}
	}
	return tools
}

// goToolName is the short name `go tool` accepts for a tool package: its last
// path element, without a /vN major-version suffix.
func goToolName(pkg string) string {
	parts := strings.Split(pkg, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && majorVersionSuffix.MatchString(last) {
		last = parts[len(parts)-2]
	}
	return last
}

// parseNPMAliasPins reads a package.json's devDependencies.
func parseNPMAliasPins(packageJSON []byte) (map[string]string, error) {
	var pkg struct {
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(packageJSON, &pkg); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if len(pkg.DevDependencies) == 0 {
		return nil, fmt.Errorf("declares no devDependencies; no protoc-gen-es is pinned")
	}
	return pkg.DevDependencies, nil
}

func containsBase(paths []string, base string) bool {
	for _, path := range paths {
		if filepath.Base(path) == base {
			return true
		}
	}
	return false
}
