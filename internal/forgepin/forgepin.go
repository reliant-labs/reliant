// Copyright (c) 2025 Reliant Labs

// Package forgepin asks the forge a project pins for what only that forge can
// say, for the parts of reliant that read forge content in-process.
//
// The forge compiled into reliant is whatever reliant was built with; every
// project pins its own (forge.yaml forge_version). What a forge prints about a
// project — its skills, the framework guide injected into every session — is
// a function of that forge: its commands, rules and layout. Rendered by the
// embedded forge in a project pinned to another, it is the wrong version's
// guidance, delivered as authoritative (forge report 86c6fd64 found the same
// skew in `reliant forge generate`). So inside such a project the content is
// asked of the pinned forge: forge's own hand-off decides which forge that is
// and obtains it (forgecli.ResolvePinnedForge), and this package runs it and
// caches what it prints.
//
// Callers decide what to do when the pinned forge cannot be obtained; the
// one thing they must not do is substitute the embedded forge's answer.
package forgepin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	forgecli "github.com/reliant-labs/forge/cli"
)

const (
	// resolveTimeout bounds obtaining the pinned forge, which may build it
	// once (about a minute).
	resolveTimeout = 4 * time.Minute
	// resolutionTTL is how long one answer to "which forge does this project
	// pin" is reused. Resolving can run the project's .tools/bin/forge to
	// read its version, and a skills index asks once per skill.
	resolutionTTL = time.Minute
	// runTimeout bounds one command of the pinned forge.
	runTimeout = time.Minute
)

// Source runs the forge projects pin. The zero value is not usable; New
// returns the real one, and tests build their own with fake funcs.
type Source struct {
	// Resolve is forge's decision of which forge runs a command in a project.
	Resolve func(ctx context.Context, projectRoot, command string, log io.Writer) (forgecli.PinnedForge, bool, error)
	// Run runs the pinned forge bin with args in dir and returns its stdout.
	Run func(bin, dir string, args ...string) ([]byte, error)
	// CacheDir is where a module version's output is kept ("" keeps nothing
	// on disk).
	CacheDir func() string

	resolutions sync.Map // projectRoot -> resolution
	outputs     sync.Map // cache key -> []byte
}

type resolution struct {
	yaml string
	at   time.Time
	pf   forgecli.PinnedForge
	ok   bool
	err  error
}

// New is the Source that resolves, runs and caches for real.
func New() *Source {
	return &Source{Resolve: forgecli.ResolvePinnedForge, Run: execForge, CacheDir: defaultCacheDir}
}

// Default is the process's Source.
var Default = New()

// Pinned reports the forge the project at root pins when it is not the
// embedded one (ok), or why it could not be obtained (err). command is the
// forge command the caller will run, which the decision depends on. An answer
// is reused for a minute while forge.yaml is unchanged — a failure too, so an
// unobtainable pin is not rebuilt once per skill.
func (s *Source) Pinned(root, command string) (forgecli.PinnedForge, bool, error) {
	yaml, _ := os.ReadFile(filepath.Join(root, "forge.yaml"))
	key := root + "\x00" + command
	if v, ok := s.resolutions.Load(key); ok {
		r := v.(resolution)
		if r.yaml == string(yaml) && time.Since(r.at) < resolutionTTL {
			return r.pf, r.ok, r.err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	pf, ok, err := s.Resolve(ctx, root, command, io.Discard)
	s.resolutions.Store(key, resolution{yaml: string(yaml), at: time.Now(), pf: pf, ok: ok, err: err})
	return pf, ok, err
}

// Output is what the pinned forge pf prints for the first of tries (each one
// a command line) that succeeds, run in dir, kept under name. name must
// change whenever anything besides the forge's version can change the output
// (Fingerprint). Kept in memory, and on disk for a module version, which
// never changes; a linked checkout's key moves with every edit, so its output
// is kept in memory only.
func (s *Source) Output(pf forgecli.PinnedForge, dir, name string, tries ...[]string) ([]byte, error) {
	memKey := pf.Key + "\x00" + name
	if v, ok := s.outputs.Load(memKey); ok {
		return v.([]byte), nil
	}
	diskDir := ""
	if strings.HasPrefix(pf.Key, "pin:") && s.CacheDir != nil {
		if base := s.CacheDir(); base != "" {
			diskDir = filepath.Join(base, digest(pf.Key))
		}
	}
	if diskDir != "" {
		if b, err := os.ReadFile(filepath.Join(diskDir, name)); err == nil {
			s.outputs.Store(memKey, b)
			return b, nil
		}
	}
	var out []byte
	err := fmt.Errorf("%s: no command to run", pf.Want)
	for _, args := range tries {
		if out, err = s.Run(pf.Path, dir, args...); err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	s.outputs.Store(memKey, out)
	if diskDir != "" {
		writeAtomic(diskDir, name, out)
	}
	return out, nil
}

// Fingerprint is a short name-safe digest of parts, for an Output name whose
// content depends on more than the forge version.
func Fingerprint(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write(p)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

// writeAtomic writes name under dir aside and renames it into place: a reader
// never sees half a file. Failing to cache is harmless.
func writeAtomic(dir, name string, b []byte) {
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(dir, name)) != nil {
		_ = os.Remove(tmp.Name())
	}
}

func defaultCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "reliant", "forge-pinned")
}

// CLIName is how forge names itself when embedded in this binary, and so the
// name an agent types: `reliant forge`.
func CLIName() string { return filepath.Base(os.Args[0]) + " forge" }

// execForge runs the pinned forge. Its argv[0] is CLIName, so command
// references in what it prints read `reliant forge …` exactly as the embedded
// forge renders them (forge names itself from argv[0]); with the hand-off,
// that command runs the pinned forge too.
func execForge(bin, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Args[0] = CLIName()
	cmd.Dir = dir
	var env []string
	for _, kv := range os.Environ() {
		// No inherited hand-off marker: a forge older than the
		// single-process marker would believe one.
		if !strings.HasPrefix(kv, "FORGE_VERSION_DELEGATED=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("forge %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
