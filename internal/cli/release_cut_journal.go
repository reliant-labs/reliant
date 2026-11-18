package cli

// Resuming an interrupted release cut (internal/cutjournal has the why).
//
// A `--release` build writes a cut journal after its last push and before its
// ledger record (writeCutJournal). A later `--release` build of the same
// version looks for it before building anything (resumeInterruptedCut): from
// the same clean commit and the same forge, with every recorded tag still
// naming its recorded digest, it restores the build state the journal holds
// and records the release over the images already pushed. Anything else is
// refused, naming why, because the tags belong to a cut this checkout cannot
// prove it is.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"oras.land/oras-go/v2/content"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/cutjournal"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/statefile"
)

// cutJournalTimeout bounds each registry conversation about a journal. A
// registry that does not answer must not hold a cut: the build carries on and
// its own pushes say what is wrong.
const cutJournalTimeout = 60 * time.Second

// openCutJournalTarget and openCutJournalImage are the registry, as vars so a
// test can state its answers with an in-memory store.
var (
	openCutJournalTarget = func(repository string) (cutjournal.Target, error) { return cutjournal.Open(repository) }
	openCutJournalImage  = func(repository string) (content.Resolver, error) { return cutjournal.Open(repository) }
)

// cutJournalRepository is where the journal of a cut of this env lives: the
// first push destination the env declares, sorted. A hosted static site's
// repository is left out — it holds site releases, nothing else.
func cutJournalRepository(plan pushPlan) string {
	var repos []string
	for _, d := range plan.destinations {
		if strings.HasSuffix(d.repository, "/"+deploytarget.StaticSiteRepositorySegment) {
			continue
		}
		repos = append(repos, d.repository)
	}
	return cutjournal.Location(repos)
}

// cutJournalStateFiles is every build-state file a cut of env harvests, by
// name: the aggregate and per-image build states of each lookup key, and the
// env's source-map records. One function for the snapshot the writer takes
// and for the files a resume replaces, so the two cannot cover different sets.
//
// The filenames cannot say which env a file belongs to on their own:
// build-prod-*.json also matches build-prod-web.json, the aggregate of env
// prod-web, and build-prod-web-api.json, its per-service state. So a file
// matched by a prefix glob is this env's only when its content says so (see
// ownsCutJournalStateFile); every other env's state is left alone, never
// snapshotted and never removed by a resume.
func cutJournalStateFiles(projectDir, env string) []string {
	dir := filepath.Join(projectDir, statefile.DirRel)
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if !seen[name] && cutjournal.StateFileName(name) {
			seen[name] = true
			out = append(out, name)
		}
	}
	glob := func(prefix string, owns func(path, rest string) bool) {
		matches, _ := filepath.Glob(filepath.Join(dir, prefix+"*.json"))
		for _, m := range matches {
			rest := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), prefix), ".json")
			if owns(m, rest) {
				add(filepath.Base(m))
			}
		}
	}
	for _, key := range buildStateLookupEnvs(env) {
		seg := statefile.SafeSegment(key)
		if _, err := os.Stat(filepath.Join(dir, "build-"+seg+".json")); err == nil {
			add("build-" + seg + ".json")
		}
		glob("build-"+seg+"-", func(path, service string) bool {
			var st buildtarget.State
			raw, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(raw, &st) != nil || st.Service == "" {
				return false // another env's aggregate, or nothing the harvest reads
			}
			return buildtarget.StateBelongsTo(&st, key, service)
		})
	}
	glob("sourcemaps-"+statefile.SafeSegment(envNameOrDefault(env))+"-", func(path, frontend string) bool {
		var rec deploytarget.SourcemapsRecord
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, &rec) != nil {
			return false
		}
		return rec.Frontend != "" && statefile.SafeSegment(rec.Frontend) == frontend
	})
	sort.Strings(out)
	return out
}

func envNameOrDefault(env string) string {
	if env == "" {
		return "default"
	}
	return env
}

