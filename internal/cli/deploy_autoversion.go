package cli

// The automatic release version a no-version `forge env deploy` cuts, and the
// rule that stops it cutting — or building — twice for one checkout (owner
// decision O-15, doc §7.2 "Auto version").
//
// `<YYYYMMDD>.<HHMMSS>-<tree12>`: the UTC cut time and the first 12 hex of the
// checkout's provenance tree.
//
// WHY THIS SHAPE.
//
//   - It is unique per project (T15) without a read-modify-write of the
//     ledger, so two deploys cannot race for a name.
//   - It SORTS by time, which is what makes `forge env status` and the
//     promote direction logic read correctly against hand-named releases.
//   - The tree makes the name READABLE as content. It is not what reuse
//     matches on: reuse compares the provenance every release RECORDS
//     (chooseDeployRelease), so a release named anything — `v1.4.0`, or a
//     release script's `<date>-<commit>` — is found by what it holds.
//
// WHAT IS DELIBERATELY NOT IN THE NAME. A dirty or branch build carries that
// fact in its PROVENANCE, never in its version (owner decision O-7: no naming
// rule). A name that encoded it would be a policy expressed as a string, and
// every consumer would have to parse it to recover what the provenance
// already states exactly.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// autoVersionTimeFormat is the UTC timestamp half.
const autoVersionTimeFormat = "20060102.150405"

// autoVersionShortHash is how much of the tree (or commit) the name carries.
// Twelve hex is git's own abbreviation length for a large repository: long
// enough that a collision is not a practical concern, short enough that the
// version stays readable in a status table.
const autoVersionShortHash = 12

// autoVersionFor is the version a no-version deploy would cut for this
// provenance, at this instant.
//
// An UNHASHED tree (F-16: `CaptureProvenance` gave up on a very large
// untracked set) falls back to the commit, and such a release is NEVER reused
// — see chooseDeployRelease. The fallback keeps the name meaningful rather
// than emitting a bare timestamp, but a commit does not identify the built
// CONTENT when the tree could not be hashed, so it must not be treated as if
// it did.
func autoVersionFor(prov release.Provenance, now time.Time) string {
	stamp := now.UTC().Format(autoVersionTimeFormat)
	switch {
	case prov.Tree != "":
		return stamp + "-" + shortHash(prov.Tree)
	case prov.Commit != "":
		return stamp + "-" + shortHash(prov.Commit)
	default:
		// Not a git tree at all. The timestamp alone is still unique per
		// project and still sorts, which is everything the ledger needs.
		return stamp
	}
}

func shortHash(h string) string {
	if len(h) <= autoVersionShortHash {
		return h
	}
	return h[:autoVersionShortHash]
}

// captureReleaseProvenance is THE provenance capture for a release: the one
// the cut records (cutReleaseFromBuildState, which both `forge env build
// --release` and the no-version deploy's build reach) and the one a
// no-version deploy matches existing releases against. One function over one
// directory (projectDirForKCL), so a release cut by either path is found by
// the next deploy of the same checkout.
//
// A var so a test can state the checkout: a t.TempDir() is not a git
// repository, so the real capture reports no tree — F-16's never-reuse path,
// the opposite of what a reuse test exercises.
var captureReleaseProvenance = captureBuildProvenance

// releaseImageResolves asks ref's registry whether it still serves ref
// (`repo@sha256:…`), with the ambient docker credentials — the same lookup
// the deploy preflight makes. A var so a test can answer without a registry.
var releaseImageResolves = func(ctx context.Context, ref string) (bool, error) {
	return cluster.RegistryImageChecker{}.ImageExists(ctx, ref)
}

// resolveFrontendPinArtifacts is where each source-pinned frontend's pin
// resolves today. A var so a test can state a moved pin without a network.
var resolveFrontendPinArtifacts = addFrontendSourceArtifacts

// inspectDeployCheckout lists the checkout's own uncommitted paths and the
// nested git checkouts it ignores, for the decision's wording only — the
// dirty verdict itself is the provenance's. A var so a test can state them.
var inspectDeployCheckout = func(ctx context.Context, projectDir string) release.Changes {
	changes, _ := release.InspectDir(ctx, projectDir)
	return changes
}

