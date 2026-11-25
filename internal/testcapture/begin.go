package testcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// The environment the capture verbs read and export.
const (
	// EnvRun names the run directory every lane of one run joins. begin
	// reuses it, and capture exports it to its command, so a lane that
	// starts another lane (a `task` calling `task`) joins the same run.
	EnvRun = "FORGE_TEST_RUN"
	// EnvLane and EnvLaneDir are exported to a lane's command: its name, and
	// the directory its coverage goes in (Vitest's reportsDirectory).
	EnvLane    = "FORGE_TEST_LANE"
	EnvLaneDir = "FORGE_TEST_LANE_DIR"
	// EnvTests=off turns recording off for one invocation: begin prints a
	// scratch directory and capture runs the command and records nothing.
	EnvTests = "FORGE_TESTS"
)

// Off reports whether EnvTests turns recording off.
func Off(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(EnvTests))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// provenanceBudget bounds begin's git reads. A clean tree costs a handful of
// git process starts; a dirty one is hashed through a temporary index under
// release.CaptureTreeTimeout. Past this, the run is recorded without them.
const provenanceBudget = 15 * time.Second

// BeginOptions are begin's inputs. Zero fields default to the process's own.
type BeginOptions struct {
	// Dir is where the run is for (the cwd); the run directory goes under
	// ProjectRoot, else the git checkout around Dir, else Dir.
	Dir         string
	ProjectRoot string
	// Base is a revision the run's change is measured against (--base).
	Base         string
	ForgeVersion string
	Getenv       func(string) string
	Now          func() time.Time
	// Provenance reads the checkout (release.CaptureProvenance).
	Provenance func(ctx context.Context, dir string) (release.Provenance, error)
	// ResolveRev turns Base into a commit (git rev-parse).
	ResolveRev func(ctx context.Context, dir, rev string) (string, error)
}

func (o *BeginOptions) defaults() {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Dir == "" {
		o.Dir, _ = os.Getwd()
	}
	if o.Provenance == nil {
		o.Provenance = func(ctx context.Context, dir string) (release.Provenance, error) {
			return release.CaptureProvenance(ctx, dir, release.CaptureOptions{Host: release.HostID()})
		}
	}
	if o.ResolveRev == nil {
		o.ResolveRev = gitResolveRev
	}
}

// BeginResult is the run directory begin settled on.
type BeginResult struct {
	// Dir is absolute, and always usable: when the run could not be
	// recorded it is a scratch directory, so a lane that writes its
	// coverage into it still runs.
	Dir string
	// Recording is false when Dir is that scratch directory.
	Recording bool
	// Reused: Dir came from EnvRun and already held a run.
	Reused   bool
	Warnings []string
}

// Begin allocates a run directory under the project's .forge/tests/runs/ and
// writes its run.json, or reuses the run EnvRun names. It never fails: what it
// cannot record it warns about, and a run it cannot create at all falls back
// to a scratch directory, because begin runs in front of a test lane and must
// never be the reason one fails.
func Begin(ctx context.Context, opts BeginOptions) BeginResult {
	if ctx == nil {
		ctx = context.Background()
	}
	opts.defaults()
	if Off(opts.Getenv) {
		return scratchRun(nil)
	}
	if dir := strings.TrimSpace(opts.Getenv(EnvRun)); dir != "" {
		abs, err := filepath.Abs(dir)
		if err == nil {
			if _, err := os.Stat(filepath.Join(abs, testreport.RunManifestFile)); err == nil {
				return BeginResult{Dir: abs, Recording: true, Reused: true}
			}
			// The caller chose the directory: create the run there.
			res := createRun(ctx, opts, abs)
			if res.Recording {
				return res
			}
			return scratchRun(res.Warnings)
		}
		return scratchRun([]string{fmt.Sprintf("%s=%q: %v", EnvRun, dir, err)})
	}
	res := createRun(ctx, opts, "")
	if res.Recording {
		return res
	}
	return scratchRun(res.Warnings)
}

