// The generate cache skips a run only when the tree is a recorded fixed
// point. A false skip — reporting "nothing to do" over an input that changed
// — is the failure that matters, so most of this file is one table: every
// input class the fingerprint claims to cover, mutated alone, must turn the
// next run into a full one. The mirror table pins the things that must NOT
// invalidate, because a cache that misses on every PATH tweak buys nothing.
package cli

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/factory"
)

// cacheFixture is a git project, a sibling module it `replace`s, and a
// directory of fake executables standing in for forge and its tools.
type cacheFixture struct {
	root    string
	lib     string
	libRepo string
	shared  string
	bin     string
	env     []string
	now     time.Time
	shadow  string
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: builds a git repo and spawns git per fingerprint; runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	f := &cacheFixture{root: t.TempDir(), lib: t.TempDir(), libRepo: t.TempDir(), shared: t.TempDir(), bin: t.TempDir(), now: time.Now()}
	// A second replace target that is a git checkout, as a bridged forge is.
	initGitRepo(t, f.libRepo, map[string]string{"go.mod": "module example.com/librepo\n", "lib.go": "package lib\n"})
	initGitRepo(t, f.root, map[string]string{
		".gitignore": "gen-ignored/\nnode_modules/\n.forge/*\n!.forge/hashes.json\ngo.work\ngo.work.sum\n.env*\n*.local\nsecrets/*.yaml\n",
		"forge.yaml": "name: demo\ngenerate:\n  hooks:\n    - name: sqlc\n      command: [sqlc, generate]\n",
		"buf.gen.yaml": "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n" +
			"  - local: [protoc-gen-custom, --flag]\n    out: gen\n" +
			"  - local: ./frontends/web/node_modules/.bin/protoc-gen-es\n    out: frontends/web/src/gen\n",
		"go.mod": "module example.com/demo\n\ngo 1.27\n\nreplace example.com/lib => " + strconv.Quote(filepath.ToSlash(f.lib)) +
			"\n\nreplace example.com/librepo => " + strconv.Quote(filepath.ToSlash(f.libRepo)) + "\n",
		"proto/a.proto":              "syntax = \"proto3\";\n",
		"gen/a.pb.go":                "package gen\n",
		"frontends/web/package.json": "{}\n",
		".forge/hashes.json":         "{}\n",
	})
	// A tracked symlink to a proto directory outside the project, the way a
	// repo shares protos with a sibling: buf reads through it.
	writeTreeFileT(t, filepath.Join(f.shared, "shared.proto"), "syntax = \"proto3\";\n")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(f.shared, filepath.Join(f.root, "proto", "shared")); err != nil {
			t.Fatal(err)
		}
		mustGit(t, f.root, "add", "proto/shared")
	}
	// A nested repository added as a gitlink — what `git submodule add`
	// leaves in the index — holding shared protos.
	sub := filepath.Join(f.root, "third_party", "protos")
	initGitRepo(t, sub, map[string]string{"common.proto": "syntax = \"proto3\";\n"})
	mustGit(t, f.root, "add", "third_party/protos")
	writeTreeFileT(t, filepath.Join(f.lib, "go.mod"), "module example.com/lib\n")
	writeTreeFileT(t, filepath.Join(f.lib, "lib.go"), "package lib\n")
	writeTreeFileT(t, filepath.Join(f.root, "gen-ignored", "out.txt"), "generated, gitignored\n")
	writeTreeFileT(t, filepath.Join(f.root, "deploy", "settings.local"), "a gitignored file nothing names\n")
	for _, tool := range []string{"forge", "go", "buf", "protoc-gen-go", "protoc-gen-custom", "sqlc", "node"} {
		f.writeExec(t, tool, "#!/bin/sh\necho "+tool+" v1\n")
	}
	f.env = []string{"GOENV=off", "HOME=/home/test", "FORGE_X=1", "PATH=/usr/bin:/bin", "GOPROXY=https://proxy.golang.org"}
	return f
}

func (f *cacheFixture) writeExec(t *testing.T, name, body string) {
	t.Helper()
	p := filepath.Join(f.bin, name)
	// A real rebuild replaces the file; rename keeps that shape and moves the
	// inode change time the identity cache keys on.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
}

func (f *cacheFixture) sys() generateCacheSys {
	return generateCacheSys{
		executable: func() (string, error) { return filepath.Join(f.bin, "forge"), nil },
		lookPath: func(name string) (string, error) {
			p := filepath.Join(f.bin, name)
			if _, err := os.Stat(p); err != nil {
				return "", exec.ErrNotFound
			}
			return p, nil
		},
		environ:    func() []string { return f.env },
		now:        func() time.Time { return f.now },
		identity:   func() string { return "forge test" },
		shadowHost: func(string) string { return f.shadow },
	}
}

