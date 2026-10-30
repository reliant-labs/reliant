// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
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
		// Heredoc bodies are data: a note-taking command that mentions a heavy
		// command is not a heavy command.
		"cat >> notes.md <<'EOF'\nrun forge lint before pushing\ngo test ./...\nEOF",
		"cat > x <<EOF\nforge generate\nEOF\nls",
		"cat >> notes.md <<-EOF\n\tnpm ci\n\tEOF",
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
	heavy = append(heavy,
		// ...but a heavy command on the opening line, or after the heredoc, is.
		"cat <<EOF && go test ./...\nbody\nEOF",
		"cat > x <<'EOF'\nbody\nEOF\ngo build ./...",
	)
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

func TestConfiguredCommandsReplaceTheDefaults(t *testing.T) {
	g, err := New(Config{Commands: []string{"cargo build", "bazel test*"}, Env: []string{"FORGE_HEAVY_SLOTS=2"}, Self: testSelf(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Heavy(Job{Command: "bazel test //..."}) || g.Heavy(Job{Command: "go test ./..."}) {
		t.Fatal("the configured list must replace the defaults")
	}

	none, err := New(Config{Commands: []string{}, Env: []string{"FORGE_HEAVY_SLOTS=2"}, Self: testSelf(t)})
	if err != nil {
		t.Fatal(err)
	}
	if none.Heavy(Job{Command: "go test ./..."}) || !none.Heavy(Job{Command: "go test ./...", Declared: true}) {
		t.Fatal("an empty list declares nothing heavy by pattern; a declared job still is")
	}

	def, err := New(Config{Env: []string{"FORGE_HEAVY_SLOTS=2"}, Self: testSelf(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !def.Heavy(Job{Command: "go test ./..."}) {
		t.Fatal("nil Commands must keep DefaultCommands")
	}
}

func TestSlotsOffRunsHeavyCommandsUnqueued(t *testing.T) {
	g, err := New(Config{Env: []string{"FORGE_HEAVY_SLOTS=off"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.String(), "unqueued") {
		t.Fatalf("String() = %q, want it to say heavy commands run unqueued", g)
	}
	cmd := exec.Command("sh", "-c", "go test ./...")
	heavy, err := g.Prepare(cmd, Job{Command: "go test ./..."})
	if !heavy || err != nil || cmd.Args[0] != "sh" {
		t.Fatalf("Prepare = %v, %v, args %q; want heavy, unrewritten", heavy, err, cmd.Args)
	}
}

type refuseAll string

func (r refuseAll) HeavyRefusal() string { return string(r) }

func TestAdmissionRefusesHeavyCommandsOnly(t *testing.T) {
	for _, env := range [][]string{{"FORGE_HEAVY_SLOTS=2"}, {"FORGE_HEAVY_SLOTS=off"}} {
		g, err := New(Config{Env: env, Self: testSelf(t), Admission: refuseAll("the disk is full")})
		if err != nil {
			t.Fatal(err)
		}
		heavy := exec.Command("sh", "-c", "go build ./...")
		isHeavy, err := g.Prepare(heavy, Job{Command: "go build ./..."})
		var refused *RefusedError
		if !isHeavy || !errors.As(err, &refused) {
			t.Fatalf("%v: Prepare = %v, %v; want a refusal", env, isHeavy, err)
		}
		if msg := err.Error(); !strings.Contains(msg, "`go build ./...` was not started") || !strings.Contains(msg, "the disk is full") {
			t.Fatalf("%v: refusal %q", env, msg)
		}
		if heavy.Args[0] != "sh" {
			t.Fatalf("%v: a refused command was rewritten: %q", env, heavy.Args)
		}

		light := exec.Command("sh", "-c", "ls")
		if isHeavy, err := g.Prepare(light, Job{Command: "ls"}); isHeavy || err != nil {
			t.Fatalf("%v: light command: %v, %v", env, isHeavy, err)
		}
	}
}

// The process guard (BackgroundAdmission) refuses only heavy commands started
// in the background: those are what piled up when a machine ran out of
// processes (2026-10-30). A foreground heavy command, which its agent waits
// on, and a light one still run.
func TestBackgroundAdmissionRefusesOnlyBackgroundHeavyCommands(t *testing.T) {
	g, err := New(Config{Env: []string{"FORGE_HEAVY_SLOTS=2"}, Self: testSelf(t), BackgroundAdmission: refuseAll("near the process limit")})
	if err != nil {
		t.Fatal(err)
	}
	bg := exec.Command("sh", "-c", "go test ./...")
	_, err = g.Prepare(bg, Job{Command: "go test ./...", Background: true})
	var refused *RefusedError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "near the process limit") {
		t.Fatalf("background heavy: err = %v, want a refusal", err)
	}
	if heavy, err := g.Prepare(exec.Command("sh", "-c", "go test ./..."), Job{Command: "go test ./..."}); !heavy || err != nil {
		t.Fatalf("foreground heavy: %v, %v; want it queued, not refused", heavy, err)
	}
	if heavy, err := g.Prepare(exec.Command("sh", "-c", "ls"), Job{Command: "ls", Background: true}); heavy || err != nil {
		t.Fatalf("background light: %v, %v", heavy, err)
	}
}

func TestPrepareRewritesOnlyHeavyCommands(t *testing.T) {
	dir := t.TempDir()
	g, err := New(Config{Env: []string{"FORGE_SLOTS_DIR=" + dir, "FORGE_HEAVY_SLOTS=3"}, Self: testSelf(t)})
	if err != nil {
		t.Fatal(err)
	}

	light := exec.Command("sh", "-c", "ls")
	if heavy, _ := g.Prepare(light, Job{Command: "ls"}); heavy {
		t.Fatal("a light command was rewritten")
	}
	if light.Args[0] != "sh" {
		t.Fatalf("light command args changed: %q", light.Args)
	}

	heavy := exec.Command("sh", "-c", "go test ./...")
	origPath := heavy.Path
	if ok, err := g.Prepare(heavy, Job{Command: "go test ./..."}); !ok || err != nil {
		t.Fatalf("a heavy command was not rewritten: %v", err)
	}
	want := []string{testSelf(t)[0], WrapperArg, "--dir", dir, "--label", "go test ./...", "--slots", "3", "--", origPath, "-c", "go test ./..."}
	if heavy.Path != testSelf(t)[0] || !slices.Equal(heavy.Args, want) {
		t.Fatalf("rewritten to %s %q, want %q", heavy.Path, heavy.Args, want)
	}
}
