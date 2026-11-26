package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/pkg/release"
)

// SBOMs at the release cut (design doc §1.1, §1.3).
//
// Every artifact a cut records gets an SBOM of the bytes that ship, generated
// by forge's pinned syft just before the release is written, and recorded as
// evidence on the artifact (release.Artifact.SBOM, outside its identity). The
// bytes always go to this machine's ledger SBOM store, whichever ledger the
// env records releases in, so a scan never needs the network for them.
//
// An artifact that is not code that ships (a frontend's source maps, a git
// source pin; release.SBOMApplicable) records a not_applicable ref saying
// what it is, so every artifact of a cut is accounted for one way or the
// other, and the cut reports "N of N applicable" with the rest named.
//
// WARN-FIRST. Nothing here can fail a cut: an artifact syft could not
// describe is recorded as unavailable, with the reason, and the cut prints
// one warning for it. Refusing a cut without an SBOM is the sbom-required
// rule's job, behind its rollout row, not this one's.
//
// Two ways to turn it off: `--no-sbom` for one run (warned, and each artifact
// records why it has none), and `provenance_sbom = forge.Control {mode =
// "off"}` for an env, declared, which records nothing and says nothing.

// sbomTool resolves forge's pinned syft. A var so tests state the tool.
var sbomTool sbom.Tool = func(ctx context.Context) (string, string, error) {
	return toolfetch.Ensure(ctx, toolfetch.Syft)
}

// sbomRunner runs syft; nil execs it. A var so tests fake syft.
var sbomRunner sbom.Runner

// sbomCacheDir is the SBOM cache's directory; "" caches nothing. A var so a
// test keeps the cache in its temp dir.
var sbomCacheDir = func() string {
	dir, err := sbom.DefaultCacheDir()
	if err != nil {
		return ""
	}
	return dir
}

// sbomGenerated is one artifact's freshly produced SBOM, as a post-generate
// hook sees it.
type sbomGenerated struct {
	ProjectDir, Env, Version string
	// CutAt is the release's CreatedAt: fixed for the release, unlike now.
	CutAt time.Time
	// Name is the artifact's key in the release (an image's repository).
	Name string
	// Built reports that this cut's build state recorded the artifact: this
	// pipeline built (or carried) and pushed it.
	Built bool
	// Artifact is the artifact as the release will record it, with
	// Artifact.SBOM the ref so far (status ok, digest set).
	Artifact release.Artifact
	// Bytes is the SBOM, already stored in the machine ledger under
	// Artifact.SBOM.Digest.
	Bytes    []byte
	Entities *KCLEntities
}

// sbomPostGenerateHook is THE SEAM after SBOM generation: it runs once per
// artifact whose SBOM was produced, after the bytes are in the machine
// ledger and before the release is cut, and returns the ref to record —
// in.Artifact.SBOM, with whatever it adds (an OCI referrer's address in
// Referrer). Attaching the SBOM beside the image as a referrer and scanning
// it plug in here.
//
// A hook may not change which SBOM the artifact names: a returned ref with a
// different Digest is refused like an error. An error warns, keeps the ref
// as it was, and never fails the cut. Hooks for different artifacts run
// concurrently (bounded); the hooks of one artifact run in order.
type sbomPostGenerateHook func(ctx context.Context, in sbomGenerated) (release.SBOMRef, error)

// sbomPostGenerate is the hook list, empty by default. Append in an init().
var sbomPostGenerate []sbomPostGenerateHook

// sbomCut is what one cut's SBOM step needs.
type sbomCut struct {
	ProjectDir, Env, Version, OutputDir string
	Entities                            *KCLEntities
	// CreatedAt is the release's cut time, handed to the hooks.
	CreatedAt time.Time
	// Built is the set of artifact names the build state recorded: images
	// this pipeline built and pushed (and hosted static sites).
	Built map[string]bool
	// NoSBOM is --no-sbom.
	NoSBOM bool
	// Sources resolves a git-sourced frontend's pin to its checkout, for
	// the lockfile its site's SBOM is read from. nil builds forge's
	// resolver (the same cache the build and the release's pin used).
	Sources pinResolver
}

