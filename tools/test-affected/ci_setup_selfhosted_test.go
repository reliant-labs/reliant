package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSetupRipgrepOnSelfHostedRunners runs the setup action's Install ripgrep
// step as each kind of runner sees it. merge-queue.yml runs the merge group on
// the cicd self-hosted runners, which have no sudo and a read-only /usr: there
// the step must never call sudo and must leave the pinned rg on PATH. A
// GitHub-hosted runner keeps the sudo install into /usr/local/bin.
// TestSetupInstallsCachedTools covers the download and cache paths.
func TestSetupRipgrepOnSelfHostedRunners(t *testing.T) {
	for _, tool := range []string{"bash", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	var action struct {
		Runs struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(readRepo(t, filepath.Join(setupDir, "action.yml")), &action); err != nil {
		t.Fatal(err)
	}
	var run string
	for _, s := range action.Runs.Steps {
		if s.Name != "Install ripgrep" {
			continue
		}
		// The step decides from the runner's own context, never an input a
		// caller could leave unset.
		if got := s.Env["RUNNER_ENV"]; got != "${{ runner.environment }}" {
			t.Fatalf("Install ripgrep: env RUNNER_ENV = %q, want ${{ runner.environment }}", got)
		}
		run = s.Run
	}
	if run == "" {
		t.Fatal("setup action has no Install ripgrep step")
	}
	const rgDir = "ripgrep-14.1.1-x86_64-unknown-linux-musl"
	fixture := tgz(t, map[string]string{rgDir + "/rg": "#!/bin/sh\necho rg\n"})

	for _, tc := range []struct {
		runnerEnv string
		sudoFails bool
		wantSudo  bool
		wantPath  bool
	}{
		{runnerEnv: "github-hosted", wantSudo: true},
		{runnerEnv: "self-hosted", sudoFails: true, wantPath: true},
	} {
		t.Run(tc.runnerEnv, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			home, tmp, bin, actionDir := filepath.Join(dir, "home"), filepath.Join(dir, "tmp"), filepath.Join(dir, "bin"), filepath.Join(dir, "action")
			for _, d := range []string{home, tmp, bin, actionDir} {
				if err := os.Mkdir(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fix := filepath.Join(dir, "fixture")
			if err := os.WriteFile(fix, fixture, 0o644); err != nil {
				t.Fatal(err)
			}
			writeExec(t, filepath.Join(actionDir, "fetch-release-asset.sh"), "#!/usr/bin/env bash\ncp "+fix+" \"$4\"\n")
			sudoLog, ghPath := filepath.Join(dir, "sudo.log"), filepath.Join(dir, "github_path")
			sudo := "#!/usr/bin/env bash\necho \"$*\" >>" + sudoLog + "\n"
			if tc.sudoFails {
				sudo += "echo 'sudo: no new privileges' >&2\nexit 1\n"
			}
			writeExec(t, filepath.Join(bin, "sudo"), sudo)

			cmd := exec.CommandContext(t.Context(), "bash", "-e", "-c", run)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HOME="+home, "GITHUB_ACTION_PATH="+actionDir,
				"RUNNER_TEMP="+tmp, "GITHUB_PATH="+ghPath, "GH_TOKEN=tok", "RUNNER_ENV="+tc.runnerEnv)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Install ripgrep as %s: %v\n%s", tc.runnerEnv, err, out)
			}

			cache := filepath.Join(home, ".cache", "ci-tools", "rg-14.1.1")
			ran, _ := os.ReadFile(sudoLog)
			if got := strings.TrimSpace(string(ran)); tc.wantSudo != (got != "") {
				t.Errorf("sudo ran %q; want it run: %v", got, tc.wantSudo)
			} else if tc.wantSudo && got != "install -m 0755 "+cache+"/rg /usr/local/bin/rg" {
				t.Errorf("sudo ran %q, want the install into /usr/local/bin", got)
			}

			paths, _ := os.ReadFile(ghPath)
			exported := strings.Fields(string(paths))
			if onPath := len(exported) == 1 && exported[0] == cache; onPath != tc.wantPath {
				t.Fatalf("GITHUB_PATH = %q; want %s exported: %v", paths, cache, tc.wantPath)
			}
			if !tc.wantPath {
				return
			}
			// rg resolves from PATH as the next step sees it, and runs.
			look := exec.CommandContext(t.Context(), "bash", "-c", "command -v rg && rg")
			look.Env = append(os.Environ(), "PATH="+cache+":/usr/bin:/bin")
			out, err := look.CombinedOutput()
			if err != nil || !strings.HasPrefix(string(out), filepath.Join(cache, "rg")+"\n") || !strings.HasSuffix(string(out), "rg\n") {
				t.Errorf("rg after setup: %v\n%s", err, out)
			}
		})
	}
}
