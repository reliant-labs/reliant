// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func usageOf(total, free uint64) Usage {
	return Usage{TotalBytes: total, UsedBytes: total - free, FreeBytes: free}
}

func TestNextPressure(t *testing.T) {
	cases := []struct {
		name    string
		current bool
		u       Usage
		want    bool
	}{
		// The incident volume: 40 GiB /dev/docker-data.
		{"40G at 87% (5.2G free) does not assert", false, usageOf(40*GiB, 52*GiB/10), false},
		{"40G at 90% asserts", false, usageOf(40*GiB, 4*GiB), true},
		{"40G full asserts", false, usageOf(40*GiB, 0), true},
		{"40G at 87% holds an asserted bit (hysteresis)", true, usageOf(40*GiB, 52*GiB/10), true},
		{"40G at 84% clears", true, usageOf(40*GiB, 64*GiB/10), false},
		// The home volume that was fine all along.
		{"157G at 21% does not assert", false, usageOf(157*GiB, 124*GiB), false},
		// Small volumes: the 2 GiB floor.
		{"10G with 1.9G free asserts on the floor", false, usageOf(10*GiB, 19*GiB/10), true},
		{"10G with 2.5G free holds (clear floor is 3G)", true, usageOf(10*GiB, 25*GiB/10), true},
		{"10G with 3.1G free clears", true, usageOf(10*GiB, 31*GiB/10), false},
		{"1G tmpfs half empty is not permanently full", false, usageOf(1*GiB, GiB/2), false},
		// Large volumes: the 20 GiB ceiling on the percentage rule.
		{"1T laptop at 91% (90G free) does not assert", false, usageOf(1000*GiB, 90*GiB), false},
		{"1T laptop with 1.5G free asserts", false, usageOf(1000*GiB, 3*GiB/2), true},
		{"unsized volume never asserts", false, Usage{}, false},
		{"inodes at 96% assert with space free", false, Usage{TotalBytes: 40 * GiB, UsedBytes: 10 * GiB, FreeBytes: 30 * GiB, FilesTotal: 1000, FilesFree: 40}, true},
		{"inodes at 92% hold", true, Usage{TotalBytes: 40 * GiB, UsedBytes: 10 * GiB, FreeBytes: 30 * GiB, FilesTotal: 1000, FilesFree: 80}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextPressure(tc.current, tc.u); got != tc.want {
				t.Fatalf("NextPressure(%t, %+v) = %t, want %t", tc.current, tc.u, got, tc.want)
			}
		})
	}
}

// fakeFS answers statFS for a set of paths: each mapped prefix is a mount with
// its own st_dev and geometry; exists lists the paths that exist.
type fakeFS struct {
	mounts map[string]fsStat // mount point -> reading
	exists map[string]bool
}

func (f *fakeFS) stat(path string) (fsStat, string, error) {
	p := path
	for !f.exists[p] {
		parent := filepath.Dir(p)
		if parent == p {
			return fsStat{}, "", os.ErrNotExist
		}
		p = parent
	}
	best := ""
	for m := range f.mounts {
		if (p == m || strings.HasPrefix(p, strings.TrimSuffix(m, "/")+"/")) && len(m) > len(best) {
			best = m
		}
	}
	if best == "" {
		return fsStat{}, "", os.ErrNotExist
	}
	return f.mounts[best], p, nil
}