// generateReleaseSBOMs sets Artifact.SBOM on every artifact it can describe
// and writes the bytes to the machine ledger. It never returns an error: see
// the warn-first note above.
func generateReleaseSBOMs(ctx context.Context, in sbomCut, artifacts map[string]release.Artifact, warn io.Writer) {
	if sbomPolicyOff(in.Entities) {
		return
	}
	if cloud.ClaimableActive() {
		// An anonymous deploy prints only what its human must act on (the
		// live URL, the claim link), as printReleaseCut does; each
		// artifact's SBOM state is still recorded on the release.
		warn = io.Discard
	}
	names := sortedArtifactNames(artifacts)
	// Every artifact no SBOM describes says so, and why, whatever happens
	// to the others: --no-sbom skips SBOMs, it does not make source maps
	// code.
	for _, name := range names {
		if ref, ok := release.NotApplicableSBOMRef(name, artifacts[name]); ok {
			setArtifactSBOM(artifacts, name, ref)
		}
	}
	if in.NoSBOM {
		n := 0
		for _, name := range names {
			if !release.SBOMApplicable(name, artifacts[name]) {
				continue
			}
			setArtifactSBOM(artifacts, name, release.SBOMRef{Status: release.SBOMStatusUnavailable, Reason: "skipped: --no-sbom"})
			n++
		}
		if n > 0 {
			fmt.Fprintf(warn, "[build] warning: --no-sbom: release %s recorded without an SBOM for its %d artifact(s); they will show as not scanned\n", in.Version, n)
		}
		return
	}

	targets, refused := planReleaseSBOMTargets(ctx, in, artifacts)
	for name, ref := range refused {
		setArtifactSBOM(artifacts, name, ref)
	}
	if len(targets) > 0 {
		recordGeneratedSBOMs(ctx, in, artifacts, targets, warn)
	}
	printSBOMCoverage(warn, in.Version, artifacts, targets)
}

// printSBOMCoverage is the cut's SBOM summary: how many of the artifacts an
// SBOM applies to recorded one (and how many came from the cache), each one
// that could not with why, and what was not applicable:
//
//	[build] SBOMs: 10 of 10 applicable artifacts recorded (6 from cache); 6 not applicable (3 source maps, 3 git source pins)
func printSBOMCoverage(warn io.Writer, version string, artifacts map[string]release.Artifact, targets []plannedSBOM) {
	applicable, recorded, cached, unavailable := 0, 0, 0, 0
	var notApplicable []string
	for _, name := range sortedArtifactNames(artifacts) {
		ref := artifacts[name].SBOM
		if !release.SBOMApplicable(name, artifacts[name]) {
			if ref.NotApplicable() {
				notApplicable = append(notApplicable, ref.Reason)
			}
			continue
		}
		applicable++
		switch {
		case ref.HasSBOM():
			recorded++
		case ref == nil:
			// Every applicable artifact is planned or refused, so this
			// is forge's bug; still never a silent gap in the count.
			unavailable++
			fmt.Fprintf(warn, "[build] warning: release %s recorded without an SBOM for %s: forge planned none for it\n", version, name)
		default:
			unavailable++
			fmt.Fprintf(warn, "[build] warning: release %s recorded without an SBOM for %s: %s\n", version, name, ref.Reason)
		}
	}
	for _, t := range targets {
		if t.cached && artifacts[t.name].SBOM.HasSBOM() {
			cached++
		}
	}
	if applicable+len(notApplicable) == 0 {
		return
	}
	line := fmt.Sprintf("[build] SBOMs: %d of %d applicable artifacts recorded (%d from cache)", recorded, applicable, cached)
	if unavailable > 0 {
		line += fmt.Sprintf(", %d unavailable", unavailable)
	}
	if na := notApplicableSummary(notApplicable); na != "" {
		line += "; " + na
	}
	fmt.Fprintln(warn, line)
}

