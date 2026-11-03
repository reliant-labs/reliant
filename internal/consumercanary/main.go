// Command consumercanary builds forge's consumers (reliant, control-plane)
// against the forge checkout under test. Run it with
// `go run ./internal/consumercanary` or `task canary`; it is CI plumbing, no
// part of the forge binary.
//
//	consumercanary [-forge .] [-base origin/main] [-consumer name=source[@ref]]...
//	               [-cache DIR] [-force] [-plan] [-no-test]
//
// WHY. forge may break its consumers' build freely (it is pre-1.0), but the
// PR that does so pays for it: fixes forge, or ships the codemod. Without this
// check the cost surfaces at pin time, on whoever pins next (research/pinning,
// failure classes F1/F2). This runs the consumers' own build against the PR's
// forge, on the PR.
//
// WHAT. For each consumer, at its main: `go build ./...`, `go vet ./...`, and
// `go test -short` of the packages that import a forge package the diff
// changed. The consumer is never modified and never committed: its tree is
// exported (git archive) into a scratch directory and pointed at the PR's
// forge with a throwaway `-modfile` that carries a `replace`; its own go.mod
// and go.sum are untouched.
//
// WHEN. Only when the diff can reach a consumer's build (internal/forgereach:
// a non-test Go file under pkg/ or a package pkg/ imports, or go.mod/go.sum).
// A CLI-only or docs-only diff exits in seconds. A pass is cached by the hash
// of the forge sources that matter and the consumer's commit.
//
// BLAME. A step that fails is re-run once against forge at -base (the
// revision the diff is judged against). Failures present there too predate
// the change (the consumer is red on its own, or forge's main already broke
// it): reported, not blamed on the PR. Only failures the change adds fail the
// run. With -force there is no diff and no baseline: every failure counts.
//
// SOURCES. A consumer source is a git URL or a local path (any repository
// with the commit; its working tree is never read), and the ref defaults to
// main. A URL is fetched shallow (--depth 1) into the cache. Credentials are
// git's own: for a private repository set GIT_SSH_COMMAND or use a credential
// helper (the CI workflow writes a deploy key; see dev-docs/consumer-canary.md).
//
//forge:exclude-contract: a CI command, no collaborator to substitute
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/forgereach"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/hostslot"
)

// defaultConsumers are the repositories whose build forge keeps green.
var defaultConsumers = []string{
	"reliant=https://github.com/reliant-labs/reliant.git",
	"control-plane=https://github.com/reliant-labs/control-plane.git",
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	os.Exit(realMain())
}

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "consumercanary:", err)
		return 1
	}
	return 0
}

// Consumer is one repository forge must keep building.
type Consumer struct {
	Name   string
	Source string
	Ref    string
}

// ParseConsumer reads name=source[@ref]. The ref follows the LAST '@' that
// comes after the last '/' or ':' of the source, so git@github.com:org/repo.git
// and https://host/org/repo.git@v1 both parse.
func ParseConsumer(spec string) (Consumer, error) {
	name, src, ok := strings.Cut(spec, "=")
	if !ok || name == "" || src == "" {
		return Consumer{}, fmt.Errorf("consumer %q: want name=source[@ref]", spec)
	}
	c := Consumer{Name: name, Source: src, Ref: "main"}
	if i := strings.LastIndex(src, "@"); i > 0 && i > strings.LastIndexAny(src, "/:") {
		c.Source, c.Ref = src[:i], src[i+1:]
	}
	if c.Source == "" || c.Ref == "" {
		return Consumer{}, fmt.Errorf("consumer %q: empty source or ref", spec)
	}
	return c, nil
}

func isURL(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(s, "git@")
}

