package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/errdef"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/cutjournal"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/statefile"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const (
	cutVersion = "v1.7.94-rc.1"
	cutCommit  = "1111111111111111111111111111111111111111"
	cutAPI     = "reg.example/p/api"
	cutWorker  = "reg.example/p/worker"
)

// cutFixture is a checkout that cut cutVersion: its images are in an
// in-memory registry and its build state is on disk, as `forge env build prod
// --release` leaves them just before the ledger record.
type cutFixture struct {
	dir      string
	registry map[string]*memory.Store
	digests  map[string]string
	prov     release.Provenance
	opts     buildOptions
	// pushed is every image repository the build's results pushed.
	pushed []string
	// cuts records each call of the one cut path, with what it harvested.
	cuts []map[string]release.Artifact
	// commits is what the cut saw as the build's commits.
	commits []string
}

func newCutFixture(t *testing.T) *cutFixture {
	t.Helper()
	f := &cutFixture{
		dir:      t.TempDir(),
		registry: map[string]*memory.Store{cutAPI: memory.New(), cutWorker: memory.New()},
		digests:  map[string]string{},
		prov:     release.Provenance{Commit: cutCommit, Tree: "tree1"},
	}
	if err := os.WriteFile(filepath.Join(f.dir, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(f.dir)

	prevTarget, prevImage, prevProv, prevName := openCutJournalTarget, openCutJournalImage, captureReleaseProvenance, hostedProjectName
	prevCut, prevBundles := cutReleaseFromBuildStateFn, writeBundlesFn
	t.Cleanup(func() {
		openCutJournalTarget, openCutJournalImage, captureReleaseProvenance, hostedProjectName = prevTarget, prevImage, prevProv, prevName
		cutReleaseFromBuildStateFn, writeBundlesFn = prevCut, prevBundles
	})
	openCutJournalTarget = func(repo string) (cutjournal.Target, error) {
		if s, ok := f.registry[repo]; ok {
			return s, nil
		}
		return nil, errors.New("unreachable registry " + repo)
	}
	openCutJournalImage = func(repo string) (content.Resolver, error) {
		s, ok := f.registry[repo]
		if !ok {
			return nil, errors.New("unreachable registry " + repo)
		}
		return digestResolver{s}, nil
	}
	captureReleaseProvenance = func(context.Context, string) release.Provenance { return f.prov }
	hostedProjectName = func() string { return "shop" }
	cutReleaseFromBuildStateFn = func(_ context.Context, projectDir, env, version, _ string, _ *KCLEntities, _ buildOptions) (releaseCutOutcome, error) {
		f.cuts = append(f.cuts, harvestReleaseArtifacts(projectDir, env))
		f.commits = harvestedBuildCommits(projectDir, env)
		return releaseCutOutcome{Release: release.Release{Version: version}, Created: true, Ledger: "test"}, nil
	}
	writeBundlesFn = func(context.Context, string, []string, bundleBuildInputs) ([]bundleWriteOutcome, error) {
		return nil, nil
	}

	f.opts = buildOptions{
		env: "prod", release: cutVersion, push: true, outputDir: "bin",
		pushPlan: pushPlan{push: true, env: "prod", destinations: []imageDestination{
			{repository: cutWorker}, {repository: cutAPI},
			// sorts first, and is never where a journal goes
			{repository: "reg.example/p/aaa/static.v1"},
		}},
	}

	// The build: both images pushed under the version, their state on disk,
	// beside a stale record from an unrelated earlier build.
	f.pushed = []string{cutAPI, cutWorker}
	f.digests[cutAPI] = pushTestImage(t, f.registry[cutAPI], cutVersion, "api")
	f.digests[cutWorker] = pushTestImage(t, f.registry[cutWorker], cutVersion, "worker")
	mustNoErr(t, WriteBuildState(f.dir, "prod", BuildState{Image: cutAPI, Tag: cutVersion, Pushed: true, Commit: cutCommit, Digest: f.digests[cutAPI]}))
	mustNoErr(t, buildtarget.WriteState(f.dir, "prod", buildtarget.State{Service: "worker", Image: cutWorker, Tag: cutVersion, Digest: f.digests[cutWorker]}))
	mustNoErr(t, WriteBuildState(f.dir, "default", BuildState{Image: "reg.example/p/old", Tag: "dev", Digest: "sha256:" + strings.Repeat("0", 64)}))
	return f
}

// reboot is the machine dying after the pushes: the checkout's build state is
// gone, and a fresh checkout may hold a record of its own.
func (f *cutFixture) reboot(t *testing.T) {
	t.Helper()
	mustNoErr(t, os.RemoveAll(filepath.Join(f.dir, statefile.DirRel)))
	mustNoErr(t, buildtarget.WriteState(f.dir, "prod", buildtarget.State{Service: "ghost", Image: "reg.example/p/ghost", Tag: "x", Digest: "sha256:" + strings.Repeat("9", 64)}))
}

// digestResolver resolves a digest reference the way a real registry does;
// the in-memory store resolves tags only.
type digestResolver struct{ *memory.Store }

func (d digestResolver) Resolve(ctx context.Context, ref string) (ocispec.Descriptor, error) {
	if desc, ok := pushedManifests[ref]; ok {
		if exists, err := d.Exists(ctx, desc); err != nil || !exists {
			return ocispec.Descriptor{}, fmt.Errorf("%s: %w", ref, errdef.ErrNotFound)
		}
		return desc, nil
	}
	return d.Store.Resolve(ctx, ref)
}

// pushedManifests is every manifest pushTestImage pushed, by digest.
var pushedManifests = map[string]ocispec.Descriptor{}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func pushTestImage(t *testing.T, store *memory.Store, tag, body string) string {
	t.Helper()
	ctx := context.Background()
	layer := []byte(body)
	layerDesc := content.NewDescriptorFromBytes("application/vnd.oci.image.layer.v1.tar", layer)
	manifest := ocispec.Manifest{MediaType: ocispec.MediaTypeImageManifest, Config: ocispec.DescriptorEmptyJSON, Layers: []ocispec.Descriptor{layerDesc}}
	manifest.SchemaVersion = 2
	raw, err := json.Marshal(manifest)
	mustNoErr(t, err)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	for _, b := range []struct {
		d ocispec.Descriptor
		v []byte
	}{{layerDesc, layer}, {ocispec.DescriptorEmptyJSON, ocispec.DescriptorEmptyJSON.Data}, {desc, raw}} {
		if err := store.Push(ctx, b.d, bytes.NewReader(b.v)); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatal(err)
		}
	}
	mustNoErr(t, store.Tag(ctx, desc, tag))
	pushedManifests[desc.Digest.String()] = desc
	return desc.Digest.String()
}

// The incident: every image pushed, the journal written, the machine rebooted
// before the record. The retry from the same commit records the release over
// the pushed digests, from exactly the build state the interrupted cut held.
func TestInterruptedCutIsResumedFromItsJournal(t *testing.T) {
	f := newCutFixture(t)
	ctx := context.Background()
	writeCutJournal(ctx, f.opts, f.pushed)
	if _, err := f.registry[cutAPI].Resolve(ctx, cutVersion+cutjournal.TagSuffix); err != nil {
		t.Fatalf("the journal is not in the first non-static destination (%s): %v", cutAPI, err)
	}
	f.reboot(t)

	resumed, err := resumeInterruptedCut(ctx, &KCLEntities{}, f.opts)
	if err != nil || !resumed {
		t.Fatalf("resumeInterruptedCut = %v, %v; want a resume", resumed, err)
	}
	if len(f.cuts) != 1 {
		t.Fatalf("the cut ran %d times, want once", len(f.cuts))
	}
	got := f.cuts[0]
	for repo, digest := range f.digests {
		if got[repo].Digests[release.SharedVariant] != digest {
			t.Errorf("the release records %s as %v, want the pushed %s", repo, got[repo].Digests, digest)
		}
	}
	if _, ok := got["reg.example/p/ghost"]; ok {
		t.Error("the fresh checkout's own state leaked into the resumed release")
	}
	if _, ok := got["reg.example/p/old"]; !ok {
		t.Error("the resumed harvest dropped a state file the interrupted cut would have harvested")
	}
	if len(f.commits) != 1 || f.commits[0] != cutCommit {
		t.Errorf("the cut saw build commits %v, want [%s]", f.commits, cutCommit)
	}
}

// Every way the registry's journal is not this checkout's cut is refused
// before anything is built: the tags are immutable and belong to that cut.
func TestInterruptedCutRefusesAResumeItCannotProve(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*testing.T, *cutFixture)
		want string
	}{
		"another commit": {func(_ *testing.T, f *cutFixture) { f.prov.Commit = strings.Repeat("2", 40) }, "built from commit 111111111111 and this checkout is at 222222222222"},
		"dirty checkout": {func(_ *testing.T, f *cutFixture) { f.prov.Dirty = true }, "this checkout is dirty"},
		"tag moved": {func(t *testing.T, f *cutFixture) {
			pushTestImage(t, f.registry[cutWorker], cutVersion, "a different worker")
		}, "reg.example/p/worker:" + cutVersion + " names"},
		"build-only binaries": {nil, "declares build-only binaries"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCutFixture(t)
			ctx := context.Background()
			writeCutJournal(ctx, f.opts, f.pushed)
			f.reboot(t)
			entities := &KCLEntities{}
			if tc.edit != nil {
				tc.edit(t, f)
			} else {
				entities.Workloads = []WorkloadEntity{{Name: "cli", Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{BuildVariants: []BuildVariant{{Name: "linux"}}}}}}
			}
			resumed, err := resumeInterruptedCut(ctx, entities, f.opts)
			if resumed || err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Cut the next version") {
				t.Fatalf("resumeInterruptedCut = %v, %v; want a refusal containing %q", resumed, err, tc.want)
			}
			if len(f.cuts) != 0 {
				t.Fatal("a refused resume ran the cut")
			}
		})
	}
}