// notApplicableSummary counts not-applicable artifacts by class, from their
// reasons: "6 not applicable (3 source maps, 3 git source pins)". "" for
// none.
func notApplicableSummary(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	type class struct{ one, many string }
	classes := map[string]class{
		release.SBOMNotApplicableSourcemaps: {"source maps", "source maps"},
		release.SBOMNotApplicableGitSource:  {"git source pin", "git source pins"},
	}
	counts := map[string]int{}
	var order []string
	for _, r := range reasons {
		key := r
		if _, ok := classes[r]; !ok {
			key = ""
		}
		if counts[key] == 0 {
			order = append(order, key)
		}
		counts[key]++
	}
	sort.Slice(order, func(i, j int) bool { return order[i] > order[j] }) // the "other" class ("") last
	parts := make([]string, 0, len(order))
	for _, key := range order {
		c, ok := classes[key]
		if !ok {
			c = class{"other", "other"}
		}
		label := c.many
		if counts[key] == 1 {
			label = c.one
		}
		parts = append(parts, fmt.Sprintf("%d %s", counts[key], label))
	}
	return fmt.Sprintf("%d not applicable (%s)", len(reasons), strings.Join(parts, ", "))
}

// plannedSBOM is one target and, once generated, whether it was a cache hit.
type plannedSBOM struct {
	name   string
	target sbom.Target
	cached bool
}

// recordGeneratedSBOMs runs syft over targets, stores each SBOM in the
// machine ledger, runs the post-generate hooks and records the refs.
func recordGeneratedSBOMs(ctx context.Context, in sbomCut, artifacts map[string]release.Artifact, targets []plannedSBOM, warn io.Writer) {
	store, err := openMachineLedger(in.ProjectDir)
	if err != nil {
		for _, t := range targets {
			setArtifactSBOM(artifacts, t.name, release.SBOMRef{Status: release.SBOMStatusUnavailable,
				Reason: "the machine ledger that stores SBOMs could not be opened: " + err.Error()})
		}
		return
	}
	gen := &sbom.Generator{
		Tool: sbomTool, Runner: sbomRunner, RegistryAuth: sbomRegistryAuth(in.ProjectDir, in.Env, in.Entities),
		Concurrency: sbom.DefaultConcurrency, Timeout: sbom.DefaultTimeout,
	}
	if dir := sbomCacheDir(); dir != "" {
		gen.Cache = &sbom.Cache{Dir: dir}
	}
	list := make([]sbom.Target, len(targets))
	for i, t := range targets {
		list[i] = t.target
	}
	results := gen.Generate(ctx, list)

	type hookJob struct {
		name string
		data []byte
	}
	var jobs []hookJob
	for i, r := range results {
		targets[i].cached = r.Cached
		name := targets[i].name
		ref := r.Ref
		if !r.Ref.HasSBOM() {
			setArtifactSBOM(artifacts, name, ref)
			continue
		}
		digest, err := store.PutSBOM(r.Bytes)
		if err == nil && digest != ref.Digest {
			err = fmt.Errorf("the ledger stored it as %s, not %s", digest, ref.Digest)
		}
		if err != nil {
			setArtifactSBOM(artifacts, name, release.SBOMRef{Status: release.SBOMStatusUnavailable, Source: ref.Source,
				Reason: "could not store the SBOM in the machine ledger: " + err.Error()})
			continue
		}
		setArtifactSBOM(artifacts, name, ref)
		jobs = append(jobs, hookJob{name: name, data: r.Bytes})
	}
	if len(sbomPostGenerate) == 0 {
		return
	}

	// The hooks see the artifacts as recorded so far; each writes back only
	// its own artifact, under mu.
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 4)
	)
	inputs := make([]sbomGenerated, 0, len(jobs))
	for _, j := range jobs {
		inputs = append(inputs, sbomGenerated{ProjectDir: in.ProjectDir, Env: in.Env, Version: in.Version, CutAt: in.CreatedAt, Name: j.name, Built: in.Built[j.name], Artifact: artifacts[j.name], Bytes: j.data, Entities: in.Entities})
	}
	for _, in := range inputs {
		wg.Add(1)
		go func(in sbomGenerated) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ref := runSBOMHooks(ctx, in, warn, &mu)
			mu.Lock()
			setArtifactSBOM(artifacts, in.Name, ref)
			mu.Unlock()
		}(in)
	}
	wg.Wait()
}