// fingerprint takes the fingerprint a run would, reading prev's recorded
// outputs and executable cache.
func (f *cacheFixture) fingerprint(t *testing.T, prev *generateCacheManifest) *generateFingerprint {
	t.Helper()
	var outputs, dirs []string
	var execs map[string]execStamp
	if prev != nil {
		outputs, dirs, execs = prev.Outputs, prev.OutputDirs, prev.Executables
	}
	fp, err := fingerprintGenerate(f.root, snapshotWorkTree(f.root), outputs, dirs, execs, f.sys())
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return fp
}

// recordFixture records the fixture's current state as a verified run whose
// outputs were gen/a.pb.go and the gitignored gen-ignored/ directory.
func (f *cacheFixture) record(t *testing.T) *generateCacheManifest {
	t.Helper()
	outputs, dirs := []string{"gen/a.pb.go", "gen-ignored/out.txt"}, []string{"gen-ignored"}
	fp, err := fingerprintGenerate(f.root, snapshotWorkTree(f.root), outputs, dirs, nil, f.sys())
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return newGenerateCacheManifest(fp, outputs, dirs, false, f.sys())
}

func (f *cacheFixture) write(t *testing.T, rel, body string) {
	t.Helper()
	writeTreeFileT(t, filepath.Join(f.root, filepath.FromSlash(rel)), body)
}

func (f *cacheFixture) setEnv(name, value string) {
	var out []string
	for _, kv := range f.env {
		if k, _, _ := strings.Cut(kv, "="); k != name {
			out = append(out, kv)
		}
	}
	f.env = append(out, name+"="+value)
}

// TestGenerateCacheUnchangedTreeMatches is the baseline every row of the
// invalidation table is measured against: re-fingerprinting an untouched
// fixture reproduces the record exactly. Without it, a table of "differs"
// assertions would pass against a fingerprint that never matches anything.
func TestGenerateCacheUnchangedTreeMatches(t *testing.T) {
	f := newCacheFixture(t)
	m := f.record(t)
	if d := diffGenerateFingerprint(m, f.fingerprint(t, m)); d != "" {
		t.Fatalf("an untouched tree must match its record, got: %s", d)
	}
	for _, key := range []string{
		"proto/a.proto", "gen-ignored/out.txt", ".forge/hashes.json",
		"frontends/web/node_modules/.package-lock.json", "ext:" + f.lib + "/lib.go",
	} {
		if _, ok := m.Files[key]; !ok {
			t.Errorf("the record must cover %s; keys: %v", key, keysOf(m.Files))
		}
	}
	for _, key := range []string{"forge", "tool:buf", "tool:protoc-gen-custom", "tool:sqlc", "tool:protoc-gen-connect-openapi", "env:FORGE_X"} {
		if _, ok := m.Env[key]; !ok {
			t.Errorf("the record must cover %s; env keys: %v", key, keysOf(m.Env))
		}
	}
	for _, key := range []string{"env:PATH", "env:GOPROXY", "tool:./frontends/web/node_modules/.bin/protoc-gen-es"} {
		if _, ok := m.Env[key]; ok {
			t.Errorf("%s must not be part of the key", key)
		}
	}
}

