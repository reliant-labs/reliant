package forgedelegate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

const pin = "v0.1.46-0.20261018032013-ee3bded463bc"

func fakeInstall(t *testing.T, calls *[]string) func(context.Context, string, string) error {
	t.Helper()
	return func(_ context.Context, pkg, gobin string) error {
		*calls = append(*calls, pkg)
		return atomicfile.WriteFile(filepath.Join(gobin, exeName()), []byte("#!/bin/sh\n"), 0o755)
	}
}

func newResolver(t *testing.T) (*Resolver, *[]string, *bytes.Buffer) {
	t.Helper()
	var calls []string
	var log bytes.Buffer
	t.Setenv(CacheDirEnv, t.TempDir())
	return &Resolver{Log: &log, Install: fakeInstall(t, &calls)}, &calls, &log
}

// The first request for a pin builds it; every later one reuses the build.
func TestPinned_BuildsOnceThenHitsCache(t *testing.T) {
	r, calls, log := newResolver(t)
	project := t.TempDir()

	first, err := r.Pinned(context.Background(), project, pin)
	if err != nil {
		t.Fatal(err)
	}
	if want := "github.com/reliant-labs/forge/cmd/forge@" + pin; len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("installed %v, want [%s]", *calls, want)
	}
	if !strings.Contains(log.String(), "building forge "+pin) {
		t.Errorf("no build notice: %q", log.String())
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("result does not exist: %v", err)
	}

	second, err := r.Pinned(context.Background(), project, pin)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Errorf("second request built again: %v", *calls)
	}
	if second.Path != first.Path {
		t.Errorf("cache path moved: %s then %s", first.Path, second.Path)
	}
}

// A failed build leaves nothing the next request would trust as a hit.
func TestPinned_FailedBuildLeavesNoBinary(t *testing.T) {
	r, _, _ := newResolver(t)
	r.Install = func(context.Context, string, string) error {
		return errors.New("dial tcp: lookup proxy.golang.org: no such host")
	}
	_, err := r.Pinned(context.Background(), t.TempDir(), pin)
	if err == nil || !strings.Contains(err.Error(), "no such host") || !strings.Contains(err.Error(), pin) {
		t.Fatalf("error does not name the version and the cause: %v", err)
	}
	var calls []string
	r.Install = fakeInstall(t, &calls)
	if _, err := r.Pinned(context.Background(), t.TempDir(), pin); err != nil {
		t.Fatalf("a retry after a failed build: %v", err)
	}
	if len(calls) != 1 {
		t.Errorf("the failed build was served as a hit: %v", calls)
	}
}

