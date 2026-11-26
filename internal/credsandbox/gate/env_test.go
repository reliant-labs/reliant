// Copyright (c) 2025 Reliant Labs
package gate

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The gate's environment is built from BaseEnv and the Spec alone: a variable
// this process holds — a daemon token, a proxy, anything — never appears.
func TestGateEnvNeverInheritsThisProcess(t *testing.T) {
	t.Setenv("RELIANT_GATE_TEST_CANARY", "daemon-secret")
	t.Setenv("GITHUB_TOKEN", "ghp_canary")
	t.Setenv("PATH", "/daemon/only/bin")
	env, err := GateEnv("run-1", map[string]string{"GOFLAGS": "-mod=mod"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range env {
		if strings.Contains(kv, "canary") || strings.Contains(kv, "daemon-secret") || strings.Contains(kv, "/daemon/only") {
			t.Errorf("gate env holds this process's value: %s", kv)
		}
	}
	want := []string{
		"CI=true",
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"GOFLAGS=-mod=mod",
		"HOME=/gate/home",
		"LANG=C.UTF-8",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"RELIANT_GATE=1",
		"RELIANT_RUN_ID=run-1",
		"TMPDIR=/gate/tmp",
		"TZ=UTC",
	}
	if !slices.Equal(env, want) {
		t.Errorf("GateEnv =\n%v\nwant\n%v", env, want)
	}
}

func TestGateEnvEveryEntryHasAValue(t *testing.T) {
	env, err := GateEnv("run-1", map[string]string{"EMPTY": ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range env {
		if !strings.Contains(kv, "=") {
			t.Errorf("bare %q would let the docker CLI copy this process's value in", kv)
		}
	}
	if !slices.Contains(env, "EMPTY=") {
		t.Errorf("an empty gate.env value must be passed as EMPTY=, got %v", env)
	}
}

func TestGateEnvOverridesAndReserved(t *testing.T) {
	env, err := GateEnv("run-1", map[string]string{"PATH": "/x", "CI": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "PATH=/x") || !slices.Contains(env, "CI=1") {
		t.Errorf("gate.env may override PATH and CI: %v", env)
	}
	for name := range ReservedEnv {
		if _, err := GateEnv("run-1", map[string]string{name: "x"}); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("gate.env setting %s: %v, want ErrInvalidSpec", name, err)
		}
	}
	for _, name := range []string{"", "1A", "A B", "A=B", "A\nB"} {
		if _, err := GateEnv("run-1", map[string]string{name: "x"}); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("gate.env name %q: %v, want ErrInvalidSpec", name, err)
		}
	}
}