type options struct {
	forge     string
	base      string
	consumers []Consumer
	cache     string
	force     bool
	plan      bool
	noTest    bool
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("consumercanary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	forge := fs.String("forge", ".", "the forge checkout under test")
	base := fs.String("base", "origin/main", "git revision the diff is judged against (CI: HEAD^1)")
	cache := fs.String("cache", "", "cache directory (default: the user cache dir)")
	force := fs.Bool("force", false, "run even when the diff cannot reach a consumer")
	plan := fs.Bool("plan", false, "print what would run and stop")
	noTest := fs.Bool("no-test", false, "build and vet only")
	var specs stringList
	fs.Var(&specs, "consumer", "name=source[@ref] (repeatable; default: reliant and control-plane at main)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(specs) == 0 {
		specs = defaultConsumers
	}
	o := options{base: *base, cache: *cache, force: *force, plan: *plan, noTest: *noTest}
	var err error
	if o.forge, err = filepath.Abs(*forge); err != nil {
		return err
	}
	for _, s := range specs {
		c, err := ParseConsumer(s)
		if err != nil {
			return err
		}
		o.consumers = append(o.consumers, c)
	}
	if o.cache == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = os.TempDir()
		}
		o.cache = filepath.Join(dir, "forge-consumer-canary")
	}
	return execute(ctx, o, stdout, stderr)
}

func execute(ctx context.Context, o options, stdout, stderr io.Writer) error {
	changed, err := forgereach.ChangedFiles(o.forge, o.base)
	if err != nil {
		return err
	}
	reach, err := forgereach.Compute(o.forge, o.base, changed)
	if err != nil {
		return err
	}
	if o.force && !reach.API() {
		reach.All = true
	}
	if !reach.API() {
		fmt.Fprintf(stdout, "consumercanary: skipped — the diff reaches no consumer build (%s)\n", reach.Summary())
		if len(reach.Generated) > 0 {
			fmt.Fprintln(stdout, "  note: it changes generated output; the canary does not regenerate consumers (see dev-docs/consumer-canary.md, \"What it does not cover\")")
		}
		return nil
	}
	fmt.Fprintf(stdout, "consumercanary: %s\n", reach.Summary())
	if o.plan {
		for _, c := range o.consumers {
			fmt.Fprintf(stdout, "  would build %s (%s @ %s) against %s\n", c.Name, c.Source, c.Ref, o.forge)
		}
		for _, p := range reach.Packages {
			fmt.Fprintf(stdout, "  changed: %s\n", p)
		}
		return nil
	}

	key, err := forgeKey(o.forge)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.cache, 0o755); err != nil {
		return err
	}

	// One heavy slot for the whole run: it is a whole-module build per consumer.
	slot, err := hostslot.Acquire(ctx, hostslot.Heavy, hostslot.Options{Out: stderr, Env: os.Environ(), Label: "forge consumer canary"})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(stderr, "consumercanary: running without a heavy slot: %v\n", err)
	} else {
		defer func() { _ = slot.Release() }()
	}

	var results []Result
	for _, c := range o.consumers {
		results = append(results, canary(ctx, o, c, reach, key, stdout, stderr))
	}
	return report(stdout, results)
}

// testFlags are the flags of the consumer test step: the short tier, as a pull
// request runs it.
var testFlags = []string{"-short", "-timeout", "20m"}

// Result is the outcome for one consumer.
type Result struct {
	Consumer string
	Commit   string
	Status   string // PASS, PASS (cached), PASS (failures predate this change), FAIL, ERROR
	Detail   string
	Took     time.Duration
}

func (r Result) failed() bool { return r.Status == "FAIL" || r.Status == "ERROR" }

func report(w io.Writer, rs []Result) error {
	var b strings.Builder
	b.WriteString("\n### Consumer canary\n\n| consumer | commit | result | time |\n|---|---|---|---|\n")
	failed := false
	for _, r := range rs {
		c := r.Commit
		if len(c) > 9 {
			c = c[:9]
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.Consumer, c, r.Status, r.Took.Round(time.Second))
		failed = failed || r.failed()
	}
	for _, r := range rs {
		if r.Detail != "" {
			fmt.Fprintf(&b, "\n**%s**\n\n```\n%s\n```\n", r.Consumer, r.Detail)
		}
	}
	fmt.Fprint(w, b.String())
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
			_, _ = f.WriteString(b.String())
			_ = f.Close()
		}
	}
	if failed {
		return errors.New("a consumer does not build against this forge: fix forge, or ship the `forge fix` codemod and declare the break (dev-docs/api-breaks.md)")
	}
	return nil
}

