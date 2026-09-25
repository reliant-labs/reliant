// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestBufRemotePluginsArePinned keeps `buf generate` reproducible.
//
// A `remote:` plugin with no `:vX.Y.Z` suffix resolves to whatever the Buf
// Schema Registry calls latest at the moment the command runs. Nothing in the
// repo changes, yet the output does: buf.gen.yaml carried
// `remote: buf.build/bufbuild/es`, and `make proto-generate` rewrote the
// committed web/src/gen/reliant/v1/forge_pb.ts from protoc-gen-es v2.14.1 to
// v2.15.0 the day upstream released. A contributor who regenerates for an
// unrelated proto change then commits a diff they did not make, and the drift
// gate cannot tell a real change from a registry release.
//
// It also keeps templates that write the SAME output directory on the same
// plugin version. buf.gen.yaml (`make proto-generate`) and buf.gen-go-only.yaml
// (`make generate-go`, which CI's drift gate runs) both write gen/; pinned to
// different versions, each command would undo the other and the drift gate
// would go red on whichever one a contributor did not run.
func TestBufRemotePluginsArePinned(t *testing.T) {
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

	for _, problem := range checkBufPluginPins(sources) {
		t.Error(problem)
	}
}

// TestCheckBufPluginPins proves the checker itself catches each failure mode,
// so the repo-level test above cannot pass by accident (for example, if the
// YAML shape changes and no plugin is decoded at all).
func TestCheckBufPluginPins(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		templates map[string]string
		wantFound []string // substrings; empty means "no problems"
	}{
		{
			name: "all pinned",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n" +
					"  - remote: buf.build/protocolbuffers/go:v1.36.12\n    out: gen\n" +
					"  - remote: buf.build/bufbuild/es:v2.15.0\n    out: web/src/gen\n" +
					"  - local: protoc-gen-go\n    out: gen\n",
			},
		},
		{
			name: "unpinned remote",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - remote: buf.build/bufbuild/es\n    out: web/src/gen\n",
			},
			wantFound: []string{`buf.gen.yaml: remote plugin "buf.build/bufbuild/es" is not pinned`},
		},
		{
			name: "non-semver label",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugins:\n  - remote: buf.build/bufbuild/es:latest\n    out: web/src/gen\n",
			},
			wantFound: []string{`"buf.build/bufbuild/es:latest" is not pinned`},
		},
		{
			name: "same plugin and out dir, different versions",
			templates: map[string]string{
				"buf.gen.yaml":         "version: v2\nplugins:\n  - remote: buf.build/connectrpc/go:v1.21.0\n    out: gen\n",
				"buf.gen-go-only.yaml": "version: v2\nplugins:\n  - remote: buf.build/connectrpc/go:v1.20.0\n    out: gen\n",
			},
			wantFound: []string{`buf.build/connectrpc/go writes gen/ at different versions`},
		},
		{
			name: "same plugin, different out dirs may differ",
			templates: map[string]string{
				"buf.gen.yaml":              "version: v2\nplugins:\n  - remote: buf.build/bufbuild/es:v2.15.0\n    out: web/src/gen\n",
				"buf.gen.controlplane.yaml": "version: v2\nplugins:\n  - remote: buf.build/bufbuild/es:v2.12.0\n    out: web/src/gen/controlplane\n",
			},
		},
		{
			name: "template with no plugins is itself a problem",
			templates: map[string]string{
				"buf.gen.yaml": "version: v2\nplugin:\n  - remote: buf.build/bufbuild/es\n",
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
			problems := checkBufPluginPins(sources)

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

// pinnedRemote is a BSR remote plugin reference with an exact semver label:
// buf.build/<owner>/<plugin>:v<major>.<minor>.<patch>[-prerelease].
var pinnedRemote = regexp.MustCompile(`^([^\s:]+/[^\s:]+/[^\s:]+):v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

// checkBufPluginPins returns one human-readable problem per violation across
// the given templates (keyed by file name). An empty result means every remote
// plugin is pinned and co-located plugins agree on a version.
func checkBufPluginPins(templates map[string][]byte) []string {
	type pluginRef struct {
		Remote string `yaml:"remote"`
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
	// plugin name + out dir -> version -> templates using it
	seen := map[string]map[string][]string{}

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
			if plugin.Remote == "" {
				continue // local: plugins are versioned by whatever is on PATH
			}
			match := pinnedRemote.FindStringSubmatch(plugin.Remote)
			if match == nil {
				problems = append(problems, fmt.Sprintf(
					"%s: remote plugin %q is not pinned to an exact version. "+
						"Unpinned, it resolves to the registry's latest on every run and silently "+
						"rewrites committed generated code. Pin it, e.g. %q, choosing the version the "+
						"committed output was generated with (see its '@generated by' / 'protoc-gen-* v' header).",
					name, plugin.Remote, strings.SplitN(plugin.Remote, ":", 2)[0]+":vX.Y.Z"))
				continue
			}
			key := match[1] + " writes " + filepath.Clean(plugin.Out) + "/"
			version := strings.TrimPrefix(plugin.Remote, match[1]+":")
			if seen[key] == nil {
				seen[key] = map[string][]string{}
			}
			seen[key][version] = append(seen[key][version], name)
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

func containsBase(paths []string, base string) bool {
	for _, path := range paths {
		if filepath.Base(path) == base {
			return true
		}
	}
	return false
}
