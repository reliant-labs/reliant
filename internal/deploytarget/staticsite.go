package deploytarget

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StaticSiteProvider publishes a frontend's assembled static tree to an
// object-storage bucket, optionally invalidating a CDN in front of it.
//
// It shares the ENTIRE build-and-assemble half with FirebaseProvider —
// both call buildStagePlan/runStagePlan in staticstage.go — and differs
// only in what happens to the finished tree. That split is deliberate:
// the details that are easy to get subtly wrong (which NODE_ENV each
// phase runs under, that the runtime config document is written after
// the copies so a dev config.js cannot ship to prod) are the ones a
// second copy would drift on first.
//
// # The two prefixes
//
// A deploy uploads the assembled tree TWICE:
//
//	<bucket>/releases/<digest>/…   immutable archive, never mutated
//	<bucket>/live/…               what the CDN serves, synced from it
//
// <digest> is a content hash over the assembled tree (stagingDigest), so
// identical content lands at an identical path and re-deploying unchanged
// content overwrites itself byte-for-byte.
//
// This is what makes promotion real rather than aspirational: PROMOTION
// re-points an environment's live/ at an EXISTING digest. Never a rebuild,
// so it cannot produce different bytes than the release that was tested.
//
// # Retention
//
// After a successful deploy, releases beyond KeepReleases are deleted
// newest-first — but the digest now live and the digest previously live
// are ALWAYS exempt, whatever the count says. A browser that loaded the
// previous release's HTML moments ago is still fetching its hashed assets;
// deleting its prefix turns that page into 404s mid-session. The KCL schema refuses keep_releases = 1
// for the same reason, so the exemption is a belt-and-braces floor
// rather than the only guard.
//
// # Invalidation
//
// See invalidationPaths. The default purges only the paths that cannot
// be content-addressed, because Cloud CDN meters invalidation requests
// per project and a naive /* on every push exhausts that quota for a
// team iterating on a preview environment.
//
// --dry-run prints the resolved plan — build command, assembled layout,
// the exact upload/sync commands, and what would be invalidated — and
// performs NO build, NO upload, and NO invalidation.
type StaticSiteProvider struct {
	// ProjectDir is the project root. Frontend paths and Bundle.Src
	// resolve against it. Empty means the current working directory.
	ProjectDir string

	// Runner is the os/exec indirection (npm / gcloud). Nil falls back
	// to the package default. Tests inject a fake runner.
	Runner commandRunner

	// There is deliberately no StagingRoot knob here. Nothing in
	// production would ever set one — the assembled tree is an
	// intermediate that gets uploaded and is of no further interest — so
	// a field only tests wrote would be a configuration point that
	// production cannot reach, and tests exercising it would be green on
	// a shape that never occurs. Tests that need the staging path read
	// it off the resolved plan (StagePlan.StagingDir) instead, which is
	// the same value production uses.
}

// StaticSiteFrontend is one frontend the StaticSite provider should
// deploy: the resolved build inputs plus the StaticSite spec. The CLI
// builds this from the rendered KCL FrontendEntity; tests construct it
// directly. Mirrors FirebaseFrontend field-for-field on the build half.
type StaticSiteFrontend struct {
	// Name is the forge frontend name (logging + the staging dir name).
	Name string

	// Path is the frontend source dir relative to the project root —
	// where install / `npm run build` run.
	Path string

	// DevRunner is "npm" (default) | "pnpm" | "yarn"; selects the
	// install command.
	DevRunner string

	// BuildEnv is the build-time env injected into the build process
	// (NEXT_PUBLIC_* / VITE_*).
	BuildEnv map[string]string

	// Project is the forge project name (recorded in the source map manifest).
	Project string

	// VersionEnvVar is the variable the bundle reads service.version from
	// (NEXT_PUBLIC_APP_VERSION / VITE_APP_VERSION); forge injects the build id
	// into it unless BuildEnv sets it. Empty injects nothing.
	VersionEnvVar string

	// Sourcemaps is "publish" (default), "public" or "off". See the
	// Sourcemaps* constants.
	Sourcemaps string

	// RuntimeConfigJS is this ENVIRONMENT's rendered runtime config
	// document, written into the assembled tree after every copy. See
	// FirebaseFrontend.RuntimeConfigJS — identical semantics, and the
	// reason a promoted bundle can carry different configuration
	// without rebuilding.
	RuntimeConfigJS string

	// StripRuntimeConfig removes any runtime config document that
	// travelled inside the built bundle (the dev copy under the frontend's
	// public/) instead of writing one. Set for a HOSTED release, whose
	// artifact must be environment-agnostic: its document is spec, written
	// by the control plane after each sync. Mutually exclusive with a
	// non-empty RuntimeConfigJS.
	StripRuntimeConfig bool

	// SourcemapPublisher files the source maps the build extracts (F1) so
	// errors can be symbolicated: pushed to the platform registry when the
	// env's observability is hosted, kept on disk otherwise. nil files nothing
	// (the maps are still extracted, and never served).
	SourcemapPublisher SourcemapPublisher

	// Spec is the StaticSite deploy config.
	Spec StaticSiteSpec
}

