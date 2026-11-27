package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/gocacheprog"
	"github.com/reliant-labs/forge/internal/goexec"
)

// cacheSeedLockWait is how long `cache seed` waits for another seed of the same
// trusted tier before it gives up (and exits 0: the other seed is doing the
// work). A var so tests can shorten it.
var (
	cacheSeedLockWait = 30 * time.Minute
	cacheSeedLockPoll = 2 * time.Second
)

// cacheSeedOptions are `cache seed`'s flags.
type cacheSeedOptions struct {
	repo, rev, trusted, tags string
	race, vet, fetch, force  bool
}

// cacheSeedRecord is <trusted>/.forge-seed/<repo>.json: what the last seed of a
// repository built, so the next one with the same commit and flags can stop
// before the export (a large tree's `git archive` costs far more than a warm
// re-seed's go commands).
type cacheSeedRecord struct {
	Sha      string   `json:"sha"`
	Rev      string   `json:"rev"`
	Started  string   `json:"started"`
	Seconds  float64  `json:"seconds"`
	Modules  []string `json:"modules"`
	OK       bool     `json:"ok"`
	Race     bool     `json:"race"`
	Vet      bool     `json:"vet"`
	Tags     string   `json:"tags"`
	Broken   []string `json:"broken,omitempty"`
	Failures []string `json:"failures,omitempty"`
}