// cloudMachine models the machine of the 2026-10-10/11 incidents (numbers from
// `stat -f` there): the docker volume holds the caches, docker and the
// /etc /usr /opt /var overlays (each its own st_dev, identical statfs); /home
// is its own volume; / and /tmp are two mounts of one filesystem.
func cloudMachine(dockerBfree, dockerBavail uint64) *fakeFS {
	docker := fsStat{Dev: 2048, Bsize: 4096, Blocks: 10243384, Bfree: dockerBfree, Bavail: dockerBavail, Files: 2621440, Ffree: 1726804}
	overlay := func(dev uint64) fsStat { s := docker; s.Dev = dev; s.Bfree -= 3; return s } // a write landed between the two statfs calls
	root := fsStat{Dev: 40, Bsize: 4096, Blocks: 25119164, Bfree: 14327031, Bavail: 14322935, Files: 12976128, Ffree: 12440165}
	tmp := root
	tmp.Dev = 36
	return &fakeFS{
		mounts: map[string]fsStat{
			"/":                    root,
			"/tmp":                 tmp,
			"/home/workspace":      {Dev: 44, Bsize: 4096, Blocks: 41007301, Bfree: 32511067, Bavail: 32506971, Files: 10485760, Ffree: 8452074},
			"/var/lib/docker":      docker,
			"/var/cache/workspace": docker,
			"/etc":                 overlay(45),
			"/usr":                 overlay(48),
			"/opt":                 overlay(51),
			"/var":                 overlay(54),
		},
		exists: map[string]bool{
			"/": true, "/tmp": true, "/home/workspace": true, "/home/workspace/.reliant/worktrees": true,
			"/var/lib/docker": true, "/var/cache/workspace": true, "/var/cache/workspace/go-build": true,
			"/var/cache/workspace/go-mod": true, "/var/cache/workspace/npm": true, "/var/cache/workspace/xdg": true,
			"/var/cache/workspace/golangci-lint": true, "/etc": true, "/usr": true, "/opt": true, "/var": true,
		},
	}
}

func cloudEnv() Environment {
	env := map[string]string{
		"GOCACHE":             "/var/cache/workspace/go-build",
		"GOMODCACHE":          "/var/cache/workspace/go-mod",
		"npm_config_cache":    "/var/cache/workspace/npm",
		"XDG_CACHE_HOME":      "/var/cache/workspace/xdg",
		"GOLANGCI_LINT_CACHE": "/var/cache/workspace/golangci-lint",
	}
	return Environment{Getenv: func(k string) string { return env[k] }, Home: "/home/workspace", GOOS: "linux"}
}

func TestGroupVolumes_CloudMachine(t *testing.T) {
	fs := cloudMachine(1878694, 1350310)
	vols := groupVolumes(DiscoverTargets(cloudEnv()), fs.stat)
	got := map[string][]string{}
	for _, v := range vols {
		got[v.label()] = v.paths()
	}
	if len(vols) != 3 {
		t.Fatalf("want 3 volumes (home, build caches & docker, temp), got %d: %v", len(vols), got)
	}
	shared := got["build caches & docker"]
	for _, want := range []string{"/var/cache/workspace/go-build", "/var/cache/workspace/go-mod", "/var/lib/docker", "/etc", "/usr", "/opt", "/var"} {
		if !contains(shared, want) {
			t.Errorf("build caches & docker volume should hold %s (overlays dedupe onto their upper's filesystem); got %v", want, shared)
		}
	}
	if !contains(got["home"], "/home/workspace") || contains(got["home"], "/var/lib/docker") {
		t.Errorf("home volume = %v", got["home"])
	}
	if !contains(got["temp"], "/tmp") {
		t.Errorf("temp volume = %v", got["temp"])
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestDiscoverTargets_LaptopDefaultsNestCachesUnderHome(t *testing.T) {
	env := Environment{Getenv: func(string) string { return "" }, Home: "/Users/me", GOOS: "darwin",
		GoEnv: func(k string) string {
			return map[string]string{"GOCACHE": "/Users/me/Library/Caches/go-build", "GOMODCACHE": "/Users/me/go/pkg/mod"}[k]
		}}
	targets := DiscoverTargets(env)
	byKind := map[string]string{}
	for _, tg := range targets {
		if tg.Kind != "" {
			byKind[tg.Kind] = tg.Path
		}
		if tg.Role == RoleDocker || tg.Role == RoleSystem {
			t.Errorf("darwin must not watch %s (%s)", tg.Path, tg.Role)
		}
	}
	if byKind[KindGoBuildCache] != "/Users/me/Library/Caches/go-build" || byKind[KindNpmCache] != "/Users/me/.npm" ||
		byKind[KindGolangciLintCache] != "/Users/me/.cache/golangci-lint" || byKind[KindXDGCache] != "/Users/me/.cache" {
		t.Fatalf("unexpected cache targets: %v", byKind)
	}
}

// watcherOn builds a Watcher over a fake filesystem whose cache directories
// are real temp dirs (so consumer measurement walks real files).
func watcherOn(t *testing.T, fs *fakeFS, env Environment, now *time.Time) *Watcher {
	t.Helper()
	return New(Options{Env: env, Now: func() time.Time { return *now }, stat: fs.stat, noDocker: true, mounts: func() []overlayMount { return nil }})
}

func writeFile(t *testing.T, path string, size int, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// tempCacheMachine is a machine whose "build caches" volume holds real temp
// directories: a Go build cache with old and fresh entries and an npm cache.
func tempCacheMachine(t *testing.T, bavail uint64) (*fakeFS, Environment, string) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	home := filepath.Join(root, "home")
	old := time.Now().Add(-3 * time.Hour)
	writeFile(t, filepath.Join(cache, "go-build", "README"), 100, old)
	writeFile(t, filepath.Join(cache, "go-build", "ab", "old-d"), 64<<10, old)
	writeFile(t, filepath.Join(cache, "go-build", "ab", "fresh-d"), 32<<10, time.Now())
	writeFile(t, filepath.Join(cache, "npm", "_cacache", "x"), 16<<10, old)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	const blocks = 10 << 20 // 40 GiB of 4 KiB blocks
	fs := &fakeFS{
		mounts: map[string]fsStat{
			cache: {Dev: 7, Bsize: 4096, Blocks: blocks, Bfree: bavail, Bavail: bavail, Files: 1000, Ffree: 900},
			home:  {Dev: 8, Bsize: 4096, Blocks: blocks * 4, Bfree: blocks * 3, Bavail: blocks * 3, Files: 1000, Ffree: 900},
		},
		exists: map[string]bool{cache: true, home: true, filepath.Join(cache, "go-build"): true, filepath.Join(cache, "go-build", "ab"): true, filepath.Join(cache, "npm"): true},
	}
	envm := map[string]string{"GOCACHE": filepath.Join(cache, "go-build"), "npm_config_cache": filepath.Join(cache, "npm"), "TMPDIR": home, "XDG_CACHE_HOME": filepath.Join(home, ".cache")}
	return fs, Environment{Getenv: func(k string) string { return envm[k] }, Home: home, GOOS: "test"}, cache
}

func waitChanged(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case <-w.Changed():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher never signalled Changed")
	}
}

// waitScanned waits for the background consumer scan a pressure flip starts.
func waitScanned(t *testing.T, w *Watcher) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		done := len(w.scanAt) > 0 && !w.scanning
		w.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("consumer scan never finished")
}

