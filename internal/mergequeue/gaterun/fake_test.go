// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// fakeBackend runs the real stage script with the machine's sh in a
// temporary directory standing in for the boundary's work volume. It is no
// boundary at all; it exists so the script, the report and the Runner are
// tested together without docker.
type fakeBackend struct {
	iso          Isolation
	unavailable  string
	preflightErr error
	root         string
	// binDir is put first on the script's PATH (a fake forge).
	binDir string
	// tamper changes the backend's Result after the script ran.
	tamper func(*Result)
	// runErr fails Run before the script.
	runErr error
	// block, when set, makes Run wait for it (or the context).
	block chan struct{}

	mu        sync.Mutex
	specs     []Spec
	teardowns []string
}

func (f *fakeBackend) Isolation() Isolation { return f.iso }

func (f *fakeBackend) Detect(context.Context) Availability {
	if f.unavailable != "" {
		return Availability{Reason: f.unavailable}
	}
	return Availability{Available: true}
}

func (f *fakeBackend) Preflight(context.Context) error { return f.preflightErr }

func (f *fakeBackend) dir(runID string) string { return filepath.Join(f.root, runID) }

func (f *fakeBackend) Run(ctx context.Context, spec Spec) (Result, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if f.runErr != nil {
		return Result{}, f.runErr
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	dir := f.dir(spec.RunID)
	if err := os.CopyFS(filepath.Join(dir, "src"), os.DirFS(spec.SourceDir)); err != nil {
		return Result{}, err
	}
	env := make([]string, 0, len(spec.Env))
	for _, kv := range spec.Env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case EnvGateRoot, "HOME", "TMPDIR", "PATH", EnvVulnDB:
			continue
		}
		env = append(env, kv)
	}
	path := os.Getenv("PATH")
	if f.binDir != "" {
		path = f.binDir + string(os.PathListSeparator) + path
	}
	env = append(env, EnvGateRoot+"="+dir, "HOME="+dir+"/home", "TMPDIR="+dir+"/tmp", "PATH="+path, EnvVulnDB+"="+dir+"/vulndb")
	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	cmd.Dir, cmd.Env = filepath.Join(dir, "src"), env
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		return Result{}, err
	}
	res := Result{Class: ClassifyExit(code), ExitCode: code}
	if f.tamper != nil {
		f.tamper(&res)
	}
	return res, nil
}

// CopyOut tars path (inside the gate) the way `docker cp <ctr>:<path> -`
// does: entries named under the path's base name, links as links.
func (f *fakeBackend) CopyOut(_ context.Context, runID, path string, w io.Writer, maxBytes int64) error {
	host := filepath.Join(f.dir(runID), strings.TrimPrefix(path, WorkMount))
	base := filepath.Base(path)
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(host, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(host, p)
		name := filepath.ToSlash(filepath.Join(base, rel))
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = name
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			fh, err := os.Open(p)
			if err != nil {
				return err
			}
			defer fh.Close()
			_, err = io.Copy(tw, fh)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

func (f *fakeBackend) Teardown(_ context.Context, runID string) error {
	f.mu.Lock()
	f.teardowns = append(f.teardowns, runID)
	f.mu.Unlock()
	return os.RemoveAll(f.dir(runID))
}

// dirFetcher "fetches" by copying a directory: the Runner's tests are about
// the gate, GitFetcher has its own.
type dirFetcher struct {
	src string
	err error
}

func (d dirFetcher) Fetch(_ context.Context, spec FetchSpec) error {
	if d.err != nil {
		return d.err
	}
	return os.CopyFS(spec.Dir, os.DirFS(d.src))
}

// newRunner is a Runner over b and a tree with the given files.
func newRunner(t *testing.T, b Backend, files map[string]string) *Runner {
	t.Helper()
	src := t.TempDir()
	for name, body := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Runner{Isolated: []Backend{b}, Fetcher: dirFetcher{src: src}, WorkRoot: t.TempDir(), Image: "gate:test",
		Limits: Limits{CPUs: 1, MemoryBytes: 1 << 30, Pids: 100, DiskBytes: 1 << 30}}
}

func newFake(t *testing.T, iso Isolation) *fakeBackend {
	t.Helper()
	return &fakeBackend{iso: iso, root: t.TempDir()}
}

const testSHA = "0123456789abcdef0123456789abcdef01234567"

// startReq is a valid StartRequest over stages.
func startReq(stages ...PlanStage) StartRequest {
	return StartRequest{GateID: "gate-b1-1", Repo: "acme/widgets", Ref: "reliant-queue/b1", SHA: testSHA,
		Stages: stages, TimeoutMs: 60_000}
}

func cmdStage(n config.StageName, command string) PlanStage {
	return PlanStage{Name: n, State: config.StageOn, Kind: config.StageCommand, Command: command}
}

func offStage(n config.StageName) PlanStage {
	return PlanStage{Name: n, State: config.StageOff, Reason: "turned off in pipeline.yaml"}
}

func scanStage(env string) PlanStage {
	return PlanStage{Name: config.StageSBOMScan, State: config.StageOn, Kind: config.StageBuiltinScan, ScanEnv: env}
}

// fakeForge writes a `forge` to a new directory that answers `vuln gate
// --help` and, for `vuln gate`, writes doc to --out/vuln-gate.json and exits
// code. It returns the directory.
func fakeForge(t *testing.T, doc string, code int) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in *--help*) exit 0 ;; esac
out=""
prev=""
for a in "$@"; do
	if [ "$prev" = "--out" ]; then out="$a"; fi
	prev="$a"
done
echo "$*" >"$RELIANT_GATE_ROOT/forge-args"
cat >"$out/vuln-gate.json" <<'DOC'
` + doc + `
DOC
exit ` + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "forge"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