// cutJournalOutputs are the build outputs a cut reads besides images.
var cutJournalOutputs = []string{MetricsSchemaArtifact}

// snapshotCutJournal is the journal of the cut opts is making, from the build
// state it just wrote.
func snapshotCutJournal(ctx context.Context, projectDir string, opts buildOptions) (cutjournal.Journal, error) {
	prov := captureReleaseProvenance(ctx, projectDir)
	j := cutjournal.Journal{
		Project:      hostedProjectName(),
		Env:          opts.env,
		Version:      opts.release,
		ForgeVersion: buildinfo.Version(),
		Commit:       prov.Commit,
		Tree:         prov.Tree,
		Dirty:        prov.Dirty,
	}
	images := map[string]cutjournal.Image{}
	stateDir := filepath.Join(projectDir, statefile.DirRel)
	// This build's own records. The env-agnostic "default" fallback the
	// harvest also reads may be any earlier build's, on any registry, so its
	// images are carried in the state but are not this cut's to vouch for.
	own := "build-" + statefile.SafeSegment(envNameOrDefault(opts.env))
	for _, name := range cutJournalStateFiles(projectDir, opts.env) {
		raw, err := os.ReadFile(filepath.Join(stateDir, name))
		if err != nil {
			return cutjournal.Journal{}, err
		}
		j.State = append(j.State, cutjournal.File{Name: name, Data: raw})
		// Both build-state shapes (BuildState, buildtarget.State) carry these.
		// An image under the release version is checked by its tag; one the
		// cut reused under another tag (a content-addressed toolchain image)
		// by its digest, since that tag may be shared and move.
		var st struct{ Image, Tag, Digest string }
		if json.Unmarshal(raw, &st) != nil || st.Image == "" || st.Digest == "" || !strings.HasPrefix(name, own) {
			continue
		}
		img := cutjournal.Image{Repository: st.Image, Digest: st.Digest}
		if st.Tag == opts.release {
			img.Tag = st.Tag
		}
		if prev, ok := images[st.Image]; !ok || prev.Tag == "" {
			images[st.Image] = img
		}
	}
	for _, img := range images {
		j.Images = append(j.Images, img)
	}
	outDir := resolveProjectPath(projectDir, outputDirOrBin(opts.outputDir))
	for _, rel := range cutJournalOutputs {
		raw, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(rel)))
		if err == nil {
			j.Outputs = append(j.Outputs, cutjournal.File{Name: rel, Data: raw})
		}
	}
	return j, nil
}

