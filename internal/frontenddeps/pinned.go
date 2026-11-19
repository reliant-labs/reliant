package frontenddeps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ErrRunnerUnusable marks a package manager that cannot run on this machine:
// the executable the frontend's packageManager pin resolves to will not start,
// or starts as another version. Retrying the install runs the same executable
// and fails the same way, so callers that retry stop on it.
var ErrRunnerUnusable = errors.New("package manager is not runnable")

// unusableRunnerError is ErrRunnerUnusable with the diagnosis and the fix.
type unusableRunnerError struct{ msg string }

func (e *unusableRunnerError) Error() string        { return e.msg }
func (e *unusableRunnerError) Is(target error) bool { return target == ErrRunnerUnusable }

// pinnedProbeTimeout bounds one `<runner> --version`. The first probe of a
// new pin is the one that downloads it (pnpm switches to the pinned release,
// corepack fetches it), so this is a download budget, not a startup one.
var pinnedProbeTimeout = 5 * time.Minute

// verifiedPins remembers the dir+pin pairs already proven runnable in this
// process, so `forge env up` over several frontends probes each pin once.
var verifiedPins sync.Map

// VerifyPinnedRunner proves that runner, as forge will invoke it in dir, is
// the release package.json's packageManager pins: it runs `<runner>
// --version` and compares. A frontend with no pin, or one owned by another
// manager, has nothing to verify.
//
// pnpm is why this exists. A system pnpm older than the pin switches to it by
// installing the pinned release into its tools directory — and pnpm 10 does
// that with the release's own install script blocked, which for pnpm 12
// leaves a placeholder text file where the native binary belongs. Every
// invocation then fails with `spawnSync …/bin/pnpm ENOEXEC`, and so does
// every retry. When the probe names an executable that will not start, this
// runs that package's own install step once (what npm would have run), probes
// again, and otherwise returns an ErrRunnerUnusable naming the fix — it never
// hands an install an executable already known to be broken.
func VerifyPinnedRunner(ctx context.Context, dir, runner string) error {
	if runner != "pnpm" {
		return nil
	}
	name, pin := manifestPackageManager(dir)
	if name != runner || pin == "" {
		return nil
	}
	key := dir + "\x00" + pin
	if _, ok := verifiedPins.Load(key); ok {
		return nil
	}
	argv := resolveRunner(dir, runner, exec.LookPath)
	got, out, err := probeVersion(ctx, dir, argv)
	if err == nil && got == pin {
		verifiedPins.Store(key, true)
		return nil
	}
	diagnosis := probeDiagnosis(argv, got, out, err, pin)
	if broken := unstartableExecutable(out, err); broken != "" {
		if pkg := owningPackage(broken, runner, pin); pkg != "" {
			fmt.Printf("  %s@%s will not start (%s): running its package's install step in %s, once\n", runner, pin, broken, pkg)
			repairErr := runPackageInstallScripts(ctx, pkg)
			if repairErr == nil {
				got, out, err = probeVersion(ctx, dir, argv)
				if err == nil && got == pin {
					verifiedPins.Store(key, true)
					return nil
				}
				diagnosis = probeDiagnosis(argv, got, out, err, pin) + " (after re-running its install step in " + pkg + ")"
			} else {
				diagnosis += fmt.Sprintf(" (re-running its install step in %s failed: %v)", pkg, repairErr)
			}
		}
	}
	return &unusableRunnerError{msg: unusableRunnerMessage(dir, runner, pin, diagnosis)}
}

// probeVersion runs argv + --version in dir and returns the last line it
// printed to stdout, and everything it printed.
func probeVersion(ctx context.Context, dir string, argv []string) (version, output string, err error) {
	ctx, cancel := context.WithTimeout(ctx, pinnedProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], "--version")...)
	cmd.Dir = dir
	// One lock for both streams: exec copies each on its own goroutine, and
	// both land in all.
	var mu sync.Mutex
	var stdout, all bytes.Buffer
	cmd.Stdout = &lockedBuffers{mu: &mu, bufs: []*bytes.Buffer{&stdout, &all}}
	cmd.Stderr = &lockedBuffers{mu: &mu, bufs: []*bytes.Buffer{&all}}
	err = cmd.Run()
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), strings.TrimSpace(all.String()), err
}

