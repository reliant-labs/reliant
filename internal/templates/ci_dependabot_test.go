package templates

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

type dependabotConfig struct {
	Updates []struct {
		Ecosystem   string                    `yaml:"package-ecosystem"`
		Directory   string                    `yaml:"directory"`
		Directories []string                  `yaml:"directories"`
		Groups      map[string]map[string]any `yaml:"groups"`
		Ignore      []struct {
			DependencyName string   `yaml:"dependency-name"`
			UpdateTypes    []string `yaml:"update-types"`
		} `yaml:"ignore"`
	} `yaml:"updates"`
}

func renderDependabot(t *testing.T, data DependabotData) dependabotConfig {
	t.Helper()
	body, err := CITemplates("github").Render("dependabot.yml.tmpl", data)
	if err != nil {
		t.Fatalf("render %+v: %v", data, err)
	}
	var cfg dependabotConfig
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("invalid YAML for %+v: %v\n%s", data, err, body)
	}
	return cfg
}

// ignoredOutright returns the dependencies an ecosystem's entry ignores for
// EVERY update type. An ignore narrowed by update-types still lets some
// bumps through, which is not what a version-stamping generator needs.
func ignoredOutright(cfg dependabotConfig, ecosystem string) []string {
	var out []string
	for _, u := range cfg.Updates {
		if u.Ecosystem != ecosystem {
			continue
		}
		for _, ig := range u.Ignore {
			if len(ig.UpdateTypes) == 0 {
				out = append(out, ig.DependencyName)
			}
		}
	}
	return out
}

// Each Dependabot PR runs the whole CI; ungrouped entries mean one PR per dependency.
func TestDependabotTemplateGroupsEveryEcosystem(t *testing.T) {
	for _, frontend := range []string{"", "web"} {
		cfg := renderDependabot(t, DependabotData{FrontendName: frontend, RunsProtocGenGo: true})
		gomodEntries := 0
		for _, u := range cfg.Updates {
			if len(u.Groups) == 0 {
				t.Errorf("frontend %q: %s entry has no groups: — would open one PR per dependency", frontend, u.Ecosystem)
			}
			if u.Ecosystem == "gomod" {
				gomodEntries++
				if len(u.Directories) != 2 || u.Directories[0] != "/" || u.Directories[1] != "/gen" {
					t.Errorf("frontend %q: gomod directories = %v, want [/ /gen] in one entry", frontend, u.Directories)
				}
			}
		}
		if gomodEntries != 1 {
			t.Errorf("frontend %q: %d gomod entries, want exactly 1", frontend, gomodEntries)
		}
	}
}

// A code generator that writes its own version into its output cannot be
// bumped by Dependabot: the PR edits go.mod or the lockfile, never
// regenerates, and so fails Verify Generated Code by construction.
// control-plane #645 was a Dependabot npm group moving protoc-gen-es
// 2.14.0 -> 2.16.0; every *_pb.ts header changed, and merging it left main red.
func TestDependabotTemplateIgnoresVersionStampingGenerators(t *testing.T) {
	t.Run("protoc-gen-es and its exact-peer runtime", func(t *testing.T) {
		got := ignoredOutright(renderDependabot(t, DependabotData{FrontendName: "web", RunsProtocGenGo: true}), "npm")
		for _, want := range []string{"@bufbuild/protoc-gen-es", "@bufbuild/protobuf"} {
			if !slices.Contains(got, want) {
				t.Errorf("npm entry does not ignore %s (ignores %q)", want, got)
			}
		}
	})

	t.Run("protoc-gen-go, versioned by google.golang.org/protobuf", func(t *testing.T) {
		got := ignoredOutright(renderDependabot(t, DependabotData{RunsProtocGenGo: true}), "gomod")
		if !slices.Contains(got, "google.golang.org/protobuf") {
			t.Errorf("gomod entry does not ignore google.golang.org/protobuf (ignores %q) — "+
				"`forge tools install` puts protoc-gen-go at that version on PATH and it stamps every *.pb.go", got)
		}
		// forge itself was already ignored; this pins that it stays.
		if !slices.Contains(got, "github.com/reliant-labs/forge") {
			t.Errorf("gomod entry no longer ignores github.com/reliant-labs/forge (ignores %q)", got)
		}
	})

	// Without protoc-gen-go in the pipeline, the protobuf runtime is an
	// ordinary dependency and freezing it would only cost security bumps.
	t.Run("a project that runs no protoc-gen-go keeps protobuf bumps", func(t *testing.T) {
		got := ignoredOutright(renderDependabot(t, DependabotData{}), "gomod")
		if slices.Contains(got, "google.golang.org/protobuf") {
			t.Errorf("gomod entry ignores google.golang.org/protobuf for a project that never runs protoc-gen-go")
		}
	})

	// protoc-gen-connect-go stamps no version, so its runtime module stays
	// Dependabot-managed. Pinned so a future "ignore every generator module"
	// sweep has to argue with this test rather than silently freeze connect.
	t.Run("connect stays managed", func(t *testing.T) {
		got := ignoredOutright(renderDependabot(t, DependabotData{RunsProtocGenGo: true}), "gomod")
		if slices.Contains(got, "connectrpc.com/connect") {
			t.Errorf("gomod entry ignores connectrpc.com/connect, whose generator stamps no version")
		}
	})
}