// forgeKey hashes what a consumer's build can depend on: every non-test Go
// file, go.mod and go.sum of the forge module. Test files, docs and testdata
// cannot change a consumer's build, so editing them keeps a cached pass valid.
func forgeKey(root string) (string, error) {
	h := sha256.New()
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// The go tool ignores directories that start with "." or "_"
			// (a consumer checked out as _consumers/ is not forge's source).
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			switch name {
			case "node_modules", "testdata", "web-runtime", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if name == "go.mod" || name == "go.sum" || (strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
		h.Write(b)
	}
	fmt.Fprintf(h, "go\x00%s", runtime.Version())
	return hex.EncodeToString(h.Sum(nil))[:24], nil
}

// materialize exports the consumer's tree at its ref into dst and returns the
// commit. A URL is fetched shallow into the cache; a local path is read with
// git archive, so its working tree and refs are never changed.
func materialize(ctx context.Context, o options, c Consumer, dst string) (string, error) {
	repo, rev := c.Source, c.Ref
	if !isURL(repo) {
		abs, err := filepath.Abs(repo)
		if err != nil {
			return "", err
		}
		repo = abs
	}
	if isURL(c.Source) {
		repo = filepath.Join(o.cache, "src", c.Name+".git")
		if _, err := os.Stat(repo); err != nil {
			if err := os.MkdirAll(repo, 0o755); err != nil {
				return "", err
			}
			if out, err := runCmd(ctx, "", nil, "git", "init", "--bare", "-q", repo); err != nil {
				return "", fmt.Errorf("git init: %w\n%s", err, out)
			}
		}
		if out, err := runCmd(ctx, "", nil, "git", "-C", repo, "fetch", "--depth", "1", "--no-tags", "-q", c.Source, c.Ref); err != nil {
			return "", fmt.Errorf("fetch %s %s: %w\n%s", c.Source, c.Ref, err, strings.TrimSpace(out))
		}
		rev = "FETCH_HEAD"
	}
	sha, err := forgereach.Git(repo, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s: cannot resolve %s: %w", c.Name, c.Ref, err)
	}
	commit := strings.TrimSpace(string(sha))
	if err := forgereach.ExportRev(repo, commit, dst); err != nil {
		return "", err
	}
	return commit, nil
}

func canary(ctx context.Context, o options, c Consumer, reach forgereach.Reach, key string, stdout, stderr io.Writer) (res Result) {
	start := time.Now()
	res.Consumer = c.Name
	defer func() { res.Took = time.Since(start) }()
	fail := func(status string, format string, args ...any) Result {
		res.Status, res.Detail = status, fmt.Sprintf(format, args...)
		return res
	}

	scratch, err := os.MkdirTemp("", "canary-"+c.Name+"-")
	if err != nil {
		return fail("ERROR", "%v", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	tree := filepath.Join(scratch, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		return fail("ERROR", "%v", err)
	}
	commit, err := materialize(ctx, o, c, tree)
	if err != nil {
		return fail("ERROR", "%v", err)
	}
	res.Commit = commit

	marker := filepath.Join(o.cache, "verdicts", key+"-"+c.Name+"-"+commit)
	if _, err := os.Stat(marker); err == nil {
		fmt.Fprintf(stdout, "== %s @ %.9s: PASS (cached for this forge and this consumer commit)\n", c.Name, commit)
		res.Status = "PASS (cached)"
		return res
	}

	mod, ok, err := pointAtForge(ctx, tree, scratch, "head", o.forge, reach.Module)
	switch {
	case err != nil:
		return fail("ERROR", "%v", err)
	case !ok:
		fmt.Fprintf(stdout, "== %s @ %.9s: does not depend on %s, nothing to check\n", c.Name, commit, reach.Module)
		res.Status = "PASS (no forge dependency)"
		return res
	}

	pr := consumerEnv(mod)

	base, err := baselineEnv(ctx, o, tree, scratch, reach.Module)
	if err != nil {
		return fail("ERROR", "%v", err)
	}

	r := &runner{ctx: ctx, o: o, name: c.Name, tree: tree, pr: pr, base: base, stdout: stdout}
	if st, detail := r.steps(reach); st != "" {
		return fail(st, "%s", detail)
	}
	if len(r.predating) > 0 {
		res.Status = "PASS (failures predate this change)"
		res.Detail = fmt.Sprintf("%s: %s fail with forge at %s too, so this change did not cause them.", c.Name, strings.Join(r.predating, ", "), o.base)
		return res
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err == nil {
		_ = atomicfile.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
	}
	res.Status = "PASS"
	return res
}

// runner runs one consumer's build, vet and test steps against the forge under
// test (pr) and, for a failure, against the baseline (base; nil on a forced run).
type runner struct {
	ctx       context.Context
	o         options
	name      string
	tree      string
	pr, base  []string
	stdout    io.Writer
	predating []string // steps whose failures the baseline has too
}

// steps runs build, vet and the affected tests. It returns a non-empty status
// ("FAIL" or "ERROR") and the explanation when one step breaks.
func (r *runner) steps(reach forgereach.Reach) (status, detail string) {
	if d, ok := r.step("build", "build", "./..."); !ok {
		return "FAIL", "go build ./... broke against this forge:\n" + d
	}
	if d, ok := r.step("vet", "vet", "./..."); !ok {
		return "FAIL", "go vet ./... broke against this forge:\n" + d
	}
	if r.o.noTest {
		return "", ""
	}
	pkgs, err := SelectTests(r.ctx, r.tree, r.pr, reach)
	if err != nil {
		return "ERROR", fmt.Sprintf("select tests: %v", err)
	}
	if len(pkgs) == 0 {
		fmt.Fprintf(r.stdout, "-- test: no %s package imports a changed forge package\n", r.name)
		return "", ""
	}
	fmt.Fprintf(r.stdout, "-- test: %d package(s) import a changed forge package\n", len(pkgs))
	if d, ok := r.step("test", append(append([]string{"test"}, testFlags...), pkgs...)...); !ok {
		return "FAIL", "go test -short broke against this forge:\n" + d
	}
	return "", ""
}

func (r *runner) keep(step, which, out string) string {
	dir := filepath.Join(r.o.cache, "logs")
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, r.name+"-"+step+"-"+which+".log")
	_ = atomicfile.WriteFile(p, []byte(out), 0o644)
	return p
}

// step runs `go <args...>` against the forge under test. On failure it re-runs
// against the baseline (a test step only the packages that failed); it blames
// the change only for failure lines the baseline does not have.
func (r *runner) step(name string, args ...string) (detail string, ok bool) {
	t := time.Now()
	fmt.Fprintf(r.stdout, "-- %s: go %s\n", name, strings.Join(args, " "))
	out, err := runCmd(r.ctx, r.tree, r.pr, "go", args...)
	took := time.Since(t).Round(time.Second)
	if err == nil {
		fmt.Fprintf(r.stdout, "   ok (%s)\n", took)
		return "", true
	}
	prLog := r.keep(name, "forge-under-test", out)
	if r.base == nil {
		fmt.Fprintf(r.stdout, "   FAILED (%s), full output %s\n", took, prLog)
		return tail(failureSummary(out), 80), false
	}
	fmt.Fprintf(r.stdout, "   FAILED (%s), full output %s; comparing with forge at %s\n", took, prLog, r.o.base)
	baseArgs := args
	if name == "test" {
		if failed := failedPackages(out); len(failed) > 0 {
			baseArgs = append(append([]string{"test"}, testFlags...), failed...)
		}
	}
	baseOut, berr := runCmd(r.ctx, r.tree, r.base, "go", baseArgs...)
	baseLog := r.keep(name, "base", baseOut)
	if berr == nil {
		return tail(failureSummary(out), 80), false
	}
	fresh := NewFailures(out, baseOut)
	if len(fresh) == 0 && r.ctx.Err() == nil {
		r.predating = append(r.predating, name)
		fmt.Fprintf(r.stdout, "   the same failures occur with forge at %s (%s): not caused by this change\n", r.o.base, baseLog)
		return "", true
	}
	return tail(strings.Join(fresh, "\n"), 80), false
}

// baselineEnv is the go environment that builds the consumer against forge at
// the revision the diff is judged against, so a failure is blamed on the change
// only when the base forge did not have it: a consumer that is already red on
// its own, or already broken by forge's main, does not turn an unrelated pull
// request red. A forced run has no diff to blame (the nightly asks "do the
// consumers build against forge main?"), so it has no baseline (nil) and every
// failure counts.
func baselineEnv(ctx context.Context, o options, tree, scratch, forgeModule string) ([]string, error) {
	if o.force {
		return nil, nil
	}
	baseForge := filepath.Join(scratch, "forge-base")
	if err := os.Mkdir(baseForge, 0o755); err != nil {
		return nil, err
	}
	if err := forgereach.ExportRev(o.forge, o.base, baseForge); err != nil {
		return nil, fmt.Errorf("export forge at %s: %w", o.base, err)
	}
	baseMod, _, err := pointAtForge(ctx, tree, scratch, "base", baseForge, forgeModule)
	if err != nil {
		return nil, err
	}
	return consumerEnv(baseMod), nil
}

// pointAtForge writes a copy of the consumer's go.mod and go.sum into scratch
// with a replace of forgeModule by the forge checkout, and returns the copy's
// path for -modfile. ok is false when the consumer does not require forge at
// all. The consumer's own files are never written.
func pointAtForge(ctx context.Context, tree, scratch, name, forge, forgeModule string) (mod string, ok bool, err error) {
	if _, err := os.Stat(filepath.Join(tree, "go.mod")); err != nil {
		return "", false, fmt.Errorf("the consumer has no go.mod at its root")
	}
	mod = filepath.Join(scratch, name+".mod")
	sum := filepath.Join(scratch, name+".sum")
	for _, f := range [][2]string{{filepath.Join(tree, "go.mod"), mod}, {filepath.Join(tree, "go.sum"), sum}} {
		b, err := os.ReadFile(f[0])
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		if err := atomicfile.WriteFile(f[1], b, 0o644); err != nil {
			return "", false, err
		}
	}
	if out, err := runCmd(ctx, tree, nil, "go", "mod", "edit", "-modfile="+mod, "-replace", forgeModule+"="+forge); err != nil {
		return "", false, fmt.Errorf("go mod edit: %w\n%s", err, out)
	}
	return mod, strings.Contains(string(mustRead(mod)), forgeModule+" "), nil
}

func mustRead(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

// consumerEnv is the environment a consumer's go commands run in: forge's own
// scrubbed go environment (goexec.CheckEnv: no inherited -mod=mod, -trimpath
// added, because the scratch directory is new every run and without -trimpath
// it would be part of every package's build-cache key), the go.work file off (a
// workspace beside the consumer would override the replace under test), and
// modfile as the module file. -mod=mod lets the go command complete the
// throwaway copy's go.sum for the forge under test; the consumer's own go.sum
// is never written.
func consumerEnv(modfile string) []string {
	base := goexec.CheckEnv("GOWORK=off")
	flags := ""
	for _, kv := range base {
		if v, ok := strings.CutPrefix(kv, "GOFLAGS="); ok {
			flags = v
		}
	}
	return goexec.WithTrimpath(append(base, "GOFLAGS="+joinFlags(flags, "-modfile="+modfile, "-mod=mod")))
}

func joinFlags(existing string, add ...string) string {
	return strings.TrimSpace(existing + " " + strings.Join(add, " "))
}

func runCmd(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

// failureSummary is the lines of out that report a failure, or out's tail when
// none matches (a failure the patterns do not know must still be shown).
func failureSummary(out string) string {
	var res []string
	for _, l := range strings.Split(out, "\n") {
		if signalRE.MatchString(l) {
			res = append(res, strings.TrimRight(l, " \t"))
		}
	}
	if len(res) == 0 {
		return out
	}
	return strings.Join(res, "\n")
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{fmt.Sprintf("... (%d earlier lines)", len(lines)-n)}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}