// cutPushedRepositories is every image repository a build's results pushed
// and its build state should record: the project image (projectImage, its
// name; "" when the build made none) and each other docker result with a tag,
// resolved the way persistProjectBuildState and persistImageBuildStates
// resolve them, so the two name each image identically.
func cutPushedRepositories(projectImage string, opts buildOptions, succeeded []buildResult) []string {
	if !opts.pushPlan.push {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range succeeded {
		if r.kind != "docker" || r.tag == "" {
			continue
		}
		var repo string
		switch {
		case projectImage != "" && r.name == projectImage+" (docker)":
			repo = opts.pushPlan.repositoryFor(projectImage)
		case r.image == "":
			continue // no build state is written for it, so no cut reads it
		case r.repository != "":
			repo = r.repository
		default:
			repo = opts.pushPlan.repositoryFor(r.image)
		}
		if repo != "" && !seen[repo] {
			seen[repo] = true
			out = append(out, repo)
		}
	}
	sort.Strings(out)
	return out
}

// cutJournalMissingImages is each pushed repository the journal does not
// name with a digest.
func cutJournalMissingImages(j cutjournal.Journal, pushed []string) []string {
	have := map[string]bool{}
	for _, img := range j.Images {
		if img.Digest != "" {
			have[img.Repository] = true
		}
	}
	var missing []string
	for _, repo := range pushed {
		if !have[repo] {
			missing = append(missing, repo)
		}
	}
	return missing
}

func outputDirOrBin(dir string) string {
	if dir == "" {
		return "bin"
	}
	return dir
}

// writeCutJournal records the cut opts is about to make in the registry, so
// a cut interrupted from here on can be finished by a retry. Called after
// every image is pushed and before the ledger record. Never fails the cut: a
// cut whose journal could not be written is exactly as recoverable as one
// before journals existed, and says so.
//
// pushed is every image repository the build pushed (cutPushedRepositories),
// from its results rather than its state: a journal that does not name each
// of them with a digest would let a resume check a subset of the cut and
// record a release over the rest unverified, so none is written.
func writeCutJournal(ctx context.Context, opts buildOptions, pushed []string) {
	if opts.release == "" || opts.composition != nil {
		return
	}
	repo := cutJournalRepository(opts.pushPlan)
	tag, err := cutjournal.Tag(opts.release)
	if repo == "" || err != nil {
		return
	}
	projectDir := projectDirForKCL()
	j, err := snapshotCutJournal(ctx, projectDir, opts)
	if err == nil && !j.PushedUnder(opts.release) {
		return // nothing was pushed under the version: a retry has nothing to adopt
	}
	if err == nil {
		if missing := cutJournalMissingImages(j, pushed); len(missing) > 0 {
			err = fmt.Errorf("no build state records a digest for %s, so a resume could not verify every image the cut pushed", strings.Join(missing, ", "))
		}
	}
	var digest string
	if err == nil {
		var t cutjournal.Target
		if t, err = openCutJournalTarget(repo); err == nil {
			pctx, cancel := context.WithTimeout(ctx, cutJournalTimeout)
			digest, err = cutjournal.Push(pctx, t, tag, j)
			cancel()
		}
	}
	if err != nil {
		fmt.Printf("[build]   Warning: the cut journal of %s was not written to %s:%s (%v).\n"+
			"[build]   The cut continues; if it is interrupted before the release is recorded, %s cannot be resumed.\n",
			opts.release, repo, tag, err, opts.release)
		return
	}
	fmt.Printf("[build] Cut journal: %s:%s@%s (%d image(s) from %s; a retry of %s from this commit resumes from it)\n",
		repo, tag, digest, len(j.Images), shortCommit(j.Commit), opts.release)
}

// interruptedCut is what the registry says about an earlier cut of a version.
type interruptedCut struct {
	// Found is true when a journal is tagged for the version.
	Found bool
	// Ref is the journal, `<repository>:<tag>@<digest>`.
	Ref     string
	Journal cutjournal.Journal
	// Problems are every reason this checkout cannot resume it. Empty means
	// it can.
	Problems []string
}

func (c interruptedCut) resumable() bool { return c.Found && len(c.Problems) == 0 }

// findInterruptedCut reads the journal of opts' release, if one was written,
// and judges it against this checkout and the registry. An error means the
// question could not be answered (no repository could be read), not that the
// answer is no.
func findInterruptedCut(ctx context.Context, projectDir string, entities *KCLEntities, opts buildOptions) (interruptedCut, error) {
	repo := cutJournalRepository(opts.pushPlan)
	tag, err := cutjournal.Tag(opts.release)
	if repo == "" || err != nil {
		return interruptedCut{}, nil //nolint:nilerr // a version no journal can be written for has none to find
	}
	t, err := openCutJournalTarget(repo)
	if err != nil {
		return interruptedCut{}, err
	}
	fctx, cancel := context.WithTimeout(ctx, cutJournalTimeout)
	defer cancel()
	j, digest, err := cutjournal.Fetch(fctx, t, tag)
	if errors.Is(err, cutjournal.ErrNotFound) {
		return interruptedCut{}, nil
	}
	if err != nil {
		return interruptedCut{}, err
	}
	prov := captureReleaseProvenance(ctx, projectDir)
	found := interruptedCut{Found: true, Ref: repo + ":" + tag + "@" + digest, Journal: j}
	found.Problems = j.Mismatches(cutjournal.Expect{
		Project:      hostedProjectName(),
		Env:          opts.env,
		Version:      opts.release,
		ForgeVersion: buildinfo.Version(),
		Commit:       prov.Commit,
		Tree:         prov.Tree,
		Dirty:        prov.Dirty,
	})
	// A build-only binary is a release artifact the registry does not hold,
	// so a resume could only record a release without it.
	for _, svc := range entities.WorkloadsOn(RuntimeBuildOnly) {
		if svc.Runtime.BuildOnly != nil && len(svc.Runtime.BuildOnly.BuildVariants) > 0 {
			found.Problems = append(found.Problems, fmt.Sprintf("workload %s declares build-only binaries, which a resume cannot recover from the registry", svc.Name))
		}
	}
	relocated, problems := relocateJournalSources(ctx, projectDir, entities, j)
	found.Journal = relocated
	found.Problems = append(found.Problems, problems...)
	found.Problems = append(found.Problems, cutjournal.CheckImages(fctx, j, openCutJournalImage)...)
	return found, nil
}

// relocateJournalSources re-points each ShellBuild state's recorded checkout
// at the checkout that service builds in HERE, after proving it is the same
// source: same repository and commit, clean. The interrupted cut ran in a
// directory that may be gone (a reboot, a disposable worktree), and the cut's
// source checks resolve pins in the recorded directory, so a stale path would
// refuse the resume for a reason that has nothing to do with the bytes.
func relocateJournalSources(ctx context.Context, projectDir string, entities *KCLEntities, j cutjournal.Journal) (cutjournal.Journal, []string) {
	cwds := map[string]string{}
	for _, svc := range externalBuildServices(entities) {
		if cwd, err := buildtarget.ResolveCwd(buildtarget.Spec{Service: svc.Name, BuildCwd: svc.EffectiveBuildCwd(), ProjectDir: projectDir}); err == nil {
			cwds[svc.Name] = cwd
		}
	}
	live := map[string]*buildtarget.Source{}
	var problems []string
	out := j
	out.State = make([]cutjournal.File, len(j.State))
	for i, f := range j.State {
		out.State[i] = f
		var st buildtarget.State
		if json.Unmarshal(f.Data, &st) != nil || st.Source == nil || st.Service == "" {
			continue
		}
		cwd, declared := cwds[st.Service]
		if !declared {
			continue // a service this env no longer builds: the cut does not judge it
		}
		src, seen := live[cwd]
		if !seen {
			var err error
			if src, err = buildtarget.CaptureSource(ctx, cwd, projectDir); err != nil {
				problems = append(problems, fmt.Sprintf("the checkout %s builds in (%s) could not be read: %v", st.Service, cwd, err))
				continue
			}
			live[cwd] = src
		}
		switch {
		case src == nil:
			problems = append(problems, fmt.Sprintf("%s was built in a git checkout at %s and its cwd here (%s) is not one", st.Service, shortCommit(st.Source.Commit), cwd))
			continue
		case src.Commit != st.Source.Commit || src.Repo != st.Source.Repo:
			problems = append(problems, fmt.Sprintf("%s was built from %s %s and its checkout here is %s %s", st.Service, st.Source.Repo, shortCommit(st.Source.Commit), src.Repo, shortCommit(src.Commit)))
			continue
		case src.Dirty || st.Source.Dirty:
			problems = append(problems, fmt.Sprintf("%s's checkout was or is dirty, so no commit names what it built", st.Service))
			continue
		case src.Dir == st.Source.Dir:
			continue
		}
		st.Source.Dir = src.Dir
		raw, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out.State[i].Data = raw
	}
	return out, problems
}

// errInterruptedCutRefused is the refusal for a journal this checkout cannot
// resume: the version's tags belong to a cut it cannot prove it is.
func errInterruptedCutRefused(opts buildOptions, c interruptedCut) error {
	return fmt.Errorf("--release %s: an earlier cut of %s pushed its images and was never recorded (journal %s),\n"+
		"  and this cut cannot resume it:\n    - %s\n"+
		"  Its tags are immutable, so this build would fail at its first push. Cut the next version,\n"+
		"  or resume from the checkout the journal names: commit %s, forge %s, clean",
		opts.release, opts.release, c.Ref, strings.Join(c.Problems, "\n    - "), c.Journal.Commit, c.Journal.ForgeVersion)
}

// resumeInterruptedCut finishes an interrupted cut of opts' release instead
// of building it, when the registry holds a journal this checkout can resume.
// It reports whether it did. Called once the build set is resolved and the
// release preflights have passed, before anything is built.
func resumeInterruptedCut(ctx context.Context, entities *KCLEntities, opts buildOptions) (bool, error) {
	if opts.release == "" || opts.composition != nil || opts.plan || !opts.push {
		return false, nil
	}
	start := time.Now()
	projectDir := projectDirForKCL()
	found, err := findInterruptedCut(ctx, projectDir, entities, opts)
	if err != nil {
		fmt.Printf("[build]   note: could not look for an interrupted cut of %s (%v); building it\n", opts.release, err)
		return false, nil
	}
	if !found.Found {
		return false, nil
	}
	if !found.resumable() {
		return false, errInterruptedCutRefused(opts, found)
	}
	fmt.Printf("\n[build] Resuming the interrupted cut of %s: its %d image(s) were pushed from %s by forge %s and never recorded.\n"+
		"[build]   Journal: %s\n[build]   Nothing is rebuilt; the release is recorded over the images already pushed.\n",
		opts.release, len(found.Journal.Images), shortCommit(found.Journal.Commit), found.Journal.ForgeVersion, found.Ref)
	for _, img := range found.Journal.Images {
		fmt.Printf("[build]   adopt %s\n", img.Ref())
	}
	if err := restoreCutJournal(projectDir, opts.outputDir, opts.env, found.Journal); err != nil {
		return false, fmt.Errorf("--release %s: restore the interrupted cut's build state: %w", opts.release, err)
	}
	if _, err := collectObservabilityFiles(projectDir, opts.outputDir); err != nil {
		return false, fmt.Errorf("--release %s: %w", opts.release, err)
	}
	// The one cut path, over the restored state: the same harvest, coverage
	// gate, source and migration checks the interrupted cut would have run.
	cut, err := cutReleaseFromBuildStateFn(ctx, projectDir, opts.env, opts.release, opts.outputDir, entities, opts)
	if err != nil {
		return false, err
	}
	printReleaseCut(os.Stdout, cut)
	if err := writeBuildBundle(ctx, opts); err != nil {
		return false, err
	}
	if err := emitBuildGate(opts, 0, 0, start); err != nil {
		return false, err
	}
	fmt.Printf("\n[build] Resumed %s: recorded over the images its interrupted cut pushed.\n", opts.release)
	return true, nil
}

// restoreCutJournal makes the build state the cut harvests exactly the
// journal's: every state file the harvest would read is removed and the
// journal's written in its place, and its outputs are written into the output
// directory. Only this env's files are removed (cutJournalStateFiles): the
// state of an env whose name extends this one (build-prod-web.json for
// prod-web) matches the same globs and is kept.
func restoreCutJournal(projectDir, outputDir, env string, j cutjournal.Journal) error {
	stateDir := filepath.Join(projectDir, statefile.DirRel)
	for _, name := range cutJournalStateFiles(projectDir, env) {
		if err := os.Remove(filepath.Join(stateDir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	for _, f := range j.State {
		if err := atomicfile.WriteFile(filepath.Join(stateDir, f.Name), f.Data, 0o644); err != nil {
			return err
		}
	}
	outDir := resolveProjectPath(projectDir, outputDirOrBin(outputDir))
	for _, f := range j.Outputs {
		dest := filepath.Join(outDir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(dest, f.Data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