// waitCleared waits for a background clean-up to finish (so t.TempDir's
// cleanup does not race it).
func waitCleared(t *testing.T, w *Watcher) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		done := len(w.clearing) == 0 && !w.scanning
		w.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("clean-up never finished")
}

func TestWatcher_PressureFlipScansConsumersAndReports(t *testing.T) {
	now := time.Now()
	fs, env, cache := tempCacheMachine(t, 0) // full
	w := watcherOn(t, fs, env, &now)

	w.poll(context.Background())
	waitChanged(t, w) // pressure asserted (and the scan it started may coalesce into this token)
	waitScanned(t, w)

	rep, ok := w.Report()
	if !ok {
		t.Fatal("no report after a poll")
	}
	var full *reliantv1.DiskVolume
	for _, v := range rep.Volumes {
		if v.Label == "build caches" {
			full = v
		}
	}
	if full == nil || !full.Pressure {
		t.Fatalf("the cache volume should be reported under pressure: %v", rep.Volumes)
	}
	if full.ConsumersMeasuredAtMs == 0 || len(full.Consumers) == 0 {
		t.Fatalf("a volume under pressure should say what is using it: %v", full)
	}
	var gocache *reliantv1.DiskConsumer
	for _, c := range full.Consumers {
		if c.Kind == KindGoBuildCache {
			gocache = c
		}
	}
	if gocache == nil || gocache.Path != filepath.Join(cache, "go-build") || gocache.Category != CategoryCaches {
		t.Fatalf("Go build cache consumer missing or wrong: %v", full.Consumers)
	}
	// Only the shard entry older than GoCacheTrimAge is reclaimable; README
	// (top level) and the fresh entry are not.
	if gocache.ReclaimableBytes == 0 || gocache.ReclaimableBytes >= gocache.Bytes || !gocache.Clearable {
		t.Fatalf("reclaimable=%d bytes=%d clearable=%t; want 0 < reclaimable < bytes, clearable", gocache.ReclaimableBytes, gocache.Bytes, gocache.Clearable)
	}
	for _, v := range rep.Volumes {
		if v.Label == "home" && (v.Pressure || len(v.Consumers) > 0) {
			t.Fatalf("a healthy volume carries no pressure and no breakdown: %v", v)
		}
	}
}

