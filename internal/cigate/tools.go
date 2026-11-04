package cigate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/toolcache"
)

// Tool is a binary the gate runs at a pinned version. CI installs these at the
// same versions (the scaffolded ci.yml renders the same templates constants),
// so a developer's `task ci` and the workflow judge the same code with the
// same linters. A tool on PATH at another version is not used: the pin is
// installed into forge's tool cache and put ahead of it for that command only.
// The developer's GOBIN and shell PATH are never touched.
type Tool struct {
	Binary string
	// Module is the `go install` package path.
	Module string
	// BuildModule is the module path the binary's build info records, when
	// that differs from Module's directory (golangci-lint's /cmd/ suffix).
	BuildModule string
	// Version is the pin, with the leading "v".
	Version string
	// MatchGo requires the binary to have been built with a Go at least as
	// new as the project's go directive: golangci-lint cannot analyze Go newer
	// than the Go it was built with, so a PATH copy at the right VERSION but
	// built with an older Go is the wrong binary.
	MatchGo bool
	// VersionArgs and VersionRE read the version from the binary's own output
	// when its build info does not carry one (release builds record
	// "(devel)").
	VersionArgs []string
	VersionRE   *regexp.Regexp
}

// Resolver turns a Tool into a directory to put first on PATH.
type Resolver struct {
	// CacheRoot is where pinned tools are installed.
	CacheRoot string
	// ProjectDir selects the Go toolchain tools are built with: the one `go`
	// picks for the project, so a linter is never built with a Go older than
	// the module it must lint.
	ProjectDir string
	// Note receives one line per install, so a first-run build is explained.
	Note io.Writer

	LookPath func(string) (string, error)
	// Probe reports the version of the binary at path, or "".
	Probe func(path string, t Tool) string
	// Install builds t into dir.
	Install func(ctx context.Context, t Tool, dir, toolchain string) error
	// Toolchain reports the Go release `go` selects for ProjectDir.
	Toolchain func(ctx context.Context) string
	// ModuleGo is the project's go directive ("1.27.0"), "" when unknown.
	ModuleGo string
	// BuiltWith reports the Go release a binary was built with ("go1.27.2").
	BuiltWith func(path string) string

	mu   sync.Mutex
	memo map[string]memoEntry
}

type memoEntry struct {
	dir string
	err error
}

// NewResolver is the production resolver.
func NewResolver(projectDir string, note io.Writer) *Resolver {
	r := &Resolver{
		ProjectDir: projectDir,
		Note:       note,
		LookPath:   exec.LookPath,
		Probe:      probeVersion,
		Install:    goInstall,
		BuiltWith:  builtWithGo,
		ModuleGo:   moduleGoDirective(projectDir),
	}
	r.CacheRoot = toolsCacheRoot()
	r.Toolchain = func(ctx context.Context) string { return moduleToolchain(ctx, projectDir) }
	return r
}

// toolsCacheRoot is the gate's kind directory in forge's shared tool cache
// (internal/toolcache), which FORGE_TOOLS_CACHE relocates along with every
// other tool forge installs.
func toolsCacheRoot() string {
	dir, err := toolcache.KindDir(toolcache.KindGate)
	if err != nil {
		return filepath.Join(os.TempDir(), "forge", "tools", toolcache.KindGate)
	}
	return dir
}

// Ensure returns the directory to prepend to PATH so t resolves at its pin, or
// "" when the binary on PATH already is the pin.
func (r *Resolver) Ensure(ctx context.Context, t Tool) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := t.Binary + "@" + t.Version
	if e, ok := r.memo[key]; ok {
		return e.dir, e.err
	}
	dir, err := r.ensure(ctx, t)
	if r.memo == nil {
		r.memo = map[string]memoEntry{}
	}
	r.memo[key] = memoEntry{dir, err}
	return dir, err
}