// A project that already installed its pin (task tools) pays no build.
func TestPinned_ProjectToolsBinaryWhenVersionMatches(t *testing.T) {
	r, calls, _ := newResolver(t)
	project := t.TempDir()
	local := filepath.Join(project, ".tools", "bin", exeName())
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(local, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	reported := pin
	r.Version = func(context.Context, string) (string, error) { return reported, nil }
	r.Same = func(want, got string) bool { return want == got }

	res, err := r.Pinned(context.Background(), project, pin)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != local || len(*calls) != 0 {
		t.Fatalf("got %s after %v, want the project's own binary and no build", res.Path, *calls)
	}

	reported = "v0.1.40"
	res, err = r.Pinned(context.Background(), project, pin)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path == local || len(*calls) != 1 {
		t.Errorf("a .tools/bin/forge of another version was used: %s, builds %v", res.Path, *calls)
	}
}

func TestPinned_NoPin(t *testing.T) {
	r, _, _ := newResolver(t)
	if _, err := r.Pinned(context.Background(), t.TempDir(), " "); err == nil {
		t.Fatal("an empty pin resolved")
	}
}

// The cache keeps the newest few builds and never the one in use.
func TestPinned_PrunesLeastRecentlyUsed(t *testing.T) {
	r, _, _ := newResolver(t)
	cache := os.Getenv(CacheDirEnv)
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < keepVersions+3; i++ {
		d := filepath.Join(cache, "v0.0."+string(rune('a'+i)))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		at := old.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(d, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Pinned(context.Background(), t.TempDir(), pin); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(cache)
	if want := keepVersions + 1; len(entries) != want {
		t.Fatalf("%d entries kept, want %d", len(entries), want)
	}
	if _, err := os.Stat(filepath.Join(cache, safeName(pin))); err != nil {
		t.Errorf("the build in use was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "v0.0.a")); err == nil {
		t.Error("the least recently used entry survived")
	}
}

// A linked checkout is built where it stands, and rebuilt only when it changes.
func TestBridge_BuildsCheckoutPerStamp(t *testing.T) {
	r, _, _ := newResolver(t)
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, "cmd", "forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	var builds []string
	r.Build = func(_ context.Context, dir, out string) error {
		builds = append(builds, dir)
		return atomicfile.WriteFile(out, []byte("#!/bin/sh\n"), 0o755)
	}
	a, err := r.Bridge(context.Background(), checkout, "abc+false@0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Bridge(context.Background(), checkout, "abc+false@0"); err != nil {
		t.Fatal(err)
	}
	if len(builds) != 1 || builds[0] != checkout {
		t.Fatalf("builds %v, want one in %s", builds, checkout)
	}
	b, err := r.Bridge(context.Background(), checkout, "abc+true@17")
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 2 || a.Path == b.Path {
		t.Errorf("an edited checkout was not rebuilt: builds %v, %s vs %s", builds, a.Path, b.Path)
	}
	if _, err := r.Bridge(context.Background(), t.TempDir(), "x"); err == nil {
		t.Error("a directory with no cmd/forge was built")
	}
}

// The marker the hand-off sets names the process it is for, once: a marker
// inherited from an earlier hand-off is replaced, not passed on beside it.
func TestEnv_CarriesAMarkerForTheExecdForgeAlone(t *testing.T) {
	t.Setenv(MarkerEnv, "4242:v0.0.1")
	var markers []string
	for _, kv := range Env("forge_version v1.0.4") {
		if strings.HasPrefix(kv, MarkerEnv+"=") {
			markers = append(markers, kv)
		}
	}
	want := MarkerEnv + "=" + strconv.Itoa(os.Getpid()) + ":forge_version v1.0.4"
	if len(markers) != 1 || markers[0] != want {
		t.Errorf("Env() markers %q, want [%s]", markers, want)
	}
}

// Only a marker addressed to this very process is believed: a bare "1" (what
// every descendant used to inherit), a copy carried to another process, and
// garbage are not.
func TestMarkerFor(t *testing.T) {
	for v, want := range map[string]bool{
		"77:forge_version v1.0.4": true,
		"77:":                     true,
		" 77:x ":                  true,
		"78:forge_version v1.0.4": false,
		"1":                       false,
		"":                        false,
		"x:77":                    false,
		"0:x":                     false,
	} {
		if got := markerFor(v, 77); got != want {
			t.Errorf("markerFor(%q, 77) = %v, want %v", v, got, want)
		}
	}
}

// Reading the marker clears it: nothing this process starts inherits it.
func TestConsumeMarker_ClearsTheEnvironment(t *testing.T) {
	t.Setenv(MarkerEnv, strconv.Itoa(markerOwner())+":forge_version v1.0.4")
	if !consumeMarker() {
		t.Error("a marker addressed to this process was not believed")
	}
	if _, ok := os.LookupEnv(MarkerEnv); ok {
		t.Error("the marker is still in the environment, for every child to inherit")
	}
	t.Setenv(MarkerEnv, "1")
	if consumeMarker() {
		t.Error("an inherited bare marker was believed")
	}
	if _, ok := os.LookupEnv(MarkerEnv); ok {
		t.Error("an untrusted marker was left for children to inherit")
	}
}

func TestRunVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	for _, line := range []string{"forge version " + pin + " (commit ee3bded)", "reliant forge version " + pin} {
		bin := filepath.Join(t.TempDir(), "forge")
		if err := atomicfile.WriteFile(bin, []byte("#!/bin/sh\necho '"+line+"'\necho '  forge build: x'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := RunVersion(context.Background(), bin)
		if err != nil || got != pin {
			t.Errorf("%q: got %q, %v", line, got, err)
		}
	}
}

// A stale .tools/bin/forge inside a project pinned to another forge: run in
// the project, that forge's skew warning resolves its remedy by probing
// .tools/bin/forge — itself — through RunVersion, which launched the next
// probe, and so on. The stand-in below does exactly that whenever it can find
// the project's forge.yaml from its working directory. Probed through
// LocalPinned it must run once and stop, its answer simply not the pin.
func TestLocalPinned_ProbesAStaleLocalForgeOnceWithoutRecursing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	project := t.TempDir()
	if err := atomicfile.WriteFile(filepath.Join(project, "forge.yaml"), []byte("forge_version: "+pin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	launches := filepath.Join(t.TempDir(), "launches")
	local := filepath.Join(project, ".tools", "bin", exeName())
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	// Recurses (bounded, so a regression fails instead of forking forever)
	// while a forge.yaml is visible from its working directory, as the real
	// skew check's remedy does.
	script := `#!/bin/sh
echo x >> "` + launches + `"
[ "$(wc -l < "` + launches + `")" -lt 5 ] || exit 3
d="$PWD"
while :; do
  if [ -f "$d/forge.yaml" ]; then "$0" version >/dev/null 2>&1; break; fi
  [ "$d" = / ] && break
  d="$(dirname "$d")"
done
echo 'forge version v0.0.1-stale'
`
	if err := atomicfile.WriteFile(local, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	r := &Resolver{Version: RunVersion, Same: func(a, b string) bool { return a == b }}
	if got, ok := r.LocalPinned(context.Background(), project, pin); ok {
		t.Errorf("a stale local forge was taken for the pin: %q", got)
	}
	data, err := os.ReadFile(launches)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "\n"); n != 1 {
		t.Errorf("probing the local forge launched it %d times, want 1 (it recursed through its own skew remedy)", n)
	}
}

func TestSafeName(t *testing.T) {
	if got := safeName("v1/../x y+dirty"); strings.ContainsAny(got, "/ +") {
		t.Errorf("unsafe: %q", got)
	}
}

// Builds that differ only in their Go settings are different binaries — the
// e2e corpus runs forge both with GOFLAGS=-trimpath and without, and
// CGO_ENABLED changes the bytes the same way. They get entries of their own,
// so one never stands in for, or replaces, the other. (CGO_ENABLED rather
// than GOFLAGS here: the build is a stub, and goexec's trimpath guard holds
// every GOFLAGS a test sets to carry -trimpath.)
func TestBridge_KeysOnTheBuildSettings(t *testing.T) {
	r, _, _ := newResolver(t)
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, "cmd", "forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	builds := 0
	r.Build = func(_ context.Context, _, out string) error {
		builds++
		return atomicfile.WriteFile(out, []byte("#!/bin/sh\n"), 0o755)
	}
	t.Setenv("CGO_ENABLED", "0")
	plain, err := r.Bridge(context.Background(), checkout, "abc+false@0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CGO_ENABLED", "1")
	cgo, err := r.Bridge(context.Background(), checkout, "abc+false@0")
	if err != nil {
		t.Fatal(err)
	}
	if builds != 2 || plain.Path == cgo.Path {
		t.Fatalf("CGO_ENABLED=1 reused the CGO_ENABLED=0 build: %d build(s), %s vs %s", builds, plain.Path, cgo.Path)
	}
	if !slices.Contains(buildSettings, "GOFLAGS") {
		t.Fatal("GOFLAGS is not part of the build-settings key")
	}
}

// A binary already published is never replaced: a process may be running it.
func TestPublish_NeverReplacesAPublishedBinary(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "forge")
	if err := os.WriteFile(to, []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(to)
	if err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(dir, "forge.new")
	if err := os.WriteFile(from, []byte("second"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publish(from, to); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(to)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(to)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" || !os.SameFile(before, after) {
		t.Fatalf("publish replaced the published binary: now %q", got)
	}

	fresh := filepath.Join(dir, "fresh")
	if err := publish(from, fresh); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(fresh); err != nil || string(got) != "second" {
		t.Fatalf("publish to an empty slot = %q, %v", got, err)
	}
}