func newCacheSeedCmd() *cobra.Command {
	var o cacheSeedOptions
	cmd := &cobra.Command{
		Use:   "seed [--repo DIR] [--rev REV] [--trusted DIR] [--race] [--vet] [--tags T] [--fetch] [--force]",
		Short: "Compile one commit of a repository into the machine's trusted Go cache tier",
		Long: "Fills the trusted tier with every package and test variant of one commit, as a trusted build:\n" +
			"run it on each merge to main, as the user that owns the trusted directory.\n\n" +
			"It exports exactly that commit's tree (git archive, never the working tree: an untracked .go\n" +
			"file would land in the trusted tier) into a temp directory, then, in each Go module of the\n" +
			"export (go.mod files outside testdata, vendor and node_modules), runs\n" +
			"`go list -e -export -deps -test ./...`: it compiles every package and test variant without\n" +
			"linking or running anything (a test binary would run TestMain). --vet adds `go vet ./...`,\n" +
			"--race the same compile under -race.\n\n" +
			"The go commands run with GOCACHE=<trusted>, GOFLAGS=-trimpath (the flags every untrusted build\n" +
			"uses, so the keys match), GOENV=off (a GOCACHEPROG in go's env file must not route the seed\n" +
			"through the helper) and no GOCACHEPROG; module settings (GOMODCACHE, GOPROXY, GOPRIVATE, ...)\n" +
			"are kept, whether set in the environment or in go's env file.\n\n" +
			"Seeds of one trusted tier queue on <trusted>/.forge-seed.lock (30 minutes, then this one is\n" +
			"skipped). --rev is resolved after the lock is taken, so a queued seed builds the newest commit.\n" +
			"A commit already seeded with the same flags is skipped (--force rebuilds it).\n" +
			"The trusted directory is refused if it is group- or world-writable or owned by another user.",
		Example: "  forge cache seed --repo ~/src/forge\n" +
			"  forge cache seed --rev HEAD --trusted /tmp/trusted --vet",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCacheSeed(o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.repo, "repo", "", "the git repository (default: the current directory's git toplevel)")
	f.StringVar(&o.rev, "rev", "origin/main", "the commit to seed, resolved after the seed lock is taken")
	f.StringVar(&o.trusted, "trusted", "", "the trusted tier (default $"+gocacheprog.TrustedEnv+", else "+gocacheprog.DefaultTrustedDir+")")
	f.BoolVar(&o.race, "race", false, "also compile everything under -race")
	f.BoolVar(&o.vet, "vet", false, "also run go vet ./... (caches its analysis facts)")
	f.StringVar(&o.tags, "tags", "", "build tags, passed as -tags to every go command")
	f.BoolVar(&o.fetch, "fetch", false, "run git fetch origin main before resolving --rev")
	f.BoolVar(&o.force, "force", false, "seed even when this commit is already recorded as seeded with these flags")
	return cmd
}

func runCacheSeed(o cacheSeedOptions, out io.Writer) error {
	if o.repo == "" {
		top, err := cacheGit("", "rev-parse", "--show-toplevel")
		if err != nil {
			return fmt.Errorf("--repo not given and the current directory is not in a git repository: %w", err)
		}
		o.repo = top
	}
	repo, err := filepath.Abs(o.repo)
	if err != nil {
		return err
	}
	trusted, err := ensureTrustedCacheDir(gocacheprog.TrustedDirForSeed(o.trusted, os.Getenv))
	if err != nil {
		return err
	}

	release, ok, err := acquireCacheSeedLock(trusted, out)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(out, "another seed of %s held the lock for %s; skipped\n", trusted, cacheSeedLockWait)
		return nil
	}
	defer release()

	if o.fetch {
		if _, err := cacheGit(repo, "fetch", "origin", "main"); err != nil {
			return fmt.Errorf("git fetch origin main: %w", err)
		}
	}
	sha, err := cacheGit(repo, "rev-parse", "--verify", "--quiet", o.rev+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve %s in %s: %w", o.rev, repo, err)
	}
	recordPath := filepath.Join(trusted, ".forge-seed", filepath.Base(repo)+".json")
	if !o.force && cacheAlreadySeeded(recordPath, sha, o) {
		fmt.Fprintf(out, "already seeded %s\n", sha)
		return nil
	}

	started := time.Now()
	rec := cacheSeedRecord{Sha: sha, Rev: o.rev, Started: started.UTC().Format(time.RFC3339), Race: o.race, Vet: o.vet, Tags: o.tags}
	export, err := os.MkdirTemp("", "forge-cache-seed-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(export) }()
	if err := gitExportTree(repo, sha, export); err != nil {
		return err
	}
	modules, err := cacheSeedModules(export)
	if err != nil {
		return err
	}
	fileVals, err := cacheSeedFileSettings(goexec.Env(), goEnvSettings)
	if err != nil {
		return err
	}
	for _, mod := range modules {
		rec.Modules = append(rec.Modules, mod)
		dir := filepath.Join(export, filepath.FromSlash(mod))
		for _, step := range cacheSeedSteps(o) {
			broken, err := runCacheSeedStep(dir, trusted, fileVals, step)
			for _, b := range broken {
				rec.Broken = append(rec.Broken, mod+": "+b)
			}
			if err != nil {
				rec.Failures = append(rec.Failures, fmt.Sprintf("%s: go %s: %v", mod, strings.Join(step, " "), err))
			}
		}
	}
	rec.Seconds = time.Since(started).Seconds()
	rec.OK = len(rec.Failures) == 0
	if err := writeCacheSeedRecord(recordPath, rec); err != nil {
		fmt.Fprintf(out, "warning: record the seed: %v\n", err)
	}

	fmt.Fprintf(out, "seeded %s @ %s into %s: %d module(s) in %.0fs\n", filepath.Base(repo), cacheShortSha(sha), trusted, len(modules), rec.Seconds)
	for _, b := range rec.Broken {
		fmt.Fprintf(out, "  broken package (seeded what compiled): %s\n", b)
	}
	for _, f := range rec.Failures {
		fmt.Fprintf(out, "  failed: %s\n", f)
	}
	if !rec.OK {
		return fmt.Errorf("seed of %s @ %s: %d go command(s) failed", filepath.Base(repo), cacheShortSha(sha), len(rec.Failures))
	}
	return nil
}

// cacheSeedSteps are the go commands run in each module (without the leading
// "go"). -f prints only broken packages, so a seed can report them.
func cacheSeedSteps(o cacheSeedOptions) [][]string {
	const brokenOnly = `{{if .Error}}{{.ImportPath}}: {{.Error}}{{end}}`
	var tags []string
	if o.tags != "" {
		tags = []string{"-tags=" + o.tags}
	}
	list := func(extra ...string) []string {
		s := append([]string{"list", "-e", "-export", "-deps", "-test"}, tags...)
		s = append(s, extra...)
		return append(s, "-f", brokenOnly, "./...")
	}
	steps := [][]string{list()}
	if o.vet {
		steps = append(steps, append(append([]string{"vet"}, tags...), "./..."))
	}
	if o.race {
		steps = append(steps, list("-race"))
	}
	return steps
}

// runCacheSeedStep runs one go command in dir and returns the broken packages it
// named (go list's -f output) and its failure, if any. Its environment is
// goexec.Env (the caller's, with -mod=mod scrubbed) made trusted by
// cacheSeedChildEnv; fileVals are cacheSeedFileSettings'.
func runCacheSeedStep(dir, trusted string, fileVals map[string]string, args []string) ([]string, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = cacheSeedChildEnv(goexec.Env(), trusted, fileVals)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var broken []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			broken = append(broken, line)
		}
	}
	if err != nil {
		return broken, fmt.Errorf("%w: %s", err, cacheTailLines(stderr.String(), 5))
	}
	return broken, nil
}