// lockedBuffers writes to every buffer it holds, under mu.
type lockedBuffers struct {
	mu   *sync.Mutex
	bufs []*bytes.Buffer
}

func (l *lockedBuffers) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.bufs {
		b.Write(p)
	}
	return len(p), nil
}

func probeDiagnosis(argv []string, got, out string, err error, pin string) string {
	cmd := strings.Join(argv, " ") + " --version"
	if err != nil {
		if out == "" {
			return fmt.Sprintf("%#q failed: %v", cmd, err)
		}
		return fmt.Sprintf("%#q failed (%v):\n%s", cmd, err, indent(out))
	}
	return fmt.Sprintf("%#q printed %q, not %s", cmd, got, pin)
}

func indent(s string) string {
	return "      " + strings.ReplaceAll(s, "\n", "\n      ")
}

// unstartablePatterns find the executable an exec failure names: node's
// spawn error (what pnpm reports when its version switch cannot start the
// pinned release) and Go's own, when the runner on PATH is the broken file.
var unstartablePatterns = []*regexp.Regexp{
	regexp.MustCompile(`spawn(?:Sync)? (\S+) (?:ENOEXEC|EACCES)`),
	regexp.MustCompile(`fork/exec (\S+): (?:exec format error|permission denied)`),
}

// unstartableExecutable is the path of the executable a failed probe could
// not start, or "".
func unstartableExecutable(output string, err error) string {
	text := output
	if err != nil {
		text += "\n" + err.Error()
	}
	for _, re := range unstartablePatterns {
		if m := re.FindStringSubmatch(text); m != nil {
			return strings.TrimSuffix(m[1], ":")
		}
	}
	return ""
}

// owningPackage finds the installed package directory of runner@pin that
// executable belongs to: the directory holding a package.json naming it, at
// or above where the executable (or the file a bin link points at) lives, or
// the hoisted node_modules/<runner> beside a bin directory. "" when none is
// found — a broken executable forge cannot attribute to the pinned release is
// not one it repairs.
func owningPackage(executable, runner, pin string) string {
	var starts []string
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		starts = append(starts, filepath.Dir(resolved))
	}
	starts = append(starts, filepath.Dir(executable))
	for _, start := range starts {
		for dir, i := start, 0; i < 4; dir, i = filepath.Dir(dir), i+1 {
			for _, candidate := range []string{dir, filepath.Join(dir, "node_modules", runner)} {
				if isPackage(candidate, runner, pin) {
					return candidate
				}
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return ""
}

func isPackage(dir, name, version string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct{ Name, Version string }
	return json.Unmarshal(data, &pkg) == nil && pkg.Name == name && pkg.Version == version
}

// runPackageInstallScripts runs pkg's install lifecycle — preinstall,
// install, postinstall, each that it declares — as npm would after unpacking
// it: in the package directory, with npm_lifecycle_event naming the stage.
func runPackageInstallScripts(ctx context.Context, pkg string) error {
	if runtime.GOOS == "windows" {
		return errors.New("not attempted on Windows")
	}
	data, err := os.ReadFile(filepath.Join(pkg, "package.json"))
	if err != nil {
		return err
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse %s/package.json: %w", pkg, err)
	}
	ran := false
	for _, stage := range []string{"preinstall", "install", "postinstall"} {
		script := manifest.Scripts[stage]
		if script == "" {
			continue
		}
		ran = true
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Dir = pkg
		cmd.Env = append(os.Environ(), "npm_lifecycle_event="+stage)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s `%s`: %w: %s", stage, script, err, strings.TrimSpace(string(out)))
		}
	}
	if !ran {
		return errors.New("the package declares no install script")
	}
	return nil
}

func unusableRunnerMessage(dir, runner, pin, diagnosis string) string {
	return fmt.Sprintf("%s@%s, the release %s/package.json pins (packageManager), is not runnable on this machine: %s\n"+
		"  Put that exact release first on PATH, then re-run:\n"+
		"    npm install -g %s@%s\n"+
		"    corepack enable %s     (alternative; corepack ships with Node through v24)\n"+
		"  An older %s that switches to the pin itself is what installed the copy that will not start: "+
		"`%s --version` in %s must print %s.",
		runner, pin, dir, diagnosis, runner, pin, runner, runner, runner, dir, pin)
}
