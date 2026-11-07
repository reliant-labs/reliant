package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The scaffolded dev env runs its API under air for hot reload. On a machine
// without air, `forge env up dev` must stop BEFORE it builds or starts
// anything, naming the workload, the install command and the go-run way out
// — not reach the host phase and fail on `exec: "air": executable file not
// found`. A runner that needs nothing beyond `go`, a job, and a workload the
// run does not target are not judged.
func TestPreflightHostRunners(t *testing.T) {
	host := func(name, kind, runner string) WorkloadEntity {
		return WorkloadEntity{Name: name, Kind: kind, Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{Runner: runner}}}
	}
	e := &KCLEntities{Workloads: []WorkloadEntity{
		host("api", "service", "air"),
		host("mailer", "worker", "go-run"),
		host("migrate", "job", "air"),
	}}

	prev := hostRunnerLookPath
	t.Cleanup(func() { hostRunnerLookPath = prev })
	installed := map[string]bool{}
	hostRunnerLookPath = func(bin string) (string, error) {
		if installed[bin] {
			return "/usr/local/bin/" + bin, nil
		}
		return "", errors.New("executable file not found in $PATH")
	}

	err := preflightHostRunners(e, nil, "dev")
	if err == nil {
		t.Fatal("air is not installed and the API runs under it: want a refusal")
	}
	for _, want := range []string{"api runs under air (hot reload)", "go install github.com/air-verse/air@latest", `runner = "go-run"`, "deploy/kcl/dev/main.k"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "migrate") || strings.Contains(err.Error(), "mailer") {
		t.Errorf("a job, or a go-run workload, is not judged:\n%v", err)
	}
	if err := preflightHostRunners(e, []string{"mailer"}, "dev"); err != nil {
		t.Errorf("--target mailer does not start the API, so air is not needed: %v", err)
	}

	installed["air"] = true
	if err := preflightHostRunners(e, nil, "dev"); err != nil {
		t.Errorf("air installed: %v", err)
	}
}

// With no air on PATH, `forge env up` provisions the pinned air itself
// (declarative: the version is forge's pin, installed into forge's tool
// cache). If that cannot happen — offline, no module proxy — the API runs
// under go-run with a one-line notice rather than failing the dev loop.
func TestProvisionAir(t *testing.T) {
	newEntities := func() (*KCLEntities, *HostRuntime) {
		h := &HostRuntime{Runner: "air"}
		return &KCLEntities{Workloads: []WorkloadEntity{
			{Name: "api", Kind: "service", Runtime: RuntimeEntity{Type: RuntimeHost, Host: h}},
		}}, h
	}
	prevLook, prevInstall, prevDirs := hostRunnerLookPath, installAirFn, hostToolDirs
	t.Cleanup(func() { hostRunnerLookPath, installAirFn, hostToolDirs = prevLook, prevInstall, prevDirs })
	hostRunnerLookPath = func(string) (string, error) { return "", errors.New("not found") }

	t.Run("installs the pinned air when PATH has none", func(t *testing.T) {
		hostToolDirs = nil
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "air"+exeSuffix()), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		installAirFn = func(context.Context) (string, error) { return dir, nil }
		e, h := newEntities()
		provisionAir(context.Background(), e, nil)
		if h.Runner != "air" {
			t.Errorf("runner = %q, want air kept", h.Runner)
		}
		if err := preflightHostRunners(e, nil, "dev"); err != nil {
			t.Errorf("provisioned air must satisfy the preflight: %v", err)
		}
		cmd := exec.Command("air", "-c", ".air.toml")
		pinHostToolPath(cmd, "air")
		if cmd.Path != filepath.Join(dir, "air"+exeSuffix()) {
			t.Errorf("cmd.Path = %q, want the provisioned binary", cmd.Path)
		}
	})

	t.Run("falls back to go-run when it cannot be installed", func(t *testing.T) {
		hostToolDirs = nil
		installAirFn = func(context.Context) (string, error) { return "", errors.New("proxy unreachable\nmore") }
		e, h := newEntities()
		provisionAir(context.Background(), e, nil)
		if h.Runner != "go-run" {
			t.Errorf("runner = %q, want go-run", h.Runner)
		}
		if err := preflightHostRunners(e, nil, "dev"); err != nil {
			t.Errorf("go-run needs no tool: %v", err)
		}
	})

	t.Run("an air on PATH is used as is", func(t *testing.T) {
		hostToolDirs = nil
		hostRunnerLookPath = func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
		defer func() { hostRunnerLookPath = func(string) (string, error) { return "", errors.New("not found") } }()
		installAirFn = func(context.Context) (string, error) { t.Fatal("must not install when air is on PATH"); return "", nil }
		e, h := newEntities()
		provisionAir(context.Background(), e, nil)
		if h.Runner != "air" {
			t.Errorf("runner = %q", h.Runner)
		}
	})
}
