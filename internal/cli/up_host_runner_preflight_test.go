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
	prevLook, prevInstall, prevDirs, prevVersion, prevOverrides := hostRunnerLookPath, installAirFn, hostToolDirs, airVersionFn, hostToolOverrides
	t.Cleanup(func() {
		hostRunnerLookPath, installAirFn, hostToolDirs, airVersionFn, hostToolOverrides = prevLook, prevInstall, prevDirs, prevVersion, prevOverrides
	})
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
		for _, version := range []string{"1.67.4", ""} { // current, and unreadable
			hostToolDirs, hostToolOverrides = nil, map[string]string{}
			hostRunnerLookPath = func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
			airVersionFn = func(context.Context, string) string { return version }
			installAirFn = func(context.Context) (string, error) { t.Fatal("must not install when air is on PATH"); return "", nil }
			e, h := newEntities()
			provisionAir(context.Background(), e, nil)
			if h.Runner != "air" {
				t.Errorf("version %q: runner = %q", version, h.Runner)
			}
			cmd := exec.Command("air", "-c", ".air.toml")
			pinHostToolPath(cmd, "air")
			if cmd.Path != "/usr/bin/air" {
				t.Errorf("version %q: cmd.Path = %q, want the air on PATH", version, cmd.Path)
			}
		}
		hostRunnerLookPath = func(string) (string, error) { return "", errors.New("not found") }
	})

	// An air below doctor's floor cannot run a config that names its binary
	// with `entrypoint`: the run would fail at the readiness wait, far from
	// the cause. forge runs its pinned air instead, and says so.
	t.Run("an air on PATH below the floor gives way to the pinned air", func(t *testing.T) {
		hostToolDirs, hostToolOverrides = nil, map[string]string{}
		hostRunnerLookPath = func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
		defer func() { hostRunnerLookPath = func(string) (string, error) { return "", errors.New("not found") } }()
		airVersionFn = func(context.Context, string) string { return "1.61.5" }
		dir := t.TempDir()
		installAirFn = func(context.Context) (string, error) { return dir, nil }
		e, h := newEntities()
		out := captureStdout(t, func() { provisionAir(context.Background(), e, nil) })
		if h.Runner != "air" {
			t.Errorf("runner = %q, want air", h.Runner)
		}
		cmd := exec.Command("air", "-c", ".air.toml")
		pinHostToolPath(cmd, "air")
		if want := filepath.Join(dir, "air"+exeSuffix()); cmd.Path != want {
			t.Errorf("cmd.Path = %q, want the pinned air %q", cmd.Path, want)
		}
		for _, want := range []string{"air 1.61.5 on PATH", "/usr/bin/air", "pinned " + airPinnedVersion} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("an old air stays, with a warning, when the pin cannot be installed", func(t *testing.T) {
		hostToolDirs, hostToolOverrides = nil, map[string]string{}
		hostRunnerLookPath = func(bin string) (string, error) { return "/usr/bin/" + bin, nil }
		defer func() { hostRunnerLookPath = func(string) (string, error) { return "", errors.New("not found") } }()
		airVersionFn = func(context.Context, string) string { return "1.61.5" }
		installAirFn = func(context.Context) (string, error) { return "", errors.New("proxy unreachable") }
		e, h := newEntities()
		out := captureStdout(t, func() { provisionAir(context.Background(), e, nil) })
		if h.Runner != "air" {
			t.Errorf("runner = %q, want air kept", h.Runner)
		}
		cmd := exec.Command("air", "-c", ".air.toml")
		pinHostToolPath(cmd, "air")
		if cmd.Path != "/usr/bin/air" {
			t.Errorf("cmd.Path = %q, want the air on PATH", cmd.Path)
		}
		for _, want := range []string{"warning: air 1.61.5", "proxy unreachable", "go install github.com/air-verse/air@latest"} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		}
	})
}