// cacheSeedEnvPassThrough are the go settings a seed keeps from the user's go env
// file when the environment does not set them: GOENV=off drops the file, and
// with it any GOCACHEPROG, but module downloads must still go where they
// always do.
var cacheSeedEnvPassThrough = []string{
	"GOMODCACHE", "GOPROXY", "GOPRIVATE", "GONOPROXY", "GONOSUMDB",
	"GOSUMDB", "GOINSECURE", "GOTOOLCHAIN", "GOAUTH", "GOVCS",
}

// cacheSeedDropped are the variables a seed's go commands never inherit: each
// would route the build through the helper (GOCACHEPROG, and the go env file
// GOENV names) or change where it writes and how its keys are computed.
var cacheSeedDropped = map[string]bool{
	"GOCACHE": true, "GOCACHEPROG": true, "GOFLAGS": true, "GOENV": true,
	gocacheprog.TrustedEnv: true, gocacheprog.StatsEnv: true,
}

// cacheSeedFileSettings reads, once per seed, the pass-through settings base
// does not set, as go resolves them from its env file (fromFile:
// goEnvSettings).
func cacheSeedFileSettings(base []string, fromFile func([]string) (map[string]string, error)) (map[string]string, error) {
	set := map[string]bool{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		set[k] = true
	}
	var missing []string
	for _, k := range cacheSeedEnvPassThrough {
		if !set[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	vals, err := fromFile(missing)
	if err != nil {
		return nil, fmt.Errorf("read go env: %w", err)
	}
	out := map[string]string{}
	for _, k := range missing {
		if v := vals[k]; v != "" {
			out[k] = v
		}
	}
	return out, nil
}

// cacheSeedChildEnv is the environment of a seed's go commands: base (the
// caller's, through goexec.Env) without cacheSeedDropped, plus fileVals,
// GOCACHE=<trusted>, GOENV=off and GOFLAGS=-trimpath. WithTrimpath finds the
// -trimpath already there, so GOFLAGS stays exactly that: the flags every
// untrusted build uses, so their keys match.
func cacheSeedChildEnv(base []string, trusted string, fileVals map[string]string) []string {
	var env []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !cacheSeedDropped[k] {
			env = append(env, kv)
		}
	}
	for _, k := range cacheSeedEnvPassThrough {
		if v, ok := fileVals[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "GOCACHE="+trusted, "GOENV=off", "GOFLAGS=-trimpath")
	return goexec.WithTrimpath(env)
}

// goEnvSettings asks go for keys as the user's settings resolve them (the
// environment, then go's env file, then $GOROOT/go.env).
func goEnvSettings(keys []string) (map[string]string, error) {
	cmd := exec.Command("go", append([]string{"env", "-json"}, keys...)...)
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	vals := map[string]string{}
	if err := json.Unmarshal(out, &vals); err != nil {
		return nil, err
	}
	return vals, nil
}

// ensureTrustedCacheDir creates dir 0755 when it is missing and refuses one
// another user could have written (gocacheprog.CheckTrustedOwnership).
func ensureTrustedCacheDir(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		dir = abs
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", fmt.Errorf("create the trusted cache's parent: %w", err)
		}
		if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create the trusted cache: %w", err)
		} else if err == nil {
			if err := os.Chmod(dir, 0o755); err != nil { // past the umask
				return "", err
			}
		}
	}
	if err := gocacheprog.CheckTrustedOwnership(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// acquireCacheSeedLock takes <trusted>/.forge-seed.lock, waiting up to
// cacheSeedLockWait. ok is false when the wait ran out.
func acquireCacheSeedLock(trusted string, out io.Writer) (release func(), ok bool, err error) {
	path := filepath.Join(trusted, ".forge-seed.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("open the seed lock: %w", err)
	}
	deadline := time.Now().Add(cacheSeedLockWait)
	announced := false
	for {
		got, err := tryLockFile(f)
		if err != nil {
			_ = f.Close()
			return nil, false, fmt.Errorf("lock %s: %w", path, err)
		}
		if got {
			return func() { _ = unlockFile(f); _ = f.Close() }, true, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, false, nil
		}
		if !announced {
			fmt.Fprintf(out, "waiting for another seed of %s (%s)\n", trusted, path)
			announced = true
		}
		time.Sleep(cacheSeedLockPoll)
	}
}

// cacheAlreadySeeded reports whether the record at path says sha was seeded, in
// full, with o's flags.
func cacheAlreadySeeded(path, sha string, o cacheSeedOptions) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var rec cacheSeedRecord
	if json.Unmarshal(b, &rec) != nil {
		return false
	}
	return rec.OK && rec.Sha == sha && rec.Race == o.race && rec.Vet == o.vet && rec.Tags == o.tags
}