// StaticSiteSpec mirrors the kcl/schema.k StaticSite schema (and the
// CLI-side StaticSiteDeploy entity). Kept in this package so the
// provider has no import on internal/cli.
type StaticSiteSpec struct {
	Bucket       string
	PublicDir    string
	BasePath     string
	Bundle       []BundleDirSpec
	CacheControl []CacheRuleSpec
	CDN          *StaticSiteCDNSpec
	KeepReleases int
}

// CacheRuleSpec is one Cache-Control header applied to the objects a
// glob matches. Order is significant — first match wins.
type CacheRuleSpec struct {
	Pattern      string
	CacheControl string
}

// StaticSiteCDNSpec is the CDN in front of the bucket and the
// per-deploy invalidation policy. A nil *StaticSiteCDNSpec means the
// bucket is served directly and a deploy invalidates nothing —
// deliberately distinct from a declared CDN whose Invalidate is "none"
// (a CDN exists; this deploy leaves its cache alone).
type StaticSiteCDNSpec struct {
	URLMap               string
	Invalidate           string // "entrypoints" (default) | "none" | "all"
	ExtraInvalidatePaths []string
}

// Invalidation policy values. Mirrors the KCL enum, which is closed —
// a value outside this set is rejected at render, so the provider's
// switch has no silent default arm.
const (
	InvalidateEntrypoints = "entrypoints"
	InvalidateNone        = "none"
	InvalidateAll         = "all"
)

// Bucket prefixes. releasesPrefix holds immutable per-digest archives;
// livePrefix is the single mutable prefix the CDN serves.
const (
	releasesPrefix = "releases"
	livePrefix     = "live"
)

// There is deliberately NO Go-side default for KeepReleases. The KCL
// schema's default (10) is applied at render and projected
// unconditionally, so a spec arriving here always carries the author's
// resolved value — and a bare 0 therefore means what the schema says it
// means, "retain everything", rather than "unset". A Go-side default
// would have to guess between those two, and guessing wrong in the
// prune direction deletes archived releases irrecoverably.

// minRetainedReleases is the floor retention can never go below: the
// live release plus its predecessor, whose assets in-flight pages still
// fetch. Enforced independently of KeepReleases.
const minRetainedReleases = 2

// Name returns the provider identifier.
func (StaticSiteProvider) Name() string { return "static-site" }

func (p StaticSiteProvider) runner() commandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return defaultRunner
}

// normalizedBucket returns the bucket as a `gs://name` URL, accepting
// either a bare name or an already-schemed value.
func (s StaticSiteSpec) normalizedBucket() string {
	b := strings.TrimSuffix(strings.TrimSpace(s.Bucket), "/")
	if strings.HasPrefix(b, "gs://") {
		return b
	}
	return "gs://" + b
}

// releaseURI is the immutable archive prefix for one content digest.
func (s StaticSiteSpec) releaseURI(digest string) string {
	return s.normalizedBucket() + "/" + releasesPrefix + "/" + digest
}

// liveURI is the single mutable prefix the CDN serves.
func (s StaticSiteSpec) liveURI() string {
	return s.normalizedBucket() + "/" + livePrefix
}

// effectiveKeepReleases resolves the retention count actually applied.
// 0 means retain everything; any positive value is floored at
// minRetainedReleases so retention can never delete the predecessor
// even if a spec reached this far carrying 1.
func (s StaticSiteSpec) effectiveKeepReleases() int {
	if s.KeepReleases == 0 {
		return 0
	}
	if s.KeepReleases < minRetainedReleases {
		return minRetainedReleases
	}
	return s.KeepReleases
}