// sbomRegistryCredential is registryCredentialFor, as a var so a test states
// the identity without a control plane.
var sbomRegistryCredential = registryCredentialFor

// registryCredentialFor is the credential forge presents to a registry host
// for an in-process push or read: nil — the ambient docker credential store,
// which `docker push` used for the same image — for every host but the env's
// platform registry, and for that one the env's control-plane identity, the
// one token the platform registry takes (bundle_registry_auth.go). The docker
// store holds the platform host only when an image push logged in to it
// earlier in this process; a site-only or CI-pushed env never did.
//
// The identity is memoized with the bundle push's, per (project, env).
func registryCredentialFor(projectDir, env string, e *KCLEntities, host string) (auth.CredentialFunc, error) {
	if registryTakesNoCredentials(host) {
		return nil, nil
	}
	platform := platformRegistryHost(e)
	if platform == "" || !strings.EqualFold(host, platform) {
		return nil, nil
	}
	key := projectDir + "\x00" + env
	if v, ok := bundleRegistryAuths.Load(key); ok {
		return v.(bundleRegistryAuth).credentialFunc(), nil
	}
	id, err := bundleRegistryAuthOf(env, e)
	if err != nil {
		return nil, err
	}
	bundleRegistryAuths.Store(key, id)
	return id.credentialFunc(), nil
}

// sbomRegistryAuth gives syft forge's own credential for a registry it reads
// an image from (registryCredentialFor): the env's control-plane identity for
// its platform registry, so reading a hosted image (a hosted backend CI
// pushed, a mirrored third-party image, an image carried from an earlier
// release) does not depend on a `docker login`. Every other registry gets
// nothing from here: syft reads the ambient docker credential store for it,
// the same store forge's own pushes to it use (dockercred).
//
// Nothing is resolved until syft reads from the platform registry, and the
// token is asked for per read, as bundle pushes do, so a long cut outlives a
// short-lived helper token.
func sbomRegistryAuth(projectDir, env string, e *KCLEntities) sbom.RegistryAuth {
	return func(ctx context.Context, host string) (sbom.RegistryCredential, bool, error) {
		credential, err := sbomRegistryCredential(projectDir, env, e, host)
		if err != nil || credential == nil {
			return sbom.RegistryCredential{}, false, err
		}
		c, err := credential(ctx, host)
		if err != nil {
			return sbom.RegistryCredential{}, false, err
		}
		if c.Password == "" {
			return sbom.RegistryCredential{}, false, nil
		}
		return sbom.RegistryCredential{Username: c.Username, Password: c.Password}, true, nil
	}
}

// runSBOMHooks runs every post-generate hook over one artifact, in order,
// and returns the ref to record.
func runSBOMHooks(ctx context.Context, in sbomGenerated, warn io.Writer, mu *sync.Mutex) release.SBOMRef {
	ref := *in.Artifact.SBOM
	for _, hook := range sbomPostGenerate {
		cur := in
		art := in.Artifact
		curRef := ref
		art.SBOM = &curRef
		cur.Artifact = art
		next, err := hook(ctx, cur)
		if err == nil && next.Digest != ref.Digest {
			err = fmt.Errorf("a hook may not change the SBOM an artifact names (%s → %s)", ref.Digest, next.Digest)
		}
		if err != nil {
			mu.Lock()
			fmt.Fprintf(warn, "[build] warning: release %s: SBOM of %s: %v\n", in.Version, in.Name, err)
			mu.Unlock()
			continue
		}
		ref = next
	}
	return ref
}

