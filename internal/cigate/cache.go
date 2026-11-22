package cigate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/goexec"
)

// The verdict cache remembers which checks PASSED on which tree, so a gate
// that judges a tree it has already judged does not redo the work: the same
// commit in a second worktree, a re-run after fixing the one check that
// failed, a rebase that changed nothing a check reads. One branch was measured
// running five full gates (~8 h) over largely identical trees.
//
// A verdict is keyed by everything it depends on:
//
//   - the tree: HEAD^{tree}, and the tree is required to be CLEAN — a dirty
//     tree turns the cache off for the run, because its content has no hash;
//   - the check: its name, argv, directory, environment and pinned tools;
//   - the judge: this forge binary's identity, the versions forge pins, the
//     Go toolchain the project selects, node's version when there are
//     frontends, and the environment that changes what Go builds (GOFLAGS…);
//   - inputs outside the tree the build reads anyway: a go.mod `replace` or a
//     go.work `use` of a sibling checkout, a frontend's `file:` dependency.
//     Each must itself be a clean git tree, whose tree hash joins the key;
//     anything else turns the cache off for the run.
//
// Only PASS is stored, and only when the run ends on the same clean tree it
// started on. A check whose verdict depends on state no hash covers (the
// network, today's advisories) declares NoCache (`cache: false`).
//
// The store is a directory of small files under the user cache dir, one
// namespace per repository (its git common dir), so every worktree of the
// repository shares it.

// NoCacheEnv turns the verdict cache off for one run.
const NoCacheEnv = "CI_NO_CACHE"

// CacheDirEnv relocates the verdict cache.
const CacheDirEnv = "FORGE_CI_CACHE_DIR"

// verdictTTL bounds the store: entries older than this are pruned when the
// gate writes new ones.
const verdictTTL = 30 * 24 * time.Hour

// CacheInput is what OpenVerdictCache keys verdicts on, beyond the tree.
type CacheInput struct {
	// Root is the project root (a git work tree).
	Root string
	// Dir is the cache root ($FORGE_CI_CACHE_DIR); empty is
	// <user cache dir>/forge/ci-verdicts.
	Dir string
	// Forge identifies the judging forge binary.
	Forge string
	// Frontends are the project-relative frontend directories, whose
	// package.json `file:` / `link:` dependencies are outside inputs.
	Frontends []string
	// Env is the gate's base environment.
	Env []string
}

// VerdictCache is an open cache for one gate run. It implements Verdicts.
type VerdictCache struct {
	dir    string
	root   string
	tree   string
	commit string
	salt   string

	mu      sync.Mutex
	pending []verdictRecord
}

type verdictRecord struct {
	Key    string    `json:"key"`
	Check  string    `json:"check"`
	Tree   string    `json:"tree"`
	Commit string    `json:"commit"`
	At     time.Time `json:"at"`
}

// buildEnvKeys are the variables that change what Go builds and tests, so a
// verdict recorded under one setting does not answer for another.
var buildEnvKeys = []string{
	"CGO_ENABLED", "GOAMD64", "GOARCH", "GOARM", "GOARM64", "GOEXPERIMENT",
	"GOFLAGS", "GOOS", "GOTOOLCHAIN", "GOWORK", "NODE_ENV", "NODE_OPTIONS",
}