func writeCacheSeedRecord(path string, rec cacheSeedRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(b, '\n'), 0o644)
}

// gitExportTree writes exactly the tree of sha into dir: `git archive | tar -x`.
func gitExportTree(repo, sha, dir string) error {
	archive := exec.Command("git", "-C", repo, "archive", "--format=tar", sha)
	untar := exec.Command("tar", "-x", "-f", "-", "-C", dir)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	untar.Stdin = pipe
	var aerr, terr bytes.Buffer
	archive.Stderr = &aerr
	untar.Stderr = &terr
	if err := untar.Start(); err != nil {
		return fmt.Errorf("start tar: %w", err)
	}
	if err := archive.Run(); err != nil {
		_ = untar.Wait()
		return fmt.Errorf("git archive %s: %w: %s", cacheShortSha(sha), err, strings.TrimSpace(aerr.String()))
	}
	if err := untar.Wait(); err != nil {
		return fmt.Errorf("extract %s: %w: %s", cacheShortSha(sha), err, strings.TrimSpace(terr.String()))
	}
	return nil
}

// cacheSeedModules lists the slash-separated directories (relative to root; "."
// first) that hold a go.mod, outside the trees go itself never builds as
// modules here: testdata, vendor, node_modules, and dot or underscore dirs.
func cacheSeedModules(root string) ([]string, error) {
	var mods []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "testdata" || name == "vendor" || name == "node_modules" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			mods = append(mods, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Slice(mods, func(i, j int) bool {
		if (mods[i] == ".") != (mods[j] == ".") {
			return mods[i] == "."
		}
		return mods[i] < mods[j]
	})
	return mods, err
}

func cacheGit(dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func cacheShortSha(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func cacheTailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