// planReleaseSBOMTargets decides, per artifact, what syft reads (design doc
// §1.1: the bytes that ship). It returns the targets, and an unavailable ref
// for each artifact that should have an SBOM but cannot be described.
// Artifacts that describe no shipped code (a source pin, a frontend's source
// maps; release.SBOMApplicable) get neither: the caller records them not
// applicable.
func planReleaseSBOMTargets(ctx context.Context, in sbomCut, artifacts map[string]release.Artifact) ([]plannedSBOM, map[string]release.SBOMRef) {
	refused := map[string]release.SBOMRef{}
	unavailable := func(name, source, reason string) {
		refused[name] = release.SBOMRef{Status: release.SBOMStatusUnavailable, Source: source, Reason: reason}
	}
	sites := hostedSiteDirs(ctx, in, artifacts)
	var npmDirs, modDirs map[string]string

	var out []plannedSBOM
	add := func(name string, t sbom.Target) {
		t.Name = name
		out = append(out, plannedSBOM{name: name, target: t})
	}
	for _, name := range sortedArtifactNames(artifacts) {
		art := artifacts[name]
		if !release.SBOMApplicable(name, art) {
			continue
		}
		switch art.Kind {
		case release.KindOCI:
			if site, ok := sites[name]; ok {
				// A static site: its bundle carries no package metadata, so
				// the SBOM is of the frontend's lockfile (amendment 5), in
				// the checkout the site was built from: this one, or a
				// git-sourced frontend's source at the pinned commit.
				if site.unresolved != "" {
					unavailable(name, release.SBOMSourceLockfile, site.unresolved)
					continue
				}
				if site.dir == "" {
					unavailable(name, release.SBOMSourceLockfile, fmt.Sprintf("frontend %s has no directory in this checkout to read a lockfile from", site.name))
					continue
				}
				lock := findLockfile(site.dir, site.root, jsLockfiles)
				if lock == "" {
					where := relOrAbs(in.ProjectDir, site.dir)
					if site.source != "" {
						where = fmt.Sprintf("its source %s (%s), up to that checkout's root", site.source, site.dir)
					}
					unavailable(name, release.SBOMSourceLockfile, fmt.Sprintf("frontend %s has no lockfile (%s) in %s or above it", site.name, strings.Join(jsLockfiles, ", "), where))
					continue
				}
				add(name, sbom.Target{Kind: sbom.KindLockfile, Ref: lock})
				continue
			}
			digest, ok := art.SharedDigest()
			if !ok {
				unavailable(name, "", "a variant image has one digest per variant, and forge records one SBOM per artifact")
				continue
			}
			kind := sbom.KindRegistry
			if in.Built[name] && art.CarriedFrom == "" {
				// Built by this pipeline: the docker daemon most likely
				// holds it, which saves the download. An image carried from
				// an earlier release was not built here.
				kind = sbom.KindImage
			}
			// No source version: the image's SBOM describes the image, which
			// later releases carry unchanged (carried_from), and the release
			// that ships it is the record's binding, not the document's. A
			// release version here would also make every cut a cache miss.
			add(name, sbom.Target{Kind: kind, Ref: name + "@" + digest, Subject: digest, Platform: sbomPlatform(art.Platforms)})
		case release.KindFile:
			path := fileArtifactPath(in.ProjectDir, in.OutputDir, name)
			add(name, sbom.Target{Kind: sbom.KindBinary, Ref: path, Subject: art.Integrity, Version: art.Version})
		case release.KindNPM:
			if npmDirs == nil {
				npmDirs = sbomNPMPackageDirs(in.ProjectDir)
			}
			dir, ok := npmDirs[name]
			if !ok {
				unavailable(name, release.SBOMSourceLockfile, "its package directory was not found in this checkout")
				continue
			}
			lock := findLockfile(dir, in.ProjectDir, jsLockfiles)
			if lock == "" {
				unavailable(name, release.SBOMSourceLockfile, fmt.Sprintf("no lockfile (%s) in %s or above it", strings.Join(jsLockfiles, ", "), relOrAbs(in.ProjectDir, dir)))
				continue
			}
			add(name, sbom.Target{Kind: sbom.KindLockfile, Ref: lock, Version: art.Version})
		case release.KindGoModule:
			if modDirs == nil {
				modDirs = sbomGoModuleDirs(in.ProjectDir)
			}
			dir, ok := modDirs[name]
			if !ok {
				unavailable(name, release.SBOMSourceLockfile, "its module directory was not found in this checkout")
				continue
			}
			add(name, sbom.Target{Kind: sbom.KindLockfile, Ref: filepath.Join(dir, "go.mod"), Version: art.Version})
		}
	}
	return out, refused
}