// No journal, or a registry that cannot be read, is an ordinary build: the
// resume never decides on evidence it does not have.
func TestNoJournalBuildsAsBefore(t *testing.T) {
	f := newCutFixture(t)
	ctx := context.Background()
	if resumed, err := resumeInterruptedCut(ctx, &KCLEntities{}, f.opts); resumed || err != nil {
		t.Fatalf("no journal: %v, %v; want an ordinary build", resumed, err)
	}
	openCutJournalTarget = func(string) (cutjournal.Target, error) { return nil, errors.New("connection refused") }
	if resumed, err := resumeInterruptedCut(ctx, &KCLEntities{}, f.opts); resumed || err != nil {
		t.Fatalf("unreadable registry: %v, %v; want an ordinary build", resumed, err)
	}
	f.opts.release = ""
	if resumed, err := resumeInterruptedCut(ctx, &KCLEntities{}, f.opts); resumed || err != nil {
		t.Fatalf("not a release: %v, %v", resumed, err)
	}
	if len(f.cuts) != 0 {
		t.Fatal("the cut ran without a journal")
	}
}

// A cut that pushed nothing under its version leaves nothing to adopt.
func TestCutJournalIsNotWrittenWithoutAVersionTaggedImage(t *testing.T) {
	f := newCutFixture(t)
	f.opts.release = "v9.9.9"
	writeCutJournal(context.Background(), f.opts, f.pushed)
	if _, err := f.registry[cutAPI].Resolve(context.Background(), "v9.9.9"+cutjournal.TagSuffix); err == nil {
		t.Fatal("a journal was written for a version no image was pushed under")
	}
}