func TestExplainNoSpace(t *testing.T) {
	now := time.Now()
	fs, env, cache := tempCacheMachine(t, 0)
	w := watcherOn(t, fs, env, &now)
	w.poll(context.Background())
	waitScanned(t, w)

	failing := "go: writing stat cache: write " + filepath.Join(cache, "go-build", "39", "abc-d") + ": no space left on device\nFAIL"
	msg := w.ExplainNoSpace(failing)
	for _, want := range []string{"[disk full]", "build caches volume", "0 B free of 43 GB", "Go build cache", "Retrying will fail"} {
		if !strings.Contains(msg, want) {
			t.Errorf("explanation missing %q:\n%s", want, msg)
		}
	}
	rep, _ := w.Report()
	for _, v := range rep.Volumes {
		if v.Label == "build caches" && v.LastNoSpaceAtMs != now.UnixMilli() {
			t.Fatalf("the failure should be recorded on the volume: %v", v)
		}
	}

	if got := w.ExplainNoSpace("exit status 1: permission denied"); got != "" {
		t.Fatalf("no ENOSPC, no explanation; got %q", got)
	}
}

func TestExplainNoSpace_DoesNotBlameAVolumeWithRoom(t *testing.T) {
	now := time.Now()
	fs, env, _ := tempCacheMachine(t, 8<<20) // 80% free
	w := watcherOn(t, fs, env, &now)
	w.poll(context.Background())
	// A container's own tmpfs filled: the path is not on this machine.
	if got := w.ExplainNoSpace("initdb: could not create directory \"/var/lib/postgresql/data/pg_wal\": No space left on device"); got != "" {
		t.Fatalf("an ENOSPC no watched volume explains must not be pinned on a disk with room; got %q", got)
	}
}

func TestNoSpacePaths(t *testing.T) {
	got := noSpacePaths("npm ERR! ENOSPC: no space left on device, open '/var/cache/workspace/npm/_cacache/tmp/x'\nok /other/path")
	if len(got) != 1 || got[0] != "/var/cache/workspace/npm/_cacache/tmp/x" {
		t.Fatalf("got %v", got)
	}
}

func TestTrimGoCache_RemovesOnlyStaleShardEntries(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-3 * time.Hour)
	writeFile(t, filepath.Join(root, "README"), 10, old)
	writeFile(t, filepath.Join(root, "trim.txt"), 10, old)
	writeFile(t, filepath.Join(root, "0a", "stale-a"), 4096, old)
	writeFile(t, filepath.Join(root, "0a", "stale-d"), 4096, old)
	writeFile(t, filepath.Join(root, "0a", "fresh-d"), 4096, time.Now())
	writeFile(t, filepath.Join(root, "notashard", "x"), 4096, old)

	_, removed, err := trimGoCache(context.Background(), root, time.Now().Add(-GoCacheTrimAge))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed %d, want the 2 stale shard entries", removed)
	}
	for _, keep := range []string{"README", "trim.txt", "0a/fresh-d", "notashard/x"} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Errorf("%s must be kept: %v", keep, err)
		}
	}
}

func TestClearCaches_RefusesWhatIsNotSafe(t *testing.T) {
	now := time.Now()
	fs, env, _ := tempCacheMachine(t, 0)
	w := watcherOn(t, fs, env, &now)
	started, skipped := w.ClearCaches([]string{KindGoModCache, KindDockerVolumes, KindDockerBuildCache, KindGoBuildCache})
	if len(started) != 1 || started[0] != KindGoBuildCache {
		t.Fatalf("only the Go build cache may start here; started %v skipped %v", started, skipped)
	}
	if len(skipped) != 3 {
		t.Fatalf("want module cache, docker volumes and (no docker) build cache refused, got %v", skipped)
	}
	waitChanged(t, w) // clearing started
	waitCleared(t, w)
}