func (r *Resolver) ensure(ctx context.Context, t Tool) (string, error) {
	have := "not on PATH"
	if p, err := r.LookPath(t.Binary); err == nil {
		got := r.Probe(p, t)
		if sameVersion(got, t.Version) && r.goOK(p, t) {
			return "", nil
		}
		if got == "" {
			got = "unknown version"
		}
		have = "PATH has " + got
		if sameVersion(got, t.Version) {
			have += ", built with " + r.BuiltWith(p)
		}
	}

	tc := ""
	if r.Toolchain != nil {
		tc = r.Toolchain(ctx)
	}
	// Keyed by toolchain as well as version: a linter built with an older Go
	// cannot analyze newer code, so one build must not stand in for another.
	name := t.Binary + "@" + t.Version
	if tc != "" {
		name += "+" + tc
	}
	dir := filepath.Join(r.CacheRoot, name)
	bin := filepath.Join(dir, t.Binary+exeSuffix())
	ready := func(dir string) bool {
		bin := filepath.Join(dir, t.Binary+exeSuffix())
		return sameVersion(r.Probe(bin, t), t.Version) && r.goOK(bin, t)
	}
	built := false
	// The install lands by one rename, so a concurrent gate that wants the
	// same pin never runs a half-written binary (internal/toolcache).
	if _, err := toolcache.Ensure(ctx, r.CacheRoot, name, ready, func(ctx context.Context, stage string) error {
		built = true
		r.note("pin: %s must be %s (%s) — installing into %s (one time)…", t.Binary, t.Version, have, dir)
		return r.Install(ctx, t, stage, tc)
	}); err != nil {
		return "", err
	}
	if built {
		if got := r.Probe(bin, t); !sameVersion(got, t.Version) {
			return "", fmt.Errorf("installed %s reports version %q, want %s", t.Binary, got, t.Version)
		}
	}
	return dir, nil
}

// goOK reports whether the binary's build Go satisfies the project, for tools
// that need it.
func (r *Resolver) goOK(path string, t Tool) bool {
	if !t.MatchGo || r.ModuleGo == "" || r.BuiltWith == nil {
		return true
	}
	built := r.BuiltWith(path)
	return built == "" || goLangAtLeast(built, r.ModuleGo)
}

// goLangAtLeast compares the language versions (major.minor) of a Go release
// ("go1.27.2") and a go directive ("1.27" or "1.27.0"), which is the check
// golangci-lint itself makes.
func goLangAtLeast(have, want string) bool {
	hm, hn, ok1 := goMinor(have)
	wm, wn, ok2 := goMinor(want)
	if !ok1 || !ok2 {
		return true
	}
	return hm > wm || (hm == wm && hn >= wn)
}

func goMinor(v string) (major, minor int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "go")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minor); err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

var goDirective = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)

func moduleGoDirective(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	if m := goDirective.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// builtWithGo reads the Go release a binary was built with from `go version`.
func builtWithGo(path string) string {
	cmd := exec.Command("go", "version", path)
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) >= 2 {
		return f[len(f)-1]
	}
	return ""
}

func (r *Resolver) note(format string, a ...any) {
	if r.Note != nil {
		fmt.Fprintf(r.Note, format+"\n", a...)
	}
}

func sameVersion(got, want string) bool {
	got, want = strings.TrimPrefix(got, "v"), strings.TrimPrefix(want, "v")
	return got != "" && got == want
}

func exeSuffix() string {
	if os.PathSeparator == '\\' {
		return ".exe"
	}
	return ""
}

// probeVersion reads a binary's version: the module version its build info
// records (installed by `go install`, or built by a release pipeline that
// stamps it), else the Tool's own --version output.
func probeVersion(path string, t Tool) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	vm := exec.Command("go", "version", "-m", path)
	vm.Env = goexec.Env()
	if out, err := vm.Output(); err == nil {
		mod := t.BuildModule
		if mod == "" {
			mod = t.Module
		}
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[0] == "mod" && f[1] == mod && f[2] != "(devel)" {
				return f[2]
			}
		}
	}
	if len(t.VersionArgs) > 0 && t.VersionRE != nil {
		out, _ := exec.Command(path, t.VersionArgs...).CombinedOutput()
		if m := t.VersionRE.FindStringSubmatch(string(out)); m != nil {
			return m[1]
		}
	}
	return ""
}

func goInstall(ctx context.Context, t Tool, dir, toolchain string) error {
	env := []string{"GOBIN=" + dir, "GOWORK=off", "GOFLAGS="}
	if toolchain != "" {
		env = append(env, "GOTOOLCHAIN="+toolchain)
	}
	cmd := exec.CommandContext(ctx, "go", "install", t.Module+"@"+t.Version)
	cmd.Dir = dir
	// GOFLAGS= drops the caller's flags; -trimpath goes back on, so the
	// install shares cache entries with every other trimmed build.
	cmd.Env = goexec.WithTrimpath(goexec.Env(env...))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go install %s@%s: %w\n%s", t.Module, t.Version, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func moduleToolchain(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	cmd.Dir = dir
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func withPathPrefix(env []string, dirs []string) []string {
	if len(dirs) == 0 {
		return env
	}
	prefix := strings.Join(dirs, string(os.PathListSeparator))
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			out = append(out, "PATH="+prefix+string(os.PathListSeparator)+strings.TrimPrefix(kv, "PATH="))
			found = true
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "PATH="+prefix)
	}
	return out
}

func joinDir(root, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

func asExit(err error, target **exec.ExitError) bool { return errors.As(err, target) }