// invalidationPolicy returns the resolved policy for this deploy. No
// CDN block means nothing to invalidate.
func (s StaticSiteSpec) invalidationPolicy() string {
	if s.CDN == nil {
		return InvalidateNone
	}
	if s.CDN.Invalidate == "" {
		return InvalidateEntrypoints
	}
	return s.CDN.Invalidate
}

// Deploy ships every frontend in the group to its bucket. Reads the
// frontends off group.StaticSites and the dry-run knob off group.DryRun
// so this provider dispatches through the registry like every other one.
func (p StaticSiteProvider) Deploy(ctx context.Context, group ServiceGroup) error {
	for i, fe := range group.StaticSites {
		if err := p.deployOne(ctx, fe, group.Env, group.DryRun); err != nil {
			if i > 0 {
				return afterShipping(err)
			}
			return err
		}
	}
	return nil
}

// staticSitePlan is the resolved, side-effect-free description of one
// frontend's static-site deploy: the shared build-and-assemble StagePlan
// plus the publish half. The digest — and therefore the release URI — is
// NOT known at plan time, because it is a hash over the tree the build
// has not produced yet. That is why the publish commands are built by
// uploadCmds(digest) after the stage runs, rather than being fields here.
type staticSitePlan struct {
	Stage StagePlan
	Spec  StaticSiteSpec
}

// stageInput projects a StaticSiteFrontend onto the target-neutral
// StageInput. Structurally identical to FirebaseProvider.stageInput —
// which is the point: both targets consume the same assembled tree.
func (p StaticSiteProvider) stageInput(fe StaticSiteFrontend) StageInput {
	staging := filepath.Join(os.TempDir(), "forge-static-site-"+fe.Name)
	return StageInput{
		Name:               fe.Name,
		Path:               fe.Path,
		DevRunner:          fe.DevRunner,
		BuildEnv:           fe.BuildEnv,
		PublicDir:          fe.Spec.PublicDir,
		BasePath:           fe.Spec.BasePath,
		Bundle:             fe.Spec.Bundle,
		RuntimeConfigJS:    fe.RuntimeConfigJS,
		StripRuntimeConfig: fe.StripRuntimeConfig,
		ProjectDir:         p.ProjectDir,
		StagingRoot:        staging,
		Project:            fe.Project,
		VersionEnvVar:      fe.VersionEnvVar,
		Sourcemaps:         fe.Sourcemaps,
	}
}

func (p StaticSiteProvider) buildPlan(fe StaticSiteFrontend) (staticSitePlan, error) {
	stage, err := buildStagePlan(p.stageInput(fe))
	if err != nil {
		return staticSitePlan{}, fmt.Errorf("static-site %s: %w", fe.Name, err)
	}
	return staticSitePlan{Stage: stage, Spec: fe.Spec}, nil
}

