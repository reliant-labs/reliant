package repowalk

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This is a real cmd/go cache proof, not an assertion about an implementation
// detail of the walk. The child's only production inputs are copies read by
// this process. No private GOCACHE and no edit to this checkout.
func TestWalkCacheTracksScannedInputsNotSkippedState(t *testing.T) {
	if testing.Short() {
		t.Skip("executes go test repeatedly to prove test-cache invalidation; runs in task test")
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("these platforms use the logged fallback")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module cacheproof\n\ngo 1.24\n")
	for _, name := range []string{"repowalk.go", "readdir_unix.go", "readdir_other.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		write(name, string(body))
	}
	write("input/value", "original")
	write(".forge/cache", "old")
	write("probe_test.go", `package repowalk
import (
 "io/fs"
 "os"
 "path/filepath"
 "strings"
 "testing"
)
func TestProbe(t *testing.T) {
 files := 0
 err := WalkDir(".", func(p string, d fs.DirEntry, err error) error {
  if err != nil { return err }
  if d.IsDir() {
   if d.Name() == ".forge" { return filepath.SkipDir }
   return nil
  }
  if strings.HasSuffix(p, ".go") || p == "go.mod" { return nil }
  b, err := os.ReadFile(p)
  if err != nil { return err }
  if strings.Contains(string(b), "BROKEN") { t.Errorf("broken input %s", p) }
  files++
  return nil
 })
 if err != nil { t.Fatal(err) }
 if files == 0 { t.Fatal("scan saw no inputs") }
}
`)
	// Avoid Go's two-second "too new" cutoff, without sleeps. Each write
	// gets a distinct old time; directory metadata remains real.
	stamp := time.Unix(1_000_000_000, 0)
	age := func(rel string) {
		t.Helper()
		stamp = stamp.Add(time.Minute)
		if err := os.Chtimes(filepath.Join(root, rel), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	age("input/value")
	run := func(wantCached, wantFailure bool) {
		t.Helper()
		cmd := exec.Command("go", "test", "-short", ".")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, err := cmd.CombinedOutput()
		if (err != nil) != wantFailure {
			t.Fatalf("failure=%v, want %v: %v\n%s", err != nil, wantFailure, err, out)
		}
		if got := strings.Contains(string(out), "(cached)"); got != wantCached {
			t.Fatalf("cached=%v, want %v\n%s", got, wantCached, out)
		}
	}
	run(false, false)
	run(true, false)
	write(".forge/cache", "rewritten generate cache")
	write(".forge/another", "new ignored state")
	run(true, false)
	write("input/value", "changed scanned content")
	age("input/value")
	run(false, false)
	run(true, false)
	write("added/value", "new top-level subtree")
	age("added/value")
	run(false, false)
	run(true, false)
	write("added/defect", "BROKEN")
	age("added/defect")
	run(false, true)
	if err := os.Remove(filepath.Join(root, "added/defect")); err != nil {
		t.Fatal(err)
	}
	run(false, false)
}
