package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

func TestCacheSeedChildEnv(t *testing.T) {
	environ := []string{
		"HOME=/home/u",
		"GOCACHE=/var/cache/workspace/go-build",
		"GOCACHEPROG=/usr/local/bin/forge cache prog",
		"FORGE_GO_TRUSTED_CACHE=/var/cache/workspace/go-build-trusted",
		"FORGE_CACHE_PROG_STATS=/tmp/stats",
		"GOFLAGS=-mod=mod -race",
		"GOENV=/home/u/.config/go/env",
		"GOPROXY=https://proxy.example",
	}
	var asked []string
	fromFile := func(keys []string) (map[string]string, error) {
		asked = keys
		return map[string]string{"GOMODCACHE": "/var/cache/workspace/go-mod", "GOPROXY": "https://from-file"}, nil
	}
	fileVals, err := cacheSeedFileSettings(environ, fromFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range asked {
		if k == "GOPROXY" {
			t.Error("asked go's env file for GOPROXY, which the environment sets")
		}
	}
	env := cacheSeedChildEnv(environ, "/trusted", fileVals)
	got := map[string][]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = append(got[k], v)
	}
	want := map[string]string{
		"GOENV":      "off",
		"GOCACHE":    "/trusted",
		"GOFLAGS":    "-trimpath",
		"GOPROXY":    "https://proxy.example", // the environment wins over the file
		"GOMODCACHE": "/var/cache/workspace/go-mod",
		"HOME":       "/home/u",
	}
	for k, v := range want {
		if len(got[k]) != 1 || got[k][0] != v {
			t.Errorf("%s = %q, want exactly %q", k, got[k], v)
		}
	}
	for _, k := range []string{"GOCACHEPROG", "FORGE_GO_TRUSTED_CACHE", "FORGE_CACHE_PROG_STATS"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s is in the seed's environment", k)
		}
	}
}

func TestCacheSeedModules(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"go.mod", "tools/go.mod", "a/b/go.mod",
		"testdata/go.mod", "vendor/x/go.mod", "web/node_modules/y/go.mod", ".git/go.mod", "_scratch/go.mod",
		"a/b/c.go",
	} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := atomicfile.WriteFile(full, []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mods, err := cacheSeedModules(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(mods, ","); got != ".,a/b,tools" {
		t.Fatalf("modules = %s, want .,a/b,tools", got)
	}
}

func TestCacheSeedSteps(t *testing.T) {
	steps := cacheSeedSteps(cacheSeedOptions{vet: true, race: true, tags: "integration"})
	var lines []string
	for _, s := range steps {
		lines = append(lines, strings.Join(s, " "))
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"list -e -export -deps -test -tags=integration -f",
		"vet -tags=integration ./...",
		"-tags=integration -race -f",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("steps lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, " test ") || strings.Contains(got, "build") {
		t.Errorf("a seed step links or runs something:\n%s", got)
	}
}

func TestCacheAlreadySeeded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.json")
	if err := writeCacheSeedRecord(path, cacheSeedRecord{Sha: "abc", OK: true, Vet: true}); err != nil {
		t.Fatal(err)
	}
	if !cacheAlreadySeeded(path, "abc", cacheSeedOptions{vet: true}) {
		t.Error("same sha and flags: not seen as seeded")
	}
	for name, o := range map[string]cacheSeedOptions{
		"other flags": {vet: false},
		"race":        {vet: true, race: true},
		"tags":        {vet: true, tags: "e2e"},
	} {
		if cacheAlreadySeeded(path, "abc", o) {
			t.Errorf("%s: seen as seeded", name)
		}
	}
	if cacheAlreadySeeded(path, "def", cacheSeedOptions{vet: true}) {
		t.Error("another sha: seen as seeded")
	}
	if err := writeCacheSeedRecord(path, cacheSeedRecord{Sha: "abc", OK: false}); err != nil {
		t.Fatal(err)
	}
	if cacheAlreadySeeded(path, "abc", cacheSeedOptions{}) {
		t.Error("a failed seed: seen as seeded")
	}
}

func TestCacheSeedRefusesAWritableTrustedDir(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("no mode bits on Windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	err := runCacheSeed(cacheSeedOptions{repo: t.TempDir(), rev: "HEAD", trusted: dir}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("seed into a world-writable dir: %v, want a refusal", err)
	}
}

func TestCacheSeedCreatesTrustedDir0755(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("no mode bits on Windows")
	}
	dir := filepath.Join(t.TempDir(), "new", "trusted")
	got, err := ensureTrustedCacheDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("trusted dir mode = %#o, want 0755", info.Mode().Perm())
	}
}