func (p StaticSiteProvider) deployOne(ctx context.Context, fe StaticSiteFrontend, env string, dryRun bool) error {
	plan, err := p.buildPlan(fe)
	if err != nil {
		return shippedNothing(err)
	}

	if dryRun {
		printStaticSitePlan(os.Stdout, plan)
		return nil
	}

	runner := p.runner()
	fmt.Printf("  [static-site] %s: building (%s) in %s...\n",
		plan.Stage.Name, strings.Join(plan.Stage.BuildCmd, " "), plan.Stage.FrontendDir)

	// Everything before the first upload is local: the bucket is untouched
	// until it runs, so a failure up to there shipped nothing
	// (frontend_not_shipped.go).
	//
	// Build + assemble — the shared static staging step.
	res, err := runStagePlan(ctx, runner, plan.Stage)
	if err != nil {
		return shippedNothing(err)
	}
	reportStageResult("static-site", plan.Stage.Name, res)

	// File the source maps before the release is uploaded or goes live: a
	// build whose maps could not be filed must not ship unsymbolicatable.
	if err := FileStageSourcemaps(ctx, fe.SourcemapPublisher, "static-site", plan.Stage.Project, plan.Stage.Name, res); err != nil {
		return shippedNothing(err)
	}

	// The artifact's content-addressed identity, computed from the tree
	// that now exists on disk. Everything downstream keys on it.
	digest, err := stagingDigest(plan.Stage.StagingDir)
	if err != nil {
		return shippedNothing(fmt.Errorf("static-site %s: %w", fe.Name, err))
	}

	// The digest currently live, read BEFORE this deploy overwrites the
	// state file. Retention exempts it explicitly: a browser that loaded
	// the previous release's HTML moments ago still fetches its assets.
	previous := ""
	if st, rerr := ReadDeployState(p.projectDir(), p.Name(), env, fe.Name); rerr == nil && st != nil {
		previous = st.Tag
	}

	// Publish: archive first, then point live at it. This ORDER matters —
	// live must never reference a release prefix that does not exist yet,
	// or a deploy interrupted between the two steps serves 404s instead of
	// the previous (still perfectly good) content.
	fmt.Printf("  [static-site] %s: publishing release %s to %s...\n", fe.Name, digest, fe.Spec.normalizedBucket())
	for _, argv := range p.uploadCmds(fe.Spec, plan.Stage.StagingDir, digest) {
		if err := runInDir(ctx, runner, plan.Stage.StagingDir, nil, argv); err != nil {
			return fmt.Errorf("static-site %s: upload: %w", fe.Name, err)
		}
	}
	if err := p.syncToLive(ctx, fe.Spec, fe.Spec.releaseURI(digest)); err != nil {
		return fmt.Errorf("static-site %s: %w", fe.Name, err)
	}

	// Record the new live digest. Written AFTER a successful sync, so a
	// failed publish leaves the previous release recorded.
	if _, err := WriteDeployState(p.projectDir(), p.Name(), env, fe.Name, DeployState{
		Image: fe.Spec.normalizedBucket(),
		Tag:   digest,
	}); err != nil {
		return fmt.Errorf("static-site %s: record deploy state: %w", fe.Name, err)
	}

	if err := p.invalidate(ctx, fe, dryRun); err != nil {
		return fmt.Errorf("static-site %s: %w", fe.Name, err)
	}

	// Retention runs LAST and is best-effort: the deploy has already
	// succeeded, and failing it because an old prefix could not be
	// deleted would report a working site as broken. The failure is
	// surfaced as a warning instead, because a silently-unbounded bucket
	// is its own (slower) problem.
	if err := p.pruneReleases(ctx, fe, digest, previous); err != nil {
		fmt.Printf("  ⚠️  static-site %s: release retention: %v\n", fe.Name, err)
	}

	fmt.Printf("  [static-site] %s: deployed (release %s).\n", fe.Name, digest)
	return nil
}

func (p StaticSiteProvider) projectDir() string {
	if p.ProjectDir != "" {
		return p.ProjectDir
	}
	return "."
}

// uploadCmds returns the argv list that uploads the assembled tree into
// its immutable release prefix, with Cache-Control applied.
//
// One `gcloud storage cp` per cache rule, most-specific FIRST, each with
// its own Cache-Control header — then a final unheadered pass that
// catches anything no rule matched. `cp -n` (no-clobber) on the trailing
// pass is what makes "first rule wins" true: an object already uploaded
// by an earlier, more specific rule keeps the header that rule gave it
// rather than being overwritten by the catch-all.
//
// A spec with no cache rules is one plain recursive copy.
func (p StaticSiteProvider) uploadCmds(spec StaticSiteSpec, stagingDir, digest string) [][]string {
	dst := spec.releaseURI(digest)
	if len(spec.CacheControl) == 0 {
		return [][]string{{"gcloud", "storage", "cp", "--recursive", ".", dst}}
	}
	cmds := make([][]string, 0, len(spec.CacheControl)+1)
	for _, rule := range spec.CacheControl {
		cmds = append(cmds, []string{
			"gcloud", "storage", "cp", "--recursive",
			"--cache-control=" + rule.CacheControl,
			rule.Pattern, dst,
		})
	}
	// Catch-all for objects no rule matched. --no-clobber preserves the
	// headers the rules above already set.
	cmds = append(cmds, []string{
		"gcloud", "storage", "cp", "--recursive", "--no-clobber", ".", dst,
	})
	return cmds
}

// syncToLive re-points the live prefix at a release prefix. `rsync
// --recursive --delete-unmatched-destination-objects` makes live an
// exact mirror: files removed since the previous release are removed
// from live too, which a plain copy would leave behind to be served
// indefinitely.
func (p StaticSiteProvider) syncToLive(ctx context.Context, spec StaticSiteSpec, releaseURI string) error {
	argv := []string{
		"gcloud", "storage", "rsync", "--recursive",
		"--delete-unmatched-destination-objects",
		releaseURI, spec.liveURI(),
	}
	if err := runInDir(ctx, p.runner(), p.projectDir(), nil, argv); err != nil {
		return fmt.Errorf("sync %s -> %s: %w", releaseURI, spec.liveURI(), err)
	}
	return nil
}