// OpenVerdictCache opens the cache for the tree at in.Root. It returns nil
// and the reason, in a few words, when this run cannot use one: not a git
// work tree, uncommitted changes, an outside input that is not a clean git
// tree.
func OpenVerdictCache(ctx context.Context, in CacheInput) (*VerdictCache, string) {
	git := func(dir string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	status, err := git(in.Root, "status", "--porcelain")
	if err != nil {
		return nil, "not a git work tree"
	}
	if status != "" {
		n := strings.Count(status, "\n") + 1
		return nil, fmt.Sprintf("the working tree has %d uncommitted path(s); verdicts are cached only for a committed tree", n)
	}
	tree, err := git(in.Root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return nil, "no HEAD commit"
	}
	commit, _ := git(in.Root, "rev-parse", "--short", "HEAD")
	repoDir, why := repoCacheDir(ctx, in.Root, in.Dir)
	if why != "" {
		return nil, why
	}

	salt := sha256.New()
	fmt.Fprintf(salt, "forge=%s\n", in.Forge)
	for _, t := range []Tool{golangciLint, BufTool(in.Root), govulncheck, goLicenses} {
		fmt.Fprintf(salt, "pin=%s@%s\n", t.Binary, t.Version)
	}
	fmt.Fprintf(salt, "go=%s\n", toolVersion(ctx, in.Root, in.Env, "go", "env", "GOVERSION"))
	if len(in.Frontends) > 0 {
		fmt.Fprintf(salt, "node=%s\n", toolVersion(ctx, in.Root, in.Env, "node", "--version"))
	}
	for _, k := range buildEnvKeys {
		fmt.Fprintf(salt, "env:%s=%s\n", k, envValue(in.Env, k))
	}
	outside, why := outsideInputs(ctx, in)
	if why != "" {
		return nil, why
	}
	for _, o := range outside {
		fmt.Fprintf(salt, "outside=%s\n", o)
	}

	return &VerdictCache{
		dir:    repoDir,
		root:   in.Root,
		tree:   tree,
		commit: commit,
		salt:   hex.EncodeToString(salt.Sum(nil)),
	}, ""
}

// repoCacheDir is this repository's directory under the gate's cache: dir
// ("" is <user cache>/forge/ci-verdicts), keyed by the git common dir, so
// every worktree of one repository shares it.
func repoCacheDir(ctx context.Context, root, dir string) (string, string) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = root
	out, err := cmd.Output()
	common := strings.TrimSpace(string(out))
	if err != nil || common == "" {
		return "", "cannot read the repository's git dir"
	}
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", "no user cache dir"
		}
		dir = filepath.Join(base, "forge", "ci-verdicts")
	}
	repo := sha256.Sum256([]byte(filepath.Clean(common)))
	return filepath.Join(dir, hex.EncodeToString(repo[:8])), ""
}

// Commit is the short commit the cache keys this run on.
func (v *VerdictCache) Commit() string { return v.commit }

// Key is the cache key of c on this tree.
func (v *VerdictCache) Key(c Check) string {
	h := sha256.New()
	fmt.Fprintf(h, "forge-ci-verdict v1\ntree=%s\nsalt=%s\nname=%s\ndir=%s\n", v.tree, v.salt, c.Name, c.Dir)
	for _, a := range c.Argv {
		fmt.Fprintf(h, "arg=%s\n", a)
	}
	env := append([]string(nil), c.Env...)
	sort.Strings(env)
	for _, e := range env {
		fmt.Fprintf(h, "env=%s\n", e)
	}
	for _, t := range c.Tools {
		fmt.Fprintf(h, "tool=%s@%s\n", t.Binary, t.Version)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Lookup implements Verdicts.
func (v *VerdictCache) Lookup(c Check) (string, bool) {
	data, err := os.ReadFile(filepath.Join(v.dir, v.Key(c)+".json"))
	if err != nil {
		return "", false
	}
	var rec verdictRecord
	if json.Unmarshal(data, &rec) != nil || rec.Tree != v.tree {
		return "", false
	}
	return rec.Commit, true
}

// Record implements Verdicts. Nothing is written until Flush.
func (v *VerdictCache) Record(c Check) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pending = append(v.pending, verdictRecord{Key: v.Key(c), Check: c.Name, Tree: v.tree, Commit: v.commit, At: time.Now().UTC()})
}

// Flush stores the PASSes recorded this run — but only if the tree is still
// clean and still the tree the run started on: a check that rewrote tracked
// files, or a commit made mid-run, would otherwise file verdicts under a tree
// they were not earned on. It reports how many it stored, and why none were
// when the tree moved.
func (v *VerdictCache) Flush(ctx context.Context) (int, string) {
	v.mu.Lock()
	pending := v.pending
	v.pending = nil
	v.mu.Unlock()
	if len(pending) == 0 {
		return 0, ""
	}
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = v.root
	if out, err := cmd.Output(); err != nil || len(bytes.TrimSpace(out)) > 0 {
		return 0, "the working tree changed during the run"
	}
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "HEAD^{tree}")
	cmd.Dir = v.root
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != v.tree {
		return 0, "HEAD moved during the run"
	}
	if err := os.MkdirAll(v.dir, 0o755); err != nil {
		return 0, err.Error()
	}
	stored := 0
	for _, rec := range pending {
		data, _ := json.Marshal(rec)
		tmp, err := os.CreateTemp(v.dir, ".verdict-*")
		if err != nil {
			continue
		}
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(v.dir, rec.Key+".json")) != nil {
			_ = os.Remove(tmp.Name())
			continue
		}
		stored++
	}
	v.prune()
	return stored, ""
}

// prune drops entries past verdictTTL. Best effort.
func (v *VerdictCache) prune() {
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-verdictTTL)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(v.dir, e.Name()))
		}
	}
}