// sbomPolicyOff reports whether the env declares provenance_sbom off.
func sbomPolicyOff(e *KCLEntities) bool {
	return e != nil && e.DeliveryPolicy != nil && e.DeliveryPolicy.ProvenanceSBOM.Mode == release.DeliveryOff
}

// sbomPlatform picks the one platform of a multi-platform image to describe:
// this machine's architecture on linux when the image has it, else the first
// the image advertises. "" (syft's choice) when the image advertises none.
func sbomPlatform(platforms []string) string {
	if len(platforms) == 0 {
		return ""
	}
	native := "linux/" + runtime.GOARCH
	for _, p := range platforms {
		if p == native {
			return p
		}
	}
	return platforms[0]
}

// jsLockfiles are the lockfiles syft reads for a JavaScript tree, in the
// order one is chosen when a directory holds several.
var jsLockfiles = []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"}

// findLockfile is the first of names in dir, then in each parent up to and
// including stop: a workspace keeps one lockfile at its root. "" when none.
func findLockfile(dir, stop string, names []string) string {
	stop = filepath.Clean(stop)
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		for _, n := range names {
			p := filepath.Join(d, n)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				return p
			}
		}
		if d == stop || filepath.Dir(d) == d || !strings.HasPrefix(d, stop) {
			return ""
		}
	}
}

type hostedSite struct {
	name, dir string
	// root is where the lockfile walk stops: the project for a frontend in
	// this checkout, the source checkout's root for a git-sourced one.
	root string
	// source is a git-sourced frontend's pin, for messages; "" in-repo.
	source string
	// unresolved says why a git-sourced frontend's checkout was not found.
	unresolved string
}

// hostedSiteDirs maps each hosted static site's release key — the address
// the build pushed it to, by the one rule the coverage gate and the push use
// (hostedStaticDestinationForDecl) — to its frontend and the directory its
// code is in.
//
// A frontend built from a forge.GitSource has no directory in this checkout:
// its code is the pinned source, which the build resolved (and rewrote Path
// to) before pushing the site. A cut that did not build it — `--no-build`, or
// a build --target narrowed away from the sites — resolves the pin here,
// through the same resolver and cache as the build and the release's own git
// pin, so the lockfile is the one at the commit the site was built from.
func hostedSiteDirs(ctx context.Context, in sbomCut, artifacts map[string]release.Artifact) map[string]hostedSite {
	out := map[string]hostedSite{}
	e := in.Entities
	if e == nil {
		return out
	}
	for _, fe := range e.Frontends {
		if !frontendIsHosted(fe) {
			continue
		}
		key := hostedStaticDestinationForDecl(e, imageRepository(fe.Image))
		if _, ok := artifacts[key]; !ok {
			continue
		}
		site := hostedSite{name: fe.Name, dir: fe.Path, root: in.ProjectDir}
		if fe.Source != nil && fe.Source.Repo != "" {
			site = sourcedSiteDir(ctx, in, artifacts, fe)
		} else if site.dir != "" && !filepath.IsAbs(site.dir) {
			site.dir = filepath.Join(in.ProjectDir, site.dir)
		}
		out[key] = site
	}
	return out
}

