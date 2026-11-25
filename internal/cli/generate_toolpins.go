package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/connectmajor"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/toolcache"
	"github.com/reliant-labs/forge/internal/toolpin"
)

// Codegen plugins write their own version into the files they emit
// (`// 	protoc-gen-go v1.36.12`), and goimports shapes committed bytes too, so
// whichever binary happens to be first on PATH silently decides whether a
// regenerate reproduces the committed tree. The project's go.mod is the single
// source of truth: before any codegen runs, each requiredProtoTools binary on
// PATH is checked against the version resolveToolVersion reports (the same
// function `forge tools install` uses), and a mismatched or missing one is
// replaced — for forge's child processes only — by a copy installed into a
// forge-managed cache. The user's GOBIN and shell PATH are never touched.

// toolBinVersion reports the version of module that a Go binary was built
// with, per `go version -m`. The module appears on the `mod` line when the
// binary was installed as `go install pkg@vX`, or on a `dep` line when it was
// built inside another module's graph; both are honoured. A `=>` replacement
// line, when present, is not followed: the pinned version is what matters.
func toolBinVersion(ctx context.Context, binPath, module string) (string, bool) {
	out, err := goVersionM(ctx, binPath)
	if err != nil {
		return "", false
	}
	return parseBuildInfoVersion(out, module)
}

// goVersionM returns `go version -m binPath`. A seam for tests.
var goVersionM = func(ctx context.Context, binPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "version", "-m", binPath)
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	return string(out), err
}

// parseBuildInfoVersion extracts module's version from `go version -m` output.
func parseBuildInfoVersion(out, module string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || (f[0] != "mod" && f[0] != "dep") || f[1] != module {
			continue
		}
		return f[2], true
	}
	return "", false
}

// toolMatches reports whether a binary at path satisfies want. A binary whose
// build info cannot be read (not a Go binary, stripped) does not match.
func toolMatches(ctx context.Context, binPath string, t protoTool, want string) (have string, ok bool) {
	have, found := toolBinVersion(ctx, binPath, t.VersionModule)
	if !found {
		return "unknown", false
	}
	return have, have == want
}

// toolCacheDir is where a tool pinned at version is installed: its entry in
// the pinned kind of forge's shared tool cache (internal/toolcache), which
// FORGE_TOOLS_CACHE relocates with every other tool forge installs.
//
// The entry is keyed <binary>@<version>, and that key already separates the
// two protoc-gen-connect-go builds that share a binary name: Go's semantic
// import versioning makes a module path ending /vN (N ≥ 2) carry only vN.x.y
// versions, so connect v1's plugin is always protoc-gen-connect-go@v1.* and
// v2's (connectrpc.com/connect/v2) protoc-gen-connect-go@v2.*. A v1 project
// is never handed v2's binary, nor the reverse — and installPinnedTool's
// ready check verifies the entry's build info names t.VersionModule, so even
// a hand-planted binary of the other major in an entry is rebuilt, not
// served. Keying on the version keeps every existing v1 entry valid.
func toolCacheDir(t protoTool, version string) (string, error) {
	kind, err := toolcache.KindDir(toolcache.KindPinned)
	if err != nil {
		return "", err
	}
	return filepath.Join(kind, t.Binary+"@"+version), nil
}

// codegenTools is the outcome of pinning: prepend Dirs to PATH for child
// processes, and resolve binaries through Path.
type codegenTools struct {
	Dirs  []string
	paths map[string]string
	// major is the connect-go major protoc-gen-connect-go was pinned for.
	// A cached resolution for another major is stale (ensureCodegenTools);
	// zero accepts any.
	major connectmajor.Major
}

// Path returns the absolute path of binary if it was resolved into the cache.
func (c *codegenTools) Path(binary string) (string, bool) {
	if c == nil {
		return "", false
	}
	p, ok := c.paths[binary]
	return p, ok
}

// Env returns the environment for a codegen child process: the scrubbed base
// environment with the pinned tool dirs prepended to PATH.
func (c *codegenTools) Env(extra ...string) []string {
	return pathPrepended(goexec.Env(extra...), c.dirs())
}

