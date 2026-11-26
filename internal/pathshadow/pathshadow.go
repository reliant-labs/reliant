// Package pathshadow finds the other Reliant Forge binaries on PATH and the
// versions they report, so `forge version` and `forge start` can say when a
// bare `forge` would run a different forge than the one running now.
//
// WHY THIS EXISTS. An evaluator had ~/go/bin/forge (v1.7.0, an old
// `go install`) ahead of the npm-installed v1.10.0 on PATH. Every `forge`
// they typed ran the old one, and nothing said so: `forge version` named the
// running binary only.
//
// It is cheap by construction: no network, ever. A forge binary on PATH is
// asked for `version` with a short timeout, and an npm shim
// (@reliantlabs/forge) is never run at all — its first run downloads the
// release binary — so its version is read from its package.json instead.
// Anything named `forge` that does not answer as Reliant Forge (Laravel
// Forge, Foundry's forge) is ignored, never reported.
package pathshadow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// DisableEnv turns the scan off when set to any non-empty value. Every probe
// sets it on the forge it runs, so a newer forge asked for its version never
// scans PATH and spawns forges in turn; forge's own version probes
// (forgedelegate) set it for the same reason.
const DisableEnv = "FORGE_NO_PATH_CHECK"

// npmPackage is the npm package whose `forge` bin is a node shim.
const npmPackage = "@reliantlabs/forge"

// Binary is one forge found on PATH.
type Binary struct {
	// Path is the file as PATH names it (not symlink-resolved).
	Path string
	// Version is the Reliant Forge version it reports.
	Version string
	// NPM is set when Path is the @reliantlabs/forge npm shim, whose version
	// was read from the package's package.json rather than by running it.
	NPM bool
	// Self is set when Path is the running executable.
	Self bool
}

// Report is the outcome of a scan: the running forge's version and every
// Reliant Forge on PATH, in PATH order (the first is what a bare `forge`
// runs).
type Report struct {
	Running  string
	Binaries []Binary
}

// Scanner finds forge binaries on PATH. The zero value is not usable; New
// returns the real one. The fields are the seams tests replace.
type Scanner struct {
	// PathEnv is the PATH value to search.
	PathEnv string
	// GOOS decides the file names searched (forge.exe and forge.cmd on
	// windows, forge elsewhere).
	GOOS string
	// Self is the running executable ("" when unknown).
	Self string
	// Probe runs `path version` and returns its stdout.
	Probe func(ctx context.Context, path string) ([]byte, error)
}

// New returns the real scanner: this process's PATH and executable, probing
// with a 2s timeout per binary.
func New() Scanner {
	self, _ := os.Executable()
	return Scanner{
		PathEnv: os.Getenv("PATH"),
		GOOS:    runtime.GOOS,
		Self:    self,
		Probe:   probeVersion,
	}
}

// probeTimeout bounds each `<forge> version` run.
const probeTimeout = 2 * time.Second

func probeVersion(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Env = append(os.Environ(), DisableEnv+"=1")
	// Outside any project, so the probed forge never checks a pin's skew
	// (see forgedelegate.RunCommandVersion).
	cmd.Dir = filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	return cmd.Output()
}

// Disabled reports whether DisableEnv turns the scan off.
func Disabled() bool { return os.Getenv(DisableEnv) != "" }

// Scan returns the running version and every Reliant Forge on PATH. running
// is the version this process reports; the running executable, when it is on
// PATH, is listed with it and Self set, and never probed. The probe wait is
// bounded by probeTimeout and the caller's context; cancellation returns the
// results collected so far, even if a custom Probe ignores its context.
func (s Scanner) Scan(ctx context.Context, running string) Report {
	rep := Report{Running: running}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return rep
	}
	cands := s.candidates()
	found := make([]*Binary, len(cands))
	type result struct {
		index  int
		binary *Binary
	}
	// Each callback can finish after Scan returns, without blocking or writing
	// to the report's storage. Only this goroutine owns found.
	results := make(chan result, len(cands))
	pending := 0
	for i, c := range cands {
		if ctx.Err() != nil {
			break
		}
		if s.isSelf(c) {
			found[i] = &Binary{Path: c, Version: running, Self: true}
			continue
		}
		if v, ok, isShim := npmShimVersion(c); isShim {
			if ok {
				found[i] = &Binary{Path: c, Version: v, NPM: true}
			}
			continue
		}
		if s.Probe == nil {
			continue
		}
		pending++
		go func(i int, c string) {
			r := result{index: i}
			if ctx.Err() == nil {
				out, err := s.Probe(ctx, c)
				if err == nil {
					if v, ok := ParseVersionOutput(out); ok {
						r.binary = &Binary{Path: c, Version: v}
					}
				}
			}
			results <- r
		}(i, c)
	}
