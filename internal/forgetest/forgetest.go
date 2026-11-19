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
//  3. `reliant forge`, when reliant is on PATH. Inside a project it hands the
//     command to the pinned forge, and fails rather than run another one;
//     outside one it runs the forge built into reliant, and says so.
//  4. Nothing: the test is skipped, naming what to install. scripts/forge.sh
//     goes on to `go run …/cmd/forge@<pin>`; a test does not, because that
//     builds forge inside the test's timeout.
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
	// Source is where it was found: "FORGE_BIN", "PATH", ".tools/bin" or
	// "reliant".
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
	// Version is what `bin --version` prints.
	Version func(bin string) (string, error)
}

// Machine is the Env of this process and machine, looking for the project
// above dir.
func Machine(dir string) Env {
	return Env{
		Dir:      dir,
		Getenv:   os.Getenv, //nolint:forbidigo // FORGE_BIN is the machine's override, as scripts/forge.sh reads it, not app configuration
		LookPath: exec.LookPath,
		Version: func(bin string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, bin, "--version").Output()
			return string(out), err
		},
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
		if isPin(env, c.bin, pin) {
			return Forge{Argv: []string{c.bin}, Source: c.source}, nil
		}
	}
	if bin, err := env.LookPath("reliant"); err == nil {
		return Forge{Argv: []string{bin, "forge"}, Source: "reliant"}, nil
	}
	return Forge{}, fmt.Errorf("%w for this test (pin %s): set FORGE_BIN, put forge %[2]s on PATH, or install reliant (`reliant forge` runs the pin)", ErrNoForge, pinOrNone(pin))
}

// isPin reports whether the forge at bin is the pinned one: its --version
// names pin as a word. Any forge is, for a pin no proxy serves.
func isPin(env Env, bin, pin string) bool {
	if !published(pin) {
		return true
	}
	out, err := env.Version(bin)
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