func (c *codegenTools) dirs() []string {
	if c == nil {
		return nil
	}
	return c.Dirs
}

func pathPrepended(env, dirs []string) []string {
	if len(dirs) == 0 {
		return env
	}
	prefix := strings.Join(dirs, string(os.PathListSeparator))
	for i, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + prefix + string(os.PathListSeparator) + v
			return env
		}
	}
	return append(env, "PATH="+prefix)
}

var (
	pinMu    sync.Mutex
	pinCache = map[string]*codegenTools{}
	// pinMajors is the connect-go major a `forge generate` run read once
	// for a project (bindCodegenToolsMajor), keyed by its absolute path.
	pinMajors = map[string]connectmajor.Major{}
)

// bindCodegenToolsMajor makes major the connect-go major every later codegen
// tool resolution for projectDir pins protoc-gen-connect-go for, in place of
// reading go.mod again. `forge generate` binds the major it read once for the
// run, so the plugin buf runs agrees with every file the run renders even if
// go.mod changes mid-run (a tidy, a user hook). It is a binding rather than a
// parameter because some tool users run outside any step's reach — the
// on-demand descriptor build codegen.LoadDescriptor triggers is a
// process-wide hook. A later run's binding replaces it; zero removes it.
func bindCodegenToolsMajor(projectDir string, major connectmajor.Major) {
	abs := absOr(projectDir)
	pinMu.Lock()
	defer pinMu.Unlock()
	if major == 0 {
		delete(pinMajors, abs)
		return
	}
	pinMajors[abs] = major
}

// codegenToolsMajor is the connect-go major codegen tools are pinned for in
// projectDir: the bound run major, else the one its go.mod selects.
func codegenToolsMajor(projectDir string) connectmajor.Major {
	abs := absOr(projectDir)
	pinMu.Lock()
	m := pinMajors[abs]
	pinMu.Unlock()
	if m != 0 {
		return m
	}
	return connectmajor.DetectOrDefault(abs)
}

