package templates

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// updateGolden re-writes every golden file from the current rendered output.
// Usage: `go test ./internal/templates/ -run TestGolden -update`.
// Kept as a package-level flag rather than a build tag so the happy path
// (compare) remains the default and no one accidentally blesses broken output.
var updateGolden = flag.Bool("update", false, "update .golden snapshots")

// goldenCase describes one template we snapshot for regression detection.
//
// Keep the data struct minimal and stable: every field you add becomes
// something a future template change might have to diff against. If a field
// isn't actually referenced by the template you're snapshotting, drop it.
type goldenCase struct {
	// name is the subtest name and the filename stem under testdata/golden/.
	name string

	// render produces the bytes to snapshot. It receives no args so each case
	// is responsible for picking a representative, deterministic input.
	render func(t *testing.T) []byte
}

// renderProject renders a project/ template with the standard templateData
// shape used by the generator (see internal/generator/project.go).
func renderProject(t *testing.T, name string, data any) []byte {
	t.Helper()
	out, err := ProjectTemplates().Render(name, data)
	if err != nil {
		t.Fatalf("ProjectTemplates().Render(%q) error = %v", name, err)
	}
	return out
}

// renderCI renders a CI workflow template.
func renderCI(t *testing.T, provider, name string, data any) []byte {
	t.Helper()
	out, err := CITemplates(provider).Render(name, data)
	if err != nil {
		t.Fatalf("CITemplates(%q).Render(%q) error = %v", provider, name, err)
	}
	return out
}

// renderDeployTemplate renders a deploy/ template.
// goldenAPIWorkload is what codegen.APIWorkloadStanza renders for the golden
// cloud envs' project (shop, ghcr.io/acme). A literal, not a call: codegen
// imports this package.
const goldenAPIWorkload = `_api = fw.Workload {
    name = "api"
    kind = "service"
    image = "ghcr.io/acme/shop"
    build = forge.GoBuild {cmd = "./cmd/shop", output_name = "shop"}
    args = ["server"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    config_secrets = ["DATABASE_URL"]
}`

func renderDeployTemplate(t *testing.T, name string, data any) []byte {
	t.Helper()
	out, err := DeployTemplates().Render(name, data)
	if err != nil {
		t.Fatalf("DeployTemplates().Render(%q) error = %v", name, err)
	}
	return out
}

// renderService renders a service/ template.
func renderService(t *testing.T, name string, data any) []byte {
	t.Helper()
	out, err := ServiceTemplates().Render(name, data)
	if err != nil {
		t.Fatalf("ServiceTemplates().Render(%q) error = %v", name, err)
	}
	return out
}

// projectData returns the struct shape used by the project generator's
// top-level file loop (see project.go: template files block).
func projectData() any {
	return struct {
		Name                   string
		ProtoName              string
		Module                 string
		ServiceName            string
		ServicePort            int
		ProjectName            string
		FrontendName           string
		FrontendPort           int
		GoVersion              string
		GoVersionMinor         string
		DockerBuilderGoVersion string
		CLI                    string
		VersionVar             string
		Binaries               []struct {
			Dir     string
			Primary bool
		}
	}{
		Name:                   "demo",
		ProtoName:              "demo",
		Module:                 "github.com/example/demo",
		ServiceName:            "api",
		ServicePort:            8080,
		ProjectName:            "demo",
		FrontendName:           "web",
		FrontendPort:           3000,
		GoVersion:              "1.27",
		GoVersionMinor:         "27",
		DockerBuilderGoVersion: "1.27",
		CLI:                    "forge",
		VersionVar:             "",
		// A fresh scaffold ships only the primary binary; the golden
		// Dockerfile builds + copies just cmd/demo. Multi-binary projects
		// add entries here (covered by the generator suite's scratch
		// 2-binary test); the golden pins the single-binary baseline.
		Binaries: []struct {
			Dir     string
			Primary bool
		}{{Dir: "demo", Primary: true}},
	}
}

