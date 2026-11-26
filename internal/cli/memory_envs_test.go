package cli

import (
	"strings"
	"testing"
)

// staleHostingClaims are scaffold-only claims or example commands. No live
// memory can infer them from directory names.
var staleHostingClaims = []string{
	"staging/prod are already bound",
	"Staging and prod are already bound",
	"Staging and prod are both bound",
	"env deploy staging",
	"env build prod",
	"--env prod",
}

// memoryWithEnvs renders the memory of a project whose deploy/kcl declares
// exactly envs, on top of files.
func memoryWithEnvs(t *testing.T, files map[string]string, envs ...string) string {
	t.Helper()
	tree := map[string]string{"deploy/kcl/kcl.mod": "[package]\nname = \"shop\"\n"}
	for _, e := range envs {
		tree["deploy/kcl/"+e+"/main.k"] = "import forge\n"
	}
	for k, v := range files {
		tree[k] = v
	}
	root := t.TempDir()
	writeTree(t, root, tree)
	return renderMemoryAs(t, root)
}

func goServiceFiles() map[string]string {
	return map[string]string{
		"forge.yaml":                          "name: shop\nmodule_path: example.com/shop\n",
		"go.mod":                              "module example.com/shop\n",
		"Taskfile.yml":                        "version: '3'\ntasks:\n  test: {cmds: [go test ./...]}\n",
		"internal/handlers/orders/service.go": "package orders\n",
	}
}

func siteFiles() map[string]string {
	return map[string]string{
		"forge.yaml":     "name: shop\n",
		"package.json":   `{"name":"shop","scripts":{"build":"next build"},"dependencies":{"next":"^16"}}`,
		"next.config.ts": "export default { output: \"export\" }\n",
	}
}

// A project whose envs are not staging and prod (reliant: dev, demo,
// desktop) is not told staging and prod are bound to Reliant hosting: its
// guide names the envs it declares and claims no hosting binding for them.
func TestMemoryNamesTheProjectsRealEnvs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, bind string
		files      map[string]string
	}{
		{"go-service", "<workload>", goServiceFiles()},
		{"static-site", "<frontend>", siteFiles()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			memory := memoryWithEnvs(t, tc.files, "dev", "demo", "desktop")
			for _, banned := range staleHostingClaims {
				if strings.Contains(memory, banned) {
					t.Errorf("memory for envs dev/demo/desktop claims %q", banned)
				}
			}
			for _, want := range []string{
				"This project's envs are `demo`, `desktop`, `dev`",
				"never assume one is hosted",
				"env new <name> --from <env> --bind " + tc.bind + "=hosted",
			} {
				if !strings.Contains(memory, want) {
					t.Errorf("memory for envs dev/demo/desktop lacks %q", want)
				}
			}
		})
	}
}

// A project with no deploy tree is told nothing is bound to hosting yet.
func TestMemoryWithNoEnvsClaimsNoHosting(t *testing.T) {
	t.Parallel()
	memory := memoryWithEnvs(t, goServiceFiles())
	for _, banned := range staleHostingClaims {
		if strings.Contains(memory, banned) {
			t.Errorf("memory with no envs claims %q", banned)
		}
	}
	if !strings.Contains(memory, "declares no env yet") {
		t.Error("memory with no envs does not say so")
	}
}

func TestMemoryDoesNotInferHostingFromStagingAndProd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		files   map[string]string
		runtime string
	}{
		{"go-service", goServiceFiles(), `forge.OnKubernetes {}`},
		{"static-site", siteFiles(), `forge.OnFirebase {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, env := range []string{"staging", "prod"} {
				tc.files["deploy/kcl/"+env+"/main.k"] = "import forge\nruntime = " + tc.runtime + "\n"
			}
			memory := memoryWithEnvs(t, tc.files, "dev", "staging", "prod")
			for _, banned := range staleHostingClaims {
				if strings.Contains(memory, banned) {
					t.Errorf("live memory for rebound envs claims %q", banned)
				}
			}
			for _, want := range []string{"This project's envs are `dev`, `prod`, `staging`", "Names don't establish where an env runs", "env render <env>"} {
				if !strings.Contains(memory, want) {
					t.Errorf("live memory for rebound envs lacks %q", want)
				}
			}
		})
	}
}
