// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExpressClassification(t *testing.T) {
	g := &Gate{patterns: parsePatterns(DefaultCommands)}
	nine := strings.Repeat("./internal/a ", 9)
	tests := []struct {
		line    string
		express bool
	}{
		// Scoped: explicit targets, none of them the module.
		{"go test -short ./internal/foo/", true},
		{"go test ./internal/foo/... -run X", true},
		{"go test -run 'TestA|TestB' -count=3 ./internal/foo", true},
		{"go test -count 3 -timeout 5m ./internal/foo/", true},
		{"go test -v ./internal/foo -args ./...", true}, // after -args: the test binary's
		{"go vet ./pkg/a ./pkg/b", true},
		{"go build -o bin/x ./cmd/x", true},
		{"go test github.com/reliant-labs/reliant/internal/foo", true},
		{"golangci-lint run ./internal/cli/", true},
		{"golangci-lint run -c .golangci.yml --timeout 5m ./internal/cli/...", true},
		{"task test:short -- ./internal/x/...", true},
		{"task test -- ./internal/x/ -run TestY", true},
		{"make test-short PKGS=./internal/x", true},
		{`make test-short PKGS="./internal/x ./internal/y"`, true},
		{"npx vitest related src/a.ts src/b.ts", true},
		{"npx vitest run src/foo.test.ts", true},
		{"vitest run --reporter dot src/foo.test.ts", true},
		{"forge lint --scope internal/x", true},
		{"reliant forge lint --quiet --scope internal/x --scope proto/x", true},
		{"forge lint --scope=internal/x", true},
		// Shell lines: every heavy command must be scoped.
		{"cd x && go test ./y", true},
		{"GOFLAGS=-mod=mod go test ./internal/x", true},
		{"env CGO_ENABLED=0 go test -short ./internal/x 2>&1 | tail -20", true},
		{"go test -short ./internal/a && go vet ./internal/a", true},
		{"timeout 300 go test ./internal/a/...", true},
		{"/usr/local/go/bin/go test ./internal/a", true},
		{"cd web && npx vitest run src/foo.test.ts; git status", true},
		{"go test -short ./internal/a && go test ./...", false},
		{"go test ./internal/a; npm ci", false},

		// Bulk: whole module, no targets, or not known to be short.
		{"go test ./...", false},
		// The current directory: the classifier is not given it, and it
		// may be a module root.
		{"go test", false},
		{"go test -short -run X", false},
		{"go vet", false},
		{"go build", false},
		{"go install", false},
		{"go test .", false},
		{"go test ./", false},
		{"cd internal/foo && go test", false},
		{"go test -v -args ./internal/foo", false}, // after -args: not a package
		{"golangci-lint run .", false},
		{"go test ...", false},
		{"go test all", false},
		{"go test std", false},
		{"go build ./...", false},
		{"go vet ./internal/...", false}, // one directory deep: most of a module
		{"go test ../other/...", false},
		{"go test /abs/path/...", false},
		{"go test github.com/x/y/...", false},
		{"go test -coverpkg=./... ./internal/a", false},
		{"go test " + nine, false},   // past maxScopedTargets
		{"golangci-lint run", false}, // lints ./...
		{"golangci-lint run ./...", false},
		{"golangci-lint run --new-from-rev origin/main", false},
		{"task test", false},
		{"task test:short", false},
		{"task test:affected", false},
		{"task test:short -- -run X", false},
		{"task test:integration", false},
		{"make test-short", false},
		{"make test", false},
		{"make build test-short PKGS=./internal/x", false},
		{"make test-short PKGS=./...", false},
		{"npm test", false},
		{"npm ci", false},
		{"npm run build", false},
		{"npm run test -- src/a.test.ts", false},
		{"npx vitest", false},
		{"npx vitest run", false},
		{"vitest run --shard 1/4", false},
		{"docker build -t x .", false},
		{"forge generate", false},
		{"reliant forge generate", false},
		{"forge lint", false},
		{"forge lint --quiet", false},
		{"forge lint --scope .", false},
		{"forge lint --scope", false},
		// Not heavy at all: no lane to speak of.
		{"ls -la", false},
		{"echo go test ./internal/a", false},
	}
	for _, tt := range tests {
		if got := g.Express(Job{Command: tt.line}); got != tt.express {
			t.Errorf("Express(%q) = %v, want %v", tt.line, got, tt.express)
		}
	}

	// A declared command is bulk: the gate cannot see what it runs.
	if g.Express(Job{Command: "go test ./internal/a", Declared: true}) {
		t.Error("a declared-heavy command took the express lane")
	}
	// Argv runs no shell: its words are classified as they are.
	if !g.Express(Job{Argv: []string{"/usr/bin/go", "test", "./internal/a"}}) {
		t.Error("argv go test of one package is not express")
	}
	if g.Express(Job{Argv: []string{"go", "test", "./..."}}) {
		t.Error("argv go test ./... is express")
	}
	// A configured pattern the classifier does not know is bulk.
	custom := &Gate{patterns: parsePatterns([]string{"cargo build"})}
	if custom.Express(Job{Command: "cargo build -p one"}) {
		t.Error("an unrecognized heavy command took the express lane")
	}
	var nilGate *Gate
	if nilGate.Express(Job{Command: "go test ./internal/a"}) {
		t.Error("a nil gate has no lanes")
	}
}