// TestGoldenSnapshots renders a curated set of templates with stable data
// and compares the output byte-for-byte to the corresponding file under
// testdata/golden/. When the output diverges — even by whitespace — the
// test fails and the maintainer must either fix the regression or rerun
// with `-update` to bless the new output.
//
// The goal is reviewability: a PR that changes CI, CORS, Dockerfile, or
// handler codegen output must show the diff in the snapshot file so it
// can't silently regress.
func TestGoldenSnapshots(t *testing.T) {
	cases := []goldenCase{
		{
			name: "Taskfile.yml",
			render: func(t *testing.T) []byte {
				return renderProject(t, "Taskfile.yml.tmpl", projectData())
			},
		},
		{
			name: "Dockerfile",
			render: func(t *testing.T) []byte {
				return renderProject(t, "Dockerfile.tmpl", projectData())
			},
		},
		// The Next.js frontend image, once per `frontends[].output` mode:
		// each copies a different build artifact and runs a different
		// server, so a regression in one mode is invisible in the others.
		{
			name: "nextjs_Dockerfile_standalone",
			render: func(t *testing.T) []byte {
				return renderNextjsDockerfile(t, FrontendTemplateData{FrontendName: "web", Output: "standalone"})
			},
		},
		{
			name: "nextjs_Dockerfile_static",
			render: func(t *testing.T) []byte {
				return renderNextjsDockerfile(t, FrontendTemplateData{FrontendName: "web", Output: "static", BasePath: "/admin"})
			},
		},
		{
			name: "nextjs_Dockerfile_server",
			render: func(t *testing.T) []byte {
				return renderNextjsDockerfile(t, FrontendTemplateData{FrontendName: "web", Output: "server"})
			},
		},
		// The Next.js tsconfig is committed, and `next dev` / `next build`
		// rewrite it whenever it lacks a value they want (see
		// TestNextjsTsconfigNeedsNoRewriteFromNext). One fixed pin keeps the
		// snapshot about the template rather than about the runtime's
		// current peer list.
		{
			name: "nextjs_tsconfig.json",
			render: func(t *testing.T) []byte {
				out, err := FrontendTemplates().Render("nextjs/tsconfig.json.tmpl", FrontendTemplateData{
					FrontendName:       "web",
					ProjectName:        "demo",
					WebRuntimeTypePins: []string{"@connectrpc/connect"},
				})
				if err != nil {
					t.Fatalf("render nextjs/tsconfig.json.tmpl: %v", err)
				}
				return out
			},
		},

		{
			name: "handlers_methods.go",
			render: func(t *testing.T) []byte {
				// A minimal, deterministic service with one unary and
				// one server-streaming method so the snapshot exercises
				// both code paths in the method-fragment template
				// (appended into the user-owned handlers.go).
				data := map[string]any{
					"ServiceName":         "api",
					"ServicePackage":      "api",
					"Module":              "github.com/example/demo",
					"ProtoImportPath":     "proto/services/api",
					"ProtoPackage":        "proto/services/api",
					"ProtoConnectPackage": "apiv1connect",
					"HandlerName":         "Service",
					"ProtoFileSymbol":     "File_services_api_v1_api_proto",
					"Methods": []map[string]any{
						{
							"Name":            "Echo",
							"InputType":       "EchoRequest",
							"OutputType":      "EchoResponse",
							"ClientStreaming": false,
							"ServerStreaming": false,
							"AuthRequired":    false,
						},
						{
							"Name":            "Tail",
							"InputType":       "TailRequest",
							"OutputType":      "TailResponse",
							"ClientStreaming": false,
							"ServerStreaming": true,
							"AuthRequired":    true,
						},
					},
				}
				return renderService(t, "handlers_methods.go.tmpl", data)
			},
		},
		{
			name: "ci_minimal.yml",
			render: func(t *testing.T) []byte {
				data := CIWorkflowData{
					ProjectName:         "demo",
					LintGolangci:        true,
					LintMigrationSafety: true,
					TestRace:            true,
					PermContents:        "read",
					Module:              "github.com/example/demo",
					HasDocker:           true,
					VerifyGenerated:     true,
				}
				return renderCI(t, "github", "ci.yml.tmpl", data)
			},
		},
		{
			name: "ci_full.yml",
			render: func(t *testing.T) []byte {
				data := CIWorkflowData{
					ProjectName:  "demo",
					HasFrontends: true,
					Frontends: []FrontendCIConfig{
						{Name: "web", Path: "frontends/web"},
					},
					HasServices:         true,
					LintGolangci:        true,
					LintBuf:             true,
					LintBufBreaking:     true,
					LintFrontend:        true,
					LintFrontendStyles:  true,
					LintMigrationSafety: true,
					TestRace:            true,
					TestCoverage:        true,
					VulnGo:              true,
					VulnDocker:          true,
					VulnNPM:             true,
					E2EEnabled:          true,
					E2ERuntime:          "docker-compose",
					PermContents:        "read",
					HasKCL:              true,
					HasDocker:           true,
					VerifyGenerated:     true,
					RunsBufGenerate:     true,
					Environments:        []string{"dev", "staging", "prod"},
					Module:              "github.com/example/demo",
					FrontendName:        "web",
				}
				return renderCI(t, "github", "ci.yml.tmpl", data)
			},
		},
		// The rest of the scaffolded workflow set, so a PR that changes any
		// of them shows the diff. Each is also run through the scaffold's
		// pinned prettier below: a golden that prettier would rewrite is a
		// fresh project failing its own pre-commit hook.
		{
			name: "proto-breaking.yml",
			render: func(t *testing.T) []byte {
				return renderCI(t, "github", "proto-breaking.yml.tmpl", CIWorkflowData{ProjectName: "demo", PermContents: "read"})
			},
		},
		{
			name: "build-images.yml",
			render: func(t *testing.T) []byte {
				return renderCI(t, "github", "build-images.yml.tmpl", BuildImagesWorkflowData{ProjectName: "demo", BuildEnv: "staging", VulnDocker: true})
			},
		},
		{
			// The hosted variant, snapshotted separately because the whole
			// ADR-0003 F3 change is a DIFFERENCE between the two: the hosted
			// job carries FORGE_CONTROL_PLANE_TOKEN and no registry login,
			// where the cluster job carries a login and no control-plane
			// token. One golden could only show one of them.
			name: "build-images_hosted.yml",
			render: func(t *testing.T) []byte {
				return renderCI(t, "github", "build-images.yml.tmpl", BuildImagesWorkflowData{ProjectName: "demo", BuildEnv: "staging", VulnDocker: true, Hosted: true})
			},
		},
		{
			name: "deploy.yml",
			render: func(t *testing.T) []byte {
				return renderCI(t, "github", "deploy.yml.tmpl", DeployWorkflowData{
					ProjectName:  "demo",
					Environments: []DeployEnv{{Name: "staging", Auto: true}, {Name: "prod", Protection: true}},
					HasFrontends: true,
					FrontendPath: "frontends/web",
					Concurrency:  true,
				})
			},
		},
		{
			name: "e2e.yml",
			render: func(t *testing.T) []byte {
				return renderCI(t, "github", "e2e.yml.tmpl", E2EWorkflowData{ProjectName: "demo", Runtime: "docker-compose", HasFrontends: true, FrontendPath: "frontends/web"})
			},
		},
		// The deployed envs `forge project new` writes, hosted on the forge
		// control plane by default. One with a frontend and services (CORS,
		// the site binding, API_URL), one bare: the binders a frontend added
		// later is bound through must exist in both.
		{
			name: "env_cloud_prod.k",
			render: func(t *testing.T) []byte {
				return renderDeployTemplate(t, "kcl/cloud/main.k.tmpl", EnvTemplateData{
					ProjectName: "shop", EnvName: "prod",
					HasFrontend: true, FrontendName: "web",
					// orders and billing run in `_api`, the binary's `server`.
					Bindings:    "    _hosted(wl.migrate)\n    _hosted(_api)",
					APIWorkload: goldenAPIWorkload,
				})
			},
		},
		{
			name: "env_cloud_staging_bare.k",
			render: func(t *testing.T) []byte {
				return renderDeployTemplate(t, "kcl/cloud/main.k.tmpl", EnvTemplateData{
					ProjectName: "shop", EnvName: "staging", Bindings: "    _hosted(wl.migrate)",
					// Declared, unbound until the first service: no idle workload.
					APIWorkload: goldenAPIWorkload,
				})
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render(t)
			goldenPath := filepath.Join("testdata", "golden", tc.name+".golden")

			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatalf("mkdir golden dir: %v", err)
				}
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				t.Logf("updated %s (%d bytes)", goldenPath, len(got))
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v\nhint: run `go test ./internal/templates/ -run TestGoldenSnapshots -update` to create it",
					goldenPath, err)
			}

			if string(got) != string(want) {
				t.Fatalf("%s snapshot mismatch\n"+
					"hint: review the diff; if intentional, rerun with -update\n"+
					"want (%d bytes):\n%s\n\ngot (%d bytes):\n%s",
					tc.name, len(want), string(want), len(got), string(got))
			}
		})
	}
}
