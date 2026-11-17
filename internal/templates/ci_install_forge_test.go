package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The forge a CI job installs is decided by the PROJECT at run time, never
// stamped into the workflow when it was scaffolded.
//
// Every workflow here is "yours": written once, never rewritten. A version
// stamped into one is therefore frozen at scaffold time while go.mod moves on
// with every forge bump. houndersclub is the incident: ci.yml pinned
// `cmd/forge@7787cb0e…` in four places after go.mod had moved to 7355bcb3,
// and verify-generated failed with "refusing to overwrite .forge-kcl/ with an
// OLDER forge's KCL module" — CI was checking the tree with a different
// generator than the one that wrote it.

// installForgeScripts returns the `run:` body of every "Install forge" step
// in a rendered workflow.
func installForgeScripts(t *testing.T, workflow []byte) []string {
	t.Helper()
	var parsed struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflow, &parsed); err != nil {
		t.Fatalf("workflow is not valid YAML: %v\n%s", err, workflow)
	}
	var scripts []string
	for _, job := range parsed.Jobs {
		for _, step := range job.Steps {
			if step.Name == "Install forge" {
				scripts = append(scripts, step.Run)
			}
		}
	}
	return scripts
}

// fullCIData turns on every job that installs forge.
func fullCIData() CIWorkflowData {
	return CIWorkflowData{
		ProjectName:         "demo",
		HasFrontends:        true,
		Frontends:           []FrontendCIConfig{{Name: "web", Path: "frontends/web"}},
		HasServices:         true,
		LintGolangci:        true,
		LintBuf:             true,
		LintMigrationSafety: true,
		VulnGo:              true,
		VulnNPM:             true,
		E2EEnabled:          true,
		E2ERuntime:          "k3d",
		HasKCL:              true,
		VerifyGenerated:     true,
		PermContents:        "read",
	}
}

var stampedForgeRefRE = regexp.MustCompile(`cmd/forge@[^"$\s]`)

// Every workflow that installs forge — every such job in ci.yml, deploy.yml,
// and e2e.yml's k3d runtime — runs installForgeScript, byte for
// byte, and none carries a literal ref. A second, divergent install recipe is
// how one job ends up checking with a different forge than its neighbour, and
// how a fix lands in three copies of a script and misses the fourth.
func TestCIWorkflows_InstallForgeFromProjectAtRunTime(t *testing.T) {
	render := func(name string, data any) []byte {
		t.Helper()
		out, err := CITemplates("github").Render(name, data)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return out
	}
	workflows := map[string][]byte{
		"ci.yml": render("ci.yml.tmpl", fullCIData()),
		"deploy.yml": render("deploy.yml.tmpl", DeployWorkflowData{
			ProjectName: "demo", Environments: []DeployEnv{{Name: "prod", Protection: true}},
		}),
		"e2e.yml": render("e2e.yml.tmpl", E2EWorkflowData{ProjectName: "demo", Runtime: "k3d"}),
	}
	// ci.yml installs forge in `checks` (the gate re-enters it) and in the k3d
	// e2e job; the per-check jobs that each installed it are folded into
	// `checks`, which is the point of the consolidation.
	want := map[string]int{"ci.yml": 2, "deploy.yml": 1, "e2e.yml": 1}
	for name, wf := range workflows {
		if m := stampedForgeRefRE.Find(wf); m != nil {
			t.Errorf("%s stamps a forge ref at scaffold time (%q) — it will drift from go.mod on the next forge bump", name, m)
		}
		scripts := installForgeScripts(t, wf)
		if len(scripts) != want[name] {
			t.Errorf("%s has %d Install forge steps, want %d", name, len(scripts), want[name])
		}
		for i, s := range scripts {
			if s != installForgeScript {
				t.Errorf("%s Install forge step %d is not installForgeScript:\n%s", name, i, s)
			}
		}
	}
}

// forgeTestPin is a real forge pseudo-version, served below from a local
// file:// module proxy — never from the network or the developer's cache.
const forgeTestPin = "v0.1.18-0.20260927181843-7355bcb3af9c"

