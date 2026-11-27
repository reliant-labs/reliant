package gocacheprog

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// serveEnv makes this test binary act as the helper: cmd/go runs it as
// GOCACHEPROG (TestMain).
const serveEnv = "FORGE_GOCACHEPROG_TEST_SERVE"

func TestMain(m *testing.M) {
	if os.Getenv(serveEnv) == "1" {
		if err := Run(Config{}, os.Stdin, os.Stdout); err != nil {
			os.Stderr.WriteString("gocacheprog test helper: " + err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestRealGoBuildReadsTrustedTier seeds a trusted tier with plain `go`, then
// compiles the same module from another directory through the helper with an
// empty overlay: every lookup is a trusted hit, and the trusted tier is left
// exactly as it was.
func TestRealGoBuildReadsTrustedTier(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a module and its stdlib deps twice with the real go command; runs in task test")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go command unavailable: %v", err)
	}
	tmp := t.TempDir()
	trusted := filepath.Join(tmp, "trusted")
	overlay := filepath.Join(tmp, "overlay")
	stats := filepath.Join(tmp, "stats.jsonl")
	seedTree := writeModule(t, filepath.Join(tmp, "seed-checkout"))
	userTree := writeModule(t, filepath.Join(tmp, "agent-worktree"))

	base := []string{"GOFLAGS=-trimpath", "GOTOOLCHAIN=local", "GOWORK=off"}
	clean := func() []string {
		var env []string
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			switch k {
			case "GOCACHE", "GOCACHEPROG", "GOFLAGS", "GOTOOLCHAIN", "GOWORK", TrustedEnv, StatsEnv, serveEnv:
				continue
			}
			env = append(env, kv)
		}
		return append(env, base...)
	}
	run := func(dir string, env []string) {
		t.Helper()
		cmd := exec.Command(gobin, "list", "-e", "-export", "-deps", "-test", "./...")
		cmd.Dir = dir
		cmd.Env = goexec.WithTrimpath(env) // GOFLAGS is -trimpath already: unchanged
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go list in %s: %v\n%s", dir, err, out)
		}
	}

	run(seedTree, append(clean(), "GOCACHE="+trusted))
	before := listing(t, trusted)

	run(userTree, append(clean(),
		"GOCACHE="+overlay,
		"GOCACHEPROG="+os.Args[0],
		serveEnv+"=1",
		TrustedEnv+"="+trusted,
		StatsEnv+"="+stats,
	))

	if after := listing(t, trusted); after != before {
		t.Fatal("the build through the helper changed the trusted tier")
	}
	var total statsLine
	f, err := os.Open(stats)
	if err != nil {
		t.Fatalf("no stats line: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l statsLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		total.Gets += l.Gets
		total.TrustedHits += l.TrustedHits
		total.Misses += l.Misses
		total.Puts += l.Puts
	}
	t.Logf("gets %d, trusted hits %d, misses %d, puts %d", total.Gets, total.TrustedHits, total.Misses, total.Puts)
	// The only lookups a trusted tier cannot answer are cmd/go's module
	// index entries for the main module's own directories: they are keyed by
	// the directory's absolute path and file mtimes, so every checkout has
	// its own (plain GOCACHE behaves the same). Everything compiled is a hit.
	if total.Gets == 0 || total.Misses > 8 || total.TrustedHits+total.Misses != total.Gets {
		t.Fatalf("trusted hits %d of %d gets (%d misses, %d puts), want every compile a trusted hit", total.TrustedHits, total.Gets, total.Misses, total.Puts)
	}
	err = filepath.Walk(overlay, func(p string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(p, "-d") || !info.Mode().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(b), "go index") {
			t.Errorf("the overlay holds a compiled output the trusted tier should have served: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeModule(t *testing.T, dir string) string {
	t.Helper()
	files := map[string]string{
		"go.mod":              "module example.com/cacheprobe\n\ngo 1.22\n",
		"greet/greet.go":      "package greet\n\nimport \"strings\"\n\nfunc Hello(n string) string { return \"hello \" + strings.ToUpper(n) }\n",
		"greet/greet_test.go": "package greet\n\nimport \"testing\"\n\nfunc TestHello(t *testing.T) {\n\tif Hello(\"a\") != \"hello A\" {\n\t\tt.Fatal(Hello(\"a\"))\n\t}\n}\n",
		"main.go":             "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/cacheprobe/greet\"\n)\n\nfunc main() { fmt.Println(greet.Hello(\"x\")) }\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeAtomic(p, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
