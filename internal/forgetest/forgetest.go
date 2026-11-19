// Copyright (c) 2025 Reliant Labs

// Package forgetest finds the forge a test runs: the one this module pins
// (forge.yaml's forge_version), from wherever the machine has it, in the
// order scripts/forge.sh takes for every task and script:
//
//  1. $FORGE_BIN, as given (the release gate builds the pin once and exports
//     it, so no test builds forge inside its own budget).
//  2. forge on PATH, then the module's .tools/bin/forge, when its --version
//     names the pin. Any forge for a pin that names no published forge
//     (`dev`, a +dirty build): it hands the command to the pin when it can.
//  3. The pin through reliant, when reliant is on PATH: `reliant forge` when
//     the forge built into reliant IS the pin, else the pinned build
//     reliant's hand-off keeps (see [viaReliant]).
//  4. Nothing: the test is skipped, naming what to install. scripts/forge.sh
//     goes on to `go run …/cmd/forge@<pin>`; a test does not, because that
//     builds forge inside the test's timeout.
//
// The Forge it returns runs the pin from ANY directory. That is why step 3
// is not simply `reliant forge`: reliant hands a command to the pinned forge
// only inside a project, and a test that runs forge in a t.TempDir() would
// get the forge built into reliant, another version, and pass against a
// contract the module does not pin.
//
// Every test that runs forge is slow, so [Pinned] calls testkit.Slow before
// it looks: a caller is gated under -short whether or not it says so itself.
//
//forge:exclude-contract: a test helper; no service, nothing to mock
package forgetest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// Forge is a resolved forge command.
type Forge struct {
	// Argv runs forge, before forge's own arguments: one executable, or
	// reliant followed by "forge".
	Argv []string
	// Source is where it was found: "FORGE_BIN", "PATH", ".tools/bin",
	// "reliant" (its built-in forge is the pin) or "reliant's pinned build".
	Source string
}

// Command returns the command that runs forge with args.
func (f Forge) Command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, f.Argv[0], append(slices.Clone(f.Argv[1:]), args...)...)
}

// String is the command line, as scripts/forge.sh --which prints it.
func (f Forge) String() string { return strings.Join(f.Argv, " ") }

// Pinned marks t slow (testkit.Slow: it runs forge, a subprocess) and returns
// the forge this module pins, skipping t when the machine has none.
func Pinned(t testing.TB) Forge {
	t.Helper()
	testkit.Slow(t, "runs forge, a subprocess")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	f, err := Resolve(Machine(wd))
	if err != nil {
		t.Skip(err.Error())
	}
	t.Logf("forge: %s (%s)", f, f.Source)
	return f
}

// Env is what Resolve reads from the machine.
type Env struct {
	// Dir is where the project is looked for: the nearest forge.yaml above it.
	Dir string
	// Getenv reads an environment variable.
	Getenv func(string) string
	// LookPath finds an executable on PATH.
	LookPath func(string) (string, error)
	// Version is what `argv… --version` prints.
	Version func(argv ...string) (string, error)
	// Run runs argv in dir, for its effect.
	Run func(dir string, argv ...string) error
	// UserCacheDir is os.UserCacheDir.
	UserCacheDir func() (string, error)
}

// Machine is the Env of this process and machine, looking for the project
// above dir.
func Machine(dir string) Env {
	return Env{
		Dir:      dir,
		Getenv:   os.Getenv, //nolint:forbidigo // FORGE_BIN and FORGE_PINNED_CACHE are the machine's, as scripts/forge.sh reads them, not app configuration
		LookPath: exec.LookPath,
		Version: func(argv ...string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, argv[0], append(slices.Clone(argv[1:]), "--version")...).Output()
			return string(out), err
		},
		Run: func(dir string, argv ...string) error {
			// A build of the pin takes minutes on a cold cache.
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s: %w\n%s", strings.Join(argv, " "), err, out)
			}
			return nil
		},
		UserCacheDir: os.UserCacheDir,
	}
}

// ErrNoForge is Resolve's error when the machine has no forge to run.
var ErrNoForge = errors.New("no forge")

// Resolve finds the pinned forge in env, in the package doc's order.
func Resolve(env Env) (Forge, error) {
	if bin := env.Getenv("FORGE_BIN"); bin != "" {
		return Forge{Argv: []string{bin}, Source: "FORGE_BIN"}, nil
	}
	root, pin, err := projectPin(env.Dir)
	if err != nil {
		return Forge{}, err
	}
	candidates := []struct{ bin, source string }{}
	if bin, err := env.LookPath("forge"); err == nil {
		candidates = append(candidates, struct{ bin, source string }{bin, "PATH"})
	}
	if root != "" {
		tools := filepath.Join(root, ".tools", "bin", "forge")
		if fi, err := os.Stat(tools); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			candidates = append(candidates, struct{ bin, source string }{tools, ".tools/bin"})
		}
	}
	for _, c := range candidates {
		if isPin(env, pin, c.bin) {
			return Forge{Argv: []string{c.bin}, Source: c.source}, nil
		}
	}
	if bin, err := env.LookPath("reliant"); err == nil {
		return viaReliant(env, bin, root, pin)
	}
	return Forge{}, fmt.Errorf("%w for this test (pin %s): set FORGE_BIN, put forge %[2]s on PATH, or install reliant (`reliant forge` runs the pin)", ErrNoForge, pinOrNone(pin))
}