// createRun writes a new run. dir "" allocates <root>/.forge/tests/runs/<ulid>.
func createRun(ctx context.Context, opts BeginOptions, dir string) BeginResult {
	var warnings []string
	warn := func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }

	now := opts.Now().UTC()
	m := testreport.RunManifest{
		Schema:       testreport.RunManifestSchema,
		ID:           ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String(),
		CreatedAt:    now,
		ForgeVersion: opts.ForgeVersion,
		Lanes:        []string{},
	}
	if dir != "" {
		// A directory named for a run keeps its name as the id.
		if id, err := ulid.ParseStrict(filepath.Base(dir)); err == nil {
			m.ID = id.String()
		}
	}

	pctx, cancel := context.WithTimeout(ctx, provenanceBudget)
	p, err := opts.Provenance(pctx, opts.Dir)
	cancel()
	switch {
	case err == nil:
	case errors.Is(err, release.ErrUnhashed):
		warn("tree hash not recorded: %v", err)
	default:
		warn("git facts not recorded: %v", err)
	}
	m.Commit, m.TreeHash, m.Branch, m.Repo, m.Host = p.Commit, p.Tree, p.Branch, p.Repo, p.Worktree.Host
	// Outside git there is no commit to vouch for the content: it is dirty
	// by definition.
	m.Dirty = p.Dirty || p.Commit == ""

	ci := detectCI(opts.Getenv)
	m.CI = ci.ci
	if m.Branch == "" {
		m.Branch = ci.branch
	}
	m.BaseSHA = ci.base
	if opts.Base != "" {
		top := p.Worktree.Path
		if top == "" {
			top = opts.Dir
		}
		if sha, err := opts.ResolveRev(ctx, top, opts.Base); err == nil {
			m.BaseSHA = sha
		} else {
			warn("--base %s: %v", opts.Base, err)
		}
	}

	root := opts.ProjectRoot
	if root == "" {
		root = p.Worktree.Path
	}
	if root == "" {
		root = opts.Dir
	}
	m.Project = projectName(root)

	if dir == "" {
		runs := testreport.RunsRoot(root)
		dir = filepath.Join(runs, m.ID)
		ignoreRunsDir(filepath.Dir(runs))
	}
	m.Warnings = warnings
	if err := writeRunManifest(dir, m); err != nil {
		warn("cannot create the run in %s: %v", dir, err)
		return BeginResult{Warnings: warnings}
	}
	return BeginResult{Dir: dir, Recording: true, Warnings: warnings}
}

// scratchRun is the directory a lane writes into when nothing is recorded.
func scratchRun(warnings []string) BeginResult {
	dir, err := os.MkdirTemp("", "forge-test-run-")
	if err != nil {
		// Even this failed; a path that does not exist is still a
		// path a lane can be given, and capture creates lane dirs.
		dir = filepath.Join(os.TempDir(), "forge-test-run")
		warnings = append(warnings, fmt.Sprintf("no scratch directory: %v", err))
	}
	return BeginResult{Dir: dir, Warnings: warnings}
}

func writeRunManifest(dir string, m testreport.RunManifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := marshalManifest(m)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, testreport.RunManifestFile), data, 0o644)
}

// marshalManifest is a manifest as written: indented, and with a command's
// < > & left readable rather than \u-escaped.
func marshalManifest(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ignoreRunsDir keeps the records out of git whatever the project ignores:
// .forge/tests/ holds a .gitignore of "*". A scaffolded project already
// ignores .forge/*, but a run that made its own checkout dirty would record
// every later run as dirty.
func ignoreRunsDir(testsDir string) {
	path := filepath.Join(testsDir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return
	}
	if os.MkdirAll(testsDir, 0o755) == nil {
		_ = atomicfile.WriteFile(path, []byte("# forge test records: per-machine, never committed\n*\n"), 0o644)
	}
}

// projectName is forge.yaml's name, or "" outside a forge project.
func projectName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "forge.yaml"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Name string `yaml:"name"`
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		return ""
	}
	return cfg.Name
}

func gitResolveRev(ctx context.Context, dir, rev string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a commit here")
	}
	sha := strings.TrimSpace(string(out))
	if !release.ValidObjectID(sha) {
		return "", fmt.Errorf("git rev-parse reported %q", sha)
	}
	return sha, nil
}