// deployReleaseChoice is what a no-version deploy decided BEFORE building
// anything: deploy a release that already holds this checkout's content, or
// cut a new one.
type deployReleaseChoice struct {
	// Nested are the nested git checkouts (parked worktrees, cloned repos)
	// that were left out of the checkout's dirty verdict. Stated on the
	// decision line so a reader can see they were seen and ignored.
	Nested []string
	// Dirty is true when the checkout had uncommitted changes of its own;
	// DirtyPaths lists them. The first reason a cut is not a reuse, and the
	// one a shared env asks about before building.
	Dirty      bool
	DirtyPaths []string
	// Reuse is the release to deploy exactly as recorded. Nil means cut.
	Reuse *release.Release
	// Version is the auto version to cut under, when Reuse is nil.
	Version string
	// Reason is why nothing was reused, when Reuse is nil. Always stated:
	// a deploy that rebuilds an unchanged checkout must say why.
	Reason string
}

// deployReuseQuery is everything the choice is made from.
type deployReuseQuery struct {
	ProjectDir string
	Env        string
	// Provenance is this checkout, through captureReleaseProvenance.
	Provenance release.Provenance
	// Changes are the checkout's uncommitted paths and ignored nested
	// checkouts. Wording only: Provenance.Dirty decides.
	Changes release.Changes
	// RenderOptions are the deploy's `-D name=value` options.
	RenderOptions []string
	Releases      releaseLedger
	Now           time.Time
}

// chooseDeployRelease decides whether this checkout already has a release.
//
// THE MATCH IS ON CONTENT PROVENANCE, NEVER ON THE NAME. A release is reused
// when what it records is what this deploy would build:
//
//   - the SOURCE: the same clean tree hash. Only a clean tree counts, on both
//     sides — a clean tree's hash is the commit's tree, which git can check;
//     a dirty tree's is the building machine's claim about files nobody
//     committed;
//   - the BUILDER: the same forge version. The images and the render are
//     functions of the forge that produced them, so the same tree built by
//     another forge is not the same release;
//   - the ENV's RENDER: the render is a function of the tree, the forge and
//     the `-D` options. The first two are compared above; a release records
//     no options, so a deploy that passes any never reuses. And the release
//     must cover every artifact the env's render declares NOW — each image,
//     each hosted static site, each source-pinned frontend at the commit its
//     pin resolves to today.
//
// And the release must still be deployable: every image it pins is asked of
// its registry, by digest, because a retention window may have expired it
// since the cut. A release the registry no longer serves is not reused.
//
// WHY REUSE BUILDS NOTHING. A build is not reproducible byte for byte: on
// 2026-10-09 a rebuild of an unchanged prod checkout pushed a reliant image
// with a different digest from the one its release, cut minutes earlier,
// pinned. Rebuilding to "make sure the images are there" therefore changes
// the images; asking the registry whether the recorded ones are there does
// not.
//
// Any doubt cuts. A ledger that cannot be read, an env that cannot be
// rendered, a registry that will not answer — each is a reason to build, never
// a reason to deploy bytes nobody verified, and each is named in Reason.
func chooseDeployRelease(ctx context.Context, q deployReuseQuery) deployReleaseChoice {
	choice := chooseDeployReleaseFor(ctx, q)
	choice.Nested = q.Changes.Nested
	choice.Dirty = q.Provenance.Dirty
	choice.DirtyPaths = q.Changes.Paths
	return choice
}

// decideDeployRelease is THE release decision of a no-version deploy: the one
// call both the real run (buildAndCutForDeploy) and `--dry-run` make, over the
// same checkout capture, so a preview cannot predict something the real run
// does not do.
func decideDeployRelease(ctx context.Context, projectDir, envName string, renderOptions []string, ledger envLedger) deployReleaseChoice {
	return chooseDeployRelease(ctx, deployReuseQuery{
		ProjectDir:    projectDir,
		Env:           envName,
		Provenance:    captureReleaseProvenance(ctx, projectDir),
		Changes:       inspectDeployCheckout(ctx, projectDir),
		RenderOptions: renderOptions,
		Releases:      ledger.Releases,
		Now:           time.Now(),
	})
}