// handOffProbe is a forge command reliant hands to the pinned forge inside a
// project (every project command is; `version` and `help` are not) and that
// does nothing: running it in the project root makes reliant obtain the pin,
// from its cache or by building it once.
var handOffProbe = []string{"forge", "lint", "--help"}

// viaReliant is the pin through the reliant at bin. When reliant's built-in
// forge is the pin, `reliant forge` runs it from anywhere. Otherwise reliant
// runs the pin only for a command inside the project, as an exec of the build
// its hand-off keeps (forge's forgedelegate cache: $FORGE_PINNED_CACHE, else
// <user cache dir>/forge/pinned). So the probe runs in the project root, and
// the executable under that cache that reports the pin is what a test runs.
//
// That cache path is forge's hand-off detail, not an interface: neither
// reliant nor forge prints the pinned forge's path. The lookup is
// version-checked, so a changed layout fails here, loudly, and never runs
// another forge.
func viaReliant(env Env, bin, root, pin string) (Forge, error) {
	if !published(pin) || root == "" || isPin(env, pin, bin, "forge") {
		return Forge{Argv: []string{bin, "forge"}, Source: "reliant"}, nil
	}
	if err := env.Run(root, append([]string{bin}, handOffProbe...)...); err != nil {
		return Forge{}, fmt.Errorf("%w: reliant could not obtain forge %s: %v", ErrNoForge, pin, err)
	}
	cache := env.Getenv("FORGE_PINNED_CACHE")
	if strings.TrimSpace(cache) == "" {
		base, err := env.UserCacheDir()
		if err != nil {
			return Forge{}, fmt.Errorf("%w: no user cache dir to find reliant's build of forge %s in: %v", ErrNoForge, pin, err)
		}
		cache = filepath.Join(base, "forge", "pinned")
	}
	if path, ok := findPinned(env, cache, pin); ok {
		return Forge{Argv: []string{path}, Source: "reliant's pinned build"}, nil
	}
	return Forge{}, fmt.Errorf("%w: reliant ran forge %s, but no executable under %s reports it (did forge's hand-off cache move?)", ErrNoForge, pin, cache)
}

// findPinned looks under cache (at most three levels down) for an executable
// named forge, in a directory naming pin, that reports pin.
func findPinned(env Env, cache, pin string) (string, bool) {
	var found string
	_ = filepath.WalkDir(cache, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is one we skip, not a failure
		}
		rel, _ := filepath.Rel(cache, path)
		if d.IsDir() {
			if rel != "." && strings.Count(rel, string(filepath.Separator)) >= 2 {
				return fs.SkipDir
			}
			return nil
		}
		if (d.Name() != "forge" && d.Name() != "forge.exe") || !strings.Contains(rel, pin) {
			return nil
		}
		if isPin(env, pin, path) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
}

// isPin reports whether the forge argv runs is the pinned one: its --version
// names pin as a word. Any forge is, for a pin no proxy serves.
func isPin(env Env, pin string, argv ...string) bool {
	if !published(pin) {
		return true
	}
	out, err := env.Version(argv...)
	if err != nil {
		return false
	}
	return slices.Contains(strings.Fields(out), pin)
}

// published reports whether pin names a published forge: not empty, `dev`,
// or a +dirty (or other +build) local build.
func published(pin string) bool {
	return pin != "" && pin != "dev" && !strings.Contains(pin, "+")
}

func pinOrNone(pin string) string {
	if pin == "" {
		return "none"
	}
	return pin
}

// projectPin finds the nearest forge.yaml at or above dir and returns its
// directory and forge_version ("" and "" when there is none).
func projectPin(dir string) (root, pin string, err error) {
	for d := dir; ; {
		f, err := os.Open(filepath.Join(d, "forge.yaml"))
		if err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				v, ok := strings.CutPrefix(sc.Text(), "forge_version:")
				if !ok {
					continue
				}
				if i := strings.Index(v, "#"); i >= 0 {
					v = v[:i]
				}
				return d, strings.Trim(strings.TrimSpace(v), `"'`), nil
			}
			return d, "", sc.Err()
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", nil
		}
		d = parent
	}
}