// sourcedSiteDir finds a git-sourced frontend's code: the directory the
// build resolved, else the pin resolved now. The commit must be the one the
// release pins for that frontend, when it pins one.
func sourcedSiteDir(ctx context.Context, in sbomCut, artifacts map[string]release.Artifact, fe FrontendEntity) hostedSite {
	src := toGitSource(fe.Source)
	site := hostedSite{name: fe.Name, dir: fe.Path, source: src.String()}
	commit := fe.SourceCommit
	if site.dir == "" {
		resolver := in.Sources
		if resolver == nil {
			r, err := frontendSourceResolver(in.ProjectDir)
			if err != nil {
				site.unresolved = fmt.Sprintf("frontend %s: its source %s could not be resolved to read its lockfile: %v", fe.Name, site.source, err)
				return site
			}
			resolver = r
		}
		res, err := resolver.Resolve(ctx, src)
		if err != nil {
			site.unresolved = fmt.Sprintf("frontend %s: its source %s could not be resolved to read its lockfile: %v", fe.Name, site.source, err)
			return site
		}
		site.dir, commit = res.Dir, res.Commit
	}
	if pin, ok := artifacts[fe.Name]; ok && pin.Kind == release.KindGit && pin.Source != nil &&
		pin.Source.Commit != "" && commit != "" && pin.Source.Commit != commit {
		site.unresolved = fmt.Sprintf("frontend %s: its source checkout %s is at %s, not the commit %s the release pins", fe.Name, site.dir, commit, pin.Source.Commit)
		return site
	}
	site.root = sourceCheckoutRoot(site.dir, fe.Source.Subdir)
	return site
}

// sourceCheckoutRoot is the root of the checkout a resolved source directory
// is in: dir without the source's subdir.
func sourceCheckoutRoot(dir, subdir string) string {
	dir = filepath.Clean(dir)
	sub := filepath.Clean(filepath.FromSlash(strings.Trim(subdir, "/")))
	if sub == "." {
		return dir
	}
	if root, ok := strings.CutSuffix(dir, string(filepath.Separator)+sub); ok {
		return root
	}
	return dir
}

// fileArtifactPath is where harvestFileArtifacts read a file artifact from.
func fileArtifactPath(projectDir, outputDir, name string) string {
	if outputDir == "" {
		outputDir = "bin"
	}
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(projectDir, outputDir)
	}
	return filepath.Join(outputDir, name)
}

// sbomNPMPackageDirs maps each npm package name to its directory, by the walk
// harvestNPMArtifacts records them from.
func sbomNPMPackageDirs(projectDir string) map[string]string {
	out := map[string]string{}
	for _, dir := range scanForManifests(projectDir, "package.json") {
		if man, ok := readNPMManifest(filepath.Join(dir, "package.json")); ok {
			out[man.Name] = dir
		}
	}
	return out
}

// sbomGoModuleDirs maps each nested Go module path to its directory, by the walk
// harvestGoModuleArtifacts records them from.
func sbomGoModuleDirs(projectDir string) map[string]string {
	out := map[string]string{}
	for _, dir := range scanForManifests(projectDir, "go.mod") {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // path comes from a walk of the project tree
		if err != nil {
			continue
		}
		if p := modfile.ModulePath(data); p != "" {
			out[p] = dir
		}
	}
	return out
}

func setArtifactSBOM(artifacts map[string]release.Artifact, name string, ref release.SBOMRef) {
	art := artifacts[name]
	art.SBOM = &ref
	artifacts[name] = art
}

func sortedArtifactNames(artifacts map[string]release.Artifact) []string {
	names := make([]string, 0, len(artifacts))
	for n := range artifacts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