func TestDockerConsumers_UseTheCLIAccounting(t *testing.T) {
	var df dockerDF
	raw := `{"LayersSize": 9783382589,
	  "Images": [{"Containers": 1, "Size": 400, "SharedSize": 100}, {"Containers": 0, "Size": 900, "SharedSize": 0}],
	  "Containers": [{"SizeRw": 10, "State": "running"}, {"SizeRw": 5, "State": "exited"}],
	  "Volumes": [{"UsageData": {"Size": 73886027, "RefCount": 0}}, {"UsageData": {"Size": 100, "RefCount": 1}}, {"UsageData": {"Size": -1, "RefCount": 0}}],
	  "BuildCache": [{"Size": 308856101, "InUse": false, "Shared": true}, {"Size": 50, "InUse": false, "Shared": false}]}`
	if err := json.Unmarshal([]byte(raw), &df); err != nil {
		t.Fatal(err)
	}
	got := map[string]*reliantv1.DiskConsumer{}
	for _, c := range df.consumers() {
		got[c.Kind] = c
	}
	if c := got[KindDockerImages]; c.Bytes != 9783382589 || c.ReclaimableBytes != 9783382589-300 || c.Clearable {
		t.Errorf("images: %v", c)
	}
	if c := got[KindDockerContainers]; c.Bytes != 15 || c.ReclaimableBytes != 5 {
		t.Errorf("containers: %v", c)
	}
	if c := got[KindDockerVolumes]; c.Bytes != 73886127 || c.ReclaimableBytes != 73886027 || c.Clearable {
		t.Errorf("volumes must never be clearable: %v", c)
	}
	// A record shared with an image frees nothing when pruned.
	if c := got[KindDockerBuildCache]; c.Bytes != 308856151 || c.ReclaimableBytes != 50 || !c.Clearable {
		t.Errorf("build cache: %v", c)
	}
}

func TestEncodeDecode(t *testing.T) {
	rep := &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{{Label: "build caches & docker", TotalBytes: 40 * GiB, FreeBytes: 1, Pressure: true}}}
	at := time.UnixMilli(1760000000000)
	s, err := Encode(rep, at)
	if err != nil {
		t.Fatal(err)
	}
	back := Decode(s)
	if back == nil || back.ReportedAtMs != at.UnixMilli() || back.Volumes[0].Label != "build caches & docker" || !back.Volumes[0].Pressure {
		t.Fatalf("round trip lost data: %v", back)
	}
	if rep.ReportedAtMs != 0 {
		t.Fatal("Encode must not mutate its input")
	}
	// The pre-report shape measured the worktree root — the wrong volume.
	if Decode(`{"path":"/home/workspace/.reliant/worktrees","free_bytes":1,"total_bytes":2,"reported_at":"2026-10-10T00:00:00Z"}`) != nil {
		t.Fatal("the legacy worktree-root reading must not decode as a report")
	}
}

func TestSignatures(t *testing.T) {
	v := &reliantv1.DiskVolume{Label: "home", TotalBytes: 100 * GiB, FreeBytes: 50*GiB + GiB/2}
	rep := &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{v}}
	sig, urgent := Signature(rep), UrgentSignature(rep)
	v.FreeBytes -= GiB / 4 // under 1% of the volume, inside one step
	if Signature(rep) != sig {
		t.Fatal("drift under 1% of the volume is not material")
	}
	v.FreeBytes -= 2 * GiB
	if Signature(rep) == sig || UrgentSignature(rep) != urgent {
		t.Fatal("a 2% step is material but not urgent")
	}
	v.Pressure = true
	if UrgentSignature(rep) == urgent {
		t.Fatal("a pressure flip is urgent")
	}
}

// The failing path is on a volume that has room again: say it ran out, not
// that a volume with gigabytes free "is out of space".
func TestExplainNoSpace_VolumeThatRecoveredIsNotCalledFull(t *testing.T) {
	now := time.Now()
	fs, env, cache := tempCacheMachine(t, 8<<20) // 80% free
	w := watcherOn(t, fs, env, &now)
	w.poll(context.Background())
	msg := w.ExplainNoSpace("write " + filepath.Join(cache, "go-build", "ab", "x-d") + ": no space left on device")
	if !strings.Contains(msg, "ran out of space") || strings.Contains(msg, "is out of space") || strings.Contains(msg, "will fail the same way") {
		t.Fatalf("a recovered volume must not be reported as full now:\n%s", msg)
	}
}