func TestCacheSeedSkipsWhenTheLockIsHeld(t *testing.T) {
	trusted := t.TempDir()
	if err := os.Chmod(trusted, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(trusted, ".forge-seed.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if ok, err := tryLockFile(f); err != nil || !ok {
		t.Fatalf("take the lock: %v %v", ok, err)
	}
	defer unlockFile(f)
	old := cacheSeedLockWait
	cacheSeedLockWait = 0
	t.Cleanup(func() { cacheSeedLockWait = old })
	var out bytes.Buffer
	if err := runCacheSeed(cacheSeedOptions{repo: t.TempDir(), rev: "HEAD", trusted: trusted}, &out); err != nil {
		t.Fatalf("seed behind a held lock: %v", err)
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Fatalf("output = %q, want a skip", out.String())
	}
}

func TestCacheEnvLines(t *testing.T) {
	got := cacheEnvLines([]string{"/usr/local/bin/reliant", "forge"}, "/var/cache/workspace/go-build-trusted")
	for _, want := range []string{
		"GOCACHEPROG=/usr/local/bin/reliant forge cache prog\n",
		"FORGE_GO_TRUSTED_CACHE=/var/cache/workspace/go-build-trusted\n",
		"GOFLAGS=-trimpath\n",
		`#   go env -w GOCACHEPROG="/usr/local/bin/reliant forge cache prog --trusted /var/cache/workspace/go-build-trusted" GOFLAGS=-trimpath`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cache env lacks %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasPrefix(line, "#") && !strings.Contains(line, "=") {
			t.Errorf("not a KEY=value or comment line: %q", line)
		}
	}
}

func TestCacheProgInvocation(t *testing.T) {
	for args, want := range map[string]bool{
		"cache prog":                 true,
		"cache prog --trusted /x":    true,
		"cache seed":                 false,
		"cache":                      false,
		"storage gocache cache prog": false,
		"-C dir cache prog":          false,
	} {
		if got := isCacheProgInvocation(strings.Fields(args)); got != want {
			t.Errorf("isCacheProgInvocation(%q) = %v, want %v", args, got, want)
		}
	}
	if handOffModeFor("cache prog", false) != runHere || handOffModeFor("cache seed", false) != runHere {
		t.Error("cache commands are handed to the pinned forge; they must run as this forge (handOffExempt)")
	}
}

func TestCacheProgFastPathServes(t *testing.T) {
	overlay := t.TempDir()
	in := strings.NewReader(`{"ID":1,"Command":"get","ActionID":"` + strings.Repeat("A", 43) + `="}` + "\n" + `{"ID":2,"Command":"close"}` + "\n")
	var out, errOut bytes.Buffer
	args := []string{"cache", "prog", "--overlay", overlay, "--trusted", filepath.Join(t.TempDir(), "not-yet")}
	if err := runCacheProgFast(args, in, &out, &errOut); err != nil {
		t.Fatalf("cache prog: %v (stderr %q)", err, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("cache prog wrote to stderr: %q", errOut.String())
	}
	dec := json.NewDecoder(&out)
	var ids []float64
	for dec.More() {
		var res map[string]any
		if err := dec.Decode(&res); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res["ID"].(float64))
		if res["ID"].(float64) == 1 && res["Miss"] != true {
			t.Errorf("get on an empty overlay = %v, want a miss", res)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("responses %v, want hello, get, close", ids)
	}
}

// TestCacheSeedEndToEnd seeds a real git repository: the committed tree is
// built (an untracked broken file in the working tree is not), the record is
// written, and the same commit is then skipped until --force.
func TestCacheSeedEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs git and compiles a module with the real go command; runs in task test")
	}
	repo := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := atomicfile.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write("go.mod", "module example.com/seedprobe\n\ngo 1.22\n")
	write("p/p.go", "package p\n\nfunc F() int { return 1 }\n")
	write("p/p_test.go", "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = F() }\n")
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	write("poison/poison.go", "package poison\n\nthis does not compile\n") // untracked

	trusted := filepath.Join(t.TempDir(), "trusted")
	o := cacheSeedOptions{repo: repo, rev: "HEAD", trusted: trusted}
	var out bytes.Buffer
	if err := runCacheSeed(o, &out); err != nil {
		t.Fatalf("seed: %v\n%s", err, out.String())
	}
	b, err := os.ReadFile(filepath.Join(trusted, ".forge-seed", filepath.Base(repo)+".json"))
	if err != nil {
		t.Fatalf("no seed record: %v\n%s", err, out.String())
	}
	var rec cacheSeedRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if !rec.OK || len(rec.Broken) != 0 || strings.Join(rec.Modules, ",") != "." || len(rec.Sha) != 40 {
		t.Fatalf("record = %+v (the untracked broken package must not be built)\n%s", rec, out.String())
	}
	entries, _ := filepath.Glob(filepath.Join(trusted, "*", "*-a"))
	if len(entries) == 0 {
		t.Fatal("the trusted tier holds no entries")
	}

	out.Reset()
	if err := runCacheSeed(o, &out); err != nil || !strings.Contains(out.String(), "already seeded "+rec.Sha) {
		t.Fatalf("second seed: %v, %q; want already seeded", err, out.String())
	}
	out.Reset()
	o.force = true
	if err := runCacheSeed(o, &out); err != nil {
		t.Fatalf("forced seed: %v, %q", err, out.String())
	}
	if strings.Contains(out.String(), "already seeded") {
		t.Fatalf("--force skipped: %q", out.String())
	}
}
