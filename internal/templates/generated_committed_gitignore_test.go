package templates_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// The scaffold's commit policy, asserted against REAL git rather than a
// hand-rolled matcher:
//
//   - generated code is COMMITTED. A scaffolded project's CI went red on
//     every check that compiles (`undefined: UnauthenticatedProcedures`)
//     because the template ignored gen/*, *_gen.go and the mocks while
//     tracked code imported them and no CI job regenerates. Ignored
//     generated files are also invisible to `forge ci verify-generated`,
//     which diffs with `git status --porcelain`.
//   - machine-local materializations are NEVER committed: .forge-kcl/ (the
//     KCL module embedded in whichever forge binary is running) and a web
//     frontend's dev runtime document public/config.js (which bakes in
//     machine-local ports). A committed copy of either drifts per machine.
//
// `git check-ignore` is the oracle because gitignore semantics (`**`,
// anchoring, negation reachability) are exactly what a string match gets
// wrong.

// generatedPaths are representative paths forge (or buf, driven by forge)
// writes. None of them may be ignored.
var generatedPaths = []string{
	"gen/go.mod",
	"gen/go.sum",
	"gen/forge_descriptor.json",
	"gen/services/api/v1/api.pb.go",
	"gen/services/api/v1/apiv1connect/api.connect.go",
	"gen/config/v1/config.pb.go",
	"internal/handlers/api/handlers_crud_ops_gen.go",
	"handlers/api/handlers_gen.go",
	"pkg/middleware/procedures_gen.go",
	"internal/app/mounts_services_gen.go",
	"mocks/api_service_mock_gen.go",
	"handlers/mocks/mock_api_service.go",
	"frontends/web/src/gen/services/api/v1/api_pb.ts",
	"frontends/web/src/hooks/api-service-hooks_gen.ts",
	"frontends/web/src/lib/config_gen.ts",
}

func TestProjectGitignore_CommitsGeneratedCode(t *testing.T) {
	body, err := templates.ProjectTemplates().Get(".gitignore")
	if err != nil {
		t.Fatalf("read project .gitignore template: %v", err)
	}
	ignored := checkIgnored(t, map[string]string{".gitignore": string(body)}, generatedPaths)
	for _, p := range generatedPaths {
		if rule, ok := ignored[p]; ok {
			t.Errorf("%s is generated code but the scaffold .gitignore ignores it (%s) — a fresh clone would not build", p, rule)
		}
	}
}

func TestProjectGitignore_IgnoresVendoredForgeKCL(t *testing.T) {
	body, err := templates.ProjectTemplates().Get(".gitignore")
	if err != nil {
		t.Fatalf("read project .gitignore template: %v", err)
	}
	paths := []string{".forge-kcl/kcl.mod", ".forge-kcl/schema.k", ".forge-kcl/.forge-version"}
	ignored := checkIgnored(t, map[string]string{".gitignore": string(body)}, paths)
	for _, p := range paths {
		if _, ok := ignored[p]; !ok {
			t.Errorf("%s must be ignored: .forge-kcl/ is a machine-local materialization of the running forge's KCL module", p)
		}
	}
}

func TestFrontendGitignore_CommitsGeneratedCodeAndIgnoresDevRuntimeConfig(t *testing.T) {
	for _, kind := range []string{"nextjs", "vite-spa", "react-native"} {
		t.Run(kind, func(t *testing.T) {
			body, err := templates.FrontendTemplates().Get(filepath.Join(kind, ".gitignore"))
			if err != nil {
				t.Fatalf("read %s .gitignore template: %v", kind, err)
			}
			files := map[string]string{"frontends/web/.gitignore": string(body)}
			committed := []string{
				"frontends/web/src/gen/services/api/v1/api_pb.ts",
				"frontends/web/src/gen/config/v1/web_config_pb.ts",
				"frontends/web/public/favicon.ico",
			}
			ignored := checkIgnored(t, files, committed)
			for _, p := range committed {
				if rule, ok := ignored[p]; ok {
					t.Errorf("%s must be committed but the %s template ignores it (%s)", p, kind, rule)
				}
			}
			if kind == "react-native" {
				return // no served static root, so no dev runtime document
			}
			const devDoc = "frontends/web/public/config.js"
			if _, ok := checkIgnored(t, files, []string{devDoc})[devDoc]; !ok {
				t.Errorf("%s must be ignored: the dev runtime document bakes in machine-local ports", devDoc)
			}
		})
	}
}

// checkIgnored writes files into a throwaway git repo and returns, for each
// of paths that git ignores, the matching rule ("source:line:pattern").
// Paths need not exist: check-ignore judges the rules alone.
func checkIgnored(t *testing.T, files map[string]string, paths []string) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "-C", dir, "check-ignore", "-v", "--no-index", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			t.Fatalf("git check-ignore: %v", err)
		}
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		rule, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		// A negation match means NOT ignored.
		if parts := strings.SplitN(rule, ":", 3); len(parts) == 3 && strings.HasPrefix(parts[2], "!") {
			continue
		}
		got[path] = rule
	}
	return got
}