// hermeticGoEnv is an environment in which the go command can resolve
// github.com/reliant-labs/forge ONLY from proxyURL: an empty module cache,
// and none of the developer's GOPRIVATE / GONOSUMDB / GOFLAGS, which (on a
// forge maintainer's machine, GOPRIVATE=github.com/reliant-labs/*) make the
// go command fetch forge over git and silently defeat GOPROXY=off. That is
// how this test once passed locally and failed on a clean runner.
func hermeticGoEnv(t *testing.T, proxyURL string, extraPath string) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GONOSUMCHECK", "GOFLAGS", "GOPROXY",
			"GOSUMDB", "GOMODCACHE", "GOWORK", "GOTOOLCHAIN", "GOENV", "PATH":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"PATH="+extraPath+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOPROXY="+proxyURL,
		"GOSUMDB=off",
		"GOMODCACHE="+t.TempDir(),
		"GOTOOLCHAIN=local",
		"GOENV=off",
	)
}

// writeForgeModuleProxy lays out a file:// GOPROXY holding exactly the
// metadata `go list -m github.com/reliant-labs/forge` reads for forgeTestPin.
func writeForgeModuleProxy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "github.com", "reliant-labs", "forge", "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		forgeTestPin + ".mod":  "module github.com/reliant-labs/forge\n\ngo 1.24\n",
		forgeTestPin + ".info": `{"Version":"` + forgeTestPin + `","Time":"2026-09-27T18:18:43Z"}`,
		"list":                 forgeTestPin + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A file URL's path is absolute and slash-separated: /tmp/x on POSIX,
	// /C:/Users/x on Windows. Without the leading slash a drive letter
	// would parse as the URL's HOST and the proxy would resolve nothing.
	p := filepath.ToSlash(root)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}

// Runs installForgeScript against real modules with the real go toolchain,
// with `go install` stubbed to print what it WOULD install, in an environment
// where forge resolves only from a local proxy (or not at all).
//
// The load-bearing case is "go.mod requires forge, and it does not resolve":
// the previous script read `go list -m … 2>/dev/null || <forge.yaml>`, so on a
// clean CI runner the failed lookup silently installed forge.yaml's OLDER
// forge — exactly the drift the script exists to prevent (#273's first CI
// run). A failed resolution must fail the step and install nothing.
func TestCIWorkflows_InstallForgeScriptResolvesFromProject(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the install script with a real go toolchain against a local module proxy; runs in task test")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("go not on PATH")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("bash not on PATH")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("jq not on PATH — installForgeScript requires it (GitHub-hosted runners ship it)")
	}

	// `go install` reports its target; every other go subcommand is real.
	shimDir := t.TempDir()
	// goBin is single-quoted and slash-separated: sh reads a backslash as an
	// escape, so a native Windows path (C:\hostedtoolcache\...) reached exec
	// as "C:hostedtoolcache..." and the shim exited 127.
	shim := "#!/bin/sh\nif [ \"$1\" = install ]; then echo \"INSTALL $2\"; exit 0; fi\nexec '" + filepath.ToSlash(goBin) + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	proxy := writeForgeModuleProxy(t)

	requires := "module example.com/svc\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge " + forgeTestPin + "\n"
	cases := []struct {
		name      string
		goMod     string
		forgeYAML string
		proxy     string
		want      string // INSTALL line, or "" when the step must fail
		wantErr   string
	}{
		{
			name:      "service: the version go.mod requires, not forge.yaml's",
			goMod:     requires,
			forgeYAML: "forge_version: v0.1.17\n",
			proxy:     proxy,
			want:      "INSTALL github.com/reliant-labs/forge/cmd/forge@" + forgeTestPin,
		},
		{
			name:      "service whose forge cannot be resolved FAILS, never falls back to forge.yaml",
			goMod:     requires,
			forgeYAML: "forge_version: v0.1.17\n",
			proxy:     "off",
			wantErr:   "module lookup disabled by GOPROXY=off",
		},
		{
			name:      "an indirect requirement still pins forge from go.mod",
			goMod:     "module example.com/svc\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge " + forgeTestPin + " // indirect\n",
			forgeYAML: "forge_version: v0.1.17\n",
			proxy:     proxy,
			want:      "INSTALL github.com/reliant-labs/forge/cmd/forge@" + forgeTestPin,
		},
		{
			name:      "cli/library: go.mod does not require forge, forge.yaml pins it",
			goMod:     "module example.com/cli\n\ngo 1.24\n",
			forgeYAML: "name: cli\nforge_version: " + forgeTestPin + "\n",
			proxy:     "off",
			want:      "INSTALL github.com/reliant-labs/forge/cmd/forge@" + forgeTestPin,
		},
		{
			name:      "replace: refuses rather than installing a different forge",
			goMod:     requires + "\nreplace github.com/reliant-labs/forge => ../forge\n",
			forgeYAML: "forge_version: " + forgeTestPin + "\n",
			proxy:     "off",
			wantErr:   "::error file=go.mod::go.mod replaces github.com/reliant-labs/forge with ../forge",
		},
		{
			name:      "an uninstallable (+dirty) forge_version fails by name",
			goMod:     "module example.com/cli\n\ngo 1.24\n",
			forgeYAML: "forge_version: " + forgeTestPin + "+dirty\n",
			proxy:     "off",
			wantErr:   "::error file=go.mod::no installable forge version (got '" + forgeTestPin + "+dirty')",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(c.goMod), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(c.forgeYAML), 0o644); err != nil {
				t.Fatal(err)
			}
			// GitHub Actions runs `run:` blocks with `bash -e {0}`; the
			// script sets its own -euo pipefail on top.
			cmd := exec.Command(bash, "-e", "-c", installForgeScript)
			cmd.Dir = dir
			cmd.Env = hermeticGoEnv(t, c.proxy, shimDir)
			out, err := cmd.CombinedOutput()
			got := strings.TrimSpace(string(out))
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("install step succeeded, want failure %q; output:\n%s", c.wantErr, got)
				}
				if !strings.Contains(got, c.wantErr) {
					t.Fatalf("install step failed without naming the cause; want %q in:\n%s", c.wantErr, got)
				}
				if strings.Contains(got, "INSTALL ") {
					t.Fatalf("install step installed a forge despite failing:\n%s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("install step failed: %v\n%s", err, got)
			}
			if !strings.Contains(got, c.want) {
				t.Fatalf("install step installed the wrong forge; want %q in:\n%s", c.want, got)
			}
		})
	}
}

