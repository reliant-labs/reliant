package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// verifyRepo is a committed checkout holding a forge project, in a directory
// of its own (the scratch copy is made beside the repository), plus one
// installed frontend.
func verifyRepo(t *testing.T) (parent, repo string) {
	t.Helper()
	isolateGit(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(parent, "app")
	gitOut(t, parent, "init", "-q", "app")
	for rel, body := range map[string]string{
		"forge.yaml":                          "name: app\nmodule_path: example.com/app\nfeatures:\n  ci: true\n",
		".gitignore":                          "node_modules/\n.env\n.forge/generate-cache.json\n.forge/forge.lock\ntmp/\n",
		"gen/widget_gen.go":                   "package gen\n",
		"internal/app/providers.go":           "package app\n",
		"frontends/web/package.json":          "{}\n",
		"frontends/web/package-lock.json":     toolsTestLockfile,
		"frontends/web/src/gen/client_gen.ts": "export {}\n",
	} {
		writeTestFile(t, filepath.Join(repo, rel), body)
	}
	gitOut(t, repo, "add", "-A")
	gitOut(t, repo, "commit", "-q", "-m", "init")
	// Gitignored state generate reads, an installed tree, and output no
	// verdict is about.
	writeTestFile(t, filepath.Join(repo, ".env"), "DATABASE_URL=postgres://x\n")
	writeTestFile(t, filepath.Join(repo, "frontends", "web", "node_modules", ".package-lock.json"), "{}\n")
	writeTestFile(t, filepath.Join(repo, "tmp", "ci", "check.log"), "log\n")
	return parent, repo
}

// checkoutEntry is one path of a checkoutSnapshot. key (mode + content, or a
// symlink's target) is what equality compares; size and mtime are carried
// only so a failure can say what happened to the path.
type checkoutEntry struct {
	key   string
	size  int64
	mtime time.Time
}

// checkoutSnapshot records every path under dir — the checkout AND its .git — by
// mode and content, so any write at all shows up in checkoutDiff.
type checkoutSnapshot map[string]checkoutEntry

func snapshotCheckout(t *testing.T, dir string) checkoutSnapshot {
	t.Helper()
	snap := checkoutSnapshot{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		e := checkoutEntry{size: info.Size(), mtime: info.ModTime()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			e.key = "-> " + target
		case info.IsDir():
			rel, e.key, e.size = rel+"/", "dir", 0
		default:
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			e.key = info.Mode().String() + " " + hex.EncodeToString(sum[:])
		}
		snap[rel] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// checkoutDiff lists every path whose presence, mode or content differs between
// two snapshots, one line each with its size and mtime on both sides, so a
// failure names the writer's footprint instead of two opaque hashes.
func checkoutDiff(before, after checkoutSnapshot) []string {
	describe := func(e checkoutEntry, ok bool) string {
		if !ok {
			return "absent"
		}
		return fmt.Sprintf("%s size=%d mtime=%s", e.key, e.size, e.mtime.UTC().Format(time.RFC3339Nano))
	}
	paths := map[string]bool{}
	for p := range before {
		paths[p] = true
	}
	for p := range after {
		paths[p] = true
	}
	var out []string
	for p := range paths {
		b, inB := before[p]
		a, inA := after[p]
		if inB && inA && a.key == b.key {
			continue
		}
		out = append(out, fmt.Sprintf("%s\n        before: %s\n        after:  %s", p, describe(b, inB), describe(a, inA)))
	}
	sort.Strings(out)
	return out
}

// fakeRegenerate stands in for the child `forge generate`, which a test
// binary cannot be re-executed as: edit runs against the scratch project.
func fakeRegenerate(t *testing.T, edit func(scratchRoot string)) *[]string {
	t.Helper()
	var seen []string
	prev := scratchRegenerate
	scratchRegenerate = func(_ context.Context, s *verifyScratch, _, _ bool) error {
		seen = append(seen, s.root)
		edit(s.root)
		return nil
	}
	t.Cleanup(func() { scratchRegenerate = prev })
	return &seen
}

func scratchesBeside(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), verifyScratchMarker) {
			out = append(out, e.Name())
		}
	}
	return out
}

