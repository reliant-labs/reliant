package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The buf plugin pre-flight (generate_buf_preflight.go). Reproduced on
// reliant adopting forge in place: its kept buf.gen.yaml runs
// `local: [node, tools/protoc-gen-es/node_modules/protoc-gen-es-v2.15.0/bin/protoc-gen-es]`,
// `npm ci` had never run in tools/protoc-gen-es, and generate died with a Node
// MODULE_NOT_FOUND stack trace from inside buf. These pin that the pre-flight
// names the plugin and the fix BEFORE buf runs, and that it stays silent for
// every form it cannot decide statically.

// reliantPluginScript is the plugin path reliant's buf.gen.yaml names.
const reliantPluginScript = "tools/protoc-gen-es/node_modules/protoc-gen-es-v2.15.0/bin/protoc-gen-es"

// preflightProject lays out a project for the pre-flight: the given files
// (relative path → body; a body starting with "#!" is written executable) and
// a bin dir of fake programs to use as PATH.
func preflightProject(t *testing.T, files map[string]string, programs ...string) (dir string, env []string) {
	t.Helper()
	requirePOSIXFake(t, "plugin programs")
	// The pre-flight installs a missing frontend's packages; a test that is
	// not about that must never reach the host's real npm.
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	dir = t.TempDir()
	for rel, body := range files {
		mode := os.FileMode(0o644)
		if strings.HasPrefix(body, "#!") {
			mode = 0o755
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	for _, prog := range programs {
		if err := os.WriteFile(filepath.Join(bin, prog), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, []string{"PATH=" + bin, "GOWORK=", "GOFLAGS="}
}

func bufGenV2(plugins string) string {
	return "version: v2\nplugins:\n" + plugins
}

func TestPreflightBufTemplate_ReliantNodeScriptNotInstalled(t *testing.T) {
	tmpl := bufGenV2("  - local: [node, " + reliantPluginScript + "]\n    out: web/src/gen\n")
	dir, env := preflightProject(t, map[string]string{
		"buf.gen.yaml":                          tmpl,
		"tools/protoc-gen-es/package.json":      "{}",
		"tools/protoc-gen-es/package-lock.json": "{}",
	}, "node")

	err := preflightBufTemplate(dir, "buf.gen.yaml", nil, env)
	if err == nil {
		t.Fatal("a node script that does not exist passed the pre-flight; buf would have died inside node")
	}
	msg := err.Error()
	for _, want := range []string{
		"buf.gen.yaml",
		reliantPluginScript,
		"out: web/src/gen",
		"tools/protoc-gen-es/node_modules, which is not installed",
		"run `npm ci` in tools/protoc-gen-es",
		"`buf generate` was not started",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("pre-flight error does not mention %q:\n%s", want, msg)
		}
	}

	// Installed, the same template passes.
	p := filepath.Join(dir, filepath.FromSlash(reliantPluginScript))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := preflightBufTemplate(dir, "buf.gen.yaml", nil, env); err != nil {
		t.Errorf("an installed node script was refused: %v", err)
	}
}

func TestPreflightBufTemplate_Flags(t *testing.T) {
	for _, tc := range []struct {
		name     string
		local    string
		files    map[string]string
		programs []string
		env      []string // appended to the project env
		want     []string // substrings of the error; nil = must pass
	}{
		{
			name:  "node itself not on PATH",
			local: "[node, scripts/gen.js]",
			files: map[string]string{"scripts/gen.js": "x"},
			want:  []string{"node is not on PATH", "install Node.js"},
		},
		{
			name:     "bare plugin not on PATH",
			local:    "protoc-gen-foo",
			programs: []string{"node"},
			want:     []string{"protoc-gen-foo is not on PATH", "install protoc-gen-foo"},
		},
		{
			name:  "a plugin forge pins names the install command",
			local: "protoc-gen-go",
			want:  []string{"protoc-gen-go is not on PATH", "tools install"},
		},
		{
			name:     "bare plugin on PATH",
			local:    "protoc-gen-foo",
			programs: []string{"protoc-gen-foo"},
		},
		{
			name:  "relative executable missing, frontend node_modules",
			local: "./frontends/web/node_modules/.bin/protoc-gen-connect-query",
			files: map[string]string{"frontends/web/package.json": "{}", "frontends/web/pnpm-lock.yaml": ""},
			want: []string{"./frontends/web/node_modules/.bin/protoc-gen-connect-query does not exist",
				"run `pnpm install --frozen-lockfile` in frontends/web"},
		},
		{
			name:  "relative executable missing, not under node_modules",
			local: "bin/protoc-gen-x",
			want:  []string{"bin/protoc-gen-x does not exist", "create bin/protoc-gen-x"},
		},
		{
			name:  "relative executable not executable",
			local: "bin/protoc-gen-x",
			files: map[string]string{"bin/protoc-gen-x": "not a script"},
			want:  []string{"bin/protoc-gen-x is not executable", "chmod +x bin/protoc-gen-x"},
		},
		{
			name:  "relative executable present",
			local: "bin/protoc-gen-x",
			files: map[string]string{"bin/protoc-gen-x": "#!/bin/sh\n"},
		},
		{
			name:     "go tool declared in go.mod",
			local:    "[go, tool, protoc-gen-go]",
			files:    map[string]string{"go.mod": "module example.com/app\n\ngo 1.24\n\ntool google.golang.org/protobuf/cmd/protoc-gen-go\n"},
			programs: []string{"go"},
		},
		{
			name:     "go tool not declared",
			local:    "[go, tool, protoc-gen-go]",
			files:    map[string]string{"go.mod": "module example.com/app\n\ngo 1.24\n"},
			programs: []string{"go"},
			want: []string{"`go tool protoc-gen-go` finds no tool directive",
				"go get -tool google.golang.org/protobuf/cmd/protoc-gen-go"},
		},
		{
			name:  "go tool declared by a go.work module",
			local: "[go, tool, protoc-gen-connect-go]",
			files: map[string]string{
				"go.mod":       "module example.com/app\n\ngo 1.24\n",
				"go.work":      "go 1.24\n\nuse (\n\t.\n\t./tools\n)\n",
				"tools/go.mod": "module example.com/tools\n\ngo 1.24\n\ntool connectrpc.com/connect/cmd/protoc-gen-connect-go\n",
			},
			programs: []string{"go"},
		},
		{
			name:  "GOWORK=off hides the workspace module's tool",
			local: "[go, tool, protoc-gen-connect-go]",
			files: map[string]string{
				"go.mod":       "module example.com/app\n\ngo 1.24\n",
				"go.work":      "go 1.24\n\nuse (\n\t.\n\t./tools\n)\n",
				"tools/go.mod": "module example.com/tools\n\ngo 1.24\n\ntool connectrpc.com/connect/cmd/protoc-gen-connect-go\n",
			},
			programs: []string{"go"},
			env:      []string{"GOWORK=off"},
			want:     []string{"`go tool protoc-gen-connect-go` finds no tool directive"},
		},
		{
			name:     "go tool with a major-version tool path",
			local:    "[go, tool, buf]",
			files:    map[string]string{"go.mod": "module example.com/app\n\ngo 1.24\n\ntool github.com/bufbuild/buf/cmd/buf/v2\n"},
			programs: []string{"go"},
		},
		{
			name:     "go toolchain built-in",
			local:    "[go, tool, cover]",
			files:    map[string]string{"go.mod": "module example.com/app\n\ngo 1.24\n"},
			programs: []string{"go"},
		},
		{
			name:     "go tool under -modfile is not decided statically",
			local:    "[go, tool, protoc-gen-go]",
			files:    map[string]string{"go.mod": "module example.com/app\n\ngo 1.24\n"},
			programs: []string{"go"},
			env:      []string{"GOFLAGS=-modfile=tools.mod"},
		},
		// Forms resolved by the program at run time: only argv[0] is checked.
		{name: "go run", local: "[go, run, example.com/protoc-gen-x@v1.2.3]", programs: []string{"go"}},
		{name: "npx", local: "[npx, protoc-gen-es]", programs: []string{"npx"}},
		{name: "node with a flag first", local: "[node, --require, ./hook.js, missing.js]", programs: []string{"node"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range tc.files {
				files[k] = v
			}
			files["buf.gen.yaml"] = bufGenV2("  - local: " + tc.local + "\n    out: gen\n")
			dir, env := preflightProject(t, files, tc.programs...)
			env = append(env, tc.env...)
			err := preflightBufTemplate(dir, "buf.gen.yaml", nil, env)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("pre-flight refused a plugin that can run (or cannot be decided statically): %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("pre-flight passed local: %s; want a refusal mentioning %q", tc.local, tc.want)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("pre-flight error does not mention %q:\n%s", want, err)
				}
			}
		})
	}
}

// Only `local:` plugins of a v2 template are resolved; remote and protoc
// built-in plugins, and any other template version, are buf's to judge.
func TestBufTemplateLocalPlugins(t *testing.T) {
	body := bufGenV2(`  - local: protoc-gen-go
    out: gen
  - local: [go, tool, protoc-gen-connect-go]
    out: gen
  - remote: buf.build/bufbuild/es
    out: web/src/gen
  - protoc_builtin: java
    out: java
  - local: []
    out: empty
  - local: [1, 2]
    out: nonsense
`)
	got := bufTemplateLocalPlugins([]byte(body))
	if len(got) != 2 {
		t.Fatalf("got %d local plugins, want 2: %+v", len(got), got)
	}
	if strings.Join(got[0].argv, " ") != "protoc-gen-go" || strings.Join(got[1].argv, " ") != "go tool protoc-gen-connect-go" {
		t.Errorf("argvs = %q, %q", got[0].argv, got[1].argv)
	}
	if got := bufTemplateLocalPlugins([]byte("version: v1\nplugins:\n  - plugin: go\n    out: gen\n    path: missing\n")); got != nil {
		t.Errorf("a v1 template was resolved: %+v", got)
	}
	if got := bufTemplateLocalPlugins([]byte("version: v2\nplugins: [\n")); got != nil {
		t.Errorf("an unparsable template was resolved: %+v", got)
	}
}

// The Go buf step refuses before buf ever runs: a fake buf records being
// invoked, and must not be.
func TestRunBufGenerateGo_PreflightRefusesBeforeBuf(t *testing.T) {
	requirePOSIXFake(t, "buf")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "buf.gen.yaml"),
		[]byte(bufGenV2("  - local: [node, "+reliantPluginScript+"]\n    out: web/src/gen\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "buf-ran")
	for name, body := range map[string]string{
		"buf":  "#!/bin/sh\ntouch " + marker + "\n",
		"node": "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// PATH holds only the fakes: with no `go`, tool pinning has nothing to
	// resolve and touches no network.
	t.Setenv("PATH", bin)

	err := runBufGenerateGo(dir)
	if err == nil || !strings.Contains(err.Error(), reliantPluginScript) {
		t.Fatalf("runBufGenerateGo = %v, want the pre-flight refusal naming the plugin", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("buf ran although the pre-flight found a plugin that cannot start")
	}
}

// fakeNPM puts an `npm` on PATH that records its arguments and, when
// materialize is non-empty, creates that executable (relative to its working
// directory) the way a real install would.
func fakeNPM(t *testing.T, materialize string) (calls func() string) {
	t.Helper()
	requirePOSIXFake(t, "npm")
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	script := "#!/bin/sh\necho \"$PWD: $*\" >> " + log + "\n"
	if materialize != "" {
		script += "mkdir -p \"$(dirname " + materialize + ")\" && printf '#!/bin/sh\\n' > " + materialize + " && chmod +x " + materialize + "\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

const feLocalPlugin = "./frontends/web/node_modules/.bin/protoc-gen-es"

// A project-owned buf template naming a frontend's local protoc-gen-es, in a
// frontend whose node_modules was never installed (a fresh worktree). forge
// installs a missing frontend's packages itself everywhere else; the
// pre-flight must too, through the same install path, instead of refusing.
func TestPreflightBufTemplate_InstallsMissingFrontendPlugin(t *testing.T) {
	calls := fakeNPM(t, "node_modules/.bin/protoc-gen-es")
	dir, _ := preflightProject(t, map[string]string{
		"buf.gen.yaml":                    bufGenV2("  - local: " + feLocalPlugin + "\n    out: frontends/web/src/gen\n"),
		"frontends/web/package.json":      "{}",
		"frontends/web/package-lock.json": "{}",
	})

	t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
	if err := preflightBufTemplate(dir, "buf.gen.yaml", nil, nil); err != nil {
		t.Fatalf("a plugin that one frontend install provides was refused: %v", err)
	}
	got := calls()
	if !strings.Contains(got, filepath.Join(dir, "frontends", "web")+": ci") {
		t.Errorf("npm was not run as `npm ci` in the frontend; calls:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "frontends", "web", "node_modules", ".bin", "protoc-gen-es")); err != nil {
		t.Errorf("plugin still absent after the pre-flight installed: %v", err)
	}
}

// FORGE_SKIP_NPM_INSTALL is honoured exactly as frontenddeps.Ensure honours
// it: no install, and the refusal names the frontend, the command, and why
// forge did not run it.
func TestPreflightBufTemplate_SkipInstallEnvRefusesWithRemedy(t *testing.T) {
	calls := fakeNPM(t, "node_modules/.bin/protoc-gen-es")
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	dir, _ := preflightProject(t, map[string]string{
		"buf.gen.yaml":                    bufGenV2("  - local: " + feLocalPlugin + "\n    out: frontends/web/src/gen\n"),
		"frontends/web/package.json":      "{}",
		"frontends/web/package-lock.json": "{}",
	})

	err := preflightBufTemplate(dir, "buf.gen.yaml", nil, nil)
	if err == nil {
		t.Fatal("pre-flight passed with the plugin absent and installs disabled")
	}
	for _, want := range []string{"frontends/web", "run `npm ci` in frontends/web", "FORGE_SKIP_NPM_INSTALL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%v", want, err)
		}
	}
	if got := calls(); got != "" {
		t.Errorf("npm ran despite FORGE_SKIP_NPM_INSTALL:\n%s", got)
	}
}

// An install that fails must refuse with the install's own failure and the
// command to reproduce it, never a bare "does not exist".
func TestPreflightBufTemplate_FailedInstallNamesFrontendAndCommand(t *testing.T) {
	requirePOSIXFake(t, "npm")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte("#!/bin/sh\necho 'ERR! registry unreachable' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir, _ := preflightProject(t, map[string]string{
		"buf.gen.yaml":                    bufGenV2("  - local: " + feLocalPlugin + "\n    out: frontends/web/src/gen\n"),
		"frontends/web/package.json":      "{}",
		"frontends/web/package-lock.json": "{}",
	})

	t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
	err := preflightBufTemplate(dir, "buf.gen.yaml", nil, nil)
	if err == nil {
		t.Fatal("pre-flight passed although the install failed")
	}
	for _, want := range []string{"frontends/web", "install", "run `npm ci` in frontends/web", "registry unreachable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%v", want, err)
		}
	}
}