// A journal covers every image the cut pushed or is not written: an image
// pushed with no build state, or a state with no digest, would leave a resume
// verifying a subset of the cut and recording a release over the rest.
func TestCutJournalIsNotWrittenWhenAPushedImageHasNoDigest(t *testing.T) {
	for name, edit := range map[string]func(*testing.T, *cutFixture){
		"no state": func(t *testing.T, f *cutFixture) {
			mustNoErr(t, os.Remove(buildtarget.StatePath(f.dir, "prod", "worker")))
		},
		"no digest": func(t *testing.T, f *cutFixture) {
			mustNoErr(t, buildtarget.WriteState(f.dir, "prod", buildtarget.State{Service: "worker", Image: cutWorker, Tag: cutVersion}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCutFixture(t)
			ctx := context.Background()
			edit(t, f)
			writeCutJournal(ctx, f.opts, f.pushed)
			if _, err := f.registry[cutAPI].Resolve(ctx, cutVersion+cutjournal.TagSuffix); err == nil {
				t.Fatal("a journal was written that does not name every pushed image")
			}
			f.reboot(t)
			if resumed, err := resumeInterruptedCut(ctx, &KCLEntities{}, f.opts); resumed || err != nil {
				t.Fatalf("resumeInterruptedCut = %v, %v; want an ordinary build", resumed, err)
			}
		})
	}
}

// The pushed set comes from the build's results, named as their build states
// name them.
func TestCutPushedRepositoriesNamesEveryPushedImage(t *testing.T) {
	opts := buildOptions{pushPlan: pushPlan{push: true, env: "prod", destinations: []imageDestination{{repository: "reg.example/p/shop"}, {repository: "reg.example/p/web"}}}}
	results := []buildResult{
		{kind: "docker", name: "shop (docker)", tag: cutVersion},
		{kind: "docker", name: "web (docker)", image: "web", tag: cutVersion},
		{kind: "docker", name: "api (docker)", image: "api", repository: "ghcr.io/acme/api", tag: cutVersion},
		{kind: "docker", name: "untagged (docker)", image: "untagged"},
		{kind: "service", name: "api"},
	}
	got := cutPushedRepositories("shop", opts, results)
	want := []string{"ghcr.io/acme/api", opts.pushPlan.repositoryFor("shop"), opts.pushPlan.repositoryFor("web")}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cutPushedRepositories = %v, want %v", got, want)
	}
	opts.pushPlan.push = false
	if got := cutPushedRepositories("shop", opts, results); len(got) != 0 {
		t.Fatalf("a build that pushed nothing: %v", got)
	}
}