collect:
	for pending > 0 {
		select {
		case r := <-results:
			found[r.index] = r.binary
			pending--
		case <-ctx.Done():
			break collect
		}
	}
	for _, b := range found {
		if b != nil {
			rep.Binaries = append(rep.Binaries, *b)
		}
	}
	return rep
}

// candidates lists the files named forge on PATH, in PATH order, each file
// once (two PATH entries may name one directory, or a symlink and its target).
func (s Scanner) candidates() []string {
	names := []string{"forge"}
	if s.GOOS == "windows" {
		names = []string{"forge.exe", "forge.cmd"}
	}
	var out []string
	var seen []os.FileInfo
	for _, dir := range filepath.SplitList(s.PathEnv) {
		if dir == "" {
			continue
		}
		for _, name := range names {
			p := filepath.Join(dir, name)
			st, err := os.Stat(p)
			if err != nil || st.IsDir() {
				continue
			}
			if s.GOOS != "windows" && st.Mode()&0o111 == 0 {
				continue
			}
			dup := false
			for _, prev := range seen {
				if os.SameFile(prev, st) {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			seen = append(seen, st)
			out = append(out, p)
		}
	}
	return out
}

func (s Scanner) isSelf(p string) bool {
	if s.Self == "" {
		return false
	}
	a, err := os.Stat(p)
	if err != nil {
		return false
	}
	b, err := os.Stat(s.Self)
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}

// versionLine is the first line every v1 forge's `version` prints:
// "forge version v1.10.0 (commit …)".
var versionLine = regexp.MustCompile(`^forge version (v?\d+\.\d+\.\d+\S*)`)

// ParseVersionOutput reads the version from `forge version` output, and
// reports false unless the output is Reliant Forge's: its first line names
// the version and its identity block carries the "forge build:" line every
// v1 forge prints. A different program named forge never matches both.
func ParseVersionOutput(out []byte) (string, bool) {
	first, rest, _ := bytes.Cut(out, []byte("\n"))
	m := versionLine.FindSubmatch(bytes.TrimSpace(first))
	if m == nil || !bytes.Contains(rest, []byte("forge build:")) {
		return "", false
	}
	return string(m[1]), true
}

// shimPath matches a path to the npm package inside a shim script, as npm's
// .cmd shims and pnpm's shell shims spell it.
var shimPath = regexp.MustCompile(`([^"'\s]*?)node_modules/@reliantlabs/forge/`)

// npmShimVersion reports the version of the @reliantlabs/forge package
// behind path when path is its npm shim: a symlink to the package's
// bin/forge.js (npm on unix), or a script naming the package (npm's .cmd on
// windows, pnpm's shell shims). isShim is set for any shim of the package,
// so a shim whose package.json cannot be read is skipped, never run: running
// it would download the release binary.
func npmShimVersion(path string) (version string, ok, isShim bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	slash := filepath.ToSlash(resolved)
	if strings.HasSuffix(slash, "/"+npmPackage+"/bin/forge.js") {
		v, ok := packageVersion(filepath.Dir(filepath.Dir(resolved)))
		return v, ok, true
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", false, false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 8192)
	n, _ := f.Read(head)
	text := strings.ReplaceAll(string(head[:n]), `\`, "/")
	script := strings.HasPrefix(text, "#!") || strings.EqualFold(filepath.Ext(resolved), ".cmd")
	if !script || !strings.Contains(text, npmPackage) {
		return "", false, false
	}
	dir := filepath.Dir(resolved)
	for _, m := range shimPath.FindAllStringSubmatch(text, -1) {
		prefix := m[1]
		for _, v := range []string{"$basedir/", "${basedir}/", "%~dp0/", "%dp0%/", "%~dp0", "%dp0%"} {
			prefix = strings.Replace(prefix, v, "", 1)
		}
		root := filepath.FromSlash(prefix + "node_modules/" + npmPackage)
		if !filepath.IsAbs(root) {
			root = filepath.Join(dir, root)
		}
		if v, ok := packageVersion(root); ok {
			return v, true, true
		}
	}
	// npm's windows shims sit beside the global node_modules.
	if v, ok := packageVersion(filepath.Join(dir, "node_modules", filepath.FromSlash(npmPackage))); ok {
		return v, true, true
	}
	return "", false, true
}

// packageVersion reads root/package.json and returns its version, as forge
// spells versions (v-prefixed), when it is the @reliantlabs/forge package.
func packageVersion(root string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", false
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Name != npmPackage || pkg.Version == "" {
		return "", false
	}
	return "v" + strings.TrimPrefix(pkg.Version, "v"), true
}

// sameVersion compares two versions with or without the v prefix.
func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// Mismatched reports whether any forge on PATH other than the running one
// reports a version that differs from it.
func (r Report) Mismatched() bool {
	for _, b := range r.Binaries {
		if !b.Self && !sameVersion(b.Version, r.Running) {
			return true
		}
	}
	return false
}

// Warning renders the multi-line warning for a mismatched report, or "" when
// every forge on PATH reports the running version. name is how this forge is
// invoked (`forge`, `reliant forge`) and self its executable.
func (r Report) Warning(name, self string) string {
	if !r.Mismatched() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "warning: PATH holds a forge whose version differs from this one (%s %s", name, r.Running)
	if self != "" {
		fmt.Fprintf(&b, ", %s", self)
	}
	b.WriteString("):\n")
	width, vwidth := 0, 0
	for _, bin := range r.Binaries {
		width = max(width, len(bin.Path))
		vwidth = max(vwidth, len(bin.Version))
	}
	for i, bin := range r.Binaries {
		var notes []string
		if i == 0 {
			notes = append(notes, "first on PATH: a bare `forge` runs this one")
		}
		if bin.Self {
			notes = append(notes, "the forge running now")
		}
		if bin.NPM {
			notes = append(notes, "npm "+npmPackage)
		}
		line := fmt.Sprintf("  %-*s  %-*s", width, bin.Path, vwidth, bin.Version)
		if len(notes) > 0 {
			line += "  (" + strings.Join(notes, "; ") + ")"
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	b.WriteString("  Fix: delete the forge you do not want, or put the directory of the one you do first on PATH.\n")
	// Updating to @latest only helps when the newest is a release: past a
	// development build it would move a forge backwards.
	newest := r.newest()
	if semver.Prerelease(newest) != "" || semver.Build(newest) != "" {
		newest = ""
	}
	for _, bin := range r.Binaries {
		if bin.Self || newest == "" || !older(bin.Version, newest) {
			continue
		}
		fmt.Fprintf(&b, "  Or update %s (%s) in place: %s\n", bin.Path, bin.Version, updateHint(bin))
	}
	return b.String()
}

// newest is the highest semver among the running and PATH versions.
func (r Report) newest() string {
	best := canon(r.Running)
	for _, bin := range r.Binaries {
		if v := canon(bin.Version); v != "" && (best == "" || semver.Compare(v, best) > 0) {
			best = v
		}
	}
	return best
}

func canon(v string) string {
	v = "v" + strings.TrimPrefix(v, "v")
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

func older(v, than string) bool {
	cv := canon(v)
	return cv != "" && semver.Compare(cv, than) < 0
}

// updateHint names the install path forge documents (docs/install.md) that
// most likely put bin there.
func updateHint(bin Binary) string {
	p := filepath.ToSlash(bin.Path)
	resolved := p
	if r, err := filepath.EvalSymlinks(bin.Path); err == nil {
		resolved = filepath.ToSlash(r)
	}
	switch {
	case bin.NPM:
		return "`npm i -g " + npmPackage + "@latest`"
	case strings.Contains(resolved, "/Cellar/") || strings.Contains(resolved, "/homebrew/") || strings.Contains(resolved, "/linuxbrew/"):
		return "`brew upgrade reliant-labs/reliant/forge`"
	case strings.Contains(p, "/go/bin/") || os.Getenv("GOBIN") != "" && filepath.ToSlash(os.Getenv("GOBIN")) == filepath.ToSlash(filepath.Dir(bin.Path)):
		return "`go install github.com/reliant-labs/forge/cmd/forge@latest`"
	default:
		return "`curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | FORGE_INSTALL_DIR=" + filepath.Dir(bin.Path) + " sh`, or the installer that put it there"
	}
}