// verify-generated can only verify what it can regenerate. The job used to
// install forge and nothing else: no protoc-gen-go / protoc-gen-connect-go /
// goimports (so the Go stubs regenerated differently or not at all), and no
// Node + `npm ci` (so forge skipped every frontend's TypeScript stubs with a
// warning and certified the tree anyway) — houndersclub PR #8.
//
// verify-generated now runs inside `checks`, as one row of `task ci:checks`,
// so the same toolchain must be in place before THAT step.
func TestCIWorkflow_VerifyGeneratedHasItsToolchain(t *testing.T) {
	ci, err := CITemplates("github").Render("ci.yml.tmpl", fullCIData())
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Jobs map[string]struct {
			Steps []struct {
				Name             string `yaml:"name"`
				Uses             string `yaml:"uses"`
				Run              string `yaml:"run"`
				WorkingDirectory string `yaml:"working-directory"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(ci, &parsed); err != nil {
		t.Fatal(err)
	}
	job, ok := parsed.Jobs["checks"]
	if !ok {
		t.Fatal("no checks job")
	}
	order := map[string]int{}
	for i, s := range job.Steps {
		switch {
		case s.Run == "forge tools install --force":
			order["tools"] = i
		case strings.HasPrefix(s.Uses, "actions/setup-node@"):
			order["node"] = i
		case s.Run == "pnpm install --frozen-lockfile" && s.WorkingDirectory == "frontends/web":
			order["npm"] = i
		case s.Run == "task ci:checks":
			order["gate"] = i
		}
	}
	for _, step := range []string{"tools", "node", "npm", "gate"} {
		if _, ok := order[step]; !ok {
			t.Errorf("checks (which runs verify-generated) is missing its %s step", step)
		}
	}
	for _, before := range []string{"tools", "node", "npm"} {
		if order[before] > order["gate"] {
			t.Errorf("%s runs after the gate that runs verify-generated", before)
		}
	}
}