// invalidationPaths resolves which CDN paths a deploy purges.
//
// THE DEFAULT IS NARROW ON PURPOSE. Cloud CDN meters invalidation
// requests per project, and each one is a global purge that also forces
// a cold origin fetch for everything it drops. A team pushing to a
// preview environment 50 times an hour will exhaust that quota with a
// naive `/*` per deploy — and the failure lands as a deploy error on
// push 40-something, long after the habit formed and with nothing
// connecting the two.
//
// So "entrypoints" purges only what CANNOT be content-addressed:
//
//   - "/" and "/index.html" — the entry documents, plus the same pair
//     under base_path when the site is mounted under one.
//   - the runtime config document, which is the ONE file that differs
//     between environments and therefore the one a promotion changes
//     without changing any bundle filename.
//   - anything named in extra_invalidate_paths, for documents forge
//     cannot infer (a bare /manifest.json, a service worker).
//
// Content-hashed assets need no invalidation ever: a new build emits new
// filenames, so the old objects are simply never requested again. That
// makes this set O(a few paths) per deploy regardless of site size, and
// correct for every build that hashes asset filenames — which is every
// modern JS build. A build that does NOT hash filenames is what
// `invalidate = "all"` is for, and it warns each time so the cost stays
// a decision rather than a default nobody revisited.
func invalidationPaths(spec StaticSiteSpec) []string {
	switch spec.invalidationPolicy() {
	case InvalidateNone:
		return nil
	case InvalidateAll:
		return []string{"/*"}
	}

	base := "/" + strings.Trim(cleanDestRel(spec.BasePath), "./")
	if base == "/" || base == "/." {
		base = ""
	}

	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	add("/")
	add("/index.html")
	if base != "" {
		add(base + "/")
		add(base + "/index.html")
	}
	add(base + "/" + FrontendConfigJSName)

	if spec.CDN != nil {
		for _, extra := range spec.CDN.ExtraInvalidatePaths {
			add(extra)
		}
	}
	return paths
}

// invalidate purges the resolved paths from the CDN. No CDN block, or
// an empty path set, is a silent no-op.
func (p StaticSiteProvider) invalidate(ctx context.Context, fe StaticSiteFrontend, dryRun bool) error {
	spec := fe.Spec
	paths := invalidationPaths(spec)
	if spec.CDN == nil || len(paths) == 0 {
		return nil
	}

	if spec.invalidationPolicy() == InvalidateAll {
		fmt.Printf("  ⚠️  static-site %s: invalidate = \"all\" purges the entire CDN cache on every deploy. "+
			"Cloud CDN rate-limits invalidations per project and each purge forces a cold origin fetch — "+
			"if this build emits content-hashed asset filenames, \"entrypoints\" (the default) is both cheaper and correct.\n", fe.Name)
	}

	for _, path := range paths {
		argv := []string{
			"gcloud", "compute", "url-maps", "invalidate-cdn-cache", spec.CDN.URLMap,
			"--path", path, "--async",
		}
		if dryRun {
			fmt.Printf("    [DRY-RUN] would exec: %s\n", strings.Join(argv, " "))
			continue
		}
		if err := runInDir(ctx, p.runner(), p.projectDir(), nil, argv); err != nil {
			return fmt.Errorf("invalidate %s: %w", path, err)
		}
	}
	return nil
}

// pruneReleases deletes archived releases beyond the retention count,
// newest first.
//
// live and previous are ALWAYS exempt regardless of the count: deleting
// either 404s pages that are still being served, which is the one
// outcome a retention policy must not be able to produce. KeepReleases == 0 means
// retain everything and prunes nothing.
func (p StaticSiteProvider) pruneReleases(ctx context.Context, fe StaticSiteFrontend, live, previous string) error {
	keep := fe.Spec.effectiveKeepReleases()
	if keep == 0 {
		return nil
	}

	digests, err := p.listReleaseDigests(ctx, fe.Spec)
	if err != nil {
		return err
	}
	victims := releasesToPrune(digests, keep, live, previous)
	for _, d := range victims {
		argv := []string{"gcloud", "storage", "rm", "--recursive", fe.Spec.releaseURI(d) + "/"}
		if err := runInDir(ctx, p.runner(), p.projectDir(), nil, argv); err != nil {
			return fmt.Errorf("delete release %s: %w", d, err)
		}
		fmt.Printf("  [static-site] %s: pruned release %s\n", fe.Name, d)
	}
	return nil
}

