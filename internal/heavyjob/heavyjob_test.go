// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"os/exec"
	"slices"
	"testing"
)

func TestHeavyClassification(t *testing.T) {
	g := &Gate{patterns: parsePatterns(DefaultCommands)}
	heavy := []string{
		"go test ./...",
		"go build ./cmd/reliant",
		"go vet ./internal/...",
		"cd web && npx vitest run src/foo.test.ts",
		"npm run build",
		"npm run build:prod",
		"npm run test -- --watch=false",
		"make test-short PKGS=./internal/x/...",
		"task test:integration",
		"GOFLAGS=-mod=mod go test -run X ./internal/a",
		"env CGO_ENABLED=0 GOOS=linux go build -o bin/x .",
		"time go test ./...",
		"timeout 300 go test ./...",
		"nice -n 10 go build ./...",
		"/usr/local/go/bin/go test ./...",
		"node_modules/.bin/vitest run",
		"git status; go vet ./... 2>&1 | tail -20",
		"(cd sub && go test ./...)",
		"for p in a b; do go test ./$p; done",
		"reliant forge generate",
		"forge lint --quiet",
		"docker build -t x .",
		"golangci-lint run ./...",
	}
	light := []string{
		"ls -la",
		"git status",
		"go version",
		"go env GOMODCACHE",
		"go list -m all",
		"go run ./cmd/tool",
		"rg -n 'go test' internal/",
		`git commit -m "fix go test flake; go build works"`,
		"echo go test",
		"npm run dev",
		"npm run lint",
		"task ci",
		"forge ci gate",
		"forge env up dev",
		"make dev",
		"cat Makefile # go test lives here",
		"gotest ./...",
		"echo $(go env GOPATH)",
	}
	for _, line := range heavy {
		if !g.Heavy(Job{Command: line}) {
			t.Errorf("heavy: %q classified light", line)
		}
	}
	for _, line := range light {
		if g.Heavy(Job{Command: line}) {
			t.Errorf("light: %q classified heavy", line)
		}
	}

	// Argv runs no shell: its words are matched as they are.
	if !g.Heavy(Job{Argv: []string{"/usr/bin/go", "test", "./..."}}) {
		t.Error("argv go test classified light")
	}
	if g.Heavy(Job{Argv: []string{"echo", "go test; go build"}}) {
		t.Error("argv echo classified heavy")
	}
	// The caller may declare a command heavy; nothing declares one light.
	if !g.Heavy(Job{Command: "./scripts/build-everything.sh", Declared: true}) {
		t.Error("declared-heavy command classified light")
	}
	var nilGate *Gate
	if nilGate.Heavy(Job{Command: "go test ./...", Declared: true}) {
		t.Error("a nil gate must queue nothing")
	}
}

func TestConfigFromEnvReplacesTheCommandList(t *testing.T) {
	cfg := ConfigFromEnv([]string{EnvCommands + "=cargo build, bazel test* ,,"})
	if want := []string{"cargo build", "bazel test*"}; !slices.Equal(cfg.Commands, want) {
		t.Fatalf("Commands = %q, want %q", cfg.Commands, want)
	}
	g, err := New(Config{Commands: cfg.Commands, Env: []string{"FORGE_HEAVY_SLOTS=2"}, Self: testSelf(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Heavy(Job{Command: "bazel test //..."}) || g.Heavy(Job{Command: "go test ./..."}) {
		t.Fatal("the configured list must replace the defaults")
	}

	if cfg := ConfigFromEnv(nil); cfg.Commands != nil {
		t.Fatalf("no %s must keep the defaults, got %q", EnvCommands, cfg.Commands)
	}
}

func TestNewIsOffWhenTheLimitIsOff(t *testing.T) {
	g, err := New(Config{Env: []string{"FORGE_HEAVY_SLOTS=off"}})
	if g != nil || err == nil {
		t.Fatalf("FORGE_HEAVY_SLOTS=off: got gate %v, err %v; want nil and a reason", g, err)
	}
}

func TestPrepareRewritesOnlyHeavyCommands(t *testing.T) {
	dir := t.TempDir()
	g, err := New(Config{Env: []string{"FORGE_SLOTS_DIR=" + dir, "FORGE_HEAVY_SLOTS=3"}, Self: testSelf(t)})
	if err != nil {
		t.Fatal(err)
	}

	light := exec.Command("sh", "-c", "ls")
	if g.Prepare(light, Job{Command: "ls"}) {
		t.Fatal("a light command was rewritten")
	}
	if light.Args[0] != "sh" {
		t.Fatalf("light command args changed: %q", light.Args)
	}

	heavy := exec.Command("sh", "-c", "go test ./...")
	origPath := heavy.Path
	if !g.Prepare(heavy, Job{Command: "go test ./..."}) {
		t.Fatal("a heavy command was not rewritten")
	}
	want := []string{testSelf(t)[0], WrapperArg, "--dir", dir, "--label", "go test ./...", "--slots", "3", "--", origPath, "-c", "go test ./..."}
	if heavy.Path != testSelf(t)[0] || !slices.Equal(heavy.Args, want) {
		t.Fatalf("rewritten to %s %q, want %q", heavy.Path, heavy.Args, want)
	}
}