func absOr(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// ensureCodegenTools makes sure every codegen tool forge runs matches the
// version projectDir's go.mod pins. It is memoised per (project, PATH) so the
// buf and goimports steps share one resolution and one message; a resolution
// made for the other connect-go major (the run bound another one since) is
// redone.
func ensureCodegenTools(ctx context.Context, projectDir string) (*codegenTools, error) {
	abs := absOr(projectDir)
	major := codegenToolsMajor(abs)
	key := abs + "\x00" + os.Getenv("PATH")
	pinMu.Lock()
	defer pinMu.Unlock()
	if c, ok := pinCache[key]; ok && (c.major == 0 || c.major == major) {
		return c, nil
	}
	c, err := pinCodegenTools(ctx, abs, major)
	if err != nil {
		return nil, err
	}
	pinCache[key] = c
	return c, nil
}

func pinCodegenTools(ctx context.Context, projectDir string, major connectmajor.Major) (*codegenTools, error) {
	res := &codegenTools{paths: map[string]string{}, major: major}
	if _, err := exec.LookPath("go"); err != nil {
		return res, nil // cannot resolve versions without the go tool
	}
	// The project's own set: protoc-gen-connect-go of the connect-go major
	// the run reads (codegenToolsMajor). toolMatches checks the binary's
	// build info against that major's module, so a PATH binary of the other
	// major never satisfies the pin, whatever its version.
	for _, t := range requiredProtoToolsFor(major) {
		want := resolveToolVersion(ctx, projectDir, t, "")
		pathBin, lookErr := exec.LookPath(t.Binary)
		if want == "latest" {
			if want = unresolvedToolPin(ctx, projectDir, t, pathBin, lookErr == nil); want == "" {
				continue
			}
		}
		have := "not found on PATH"
		if lookErr == nil {
			var ok bool
			if have, ok = toolMatches(ctx, pathBin, t, want); ok {
				continue
			}
			have = "PATH has " + have
		}

		dir, err := toolCacheDir(t, want)
		if err != nil {
			return nil, err
		}
		bin := filepath.Join(dir, t.Binary+exeSuffix())
		built, err := installPinnedTool(ctx, t, want, dir, have, func() {
			fmt.Fprintf(os.Stderr, "📌 installing %s %s, which go.mod pins (%s), into forge's tool cache (one time) → %s\n",
				t.Binary, want, have, dir)
		})
		if err != nil {
			return nil, err
		}
		if built {
			if got, ok := toolMatches(ctx, bin, t, want); !ok {
				return nil, pinError(t, want, have, fmt.Errorf("installed binary reports %s", got))
			}
		}
		pinNotice("📌 %s resolved to %s (%s, go.mod pins %s) → %s\n", t.Binary, want, have, want, dir)
		res.Dirs = append(res.Dirs, dir)
		res.paths[t.Binary] = bin
	}
	if err := pinBuf(ctx, projectDir, res); err != nil {
		return nil, err
	}
	return res, nil
}

// unresolvedToolPin is the version to pin t at when the module graph could
// not name one (resolveToolVersion said "latest"), or "" to run whatever PATH
// holds.
//
// For every tool but protoc-gen-connect-go "" is right: the module is not in
// the graph, so any build will do. protoc-gen-connect-go is the one tool whose
// name two incompatible builds share — connect-go v1's and v2's — and each
// emits stubs only its own runtime compiles against. "Latest" also does not
// mean "not in the graph" for it: `go list -m` fails on a go.mod that merely
// needs a tidy ("updates to go.mod needed"), which is what an appended
// require or replace leaves behind. Running PATH's build then generated v2
// stubs (Register<Svc>Handler) into a v1 project whose handlers call
// New<Svc>Handler. So a PATH build counts only when it is of t's major (at
// any version); otherwise the pin is the version go.mod's own require line
// names, read from the file rather than the module graph.
func unresolvedToolPin(ctx context.Context, projectDir string, t protoTool, pathBin string, onPath bool) string {
	if _, ok := connectmajor.IsRuntimeImport(t.VersionModule); !ok {
		return "" // module not in the graph: any binary on PATH is fine
	}
	if onPath {
		if _, ofMajor := toolBinVersion(ctx, pathBin, t.VersionModule); ofMajor {
			return ""
		}
	}
	return goModRequireVersion(projectDir, t.VersionModule)
}

// goModRequireVersion is the version projectDir's go.mod requires module at
// (direct or indirect), read from the file itself, or "" when it names none
// or cannot be read. It answers when `go list -m` cannot: a go.mod that
// needs a tidy.
func goModRequireVersion(projectDir, module string) string {
	path := filepath.Join(projectDir, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	f, err := modfile.ParseLax(path, data, nil)
	if err != nil {
		return ""
	}
	// Go selects the highest required version, even when an untidy file
	// repeats the module in separate require directives.
	var version string
	for _, r := range f.Require {
		if r.Mod.Path == module && semver.Compare(r.Mod.Version, version) > 0 {
			version = r.Mod.Version
		}
	}
	return version
}

// pinNotice reports which pinned build a codegen tool resolved to. It is
// routine (generate_verbosity.go): with the pin already in forge's tool cache
// the line is identical on every run, and nothing is wrong — forge's own steps
// run the pin whatever PATH holds, so PATH lagging go.mod needs no action.
// Printing it on every `forge generate` and `forge scaffold` taught readers to
// skim past the block it sat in. So it prints under `forge generate
// --verbose`, for debugging which binary a step ran. What does need saying
// prints regardless: a pin being installed (a one-time build, announced by
// installPinnedTool's caller and by cigate's resolver) and a pin that cannot
// be installed (pinError).
func pinNotice(format string, args ...any) {
	if generateVerbose {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

// pinBuf runs buf at the version the project's go.mod declares
// (internal/toolpin), resolved exactly as `forge ci gate` resolves it — the
// buf on PATH when it already is that version, else a build in the gate's
// tool cache. A project that declares no buf keeps whichever buf PATH has:
// the gate still holds that one to forge's pin. With no buf on PATH at all
// (a fresh machine's first `forge project new`, whose go.mod is not tidied
// yet) it builds forge's pin, so a new user never needs buf preinstalled.
func pinBuf(ctx context.Context, projectDir string, res *codegenTools) error {
	b := toolpin.BufFor(projectDir)
	if !b.Declared() {
		if _, err := exec.LookPath("buf"); err == nil {
			return nil
		}
	}
	dir, err := cigate.NewResolver(projectDir, os.Stderr).Ensure(ctx, cigate.BufTool(projectDir))
	if err != nil {
		return fmt.Errorf("buf %s, which go.mod pins (%s), could not be installed: %w\n"+
			"  fix: run `%s tools install buf`, or put a buf %s on PATH", b.Version, toolpin.BufModule, err, Name(), b.Bare())
	}
	if dir == "" {
		return nil // the buf on PATH is the pin
	}
	pinNotice("📌 buf resolved to %s (go.mod pins %s) → %s\n", b.Version, b.Version, dir)
	res.Dirs = append(res.Dirs, dir)
	res.paths["buf"] = filepath.Join(dir, "buf"+exeSuffix())
	return nil
}

// installPinnedTool resolves t@version at dir, an entry of forge's shared
// tool cache, building it there when the binary is missing or reports another
// version, and says whether it built. Many forge processes want the same pin
// at once — every fresh project's first generate pins the same protoc-gen-go
// — so the build lands by renaming a complete staging directory
// (internal/toolcache), and no reader ever sees a half-written binary (`go
// version -m` on one failed the pin with "installed binary reports unknown":
// TestE2EFixtureCorpusKalshiShaped). announce, when set, runs only when this
// process builds.
func installPinnedTool(ctx context.Context, t protoTool, version, dir, have string, announce func()) (built bool, err error) {
	ready := func(dir string) bool {
		_, ok := toolMatches(ctx, filepath.Join(dir, t.Binary+exeSuffix()), t, version)
		return ok
	}
	_, err = toolcache.Ensure(ctx, filepath.Dir(dir), filepath.Base(dir), ready, func(ctx context.Context, stage string) error {
		built = true
		if announce != nil {
			announce()
		}
		spec := t.Module + "@" + version
		if err := goInstallPinned(ctx, spec, stage); err != nil {
			return fmt.Errorf("go install %s: %w", spec, err)
		}
		return nil
	})
	if err != nil {
		return built, pinError(t, version, have, err)
	}
	return built, nil
}

// goInstallPinned runs `go install spec` with GOBIN=gobin. A seam for tests.
var goInstallPinned = func(ctx context.Context, spec, gobin string) error {
	cmd := goexec.Graceful(exec.CommandContext(ctx, "go", "install", spec))
	cmd.Dir = gobin
	// GOWORK=off: `go install pkg@version` must not be bent by a go.work
	// found above the cache dir.
	cmd.Env = goexec.Env("GOBIN="+gobin, "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func pinError(t protoTool, pinned, have string, cause error) error {
	return fmt.Errorf("%s does not match the version go.mod pins and could not be fixed automatically: %s, go.mod pins %s %s: %w\n"+
		"  fix: run `forge tools install --force` (installs %s@%s onto PATH), or put a %s build on PATH",
		t.Binary, have, t.VersionModule, pinned, cause, t.Module, pinned, pinned)
}

func exeSuffix() string {
	if os.PathSeparator == '\\' {
		return ".exe"
	}
	return ""
}

// withPinnedTools sets cmd.Env so cmd resolves codegen plugins from the
// go.mod-pinned set (see generate_toolpins.go).
func withPinnedTools(cmd *exec.Cmd, projectDir string) error {
	c, err := ensureCodegenTools(context.Background(), projectDir)
	if err != nil {
		return err
	}
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = pathPrepended(append([]string(nil), base...), c.dirs())
	// exec.Command resolved the program against THIS process's PATH when cmd
	// was built (and recorded a not-found in cmd.Err): a pinned buf replaces
	// that resolution, not just the child's PATH.
	if len(cmd.Args) > 0 && cmd.Args[0] == "buf" {
		if p, ok := c.Path("buf"); ok {
			cmd.Path, cmd.Err = p, nil
		}
	}
	return nil
}