// listReleaseDigests returns the digests currently archived in the
// bucket, in listing order.
func (p StaticSiteProvider) listReleaseDigests(ctx context.Context, spec StaticSiteSpec) ([]string, error) {
	prefix := spec.normalizedBucket() + "/" + releasesPrefix + "/"
	out, err := outputWithEnv(ctx, p.runner(), nil, "gcloud", "storage", "ls", prefix)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	var digests []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "/"))
		if line == "" || !strings.HasPrefix(line, prefix) {
			continue
		}
		if d := strings.TrimPrefix(line, prefix); d != "" && !strings.Contains(d, "/") {
			digests = append(digests, d)
		}
	}
	return digests, nil
}

// releasesToPrune decides which archived digests to delete.
//
// Pure, and separated from the gcloud plumbing precisely so the
// never-delete-live-or-previous rule is testable without a bucket. The
// exemptions are applied BEFORE the count, so `keep` bounds the
// prunable set rather than the total — a bucket holding exactly `keep`
// releases of which two are exempt deletes nothing rather than deleting
// one that is still referenced.
func releasesToPrune(digests []string, keep int, live, previous string) []string {
	if keep <= 0 {
		return nil
	}
	exempt := map[string]bool{}
	if live != "" {
		exempt[live] = true
	}
	if previous != "" {
		exempt[previous] = true
	}

	candidates := make([]string, 0, len(digests))
	for _, d := range digests {
		if !exempt[d] {
			candidates = append(candidates, d)
		}
	}
	// Sorted so the choice is deterministic rather than dependent on
	// listing order. A content digest carries no recency information, so
	// there is no "oldest" to prefer — determinism is the property worth
	// having, and the exemptions above are what protect correctness.
	sort.Strings(candidates)

	// Room left after the exempt releases are counted against the budget.
	room := keep - (len(exempt))
	if room < 0 {
		room = 0
	}
	if len(candidates) <= room {
		return nil
	}
	return candidates[:len(candidates)-room]
}

// printStaticSitePlan renders the dry-run plan for one frontend: the
// shared build/assemble lines, then the publish and invalidation steps.
//
// The release digest is shown as a placeholder because it is a hash over
// a tree the dry run deliberately does not build. Printing a fabricated
// digest would be worse than the placeholder — it would look like a real
// artifact identity a reader could go find in the bucket.
func printStaticSitePlan(w io.Writer, plan staticSitePlan) {
	_, _ = fmt.Fprintf(w, "  [DRY-RUN] static-site deploy plan for frontend %q:\n", plan.Stage.Name)
	printStagePlanBuild(w, plan.Stage)

	const placeholder = "<digest>"
	_, _ = fmt.Fprintf(w, "    release:      %s   (sha256 over the assembled tree)\n", plan.Spec.releaseURI(placeholder))
	_, _ = fmt.Fprintf(w, "    live:         %s\n", plan.Spec.liveURI())
	for _, argv := range (StaticSiteProvider{}).uploadCmds(plan.Spec, plan.Stage.StagingDir, placeholder) {
		_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec (cwd %s): %s\n", plan.Stage.StagingDir, strings.Join(argv, " "))
	}
	_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec: gcloud storage rsync --recursive --delete-unmatched-destination-objects %s %s\n",
		plan.Spec.releaseURI(placeholder), plan.Spec.liveURI())

	if plan.Spec.CDN == nil {
		_, _ = fmt.Fprintf(w, "    cdn:          none declared (no invalidation)\n")
	} else {
		_, _ = fmt.Fprintf(w, "    cdn:          url_map=%s invalidate=%s\n",
			plan.Spec.CDN.URLMap, plan.Spec.invalidationPolicy())
		for _, path := range invalidationPaths(plan.Spec) {
			_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec: gcloud compute url-maps invalidate-cdn-cache %s --path %s --async\n",
				plan.Spec.CDN.URLMap, path)
		}
	}

	if keep := plan.Spec.effectiveKeepReleases(); keep == 0 {
		_, _ = fmt.Fprintf(w, "    retention:    keep everything (no releases pruned)\n")
	} else {
		_, _ = fmt.Fprintf(w, "    retention:    keep %d releases (the live release and its predecessor are never pruned)\n", keep)
	}
}