// `forge ci verify-generated` must never write the checkout it judges. It
// used to regenerate in place and then read `git status`: a concurrent gate
// had to keep every reader of the tree waiting for it, and a crash or kill
// mid-run left the tree dirty. It must still catch drift — modified,
// created and deleted generated files — and report it as before.
func TestVerifyGenerated_LeavesTheCheckoutByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dirty     bool // an uncommitted edit before the run
		edit      func(root string)
		wantDrift []string
	}{
		{name: "up to date", edit: func(string) {}},
		{name: "uncommitted edits are judged, not blamed on generate", dirty: true, edit: func(string) {}},
		{
			name: "drift",
			edit: func(root string) {
				writeTestFile(t, filepath.Join(root, "gen", "widget_gen.go"), "package gen\n\nvar Stale = 1\n")
				writeTestFile(t, filepath.Join(root, "gen", "gadget_gen.go"), "package gen\n")
				if err := os.Remove(filepath.Join(root, "frontends", "web", "src", "gen", "client_gen.ts")); err != nil {
					t.Fatal(err)
				}
			},
			wantDrift: []string{" D frontends/web/src/gen/client_gen.ts", "?? gen/gadget_gen.go", " M gen/widget_gen.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, repo := verifyRepo(t)
			if tc.dirty {
				writeTestFile(t, filepath.Join(repo, "frontends", "web", "package-lock.json"), `{"rewritten":"by npm on linux"}`)
			}
			clearProjectDir(t)
			seen := fakeRegenerate(t, tc.edit)
			before := snapshotCheckout(t, repo)

			var stderr strings.Builder
			err := runVerifyGeneratedIn(t, repo, &stderr)

			if diff := checkoutDiff(before, snapshotCheckout(t, repo)); len(diff) > 0 {
				t.Errorf("the checkout changed during verify-generated, at %d path(s):\n  %s", len(diff), strings.Join(diff, "\n  "))
			}
			if len(*seen) != 1 || strings.HasPrefix((*seen)[0], repo) {
				t.Fatalf("regenerate ran in %v, want once, in a scratch copy outside %s (err=%v)\n%s", *seen, repo, err, stderr.String())
			}
			if left := scratchesBeside(t, parent); len(left) != 0 {
				t.Errorf("scratch copies left behind: %v", left)
			}
			if tc.wantDrift == nil {
				if err != nil {
					t.Fatalf("verify-generated failed on a fresh tree: %v\n%s", err, stderr.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "out of date (3 file(s))") {
				t.Fatalf("drift not reported: err=%v\n%s", err, stderr.String())
			}
			for _, line := range tc.wantDrift {
				if !strings.Contains(stderr.String(), "  "+line+"\n") {
					t.Errorf("report lacks %q:\n%s", line, stderr.String())
				}
			}
		})
	}
}

// runVerifyGeneratedIn runs the real command against the project at repo,
// with its stderr captured.
func runVerifyGeneratedIn(t *testing.T, repo string, stderr io.Writer) error {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderr, r)
		close(done)
	}()
	_, runErr := runForge(t, "-C", repo, "ci", "verify-generated")
	_ = w.Close()
	os.Stderr = prev
	<-done
	return runErr
}