func chooseDeployReleaseFor(ctx context.Context, q deployReuseQuery) deployReleaseChoice {
	cut := func(reason string) deployReleaseChoice {
		return deployReleaseChoice{Version: autoVersionFor(q.Provenance, q.Now), Reason: reason}
	}
	switch {
	case q.Provenance.Tree == "":
		return cut("this checkout's tree could not be hashed, so no recorded release can be shown to hold the same content")
	case q.Provenance.Dirty:
		return cut("the checkout has uncommitted changes" + describePaths(q.Changes.Paths) + ", and only a clean tree — whose hash git can check against its commit — is matched to an existing release")
	case len(q.RenderOptions) > 0:
		return cut("render options (-D) were passed, and a release does not record the options its build rendered under")
	case q.Releases == nil:
		return cut("env " + q.Env + " has no release ledger to search")
	}
	releases, err := q.Releases.List(ctx)
	if err != nil {
		return cut(fmt.Sprintf("the release ledger at %s could not be read (%v)", q.Releases.Location(), err))
	}
	var candidates []release.Release
	for _, rel := range releases {
		if rel.Provenance != nil && rel.Provenance.Tree == q.Provenance.Tree {
			candidates = append(candidates, rel)
		}
	}
	if len(candidates) == 0 {
		return cut(fmt.Sprintf("no release in %s records this checkout's tree (%s)", q.Releases.Location(), shortHash(q.Provenance.Tree)))
	}
	// Newest first, List's order. The first candidate that passes every
	// check is reused; when none does, the NEWEST one's refusal is the
	// reason, because it is the release an operator expects to see reused.
	var refusal string
	for i := range candidates {
		why := releaseReuseRefusal(ctx, q, candidates[i])
		if why == "" {
			return deployReleaseChoice{Reuse: &candidates[i]}
		}
		if refusal == "" {
			refusal = why
		}
	}
	return cut(refusal)
}

// releaseReuseRefusal is why rel, which records this checkout's tree, cannot
// be deployed in place of a new cut — "" when it can.
func releaseReuseRefusal(ctx context.Context, q deployReuseQuery, rel release.Release) string {
	recorded := rel.Provenance
	switch {
	case recorded.Dirty:
		return fmt.Sprintf("release %s records this tree, but it was cut from a checkout with uncommitted changes, whose tree hash git cannot check", rel.Version)
	case q.Provenance.ForgeVersion == "" || recorded.ForgeVersion != q.Provenance.ForgeVersion:
		return fmt.Sprintf("release %s records this tree but was built by forge %s, and this is forge %s — a different forge can render and build different bytes",
			rel.Version, emptyAs(recorded.ForgeVersion, "(unrecorded)"), emptyAs(q.Provenance.ForgeVersion, "(unknown)"))
	}
	entities, err := RenderKCL(ctx, q.ProjectDir, q.Env)
	if err != nil {
		return fmt.Sprintf("env %s could not be rendered to check that release %s covers it (%v)", q.Env, rel.Version, err)
	}
	gaps, err := releaseGapsForEnv(ctx, q.ProjectDir, q.Env, entities, rel)
	if err != nil {
		return fmt.Sprintf("the sources env %s pins could not be resolved to check release %s against them (%v)", q.Env, rel.Version, err)
	}
	if len(gaps) > 0 {
		return fmt.Sprintf("release %s does not cover everything env %s declares now: %s", rel.Version, q.Env, strings.Join(gaps, "; "))
	}
	if ref, err := firstUnresolvableImage(ctx, q.Env, entities, rel); ref != "" {
		return fmt.Sprintf("release %s pins %s, which could not be confirmed in its registry (%v)", rel.Version, ref, err)
	}
	return ""
}

// releaseGapsForEnv is what env's CURRENT render needs that rel does not
// hold: a declared artifact it lacks (the cut's own completeness rule), or a
// source-pinned frontend whose pin resolves today to a different commit than
// the one rel froze — a branch ref that moved since the cut.
func releaseGapsForEnv(ctx context.Context, projectDir, env string, entities *KCLEntities, rel release.Release) ([]string, error) {
	gaps := releaseCoverageGaps(entities, rel.Artifacts, env)
	current := map[string]release.Artifact{}
	if err := resolveFrontendPinArtifacts(ctx, projectDir, entities, current); err != nil {
		return nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(current)) {
		frozen, ok := rel.Artifacts[name]
		if !ok {
			continue // already a coverage gap
		}
		now := current[name].Source
		if frozen.Source == nil || frozen.Source.Commit != now.Commit {
			was := "no commit"
			if frozen.Source != nil {
				was = shortHash(frozen.Source.Commit)
			}
			gaps = append(gaps, fmt.Sprintf("%s (its source %s@%s resolves to %s now; the release froze %s)",
				name, now.Repo, now.Ref, shortHash(now.Commit), was))
		}
	}
	return gaps, nil
}

