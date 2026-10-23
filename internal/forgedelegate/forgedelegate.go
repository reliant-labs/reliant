// Package forgedelegate finds, and if need be builds, the forge a project pins,
// so a forge of another version can hand a command to it.
//
// WHY. forge's templates, KCL schemas and analyzers ship inside the binary, so
// a command run by the wrong forge rewrites a project with the wrong output or
// fails with errors that blame the project. The forge on PATH is routinely
// not the project's: a Reliant workspace bundles one at image-build time, and
// every project pins its own. Refusing and printing a `go run …@<pin>` line
// made the version skew a wall that every agent climbed by hand. Delegating
// removes it: the project says which forge it is, and that forge runs.
//
// WHERE THE PINNED FORGE COMES FROM, cheapest first:
//
//  1. the project's own .tools/bin/forge, when it reports the pinned version
//     (`task tools` / scripts/install-tools.sh installs it there);
//  2. the content-addressed cache, <user cache>/forge/pinned/<version>/…
//     A pseudo-version names one commit, so an entry never goes stale;
//  3. a build: `go install github.com/reliant-labs/forge/cmd/forge@<version>`
//     into a temp dir that is renamed into the cache, so a build that dies
//     never leaves a half-written binary where step 2 would trust it.
//
// A project whose go.mod or go.work points forge at a local checkout (the
// dev link) is a different case: the forge it runs is that checkout, built
// where it stands (Bridge).
//
// This package only resolves and executes. Whether to delegate is the
// caller's decision (internal/cli/forge_pin_delegate.go).
package forgedelegate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"
)

// MarkerEnv is set in the environment of a forge that was delegated to. It
// ends the hand-off: the parent has already decided this is the right forge,
// and a child that second-guessed it (its own version string can differ in
// spelling from the pin) would delegate again, forever.
const MarkerEnv = "FORGE_VERSION_DELEGATED"

// Delegated reports whether this process was started by a delegation.
func Delegated() bool { return strings.TrimSpace(os.Getenv(MarkerEnv)) != "" }

// modulePath is forge's module; cmdPackage its command.
const (
	modulePath = "github.com/reliant-labs/forge"
	cmdPackage = modulePath + "/cmd/forge"
)

// keepVersions is how many other pinned builds the cache keeps beside the one
// just used: each is a ~100 MB binary, and projects move their pins often.
const keepVersions = 4

// Result is a forge ready to exec.
type Result struct {
	// Path is the executable.
	Path string
	// Source says where it came from, for the one-line notice.
	Source string
}

// CacheDirEnv relocates the builds the resolver keeps (default
// <user cache dir>/forge/pinned), for read-only home directories and test
// isolation.
const CacheDirEnv = "FORGE_PINNED_CACHE"

// Resolver finds or builds forge binaries. The zero value is usable; the func
// fields are seams for tests and default to the real thing.
type Resolver struct {
	// Log receives the notice that a build is starting (a minute of silence
	// reads as a hang).
	Log io.Writer

	// Version runs a forge binary and returns the version it reports.
	Version func(ctx context.Context, path string) (string, error)
	// Same reports whether version got is the forge want names.
	Same func(want, got string) bool
	// Install runs `go install pkg` with GOBIN=gobin.
	Install func(ctx context.Context, pkg, gobin string) error
	// Build runs `go build` of cmd/forge in checkout dir, writing out.
	Build func(ctx context.Context, dir, out string) error
}

func (r *Resolver) logw() io.Writer {
	if r.Log != nil {
		return r.Log
	}
	return io.Discard
}

func (r *Resolver) cacheDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(CacheDirEnv)); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no user cache directory to keep the pinned forge in (set %s): %w", CacheDirEnv, err)
	}
	return filepath.Join(base, "forge", "pinned"), nil
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "forge.exe"
	}
	return "forge"
}

// LocalPinned reports the project's own .tools/bin/forge when it exists and
// reports the pinned version. It runs the binary (`forge version`), never
// builds anything.
func (r *Resolver) LocalPinned(ctx context.Context, projectRoot, pin string) (string, bool) {
	if r.Version == nil || r.Same == nil {
		return "", false
	}
	local := filepath.Join(projectRoot, ".tools", "bin", exeName())
	if st, err := os.Stat(local); err != nil || st.IsDir() {
		return "", false
	}
	if got, err := r.Version(ctx, local); err != nil || !r.Same(pin, got) {
		return "", false
	}
	return local, true
}