func toolVersion(ctx context.Context, dir string, env []string, argv ...string) string {
	if env == nil {
		env = goexec.Env()
	}
	bin, err := lookPathIn(env, argv[0])
	if err != nil {
		return "absent"
	}
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func envValue(env []string, key string) string {
	if env == nil {
		return os.Getenv(key)
	}
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}

// outsideInputs pins every input outside the tree that a check reads anyway:
// local go.mod replaces and go.work uses, and frontends' file:/link:
// dependencies. Each pins to "<path>=<git tree hash>"; a non-git or dirty one
// returns a reason the cache must stay off.
func outsideInputs(ctx context.Context, in CacheInput) ([]string, string) {
	root, _ := filepath.Abs(in.Root)
	dirs := map[string]string{} // abs dir -> what declared it
	add := func(dir, from string) {
		if !filepath.IsAbs(dir) {
			return
		}
		dir = filepath.Clean(dir)
		if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return // inside the tree: its hash already covers it
		}
		if _, seen := dirs[dir]; !seen {
			dirs[dir] = from
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		if mf, err := modfile.Parse("go.mod", data, nil); err == nil {
			for _, r := range mf.Replace {
				if modfile.IsDirectoryPath(r.New.Path) {
					add(absFrom(root, r.New.Path), "go.mod replace "+r.Old.Path)
				}
			}
		}
	}
	var pins []string
	if work := goWork(ctx, root, in.Env); work != "" {
		data, err := os.ReadFile(work)
		if err != nil {
			return nil, "cannot read " + work
		}
		sum := sha256.Sum256(data)
		pins = append(pins, "go.work="+hex.EncodeToString(sum[:8]))
		if wf, err := modfile.ParseWork(work, data, nil); err == nil {
			for _, u := range wf.Use {
				add(absFrom(filepath.Dir(work), u.Path), "go.work use")
			}
			for _, r := range wf.Replace {
				if modfile.IsDirectoryPath(r.New.Path) {
					add(absFrom(filepath.Dir(work), r.New.Path), "go.work replace "+r.Old.Path)
				}
			}
		}
	}
	for _, fe := range in.Frontends {
		feDir := filepath.Join(root, fe)
		data, err := os.ReadFile(filepath.Join(feDir, "package.json"))
		if err != nil {
			continue
		}
		var pkg map[string]json.RawMessage
		if json.Unmarshal(data, &pkg) != nil {
			continue
		}
		for _, field := range []string{"dependencies", "devDependencies", "optionalDependencies"} {
			var deps map[string]string
			if json.Unmarshal(pkg[field], &deps) != nil {
				continue
			}
			for name, spec := range deps {
				for _, prefix := range []string{"file:", "link:"} {
					if p, ok := strings.CutPrefix(spec, prefix); ok {
						add(absFrom(feDir, p), fe+"/package.json "+name)
					}
				}
			}
		}
	}
	rest, why := pinDirs(ctx, dirs)
	return append(pins, rest...), why
}

func pinDirs(ctx context.Context, dirs map[string]string) ([]string, string) {
	keys := make([]string, 0, len(dirs))
	for d := range dirs {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	var out []string
	for _, d := range keys {
		hash, why := cleanTreeHash(ctx, d)
		if why != "" {
			return nil, fmt.Sprintf("%s (%s) %s, so no hash covers it", d, dirs[d], why)
		}
		out = append(out, d+"="+hash)
	}
	return out, ""
}

// cleanTreeHash is the git tree hash of dir at HEAD in its own repository,
// when dir has no uncommitted changes.
func cleanTreeHash(ctx context.Context, dir string) (string, string) {
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	prefix, err := git("rev-parse", "--show-prefix")
	if err != nil {
		return "", "is not in a git work tree"
	}
	if st, err := git("status", "--porcelain", "--", "."); err != nil || st != "" {
		return "", "has uncommitted changes"
	}
	hash, err := git("rev-parse", "HEAD:"+prefix)
	if err != nil || hash == "" {
		return "", "is not committed"
	}
	return hash, ""
}

func goWork(ctx context.Context, root string, env []string) string {
	if v := envValue(env, "GOWORK"); v == "off" {
		return ""
	}
	w := toolVersion(ctx, root, env, "go", "env", "GOWORK")
	if w == "" || w == "off" || w == "absent" || w == "unknown" {
		return ""
	}
	return w
}

func absFrom(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, filepath.FromSlash(p))
}