// firstUnresolvableImage is the first image rel pins that its registry does
// not serve, with why. ("", nil) when every image resolves.
//
// Every digest is asked, not a sample: the deploy pins all of them, and one
// expired image is a workload that cannot start.
//
// The PLATFORM registry is logged in to first, when an image lives there. It
// authenticates with the env's control-plane credential, and the build does
// that login before it pushes (autoLoginForPush) — a reuse builds nothing, so
// without this its read would be refused for want of a login the build would
// have made.
func firstUnresolvableImage(ctx context.Context, env string, entities *KCLEntities, rel release.Release) (string, error) {
	platform := platformRegistryHost(entities)
	for _, name := range rel.ArtifactNames() {
		art := rel.Artifacts[name]
		if art.Kind != release.KindOCI {
			continue
		}
		repo := releaseImageRepository(name, art)
		if repo == "" {
			return name, errors.New("the release records no registry for it")
		}
		if host, _ := ociManifestCoordinates(repo); platform != "" && host == platform {
			if err := loginToPlatformRegistry(ctx, env, platform, "", declarationFromEntities(entities)); err != nil {
				return repo, err
			}
		}
		for _, variant := range slices.Sorted(maps.Keys(art.Digests)) {
			ref := repo + "@" + art.Digests[variant]
			ok, err := releaseImageResolves(ctx, ref)
			if ok {
				continue
			}
			if err == nil {
				err = errors.New("the registry does not hold it")
			}
			return ref, err
		}
	}
	return "", nil
}

// releaseImageRepository is where an OCI artifact was pushed: its NAME when
// that names a registry host, which is how every image forge builds is keyed;
// otherwise the registry the release recorded beside it (URI — a hosted
// backend's push base) joined with the name. "" when neither says, which a
// reuse treats as unverifiable.
func releaseImageRepository(name string, art release.Artifact) string {
	// A source map artifact is named for its frontend, not its repository; its
	// URI is the repository whole.
	if _, ok := release.SourcemapsFrontend(name); ok && art.URI != "" {
		return art.URI
	}
	if host, _ := ociManifestCoordinates(name); host != "" {
		return name
	}
	if art.URI != "" {
		return strings.TrimSuffix(art.URI, "/") + "/" + name
	}
	return ""
}

// describePaths is " (a, b, c, +N more)" for a non-empty list, bounded so a
// checkout with thousands of changes still gets one readable line.
func describePaths(paths []string) string {
	const shown = 5
	if len(paths) == 0 {
		return ""
	}
	list := paths
	more := ""
	if len(paths) > shown {
		list = paths[:shown]
		more = fmt.Sprintf(", +%d more", len(paths)-shown)
	}
	return " (" + strings.Join(list, ", ") + more + ")"
}

// announce prints the choice, ONE line, before anything is built: an operator
// who sees an unchanged checkout rebuilding must be told why at the top, not
// infer it from ten minutes of build output.
//
// dryRun changes only the tense. `forge env deploy <env> --dry-run` prints
// the same decision from the same function, as a prediction, so the line a
// preview shows and the line the real run shows are one sentence.
func (c deployReleaseChoice) announce(out io.Writer, env string, dryRun bool) {
	nested := ""
	if len(c.Nested) > 0 {
		nested = fmt.Sprintf(" [nested git checkouts are not part of this tree and were ignored: %s]", strings.Join(c.Nested, ", "))
	}
	if rel := c.Reuse; rel != nil {
		cutAt := "an unrecorded time"
		if !rel.CreatedAt.IsZero() {
			cutAt = rel.CreatedAt.UTC().Format(time.RFC3339)
		}
		verb := "reusing"
		if dryRun {
			verb = "would reuse"
		}
		fmt.Fprintf(out, "[deploy] %s release %s (cut at %s): it records this checkout's tree %s and this forge (%s), "+
			"covers everything env %s declares, and every image it pins still resolves — nothing is built%s\n",
			verb, rel.Version, cutAt, shortHash(rel.Provenance.Tree), rel.Provenance.ForgeVersion, env, nested)
		return
	}
	if dryRun {
		fmt.Fprintf(out, "[deploy] would cut new release %s because %s — a real run builds and pushes every artifact env %s declares first%s\n",
			c.Version, c.Reason, env, nested)
		return
	}
	fmt.Fprintf(out, "[deploy] cutting new release %s because %s%s\n", c.Version, c.Reason, nested)
}