// TestGenerateCacheEveryInputClassInvalidates mutates one input class at a
// time. Each must make the next run a full one, and the reason must name it.
func TestGenerateCacheEveryInputClassInvalidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *cacheFixture)
		reason string // substring the miss reason must carry
	}{
		{"tracked input edited", func(t *testing.T, f *cacheFixture) {
			f.write(t, "proto/a.proto", "syntax = \"proto3\";\nmessage A {}\n")
		}, "proto/a.proto"},
		{"untracked input added", func(t *testing.T, f *cacheFixture) { f.write(t, "proto/b.proto", "syntax = \"proto3\";\n") }, "proto/b.proto"},
		{"tracked output deleted", func(t *testing.T, f *cacheFixture) { mustRemove(t, filepath.Join(f.root, "gen", "a.pb.go")) }, "gen/a.pb.go"},
		{"file mode changed", func(t *testing.T, f *cacheFixture) {
			if runtime.GOOS == "windows" {
				t.Skip("no POSIX permission bits on Windows")
			}
			if err := os.Chmod(filepath.Join(f.root, "proto", "a.proto"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "proto/a.proto"},
		{"gitignored output edited", func(t *testing.T, f *cacheFixture) { f.write(t, "gen-ignored/out.txt", "hand edit\n") }, "gen-ignored/out.txt"},
		{"gitignored output deleted", func(t *testing.T, f *cacheFixture) { mustRemove(t, filepath.Join(f.root, "gen-ignored", "out.txt")) }, "gen-ignored/out.txt"},
		{"stray file in a gitignored output dir", func(t *testing.T, f *cacheFixture) { f.write(t, "gen-ignored/stray.txt", "x\n") }, "gen-ignored/stray.txt"},
		{"go.work appears", func(t *testing.T, f *cacheFixture) { f.write(t, "go.work", "go 1.27\n\nuse .\n") }, "go.work"},
		{"node_modules installed", func(t *testing.T, f *cacheFixture) {
			f.write(t, "frontends/web/node_modules/.package-lock.json", "{\"lockfileVersion\":3}\n")
		}, "node_modules/.package-lock.json"},
		{".forge port ledger written", func(t *testing.T, f *cacheFixture) { f.write(t, ".forge/ports-dev.json", "{\"api\":8080}\n") }, "1 .forge/ state file(s)"},
		{"unnamed gitignored file edited", func(t *testing.T, f *cacheFixture) { f.write(t, "deploy/settings.local", "changed\n") }, "deploy/settings.local"},
		{"dotenv edited", func(t *testing.T, f *cacheFixture) { f.write(t, ".env.dev", "DATABASE_URL=postgres://x\n") }, ".env.dev"},
		{"replace target edited", func(t *testing.T, f *cacheFixture) {
			writeTreeFileT(t, filepath.Join(f.lib, "lib.go"), "package lib\n\nfunc X() {}\n")
		}, "lib.go"},
		{"replace target gained a file", func(t *testing.T, f *cacheFixture) {
			writeTreeFileT(t, filepath.Join(f.lib, "more.go"), "package lib\n")
		}, "more.go"},
		{"replace target checkout gained an untracked Go file", func(t *testing.T, f *cacheFixture) {
			writeTreeFileT(t, filepath.Join(f.libRepo, "extra.go"), "package lib\n")
		}, "extra.go"},
		{"forge binary rebuilt", func(t *testing.T, f *cacheFixture) { f.writeExec(t, "forge", "#!/bin/sh\necho forge v2\n") }, "the forge binary"},
		{"tool rebuilt", func(t *testing.T, f *cacheFixture) { f.writeExec(t, "buf", "#!/bin/sh\necho buf v2\n") }, "buf"},
		{"go toolchain replaced", func(t *testing.T, f *cacheFixture) { f.writeExec(t, "go", "#!/bin/sh\necho go v2\n") }, "go"},
		{"tool appears on PATH", func(t *testing.T, f *cacheFixture) { f.writeExec(t, "protoc-gen-connect-openapi", "#!/bin/sh\n") }, "protoc-gen-connect-openapi"},
		{"buf template's own plugin rebuilt", func(t *testing.T, f *cacheFixture) { f.writeExec(t, "protoc-gen-custom", "#!/bin/sh\necho v2\n") }, "protoc-gen-custom"},
		{"generate hook's command rebuilt", func(t *testing.T, f *cacheFixture) { f.writeExec(t, "sqlc", "#!/bin/sh\necho v2\n") }, "sqlc"},
		{"FORGE_ variable changed", func(t *testing.T, f *cacheFixture) { f.setEnv("FORGE_X", "2") }, "$FORGE_X"},
		{"GOFLAGS set", func(t *testing.T, f *cacheFixture) { f.setEnv("GOFLAGS", "-tags=integration") }, "$GOFLAGS"},
		{"CI set", func(t *testing.T, f *cacheFixture) { f.setEnv("CI", "true") }, "$CI"},
		{"file behind a symlinked dir edited", func(t *testing.T, f *cacheFixture) {
			if runtime.GOOS == "windows" {
				t.Skip("the fixture creates no symlink on Windows")
			}
			writeTreeFileT(t, filepath.Join(f.shared, "shared.proto"), "syntax = \"proto3\";\nmessage S {}\n")
		}, "shared.proto"},
		{"file in a submodule edited", func(t *testing.T, f *cacheFixture) {
			writeTreeFileT(t, filepath.Join(f.root, "third_party", "protos", "common.proto"), "syntax = \"proto3\";\nmessage C {}\n")
		}, "common.proto"},
		{"BUF_ variable set", func(t *testing.T, f *cacheFixture) { f.setEnv("BUF_BETA_SUPPRESS_WARNINGS", "1") }, "$BUF_BETA_SUPPRESS_WARNINGS"},
		{"shadow postgres came up", func(t *testing.T, f *cacheFixture) { f.shadow = "postgres://localhost:5432/postgres" }, "shadow"},
		{"secret store edited", func(t *testing.T, f *cacheFixture) { f.write(t, "secrets/dev.yaml", "DATABASE_URL: postgres://x\n") }, "secrets/dev.yaml"},
		{"shadow database moved", func(t *testing.T, f *cacheFixture) { f.setEnv("DATABASE_URL", "postgres://elsewhere") }, "$DATABASE_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // each row owns its fixture; nothing global is touched
			f := newCacheFixture(t)
			m := f.record(t)
			tc.mutate(t, f)
			d := diffGenerateFingerprint(m, f.fingerprint(t, m))
			if d == "" {
				t.Fatalf("FALSE SKIP: %s did not invalidate the record", tc.name)
			}
			if !strings.Contains(d, tc.reason) {
				t.Errorf("the miss reason should name %q, got: %s", tc.reason, d)
			}
		})
	}
}

// TestGenerateCacheIrrelevantChangesStillMatch pins what must NOT cost a
// full run: each of these happens between an agent's generate and the gate's,
// and none can change what generate writes.
func TestGenerateCacheIrrelevantChangesStillMatch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *cacheFixture)
	}{
		{"PATH reordered (tools resolve to the same binaries)", func(t *testing.T, f *cacheFixture) { f.setEnv("PATH", "/opt/tools/bin:/usr/bin:/bin") }},
		{"module fetch settings changed", func(t *testing.T, f *cacheFixture) {
			f.setEnv("GOPROXY", "direct")
			f.setEnv("GOPRIVATE", "github.com/acme/*")
			f.setEnv("GONOSUMDB", "github.com/acme/*")
		}},
		{"generate's own switches set", func(t *testing.T, f *cacheFixture) {
			f.setEnv(generateTimingsEnv, "1")
			f.setEnv("FORGE_SILENCE_EXPERIMENTAL", "1")
		}},
		{"file rewritten with identical bytes", func(t *testing.T, f *cacheFixture) {
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(filepath.Join(f.root, "proto", "a.proto"), later, later); err != nil {
				t.Fatal(err)
			}
		}},
		{"project lock and friction log rewritten", func(t *testing.T, f *cacheFixture) {
			f.write(t, ".forge/forge.lock", "pid 4242\n")
			f.write(t, ".forge/friction.jsonl", "{}\n")
		}},
		{"CI report written into a replace target's checkout", func(t *testing.T, f *cacheFixture) {
			// The forge e2e job tees e2e-shard-N.json into the forge checkout
			// that the scaffolded project's go.work bridges to, while generate
			// runs. No Go build reads it.
			writeTreeFileT(t, filepath.Join(f.libRepo, "e2e-shard-1.json"), "{\"Action\":\"run\"}\n")
		}},
		{"tree committed", func(t *testing.T, f *cacheFixture) {
			mustGit(t, f.root, "commit", "--quiet", "--allow-empty", "-m", "another commit")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // each row owns its fixture; nothing global is touched
			f := newCacheFixture(t)
			m := f.record(t)
			tc.mutate(t, f)
			if d := diffGenerateFingerprint(m, f.fingerprint(t, m)); d != "" {
				t.Fatalf("%s must not invalidate the record, got: %s", tc.name, d)
			}
		})
	}
}