// The restore writes the journal's state and outputs and nothing else, and
// never outside the two directories.
func TestRestoreCutJournalReplacesTheEnvsState(t *testing.T) {
	dir := t.TempDir()
	mustNoErr(t, buildtarget.WriteState(dir, "prod", buildtarget.State{Service: "stale", Image: "x", Tag: "y"}))
	mustNoErr(t, buildtarget.WriteState(dir, "staging", buildtarget.State{Service: "other-env", Image: "x", Tag: "y"}))
	j := cutjournal.Journal{
		State:   []cutjournal.File{{Name: "build-prod.json", Data: []byte(`{"image":"a"}`)}},
		Outputs: []cutjournal.File{{Name: MetricsSchemaArtifact, Data: []byte(`{"metrics":[]}`)}},
	}
	mustNoErr(t, restoreCutJournal(dir, "bin", "prod", j))
	if got := cutJournalStateFiles(dir, "prod"); strings.Join(got, ",") != "build-prod.json" {
		t.Fatalf("prod's state after restore: %v, want only the journal's", got)
	}
	if _, err := os.Stat(filepath.Join(dir, statefile.DirRel, "build-staging-other-env.json")); err != nil {
		t.Fatalf("another env's state was touched: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "bin", filepath.FromSlash(MetricsSchemaArtifact)))
	if err != nil || string(raw) != `{"metrics":[]}` {
		t.Fatalf("metrics schema = %q, %v", raw, err)
	}
}

// An env whose name extends this one (prod-web for prod) shares its filename
// prefix. Its state is neither snapshotted nor removed by a resume of prod.
func TestRestoreCutJournalKeepsAnExtendingEnvsState(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, statefile.DirRel)
	mustNoErr(t, WriteBuildState(dir, "prod", BuildState{Image: "reg/app", Tag: "old"}))
	mustNoErr(t, buildtarget.WriteState(dir, "prod", buildtarget.State{Service: "api", Image: "reg/api", Tag: "old"}))
	mustNoErr(t, WriteBuildState(dir, "prod-web", BuildState{Image: "reg/web", Tag: "w1"}))
	mustNoErr(t, buildtarget.WriteState(dir, "prod-web", buildtarget.State{Service: "api", Image: "reg/web-api", Tag: "w1"}))
	mustNoErr(t, deploytarget.WriteSourcemapsState(dir, "prod-web", deploytarget.SourcemapsRecord{Frontend: "site"}))
	mustNoErr(t, deploytarget.WriteSourcemapsState(dir, "prod", deploytarget.SourcemapsRecord{Frontend: "site"}))

	want := "build-prod-api.json,build-prod.json,sourcemaps-prod-site.json"
	if got := strings.Join(cutJournalStateFiles(dir, "prod"), ","); got != want {
		t.Fatalf("prod's state files = %s, want %s", got, want)
	}
	j := cutjournal.Journal{State: []cutjournal.File{{Name: "build-prod.json", Data: []byte(`{"image":"a"}`)}}}
	mustNoErr(t, restoreCutJournal(dir, "bin", "prod", j))
	for _, name := range []string{"build-prod-web.json", "build-prod-web-api.json", "sourcemaps-prod-web-site.json"} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); err != nil {
			t.Fatalf("prod-web's %s was removed by a resume of prod: %v", name, err)
		}
	}
	if got := strings.Join(cutJournalStateFiles(dir, "prod"), ","); got != "build-prod.json" {
		t.Fatalf("prod's state after restore: %s, want only the journal's", got)
	}
}