func TestPrepareSetsTheExpressLane(t *testing.T) {
	dir := t.TempDir()
	g, err := New(Config{Env: []string{"FORGE_SLOTS_DIR=" + dir, "FORGE_HEAVY_SLOTS=3"}, Self: testSelf(t)})
	if err != nil {
		t.Fatal(err)
	}
	lanes := func(env []string) []string {
		var out []string
		for _, kv := range env {
			if strings.HasPrefix(kv, laneEnv+"=") {
				out = append(out, kv)
			}
		}
		return out
	}
	for _, tt := range []struct {
		line string
		env  []string
		want []string
	}{
		{"go test -short ./internal/a/", []string{"PATH=/bin"}, []string{laneEnv + "=express"}},
		{"go test ./...", []string{"PATH=/bin"}, nil},
		// A lane the command's environment names is kept as it is.
		{"go test -short ./internal/a/", []string{"PATH=/bin", laneEnv + "=bulk"}, []string{laneEnv + "=bulk"}},
	} {
		cmd := exec.Command("sh", "-c", tt.line)
		cmd.Env = tt.env
		if heavy, err := g.Prepare(cmd, Job{Command: tt.line}); !heavy || err != nil {
			t.Fatalf("%q: Prepare = %v, %v", tt.line, heavy, err)
		}
		if got := lanes(cmd.Env); !slices.Equal(got, tt.want) {
			t.Errorf("%q with %q: wrapper lane %q, want %q", tt.line, tt.env, got, tt.want)
		}
		// No flag: a wrapper binary that predates lanes must accept the line.
		if slices.ContainsFunc(cmd.Args, func(a string) bool { return strings.Contains(a, "lane") }) {
			t.Errorf("%q: the lane reached the wrapper's flags: %q", tt.line, cmd.Args)
		}
	}
}

// The lane reaches the wrapper, which hands hostslot its own environment, and
// through it the command, so a nested `forge slot run` asks for the same lane.
func TestExpressLaneReachesTheCommand(t *testing.T) {
	g, _ := oneSlotGate(t)
	run := func(job Job) string {
		t.Helper()
		c := exec.Command("sh", "-c", "echo LANE=[$"+laneEnv+"]")
		c.Env = testEnv()
		out := &syncBuf{}
		c.Stdout, c.Stderr = out, out
		if heavy, err := g.Prepare(c, job); !heavy || err != nil {
			t.Fatalf("Prepare = %v, %v", heavy, err)
		}
		done := make(chan error, 1)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { done <- c.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("command failed: %v\n%s", err, out)
			}
		case <-time.After(10 * time.Second):
			_ = c.Process.Kill()
			t.Fatalf("command still running; output:\n%s", out)
		}
		return out.String()
	}
	if out := run(Job{Command: "go test -short ./internal/a/"}); !strings.Contains(out, "LANE=[express]") {
		t.Fatalf("express command's environment:\n%s", out)
	}
	if out := run(Job{Command: "go test ./..."}); !strings.Contains(out, "LANE=[]") {
		t.Fatalf("bulk command's environment:\n%s", out)
	}
}

func TestWaitNoticeNamesTheLane(t *testing.T) {
	for _, lane := range []string{"", "express"} {
		log := &writeLog{}
		n := &waitNotice{w: log, label: "go test ./internal/a", lane: lane}
		if _, err := n.Write([]byte("⏳ forge: waiting\n")); err != nil {
			t.Fatal(err)
		}
		named := strings.Contains(log.writes[0], fmt.Sprintf("(%s lane)", lane))
		if named != (lane != "") || !WaitedFor(log.writes[0]) {
			t.Errorf("lane %q: header %q", lane, log.writes[0])
		}
	}
}