// TestGenerateCacheRefusesMidMerge: the drift guard reads the merge state, so
// a merge in progress gets neither a skip nor a record.
func TestGenerateCacheRefusesMidMerge(t *testing.T) {
	f := newCacheFixture(t)
	mergeHead := filepath.Join(f.root, ".git", "MERGE_HEAD")
	if err := os.WriteFile(mergeHead, []byte("0000000000000000000000000000000000000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := fingerprintGenerate(f.root, snapshotWorkTree(f.root), nil, nil, nil, f.sys())
	if err == nil || !strings.Contains(err.Error(), "merge is in progress") {
		t.Fatalf("a merge in progress must refuse the fingerprint, got %v", err)
	}
}

// TestGenerateCacheExecutableIdentityCache pins the stat cache that keeps a
// fingerprint from re-reading a 170MB forge binary: reused when the stat is
// unchanged and was settled when hashed, recomputed otherwise.
func TestGenerateCacheExecutableIdentityCache(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tool")
	if err := os.WriteFile(p, []byte("binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	// The cache keys on the resolved path (Windows temp dirs carry 8.3 short
	// names that EvalSymlinks expands), so the test's records must too.
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	stamp := execStamp{Size: info.Size(), ModTime: info.ModTime().UnixNano(), ChangeTime: statChangeTime(info), ID: "sentinel"}

	// Hashed well after both timestamps (the inode change time cannot be
	// backdated, so "later" is the only way to make it settled).
	settled := stamp
	settled.HashedAt = time.Now().Add(time.Minute).UnixNano()
	e := &execIdentities{prev: map[string]execStamp{p: settled}, next: map[string]execStamp{}, now: time.Now()}
	if id, err := e.identify(p, false); err != nil || id != "sentinel" {
		t.Fatalf("a settled, unchanged stat must reuse the cached identity, got %q, %v", id, err)
	}

	// Hashed within the racy window of its own timestamps: the file could
	// have been rewritten in the same tick without its stat moving.
	racy := stamp
	racy.HashedAt = max(stamp.ModTime, stamp.ChangeTime)
	e = &execIdentities{prev: map[string]execStamp{p: racy}, next: map[string]execStamp{}, now: time.Now()}
	if id, err := e.identify(p, false); err != nil || id == "sentinel" || !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("a racy cached stamp must be re-hashed, got %q, %v", id, err)
	}

	moved := settled
	moved.Size++
	e = &execIdentities{prev: map[string]execStamp{p: moved}, next: map[string]execStamp{}, now: time.Now()}
	if id, _ := e.identify(p, false); id == "sentinel" {
		t.Fatal("a changed stat must never reuse the cached identity")
	}
}

func TestReleasedModuleVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"v1.50.0":                                     true,
		"v0.1.45-0.20261013180900-6f9811cb05e0":       true,
		"v2.0.0+incompatible":                         true,
		"v0.1.45-0.20261013180900-6f9811cb05e0+dirty": false,
		"(devel)": false,
		"":        false,
	} {
		if got := releasedModuleVersion(v); got != want {
			t.Errorf("releasedModuleVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestGenerateCacheModeFor(t *testing.T) {
	cases := []struct {
		name  string
		flags pipelineFlags
		want  generateCacheMode
	}{
		{"a scaffold's internal regenerate", pipelineFlags{}, generateCacheMode{}},
		{"plain forge generate", pipelineFlags{Cache: true}, generateCacheMode{consult: true, record: true}},
		{"--no-cache still records", pipelineFlags{Cache: true, NoCache: true}, generateCacheMode{record: true}},
		{"--check (skips validate) consults, never records", pipelineFlags{Cache: true, SkipValidate: true}, generateCacheMode{consult: true}},
		{"--skip-pre-checks", pipelineFlags{Cache: true, SkipPreChecks: true}, generateCacheMode{consult: true}},
		{"--steps", pipelineFlags{Cache: true, Steps: "mocks"}, generateCacheMode{}},
		{"--templates-only", pipelineFlags{Cache: true, TemplatesOnly: true}, generateCacheMode{}},
		{"--force", pipelineFlags{Cache: true, Force: true}, generateCacheMode{}},
		{"--heal", pipelineFlags{Cache: true, Heal: true}, generateCacheMode{}},
		{"--force-cleanup", pipelineFlags{Cache: true, ForceCleanup: true}, generateCacheMode{}},
		{"--explain-drift", pipelineFlags{Cache: true, ExplainDrift: true}, generateCacheMode{}},
		{"--strict, --verbose, --timings change nothing written", pipelineFlags{Cache: true, Strict: true, Verbose: true, Timings: true}, generateCacheMode{consult: true, record: true}},
	}
	for _, tc := range cases {
		if got := generateCacheModeFor(tc.flags); got != tc.want {
			t.Errorf("%s: mode = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestGenerateCacheModeHonorsNoCacheEnv(t *testing.T) {
	t.Setenv(generateNoCacheEnv, "1")
	if got := generateCacheModeFor(pipelineFlags{Cache: true}); got.consult || !got.record {
		t.Fatalf("%s=1 must stop the skip but keep the record, got %+v", generateNoCacheEnv, got)
	}
}

// TestGenerateCachePipelineSkipsOnlyAVerifiedFixedPoint drives the real
// pipeline driver over a one-step plan: the first run verifies and records,
// the second skips without running the step, an input change runs it again,
// and --no-cache always runs it.
func TestGenerateCachePipelineSkipsOnlyAVerifiedFixedPoint(t *testing.T) {
	f := newCacheFixture(t)
	withCacheSystem(t, f)
	runs := 0
	withPipelineSteps(t, []GenStep{{
		Name: "emit gen/a.pb.go",
		Gate: always,
		Run: func(*pipelineContext) error {
			runs++
			// Rewrites its output with the bytes already there: a fixed point.
			return os.WriteFile(filepath.Join(f.root, "gen", "a.pb.go"), []byte("package gen\n"), 0o644)
		},
	}})

	run := func(flags pipelineFlags) string {
		t.Helper()
		var runErr error
		out := captureStdout(t, func() { runErr = runGeneratePipelineFlags(f.root, flags) })
		if runErr != nil {
			t.Fatalf("pipeline: %v\n%s", runErr, out)
		}
		return out
	}

	out := run(pipelineFlags{Cache: true})
	if runs != 1 || !strings.Contains(out, "Recorded as a verified fixed point") {
		t.Fatalf("the first run must run and record (runs=%d):\n%s", runs, out)
	}
	out = run(pipelineFlags{Cache: true})
	if runs != 1 || !strings.Contains(out, "nothing to do") {
		t.Fatalf("an unchanged tree must skip without running a step (runs=%d):\n%s", runs, out)
	}
	out = run(pipelineFlags{Cache: true, Strict: true})
	if runs != 2 || !strings.Contains(out, "--strict") {
		t.Fatalf("a --strict run must not accept a non-strict record (runs=%d):\n%s", runs, out)
	}
	run(pipelineFlags{Cache: true}) // the strict run's record serves a lenient one
	if runs != 2 {
		t.Fatalf("a strict record must satisfy a lenient run (runs=%d)", runs)
	}

	f.write(t, "proto/a.proto", "syntax = \"proto3\";\nmessage A {}\n")
	out = run(pipelineFlags{Cache: true})
	if runs != 3 || !strings.Contains(out, "proto/a.proto") {
		t.Fatalf("a changed input must run every step and say which input (runs=%d):\n%s", runs, out)
	}
	run(pipelineFlags{Cache: true, NoCache: true})
	if runs != 4 {
		t.Fatalf("--no-cache must run every step (runs=%d)", runs)
	}
	run(pipelineFlags{Cache: true, SkipValidate: true})
	if runs != 4 {
		t.Fatalf("--check's skip-validate run may consult a full-strength record (runs=%d)", runs)
	}
	run(pipelineFlags{}) // a scaffold's regenerate never consults
	if runs != 5 {
		t.Fatalf("a pipeline run without Cache must run every step (runs=%d)", runs)
	}
}

// TestGenerateCacheRecordUsesTheStartingEnvironment: forge sets variables in
// its own process mid-run (the KCL runtime's KCL_FILE_SCOPE on the first
// render). That is not an input changing under the run, and must not stop
// it recording — while a variable the run STARTED with still keys the record.
func TestGenerateCacheRecordUsesTheStartingEnvironment(t *testing.T) {
	f := newCacheFixture(t)
	withCacheSystem(t, f)
	withPipelineSteps(t, []GenStep{{
		Name: "render KCL",
		Gate: always,
		Run: func(*pipelineContext) error {
			f.setEnv("KCL_FILE_SCOPE", "off") // what kclplugin does on first use
			return nil
		},
	}})
	var runErr error
	out := captureStdout(t, func() { runErr = runGeneratePipelineFlags(f.root, pipelineFlags{Cache: true}) })
	if runErr != nil {
		t.Fatalf("pipeline: %v\n%s", runErr, out)
	}
	if !strings.Contains(out, "Recorded as a verified fixed point") {
		t.Fatalf("a variable forge set in its own process must not block the record:\n%s", out)
	}
	m := loadGenerateCacheManifest(f.root)
	if m == nil {
		t.Fatal("no manifest recorded")
	}
	if _, ok := m.Env["env:KCL_FILE_SCOPE"]; ok {
		t.Error("the record must describe the environment the run started with")
	}
}

// TestGenerateCachedNoopBeforeTheSlot pins the check `forge generate` makes
// before it queues for a heavy slot: a recorded fixed point skips there, and
// anything else falls through to the pipeline (which re-checks and says why)
// without printing a word.
func TestGenerateCachedNoopBeforeTheSlot(t *testing.T) {
	f := newCacheFixture(t)
	withCacheSystem(t, f)
	withPipelineSteps(t, []GenStep{{Name: "noop", Gate: always, Run: func(*pipelineContext) error { return nil }}})

	noop := func(flags pipelineFlags) (bool, string) {
		t.Helper()
		var skipped bool
		var err error
		out := captureStdout(t, func() { skipped, err = generateCachedNoop(f.root, flags) })
		if err != nil {
			t.Fatal(err)
		}
		return skipped, out
	}
	if skipped, out := noop(pipelineFlags{Cache: true}); skipped || out != "" {
		t.Fatalf("with nothing on record the pre-check must fall through silently, got %v %q", skipped, out)
	}
	captureStdout(t, func() {
		if err := runGeneratePipelineFlags(f.root, pipelineFlags{Cache: true}); err != nil {
			t.Fatal(err)
		}
	})
	if skipped, out := noop(pipelineFlags{Cache: true}); !skipped || !strings.Contains(out, "nothing to do") {
		t.Fatalf("a recorded fixed point must skip before the slot, got %v %q", skipped, out)
	}
	if skipped, _ := noop(pipelineFlags{Cache: true, NoCache: true}); skipped {
		t.Fatal("--no-cache must never skip")
	}
	if skipped, _ := noop(pipelineFlags{Cache: true, Steps: "mocks"}); skipped {
		t.Fatal("a narrowed run must never skip")
	}
	f.write(t, "proto/a.proto", "changed\n")
	if skipped, out := noop(pipelineFlags{Cache: true}); skipped || out != "" {
		t.Fatalf("a miss must fall through silently (the pipeline names the change), got %v %q", skipped, out)
	}
}

// TestGenerateCachePipelineNeverRecordsARunThatWrote: a run that changed a
// file has not shown the tree is a fixed point, so the next run is full.
func TestGenerateCachePipelineNeverRecordsARunThatWrote(t *testing.T) {
	f := newCacheFixture(t)
	withCacheSystem(t, f)
	runs := 0
	withPipelineSteps(t, []GenStep{{
		Name: "emit a growing file",
		Gate: always,
		Run: func(*pipelineContext) error {
			runs++
			return os.WriteFile(filepath.Join(f.root, "gen", "a.pb.go"), []byte("package gen\n"+strings.Repeat("//\n", runs)), 0o644)
		},
	}})
	for i := 0; i < 2; i++ {
		var runErr error
		out := captureStdout(t, func() { runErr = runGeneratePipelineFlags(f.root, pipelineFlags{Cache: true}) })
		if runErr != nil {
			t.Fatalf("pipeline: %v\n%s", runErr, out)
		}
		if strings.Contains(out, "Recorded") || strings.Contains(out, "nothing to do") {
			t.Fatalf("a run that wrote must neither record nor be skipped next time:\n%s", out)
		}
	}
	if runs != 2 {
		t.Fatalf("both runs must have run their step, ran %d", runs)
	}
	if _, err := os.Stat(filepath.Join(f.root, generateCacheRel)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no manifest may exist after runs that only ever wrote: %v", err)
	}
}

// TestGenerateCacheNeverRecordsASoftFailure: a step that failed as a
// warning (warnOrFail without --strict) wrote nothing, so a run that
// "changed nothing" did not verify that step's outputs.
func TestGenerateCacheNeverRecordsASoftFailure(t *testing.T) {
	f := newCacheFixture(t)
	withCacheSystem(t, f)
	withPipelineSteps(t, []GenStep{{
		Name: "render something",
		Gate: always,
		Run: func(ctx *pipelineContext) error {
			return ctx.warnOrFail("render something", errors.New("kcl is not installed"))
		},
	}})
	var runErr error
	out := captureStdout(t, func() { runErr = runGeneratePipelineFlags(f.root, pipelineFlags{Cache: true}) })
	// The run goes on past the failed step, and still fails at the end.
	var stepsFailed *factory.StepsFailedError
	if !errors.As(runErr, &stepsFailed) {
		t.Fatalf("a run with a failed step must fail with *factory.StepsFailedError, got %v\n%s", runErr, out)
	}
	if strings.Contains(out, "Recorded as a verified fixed point") || !strings.Contains(out, "not recorded") {
		t.Fatalf("a run with a soft-failed step must not record, and must say why:\n%s", out)
	}
	if loadGenerateCacheManifest(f.root) != nil {
		t.Fatal("no manifest may exist after a run whose step failed soft")
	}
}

// TestGenerateCacheSkipLineAndMissReason pins the user-facing lines: one line
// either way, and the miss names what changed.
func TestGenerateCacheSkipLineAndMissReason(t *testing.T) {
	f := newCacheFixture(t)
	m := f.record(t)
	if err := saveGenerateCacheManifest(f.root, m); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(5 * time.Minute)
	c := openGenerateCache(f.root, pipelineFlags{Cache: true}, snapshotWorkTree(f.root), f.sys())
	var buf bytes.Buffer
	if !c.skip(&buf) {
		t.Fatalf("an unchanged tree must skip:\n%s", buf.String())
	}
	if got := strings.Count(buf.String(), "\n"); got != 1 || !strings.Contains(buf.String(), "5m ago") || !strings.Contains(buf.String(), "--no-cache") {
		t.Errorf("the skip must be one line saying when it was verified and how to force a run, got:\n%s", buf.String())
	}

	f.write(t, "proto/a.proto", "changed\n")
	buf.Reset()
	c = openGenerateCache(f.root, pipelineFlags{Cache: true}, snapshotWorkTree(f.root), f.sys())
	if c.skip(&buf) {
		t.Fatal("a changed tree must not skip")
	}
	if got := buf.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "1 file(s) changed (proto/a.proto)") {
		t.Errorf("the miss must be one line naming the change, got:\n%s", got)
	}
}

func withCacheSystem(t *testing.T, f *cacheFixture) {
	t.Helper()
	prev := generateCacheSystem
	generateCacheSystem = f.sys
	t.Cleanup(func() { generateCacheSystem = prev })
	t.Cleanup(checksums.ResetPerRunState)
}

func mustRemove(t *testing.T, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}

// TestMovedDuringRun pins the record-time check: a run may record only
// when every input it saw before running still has the identity it had.
// Keys that appear only afterwards are the run's own outputs.
func TestMovedDuringRun(t *testing.T) {
	base := func() *generateFingerprint {
		return &generateFingerprint{
			files: map[string]string{"proto/a.proto": "1", "gen/a.pb.go": "2"},
			env:   map[string]string{"forge": "f", "tool:go": "g"},
		}
	}
	root := t.TempDir()
	writeTreeFileT(t, filepath.Join(root, "old", "output.txt"), "still here\n")
	pre := base()
	if movedDuringRun(root, pre, base()) != "" {
		t.Fatal("identical fingerprints must agree")
	}
	grown := base()
	grown.files["gen/new.pb.go"] = "3"
	if movedDuringRun(root, pre, grown) != "" {
		t.Error("a key only the post-run fingerprint has is an output, not a moved input")
	}
	moved := base()
	moved.files["proto/a.proto"] = "edited during the run"
	if got := movedDuringRun(root, pre, moved); !strings.Contains(got, "proto/a.proto") {
		t.Errorf("an input edited while the run ran must block the record and be named, got %q", got)
	}
	tool := base()
	tool.env["tool:go"] = "upgraded during the run"
	if got := movedDuringRun(root, pre, tool); !strings.Contains(got, "go changed") {
		t.Errorf("a tool replaced while the run ran must block the record and be named, got %q", got)
	}
	// A previous run's output this run no longer lists: re-read from disk.
	withOld := base()
	withOld.files["old/output.txt"] = "will not match"
	if got := movedDuringRun(root, withOld, base()); !strings.Contains(got, "old/output.txt") {
		t.Errorf("an unlisted pre-run file whose bytes differ now must block the record, got %q", got)
	}
	id, err := pathIdentity(filepath.Join(root, "old", "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	withOld.files["old/output.txt"] = id
	if got := movedDuringRun(root, withOld, base()); got != "" {
		t.Errorf("an unlisted pre-run file that is unchanged on disk is no change, got %q", got)
	}
	mustRemove(t, filepath.Join(root, "old", "output.txt"))
	if got := movedDuringRun(root, withOld, base()); !strings.Contains(got, "old/output.txt") {
		t.Errorf("a pre-run file the run deleted must block the record, got %q", got)
	}
	extra := base()
	extra.env["env:FORGE_X"] = "set"
	if movedDuringRun(root, pre, extra) == "" {
		t.Error("an environment that differs must block the record")
	}
}

// TestDiffGenerateFingerprint pins the miss reason: every kind of key reads
// as what it is, and a long list is cut short.
func TestDiffGenerateFingerprint(t *testing.T) {
	m := &generateCacheManifest{
		Env:   map[string]string{"forge": "a", "tool:buf": "a", "env:GOFLAGS": "a", "cache": "1"},
		Files: map[string]string{"proto/a.proto": "a", "gowork:/w/go.work": "a", "ext:/lib/x.go": "a", "gen-ignored/": "absent"},
	}
	same := &generateFingerprint{env: maps.Clone(m.Env), files: maps.Clone(m.Files)}
	if d := diffGenerateFingerprint(m, same); d != "" {
		t.Fatalf("identical: got %q", d)
	}
	fp := &generateFingerprint{
		env:   map[string]string{"forge": "b", "tool:buf": "b", "env:GOFLAGS": "b", "cache": "1"},
		files: map[string]string{"proto/a.proto": "b", "gowork:/w/go.work": "b", "ext:/lib/x.go": "b", "gen-ignored/": "absent", "new.go": "n"},
	}
	d := diffGenerateFingerprint(m, fp)
	for _, want := range []string{"the forge binary", "buf", "$GOFLAGS", "4 file(s) changed", "/w/go.work", "+1 more"} {
		if !strings.Contains(d, want) {
			t.Errorf("miss reason %q should contain %q", d, want)
		}
	}
	state := describeFileChange([]string{".forge/disowned.json", ".forge/hashes.json", "proto/a.proto"})
	if state != "3 file(s) changed (proto/a.proto, 2 .forge/ state file(s))" {
		t.Errorf("forge's own state must collapse to one entry after the user's files, got %q", state)
	}
	gone := &generateFingerprint{env: maps.Clone(m.Env), files: map[string]string{}}
	if d := diffGenerateFingerprint(m, gone); !strings.Contains(d, "4 file(s) changed") {
		t.Errorf("keys that vanished are changes too, got %q", d)
	}
}

// TestGenerateCacheEnvRelevant pins which variables are part of the key.
func TestGenerateCacheEnvRelevant(t *testing.T) {
	for name, want := range map[string]bool{
		"FORGE_ALLOW_VERSION_SKEW":   true,
		"FORGE_TEST_POSTGRES_URL":    true,
		"GOFLAGS":                    true,
		"GOWORK":                     true,
		"GOTOOLCHAIN":                true,
		"GOOS":                       true,
		"CGO_ENABLED":                true,
		"KCL_LIB_HOME":               true,
		"BUF_CACHE_DIR":              true,
		"NODE_OPTIONS":               true,
		"POSTGRES_PORT":              true,
		"DATABASE_URL":               true,
		"CI":                         true,
		"HOME":                       true,
		"PATH":                       false, // what it resolves is keyed tool by tool
		"GOPROXY":                    false,
		"GOMODCACHE":                 false,
		"GOCACHE":                    false,
		"GOMAXPROCS":                 false,
		generateTimingsEnv:           false,
		generateNoCacheEnv:           false,
		"FORGE_SILENCE_EXPERIMENTAL": false,
		"TERM":                       false,
	} {
		if got := generateCacheEnvRelevant(name); got != want {
			t.Errorf("generateCacheEnvRelevant(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestBufLocalPluginNames: the plugins a buf template runs from PATH are
// fingerprinted, in both spellings buf accepts.
func TestBufLocalPluginNames(t *testing.T) {
	p := filepath.Join(t.TempDir(), "buf.gen.yaml")
	writeTreeFileT(t, p, "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n"+
		"  - local: [protoc-gen-x, --flag]\n    out: gen\n  - remote: buf.build/x/y\n    out: gen\n")
	got := bufLocalPluginNames(p)
	if strings.Join(got, ",") != "protoc-gen-go,protoc-gen-x" {
		t.Fatalf("bufLocalPluginNames = %v", got)
	}
}