// An image the cut reused under another tag (a content-addressed toolchain
// image) is recorded by digest: its tag may be shared and move, its bytes must
// still be there.
func TestCutJournalChecksAReusedImageByDigest(t *testing.T) {
	f := newCutFixture(t)
	ctx := context.Background()
	runtime := "reg.example/p/runtime"
	f.registry[runtime] = memory.New()
	digest := pushTestImage(t, f.registry[runtime], "inputs-abc", "toolchain")
	mustNoErr(t, buildtarget.WriteState(f.dir, "prod", buildtarget.State{Service: "runtime", Image: runtime, Tag: "inputs-abc", Digest: digest}))
	writeCutJournal(ctx, f.opts, f.pushed)
	f.reboot(t)

	// The shared tag moving is not a problem; the journal never named it.
	pushTestImage(t, f.registry[runtime], "inputs-abc", "a newer toolchain")
	found, err := findInterruptedCut(ctx, f.dir, &KCLEntities{}, f.opts)
	if err != nil || !found.resumable() {
		t.Fatalf("findInterruptedCut = %+v, %v; want resumable", found, err)
	}
	var recorded bool
	for _, img := range found.Journal.Images {
		if img.Repository == runtime {
			recorded = img.Tag == "" && img.Digest == digest
		}
	}
	if !recorded {
		t.Fatalf("the reused image is not recorded by digest alone: %+v", found.Journal.Images)
	}

	// Its bytes gone is.
	f.registry[runtime] = memory.New()
	found, err = findInterruptedCut(ctx, f.dir, &KCLEntities{}, f.opts)
	if err != nil || found.resumable() || !strings.Contains(strings.Join(found.Problems, ";"), runtime+"@"+digest+" no longer exists") {
		t.Fatalf("findInterruptedCut = %+v, %v; want the missing digest named", found, err)
	}
}

// The interrupted cut's sibling checkout may be gone (a reboot, a disposable
// worktree). A resume re-points the recorded checkout at the one here, and
// only when it is the same repository at the same commit, clean.
func TestInterruptedCutRelocatesItsSiblingCheckout(t *testing.T) {
	testkit.Slow(t, "builds real git repositories")
	ctx := context.Background()
	root := t.TempDir()
	sib := filepath.Join(root, "sib")
	initGitRepo(t, sib, map[string]string{"go.mod": "module example.com/sib\n"})
	live, err := buildtarget.CaptureSource(ctx, sib, root)
	mustNoErr(t, err)
	ents := &KCLEntities{Workloads: []WorkloadEntity{{
		Name: "sib", Build: BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "true", Cwd: "../sib"}},
	}}}
	project := filepath.Join(root, "acme")
	mustNoErr(t, os.MkdirAll(project, 0o755))

	recorded := *live
	recorded.Dir = "/gone/rc-pair-1/sib"
	journal := func(src buildtarget.Source) cutjournal.Journal {
		raw, err := json.Marshal(buildtarget.State{Service: "sib", Env: "prod", Image: "reg/sib", Tag: "v1", Digest: "sha256:" + strings.Repeat("1", 64), Source: &src})
		mustNoErr(t, err)
		return cutjournal.Journal{State: []cutjournal.File{{Name: "build-prod-sib.json", Data: raw}}}
	}

	out, problems := relocateJournalSources(ctx, project, ents, journal(recorded))
	if len(problems) != 0 {
		t.Fatalf("same commit, clean: %v", problems)
	}
	var st buildtarget.State
	mustNoErr(t, json.Unmarshal(out.State[0].Data, &st))
	if st.Source.Dir != live.Dir || st.Source.Commit != live.Commit {
		t.Fatalf("relocated source = %+v, want dir %s at %s", st.Source, live.Dir, live.Commit)
	}

	other := recorded
	other.Commit = strings.Repeat("f", 40)
	if _, problems := relocateJournalSources(ctx, project, ents, journal(other)); len(problems) != 1 || !strings.Contains(problems[0], "its checkout here is") {
		t.Fatalf("another commit: %v, want one refusal", problems)
	}
	mustNoErr(t, os.WriteFile(filepath.Join(sib, "dirty.txt"), []byte("x"), 0o644))
	if _, problems := relocateJournalSources(ctx, project, ents, journal(recorded)); len(problems) != 1 || !strings.Contains(problems[0], "dirty") {
		t.Fatalf("a dirty checkout: %v, want one refusal", problems)
	}
}