// Pinned returns the forge named by pin (forge.yaml's forge_version), for the
// project at projectRoot.
func (r *Resolver) Pinned(ctx context.Context, projectRoot, pin string) (Result, error) {
	if strings.TrimSpace(pin) == "" {
		return Result{}, errors.New("forge.yaml pins no forge_version")
	}
	if local, ok := r.LocalPinned(ctx, projectRoot, pin); ok {
		return Result{Path: local, Source: "the project's .tools/bin/forge"}, nil
	}

	dir, err := r.cacheDir()
	if err != nil {
		return Result{}, err
	}
	entry := filepath.Join(dir, safeName(pin), runtime.GOOS+"-"+runtime.GOARCH)
	bin := filepath.Join(entry, exeName())
	if st, err := os.Stat(bin); err == nil && !st.IsDir() {
		touch(entry)
		return Result{Path: bin, Source: "the cached build of " + pin}, nil
	}

	if err := os.MkdirAll(entry, 0o755); err != nil {
		return Result{}, err
	}
	tmp, err := os.MkdirTemp(entry, ".build-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	fmt.Fprintf(r.logw(), "forge: building forge %s (one time; cached in %s)\n", pin, dir)
	install := r.Install
	if install == nil {
		install = goInstall
	}
	if err := install(ctx, cmdPackage+"@"+pin, tmp); err != nil {
		return Result{}, fmt.Errorf("could not build forge %s (offline, or the version is not published?): %w", pin, err)
	}
	if err := publish(filepath.Join(tmp, exeName()), bin); err != nil {
		return Result{}, err
	}
	touch(entry)
	r.prune(dir, safeName(pin))
	return Result{Path: bin, Source: "forge " + pin + " (just built)"}, nil
}

// Bridge returns a forge built from the checkout at dir, the forge a project
// bridged to it (go.work `use`, or a directory `replace`) compiles against.
// stamp must change whenever the checkout's contents do (HEAD plus its
// uncommitted edits), so an unchanged checkout is built once. The build's own
// settings join the key (buildSettingsKey): two callers whose GOFLAGS differ
// (one with -trimpath, one without) build different bytes from the same
// checkout, and must not share — or replace — one cached binary.
func (r *Resolver) Bridge(ctx context.Context, dir, stamp string) (Result, error) {
	if st, err := os.Stat(filepath.Join(dir, "cmd", "forge")); err != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("%s has no cmd/forge to build", dir)
	}
	cache, err := r.cacheDir()
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256([]byte(dir + "\x00" + stamp + "\x00" + buildSettingsKey(goexec.Env(buildEnvExtra()...))))
	entry := filepath.Join(cache, "checkout-"+hex.EncodeToString(sum[:8]), runtime.GOOS+"-"+runtime.GOARCH)
	bin := filepath.Join(entry, exeName())
	if st, err := os.Stat(bin); err == nil && !st.IsDir() {
		touch(entry)
		return Result{Path: bin, Source: "the build of checkout " + dir}, nil
	}
	if err := os.MkdirAll(entry, 0o755); err != nil {
		return Result{}, err
	}
	tmp, err := os.MkdirTemp(entry, ".build-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	fmt.Fprintf(r.logw(), "forge: building forge from the checkout %s (cached until it changes)\n", dir)
	build := r.Build
	if build == nil {
		build = goBuild
	}
	out := filepath.Join(tmp, exeName())
	if err := build(ctx, dir, out); err != nil {
		return Result{}, fmt.Errorf("could not build forge from %s: %w", dir, err)
	}
	if err := publish(out, bin); err != nil {
		return Result{}, err
	}
	touch(entry)
	r.prune(cache, filepath.Base(filepath.Dir(entry)))
	return Result{Path: bin, Source: "forge built from checkout " + dir}, nil
}

// buildSettings are the environment variables that change the bytes `go
// build` / `go install` produce from the same source. GOOS and GOARCH are
// here as well as in the entry path: the path names the running forge's
// platform, the variables the one being built.
var buildSettings = []string{"GOFLAGS", "GOEXPERIMENT", "CGO_ENABLED", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOTOOLCHAIN"}

// buildSettingsKey is env's values for buildSettings, the last assignment
// winning as it does for exec.
func buildSettingsKey(env []string) string {
	vals := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vals[k] = v
		}
	}
	var b strings.Builder
	for _, k := range buildSettings {
		b.WriteString(k + "=" + vals[k] + "\x00")
	}
	return b.String()
}