// The scratch copy is the working tree as git shows it — tracked files,
// with their uncommitted edits, and untracked files git does not ignore —
// plus the ignored inputs generate reads, and each installed node_modules
// linked rather than copied. Its index is the checkout's.
func TestVerifyScratch_CopiesTheWorkingTreeAndTheInputsGenerateReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinked node_modules")
	}
	_, repo := verifyRepo(t)
	writeTestFile(t, filepath.Join(repo, "internal", "app", "providers.go"), "package app // edited\n")
	writeTestFile(t, filepath.Join(repo, "internal", "app", "new.go"), "package app\n")
	if err := os.Remove(filepath.Join(repo, "gen", "widget_gen.go")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, ".forge", "generate-cache.json"), `{"version":0}`)
	// A gitignored vendored module the build compiles through a replace.
	writeTestFile(t, filepath.Join(repo, "go.mod"), "module example.com/app\n\ngo 1.24\n\nreplace example.com/lib => ./tmp/lib\n")
	writeTestFile(t, filepath.Join(repo, "tmp", "lib", "go.mod"), "module example.com/lib\n")
	writeTestFile(t, filepath.Join(repo, "tmp", "lib", "lib.go"), "package lib\n")

	s, err := newVerifyScratch(context.Background(), repo, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()

	read := func(rel string) string {
		body, err := os.ReadFile(filepath.Join(s.root, rel))
		if err != nil {
			return "<absent>"
		}
		return string(body)
	}
	for rel, want := range map[string]string{
		"internal/app/providers.go":  "package app // edited\n",
		"internal/app/new.go":        "package app\n",
		"gen/widget_gen.go":          "<absent>",
		".env":                       "DATABASE_URL=postgres://x\n",
		".forge/generate-cache.json": `{"version":0}`,
		"tmp/ci/check.log":           "<absent>",
		"tmp/lib/lib.go":             "package lib\n",
	} {
		if got := read(rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	link, err := os.Readlink(filepath.Join(s.root, "frontends", "web", "node_modules"))
	if err != nil || link != filepath.Join(repo, "frontends", "web", "node_modules") {
		t.Errorf("node_modules link = %q (%v), want the checkout's", link, err)
	}
	tracked := gitOut(t, s.top, "ls-files")
	if want := gitOut(t, repo, "ls-files"); tracked != want {
		t.Errorf("scratch index:\n%s\nwant the checkout's:\n%s", tracked, want)
	}
	if strings.Contains(gitOut(t, s.top, "status", "--porcelain", "--untracked-files=all"), "node_modules") {
		t.Error("the node_modules link shows in the scratch's git status")
	}
	if changes, err := s.changes(context.Background()); err != nil || len(changes) != 0 {
		t.Errorf("an untouched scratch reports changes %v (%v)", changes, err)
	}
}

// The ignored files the scratch copies come from overlapping lists: an output
// the cache manifest names can also sit under one of its output dirs, or be an
// input generate reads (control-plane's frontends/<fe>/public/config.js is
// both). Each must be copied once; copying one twice, in parallel and with
// O_EXCL, failed every verify with "file exists".
func TestVerifyScratch_CopiesAFileListedTwiceOnce(t *testing.T) {
	_, repo := verifyRepo(t)
	writeTestFile(t, filepath.Join(repo, "tmp", "out", "config.js"), "window.cfg = {}\n")
	body, err := json.Marshal(generateCacheManifest{
		Version:    generateCacheVersion,
		Env:        map[string]string{},
		Files:      map[string]string{},
		Outputs:    []string{".env", "tmp/out/config.js"},
		OutputDirs: []string{"tmp/out"},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, ".forge", "generate-cache.json"), string(body))

	s, err := newVerifyScratch(context.Background(), repo, io.Discard)
	if err != nil {
		t.Fatalf("scratch copy: %v", err)
	}
	defer s.Remove()
	for rel, want := range map[string]string{
		".env":              "DATABASE_URL=postgres://x\n",
		"tmp/out/config.js": "window.cfg = {}\n",
	} {
		if got, err := os.ReadFile(filepath.Join(s.root, rel)); err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q", rel, got, err, want)
		}
	}
}

// A scratch copy whose process was SIGKILLed — so it never removed its own —
// is removed by the next check; a live process's copy is left alone.
func TestVerifyScratch_PrunesCopiesOfDeadProcesses(t *testing.T) {
	parent, repo := verifyRepo(t)
	stem := ".app" + verifyScratchMarker
	dead := filepath.Join(parent, stem+"999999999-abc")
	live := filepath.Join(parent, stem+"1-abc") // pid 1 always runs
	for _, d := range []string{dead, live} {
		writeTestFile(t, filepath.Join(d, "x"), "x")
	}
	s, err := newVerifyScratch(context.Background(), repo, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	s.Remove()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("a dead process's scratch copy survived: %v", err)
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(live); err != nil {
			t.Errorf("a live process's scratch copy was removed: %v", err)
		}
	}
	if _, err := os.Stat(s.top); !os.IsNotExist(err) {
		t.Errorf("Remove left %s", s.top)
	}
}

// A fixed point the scratch run records reaches the checkout's generate
// cache — gitignored state, never the tree — together with the .forge/ state
// it names (the generating-build record a run writes), so the next check and
// the next in-place `forge generate` skip. Never over a record that moved
// meanwhile.
func TestVerifyScratch_CopiesARecordedFixedPointBack(t *testing.T) {
	_, repo := verifyRepo(t)
	cache := filepath.Join(repo, ".forge", "generate-cache.json")
	build := filepath.Join(repo, ".forge", "generating-build")
	writeTestFile(t, cache, "old")
	record := func(s *verifyScratch) string {
		t.Helper()
		writeTestFile(t, filepath.Join(s.root, ".forge", "generating-build"), "forge test-build\n")
		id, err := pathIdentity(filepath.Join(s.root, ".forge", "generating-build"))
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(generateCacheManifest{
			Version: generateCacheVersion,
			Env:     map[string]string{},
			Files:   map[string]string{".forge/generating-build": id, "gen/widget_gen.go": "x"},
		})
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(s.root, ".forge", "generate-cache.json"), string(body))
		return string(body)
	}

	s, err := newVerifyScratch(context.Background(), repo, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	want := record(s)
	s.copyCacheBack()
	if got, _ := os.ReadFile(cache); string(got) != want {
		t.Errorf("cache = %q, want the scratch run's record", got)
	}
	if got, _ := os.ReadFile(build); string(got) != "forge test-build\n" {
		t.Errorf(".forge/generating-build = %q, want the state the record names", got)
	}

	if err := os.Remove(build); err != nil {
		t.Fatal(err)
	}
	s2, err := newVerifyScratch(context.Background(), repo, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Remove()
	writeTestFile(t, cache, "a concurrent in-place run")
	record(s2)
	s2.copyCacheBack()
	if got, _ := os.ReadFile(cache); string(got) != "a concurrent in-place run" {
		t.Errorf("cache = %q: a record made meanwhile was replaced", got)
	}
	if _, err := os.Stat(build); !os.IsNotExist(err) {
		t.Errorf("state was copied back over a cache that moved meanwhile: %v", err)
	}
}

// The scratch copy fingerprints like the checkout, so the generate cache's
// record of one is a record of the other. The node_modules link and a
// go.work inside the project used to key differently.
func TestVerifyScratch_FingerprintsLikeTheCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinked node_modules")
	}
	_, repo := verifyRepo(t)
	writeTestFile(t, filepath.Join(repo, "go.work"), "go 1.24\n\nuse .\n")
	writeTestFile(t, filepath.Join(repo, ".gitignore"), "node_modules/\n.env\n.forge/generate-cache.json\n.forge/forge.lock\ntmp/\ngo.work\ngo.work.sum\n")
	gitOut(t, repo, "commit", "-q", "-am", "ignore go.work")
	s, err := newVerifyScratch(context.Background(), repo, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()

	sys := generateCacheSys{
		executable: func() (string, error) { return filepath.Join(repo, "forge.yaml"), nil },
		lookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		environ:    func() []string { return []string{"GOFLAGS=-mod=mod"} },
		now:        osGenerateCacheSys().now,
		identity:   func() string { return "test" },
		shadowHost: func(string) string { return "embedded" },
	}
	fingerprint := func(root string) map[string]string {
		fp, err := fingerprintGenerate(root, snapshotWorkTree(root), nil, nil, nil, sys)
		if err != nil {
			t.Fatal(err)
		}
		return fp.files
	}
	want, got := fingerprint(repo), fingerprint(s.root)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: checkout %q, scratch %q", k, v, got[k])
		}
	}
	for k, v := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: only the scratch has it (%q)", k, v)
		}
	}
}

