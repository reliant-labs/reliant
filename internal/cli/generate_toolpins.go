package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/goexec"
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
	cmd := exec.CommandContext(ctx, "go", "version", "-m", binPath)
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return parseBuildInfoVersion(string(out), module)
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

// toolCacheDir is where a tool pinned at module@version is installed.
func toolCacheDir(t protoTool, version string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}
	return filepath.Join(base, "forge", "tools", t.VersionModule+"@"+version), nil
}

// codegenTools is the outcome of pinning: prepend Dirs to PATH for child
// processes, and resolve binaries through Path.
type codegenTools struct {
	Dirs  []string
	paths map[string]string
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
)

// ensureCodegenTools makes sure every codegen tool forge runs matches the
// version projectDir's go.mod pins. It is memoised per (project, PATH) so the
// buf and goimports steps share one resolution and one message.
func ensureCodegenTools(ctx context.Context, projectDir string) (*codegenTools, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	key := abs + "\x00" + os.Getenv("PATH")
	pinMu.Lock()
	defer pinMu.Unlock()
	if c, ok := pinCache[key]; ok {
		return c, nil
	}
	c, err := pinCodegenTools(ctx, abs)
	if err != nil {
		return nil, err
	}
	pinCache[key] = c
	return c, nil
}

func pinCodegenTools(ctx context.Context, projectDir string) (*codegenTools, error) {
	res := &codegenTools{paths: map[string]string{}}
	if _, err := exec.LookPath("go"); err != nil {
		return res, nil // cannot resolve versions without the go tool
	}
	for _, t := range requiredProtoTools {
		want := resolveToolVersion(ctx, projectDir, t, "")
		if want == "latest" {
			continue // module not in the graph: any binary on PATH is fine
		}
		pathBin, lookErr := exec.LookPath(t.Binary)
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
		if _, ok := toolMatches(ctx, bin, t, want); !ok {
			if err := installPinnedTool(ctx, t, want, dir, have); err != nil {
				return nil, err
			}
			if got, ok := toolMatches(ctx, bin, t, want); !ok {
				return nil, pinError(t, want, have, fmt.Errorf("installed binary reports %s", got))
			}
		}
		fmt.Printf("📌 %s resolved to %s (%s, go.mod pins %s) → %s\n", t.Binary, want, have, want, dir)
		res.Dirs = append(res.Dirs, dir)
		res.paths[t.Binary] = bin
	}
	if err := pinBuf(ctx, projectDir, res); err != nil {
		return nil, err
	}
	return res, nil
}

// pinBuf runs buf at the version the project's go.mod declares
// (internal/toolpin), resolved exactly as `forge ci gate` resolves it — the
// buf on PATH when it already is that version, else a build in the gate's
// tool cache. A project that declares no buf keeps whichever buf PATH has:
// the gate still holds that one to forge's pin.
func pinBuf(ctx context.Context, projectDir string, res *codegenTools) error {
	b := toolpin.BufFor(projectDir)
	if !b.Declared() {
		return nil
	}
	dir, err := cigate.NewResolver(projectDir, os.Stdout).Ensure(ctx, cigate.BufTool(projectDir))
	if err != nil {
		return fmt.Errorf("buf %s, which go.mod pins (%s), could not be installed: %w\n"+
			"  fix: run `%s tools install buf`, or put a buf %s on PATH", b.Version, toolpin.BufModule, err, Name(), b.Bare())
	}
	if dir == "" {
		return nil // the buf on PATH is the pin
	}
	fmt.Printf("📌 buf resolved to %s (go.mod pins %s) → %s\n", b.Version, b.Version, dir)
	res.Dirs = append(res.Dirs, dir)
	res.paths["buf"] = filepath.Join(dir, "buf"+exeSuffix())
	return nil
}

// installPinnedTool installs t@version into dir, atomically: `go install`
// writes into a private temp dir beside it, and the finished binary is
// renamed into place. Parallel forge processes pin the same tool into the
// same cache dir (every fresh project's first generate does), and `go
// install` straight into dir let one process read a binary another was still
// writing: `go version -m` failed on the half-written file, the pin reported
// "installed binary reports unknown", and the project's bootstrap generate
// failed (TestE2EFixtureCorpusKalshiShaped, TestE2EOneCommandPerRoot in CI).
func installPinnedTool(ctx context.Context, t protoTool, version, dir, have string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return pinError(t, version, have, err)
	}
	tmp, err := os.MkdirTemp(dir, ".install-")
	if err != nil {
		return pinError(t, version, have, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	spec := t.Module + "@" + version
	if err := goInstallPinned(ctx, spec, tmp); err != nil {
		return pinError(t, version, have, fmt.Errorf("go install %s: %w", spec, err))
	}
	name := t.Binary + exeSuffix()
	if err := os.Rename(filepath.Join(tmp, name), filepath.Join(dir, name)); err != nil {
		// Another process published the same version first: same bytes.
		if st, serr := os.Stat(filepath.Join(dir, name)); serr != nil || st.IsDir() {
			return pinError(t, version, have, err)
		}
	}
	return nil
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
		t.Binary, have, t.VersionModule, pinned, cause, t.Binary, pinned, pinned)
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