// publish moves a finished build into the cache. A binary already there wins
// and is never replaced: another process may be running it right now, and a
// generate run that fingerprints its own executable before and after would
// see it change underneath it. Two processes that build the same entry race;
// the loser's build is discarded with its temp directory.
func publish(from, to string) error {
	if _, err := os.Stat(from); err != nil {
		return fmt.Errorf("the build produced no %s: %w", filepath.Base(from), err)
	}
	// A hard link fails when the target exists, which is the no-replace
	// rename os.Rename cannot express.
	lerr := os.Link(from, to)
	if lerr == nil {
		return nil
	}
	if st, serr := os.Stat(to); serr == nil && !st.IsDir() {
		return nil
	}
	// No hard links on this filesystem: fall back to a rename, which can
	// replace only a binary that appeared since the Stat just above.
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("publish %s: %w (link: %v)", to, err, lerr)
	}
	return nil
}

// prune removes the least recently used entries beyond keepVersions, never
// the one named keep.
func (r *Resolver) prune(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type ent struct {
		name string
		mod  time.Time
	}
	var old []ent
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		if info, err := e.Info(); err == nil {
			old = append(old, ent{e.Name(), info.ModTime()})
		}
	}
	if len(old) <= keepVersions {
		return
	}
	sort.Slice(old, func(i, j int) bool { return old[i].mod.After(old[j].mod) })
	for _, e := range old[keepVersions:] {
		_ = os.RemoveAll(filepath.Join(dir, e.name))
	}
}

// touch marks an entry used now, which is what prune ranks by. Failing is
// harmless: the entry just looks older than it is.
func touch(entry string) {
	now := time.Now()
	_ = os.Chtimes(entry, now, now)
	_ = os.Chtimes(filepath.Dir(entry), now, now)
}

// safeName makes a version a single path element.
func safeName(v string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, v)
}

// buildEnvExtra is what building forge adds to goexec.Env. It is the recipe
// scripts/install-tools.sh uses for the project's own pin: forge's modules are
// fetched straight from GitHub because the module proxy lags a fresh commit by
// ~20 minutes, and the checksum database cannot know a commit that new.
// GOWORK=off keeps a go.work above the temp directory from being consulted.
func buildEnvExtra(more ...string) []string {
	extra := []string{"GOWORK=off"}
	if os.Getenv("GOPRIVATE") == "" {
		extra = append(extra, "GOPRIVATE=github.com/reliant-labs/*")
	}
	if os.Getenv("GONOSUMDB") == "" {
		extra = append(extra, "GONOSUMDB=github.com/reliant-labs/*")
	}
	return append(extra, more...)
}

func goInstall(ctx context.Context, pkg, gobin string) error {
	cwd, err := os.MkdirTemp("", "forge-install-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(cwd) }()
	cmd := exec.CommandContext(ctx, "go", "install", pkg)
	cmd.Dir = cwd
	cmd.Env = goexec.Env(buildEnvExtra("GOBIN=" + gobin)...)
	return runCaptured(cmd)
}

func goBuild(ctx context.Context, dir, out string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=true",
		"-ldflags", "-X "+modulePath+"/internal/buildinfo.DevForgeRoot="+dir,
		"-o", out, "./cmd/forge")
	cmd.Dir = dir
	cmd.Env = goexec.Env(buildEnvExtra()...)
	return runCaptured(cmd)
}

// runCaptured runs cmd, keeping its output for the error and not for the
// terminal: the notice already said what is happening, and a failed build's
// last lines are what the reader needs.
func runCaptured(cmd *exec.Cmd) error {
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w\n%s", err, tail(buf.String(), 12))
	}
	return nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// RunVersion is Resolver.Version's default: `<path> version`, first line,
// the token after "version".
func RunVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Env = Env()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(first)
	for i, f := range fields {
		if f == "version" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("unrecognised version output %q", first)
}

// Env is the environment to give the delegated forge: ours, plus the marker.
func Env() []string { return append(os.Environ(), MarkerEnv+"=1") }