// A CI checkout is usually shallow (actions/checkout fetch-depth 1): its HEAD
// commit's parent is not in the object store. The scratch copy must carry the
// clone's shallow boundary, or every `git log` there — the Go toolchain's VCS
// stamp in the regenerate's validate build among them — fails with exit 128
// reading the missing parent.
func TestVerifyScratch_WorksInAShallowClone(t *testing.T) {
	_, repo := verifyRepo(t)
	writeTestFile(t, filepath.Join(repo, "internal", "app", "providers.go"), "package app // second\n")
	gitOut(t, repo, "commit", "-q", "-am", "second")
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitOut(t, parent, "clone", "-q", "--depth", "1", "file://"+repo, "shallow")
	clone := filepath.Join(parent, "shallow")

	s, err := newVerifyScratch(context.Background(), clone, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	// What cmd/go runs to stamp a build with VCS information.
	for _, args := range [][]string{
		{"-c", "log.showsignature=false", "log", "-1", "--format=%H:%ct"},
		{"status", "--porcelain"},
	} {
		if _, err := scratchGit(context.Background(), s.top, nil, args...); err != nil {
			t.Errorf("git %v in the scratch copy of a shallow clone: %v", args, err)
		}
	}
}

// TestIsolateGit_CommitSpawnsNoBackgroundMaintenance pins, deterministically,
// the fix #940 made for TestVerifyGenerated_LeavesTheCheckoutByteIdentical's
// flake: under a default config `git commit` starts `git maintenance run
// --auto --detach`, which holds .git/objects/maintenance.lock after commit
// returns, so a fixture repository kept being written by a process no test
// waited for. Asserted on git's own trace of the processes commit starts, so
// it fails whenever isolateGit stops disabling maintenance, not only when the
// detached child happens to be slow.
func TestIsolateGit_CommitSpawnsNoBackgroundMaintenance(t *testing.T) {
	isolateGit(t)
	repo := t.TempDir()
	gitOut(t, repo, "init", "-q")
	writeTestFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitOut(t, repo, "add", "-A")

	trace := filepath.Join(t.TempDir(), "trace2.json")
	cmd := exec.Command("git", "commit", "-q", "-m", "init")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT="+trace)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	events, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("read git's trace2 events: %v", err)
	}
	for _, line := range strings.Split(string(events), "\n") {
		if !strings.Contains(line, `"event":"child_start"`) {
			continue
		}
		if strings.Contains(line, `"maintenance"`) || strings.Contains(line, `"gc"`) {
			t.Fatalf("git commit in an isolateGit repository started background maintenance — a writer the fixture never waits for:\n%s", line)
		}
	}
}
