package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/internal/statefile"
	"github.com/reliant-labs/forge/pkg/release"
)

// The release ledger + env→release binding are the "build once → promote"
// spine described in the artifact-pipeline design. They sit ABOVE the
// per-image digest foundation that already shipped (build_state.go captures
// a digest per pushed image; deploy.go resolveDeployImageDigests pins each
// service's manifest to its own `@sha256:...`). This file adds the layer
// that lets a SINGLE build feed MANY envs:
//
//	forge env build --release v1.4.0   → builds the env-agnostic images ONCE,
//	                                  records each image's digest in a
//	                                  Release ledger (.forge/releases/<v>.json).
//	forge env deploy prod v1.4.0 → appends a promotion of prod → v1.4.0 to
//	                                  the env's append-only ledger
//	                                  (.forge/promotions/prod.jsonl, or the
//	                                  hosted control plane). No rebuild.
//	forge env deploy prod              → if prod is bound, pins the SAME digests
//	                                  the promotion froze (build once, promote);
//	                                  else falls back to the per-env build state.
//
// Scope: forge's build produces "shared" images (one digest, every env) and
// "source" frontends (a pinned commit). release.ModeVariant is representable
// and validated, but nothing here produces or deploys one yet.

// releasesDirRel was ".forge/releases", one file per version INSIDE the
// checkout. It is gone: see retiredReleasesDirRel in ledger_unimported.go,
// which is the only remaining reference to that location and treats it as
// history to be imported rather than as a ledger.

// The release TYPES live in forge/pkg/release, not here. What remains in this
// file is the build-time HARVEST that produces a release (reading the
// per-image digests the build captured) — not its storage, which belongs to
// whichever ledger the env selected. The two stores
// (internal/ledgerfile and hosted_ledger.go) read and write the same types,
// so "what forge cut" and "what the control plane stores" are one vocabulary
// rather than two structs kept in step by
// comments.

// releaseFileStem maps a release version label to its on-disk ledger
// filename stem (without the .json extension). It is the SINGLE mapping
// build (write), promote (read), and deploy (read) all funnel through via
// releasePath, so they always agree on which file a version names.
//
// Unlike statefile.SafeSegment — which flattens EVERY non-[A-Za-z0-9_-]
// byte to '_', turning the common semver "v1.0.0" into the surprising
// "v1_0_0.json" a user never looks for — this preserves the dot, because a
// dot IS filesystem-safe and keeping it makes the literal version the
// filename ("v1.0.0" → "v1.0.0.json"): least surprise. Path separators
// ('/', '\') and any other unsafe byte still flatten to '_' so the write
// can never escape .forge/releases. The lone exception is the traversal
// token "." / ".." (or a name that is only dots): a dot-only stem is a
// directory reference, not a release, so it flattens to underscores. In
// practice version labels never hit that case — it's a pure safety guard.
func releaseFileStem(version string) string {
	out := make([]byte, 0, len(version))
	allDots := true
	for i := 0; i < len(version); i++ {
		c := version[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-' || c == '_':
			out = append(out, c)
			allDots = false
		case c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
			allDots = false
		}
	}
	if len(out) == 0 {
		return "_"
	}
	// A stem composed only of dots (".", "..", "...") is a path reference,
	// not a usable filename stem — neutralize it.
	if allDots {
		return strings.Repeat("_", len(out))
	}
	return string(out)
}

// releasePath, WriteRelease and ReadRelease ARE DELETED. They were the
// in-checkout file backend: one .forge/releases/<stem>.json per version,
// written and read directly by build, promote and deploy.
//
// Releases now live in the ledger the env selected — the control plane, or
// the machine store under $FORGE_LEDGER_HOME (internal/ledgerfile) — and are
// reached through the releaseLedger seam (Cut, Get, List), so no caller
// resolves a version to a path any more. The immutability rule those two
// functions enforced did not move: release.CheckRecut is still the one
// implementation, called by both stores.
//
// releaseFileStem survives above because reading the RETIRED location is
// still necessary — not as a ledger, but to detect a checkout whose
// committed promotions have never been imported (ledger_unimported.go).

// harvestReleaseArtifacts collects the per-image digests captured by the build
// that just ran, from the SAME build-state sources resolveDeployImageDigests
// reads at deploy time:
//
//   - the aggregate .forge/state/build-<env>.json (and the env-agnostic
//     "default" record a plain `forge build` writes) — the project image's
//     digest.
//   - every per-service .forge/state/build-<env>-<service>.json the external-
//     build dispatcher writes — each external image's own digest.
//
// Reusing the existing digest capture is the whole point: the release ledger is
// a DURABLE projection of the ephemeral build state, not a parallel capture
// mechanism. Every harvested image becomes a "shared" artifact (one digest,
// promoted to all envs) — the MVP scope.
//
// Returns an image-name → ReleaseArtifact map. An image with no captured digest
// is omitted (a release records only what was content-addressed); the caller
// errors if the map is empty so a release is never cut with zero digests.
func harvestReleaseArtifacts(projectDir, envName string) map[string]release.Artifact {
	out := map[string]release.Artifact{}

	// An OCI artifact is KEYED BY ITS REPOSITORY — registry host included,
	// exactly as the workload declared it (`ghcr.io/acme/shop`). The key IS
	// the address, which is why there is no separate URI to record and no way
	// for the two to disagree.
	//
	// Keying by bare name could not survive a workload declaring its own
	// registry: two envs building the same app to two different registries
	// collapse onto one entry, and a prod promotion would read a digest that
	// only ever existed in the dev registry. The host in the key keeps them
	// distinct.
	//
	// builtFrom is the checkout a ShellBuild ran in, when its state recorded
	// one: the release keeps it beside the digest (minus the local path), so
	// "which commit of the sibling is in this image" is answerable from the
	// ledger alone.
	add := func(repository, digest string, platforms []string, builtFrom *release.BuildSource) {
		if repository == "" || digest == "" {
			return
		}
		// Several services share one image (three run `reliant`); a state
		// that recorded no checkout must not erase one that did.
		if prev, ok := out[repository]; ok && builtFrom == nil && prev.Digests[release.SharedVariant] == digest {
			builtFrom = prev.BuiltFrom
		}
		out[repository] = release.Artifact{
			Kind:      release.KindOCI,
			Mode:      release.ModeShared,
			Digests:   map[string]string{release.SharedVariant: digest},
			Platforms: platforms,
			BuiltFrom: builtFrom,
		}
	}

	// Aggregate project-image build state(s): env-specific, then the
	// env-agnostic default a plain `forge env build --release` (no --env) writes.
	for _, key := range buildStateLookupEnvs(envName) {
		st, err := ReadBuildState(projectDir, key)
		if err != nil || st == nil {
			continue
		}
		add(st.Image, st.Digest, st.Platforms, nil)
	}

	// Per-service external-build states: build-<env>-<service>.json. Glob the
	// state dir for each lookup env's per-service files and read each typed
	// record. A `forge env build --release` runs env-agnostic (env ""), so the
	// per-service external builds land under the "default" key — iterating the
	// same buildStateLookupEnvs fallback the aggregate uses keeps the two
	// sources symmetric (and a release built with an explicit --env still picks
	// up its env-specific per-service files).
	stateDir := filepath.Join(projectDir, statefile.DirRel)
	for _, key := range buildStateLookupEnvs(envName) {
		prefix := "build-" + statefile.SafeSegment(key) + "-"
		matches, _ := filepath.Glob(filepath.Join(stateDir, prefix+"*.json"))
		for _, path := range matches {
			base := filepath.Base(path)
			if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".json") {
				continue
			}
			service := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".json")
			st, err := buildtarget.ReadState(projectDir, key, service)
			if err != nil || st == nil {
				continue
			}
			add(st.Image, st.Digest, st.Platforms, st.Source.ForRelease())
		}
	}

	return out
}

// harvestedBuildCommits is every distinct commit the env's AGGREGATE build
// state records, sorted. Empty when no state names one (an older forge wrote
// none). Per-service external-build states carry no commit; they are built in
// the same invocation as the aggregate, so its commit is the build's.
//
// Same lookup order as harvestReleaseArtifacts, so "the commit these artifacts
// were built from" comes from the same source as "these artifacts".
func harvestedBuildCommits(projectDir, envName string) []string {
	seen := map[string]bool{}
	for _, key := range buildStateLookupEnvs(envName) {
		if st, err := ReadBuildState(projectDir, key); err == nil && st != nil && st.Commit != "" {
			seen[st.Commit] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// checkCutMatchesBuild refuses to seal a release over a build the checkout no
// longer matches.
//
// A --no-build cut harvests digests an earlier build produced, but stamps the
// release's provenance from the CHECKOUT it runs in. When those are different
// commits the release is immutable and WRONG: it claims code the images were
// never built from, and the deploy's freshness check (rightly) refuses it later
// — after the hosted ledger already holds the bad record, which cannot be
// amended. Refusing here costs nothing; the remedy is to run the cut from the
// commit that was built, or to rebuild.
func checkCutMatchesBuild(version, headCommit string, buildCommits []string) error {
	if headCommit == "" || len(buildCommits) == 0 {
		return nil
	}
	for _, c := range buildCommits {
		if c != headCommit {
			return fmt.Errorf("--release %s: the build state was built from %s but this checkout is at %s.\n"+
				"  A release is sealed with the checkout's commit, so cutting it here would record code the images\n"+
				"  were never built from — and a release cannot be amended afterwards.\n"+
				"  fix: run the cut from a checkout of %s (the commit that was built), or rebuild from this one:\n"+
				"       forge env build <env> --release %s --push",
				version, shortSHA(c), shortSHA(headCommit), shortSHA(c), version)
		}
	}
	return nil
}

func envNameOr(env string) string {
	if env == "" {
		return "<env>"
	}
	return env
}

// addFrontendSourceArtifacts records every source-pinned frontend the env
// declares as a source-mode artifact, keyed by frontend name.
//
// WHY THIS IS NOT OPTIONAL. A release is meant to be the whole environment at
// a version. Container images are only the containerized half of one: a
// Firebase Hosting SPA declared via forge.GitSource is built from a pinned
// commit and uploaded to a CDN, so it produces no image, writes no build
// state, and — before this — appeared nowhere in the ledger. The consequence
// is not cosmetic. Such a frontend has no promotion record, no staleness
// check, and no entry a deploy could verify; its only pin is a ref string in
// KCL that nothing reconciles, so it drifts out of step with the API it talks
// to while every image around it advances correctly. Recording it here makes
// the release cover the environment rather than a subset of it.
//
// The pin is resolved through the SAME gitsource resolver the build and
// deploy paths use, so the commit recorded is the commit those paths would
// build. A local source-override (a developer pointing at a working tree) is
// deliberately NOT recorded: an override is "build what is in front of me",
// which by definition is not a reproducible pin and must never be frozen into
// a release as though it were one.
//
// Errors are collected rather than swallowed. A frontend that cannot be
// resolved is a release that cannot honestly claim to contain it.
func addFrontendSourceArtifacts(ctx context.Context, projectDir string, entities *KCLEntities, out map[string]release.Artifact) error {
	if entities == nil {
		return nil
	}
	if !hasPinnedFrontend(entities) {
		return nil
	}
	resolver, err := frontendSourceResolver(projectDir)
	if err != nil {
		return err
	}
	return addFrontendSourceArtifactsWith(ctx, resolver, entities, out)
}

// hasPinnedFrontend reports whether any frontend declares a cross-repo pin.
// Checked before constructing a resolver so a project with no such frontend
// never touches the source cache.
func hasPinnedFrontend(entities *KCLEntities) bool {
	for _, fe := range entities.Frontends {
		if fe.Source != nil && fe.Source.Repo != "" {
			return true
		}
	}
	return false
}

// pinResolver is the one thing the release path needs from the source
// resolver: turn a declared pin into a resolution. Declared HERE, at the
// consumer, rather than exported from internal/gitsource — the release path
// does not care about caching, overrides files, or fetching, and a narrow
// local interface is what lets this be tested without a network.
type pinResolver interface {
	Resolve(ctx context.Context, src gitsource.Source) (gitsource.Resolution, error)
}

// addFrontendSourceArtifactsWith is the resolver-injected half of
// addFrontendSourceArtifacts. Split so the capture logic — including the
// override rejection, which is a correctness rule and not an implementation
// detail — is testable against a stub.
func addFrontendSourceArtifactsWith(ctx context.Context, resolver pinResolver, entities *KCLEntities, out map[string]release.Artifact) error {
	var pinned []FrontendEntity
	for _, fe := range entities.Frontends {
		if fe.Source != nil && fe.Source.Repo != "" {
			pinned = append(pinned, fe)
		}
	}
	for _, fe := range pinned {
		src := toGitSource(fe.Source)
		res, rerr := resolver.Resolve(ctx, src)
		if rerr != nil {
			return fmt.Errorf("frontend %q: resolve pinned source %s: %w", fe.Name, src, rerr)
		}
		if res.Overridden {
			return fmt.Errorf("frontend %q is served by a LOCAL SOURCE OVERRIDE (%s), not its declared pin %s.\n"+
				"  A release must be reproducible, and an override is by definition not — freezing one into a\n"+
				"  release ledger would record a version that no other machine can rebuild.\n"+
				"  Remove the entry from %s and re-cut",
				fe.Name, res.Dir, src, filepath.Join(gitsource.OverridesDirName, gitsource.OverridesFileName))
		}
		out[fe.Name] = release.Artifact{
			Kind: release.KindGit,
			Mode: release.ModeSource,
			Source: &release.Source{
				Repo:   src.Repo,
				Ref:    src.Ref,
				Subdir: src.Subdir,
				Commit: res.Commit,
			},
		}
	}
	return nil
}

// checkReleaseCoversEnv fails the cut when the env DECLARES an artifact the
// ledger does not contain.
//
// WHY A CUT SHOULD FAIL RATHER THAN WARN. A release names a version, and
// everything downstream treats that name as covering the environment: promote
// binds it wholesale, deploy pins from it, and a human reviewing "v1.6.0"
// reasonably assumes v1.6.0 is all of v1.6.0. A ledger missing one declared
// artifact violates that silently — the deploy falls back to a mutable tag (or
// to whatever the previous binding held) for the missing piece and ships a mix
// of two releases under one version. Nothing goes red; the environment is just
// quietly wrong, and it stays wrong until someone notices behaviour that does
// not match the version they think is deployed.
//
// The check is DERIVED from the rendered env, never from a list. That is the
// whole point: the artifact set is discovered from deploy/kcl/<env>/main.k, so
// declaring a new service or frontend puts it in the release automatically and
// a hand-maintained enumeration cannot fall behind the declaration.
func checkReleaseCoversEnv(entities *KCLEntities, artifacts map[string]release.Artifact, opts buildOptions) error {
	missing := releaseCoverageGaps(entities, artifacts, opts.env)
	if len(missing) == 0 {
		return nil
	}

	envName := opts.env
	if envName == "" {
		envName = "<env>"
	}
	return fmt.Errorf("--release %s: the release does not cover everything %s declares.\n"+
		"  Missing from the ledger:\n    %s\n"+
		"  These are declared in deploy/kcl/%s/main.k but no digest or commit was captured for them,\n"+
		"  so promoting this release would deploy them from a mutable tag (or leave them on whatever the\n"+
		"  previous binding pinned) while every other artifact advanced — one version, two releases.\n"+
		"  Build the full set (drop --target, and pass --push so images are digest-addressable),\n"+
		"  or remove what the environment no longer ships",
		opts.release, envName, strings.Join(missing, "\n    "), envName)
}

// releaseCoverageGaps is every artifact the rendered env declares that
// artifacts does not hold, one line each, sorted. It is the completeness rule
// itself, shared by the cut (checkReleaseCoversEnv, which refuses) and by a
// no-version deploy deciding whether an existing release can be reused
// (chooseDeployRelease, which cuts a new one instead).
func releaseCoverageGaps(entities *KCLEntities, artifacts map[string]release.Artifact, env string) []string {
	if entities == nil {
		// No render (no --env) means nothing to compare against. --release
		// requires an env argument, so this is unreachable in practice; a
		// nil check here is cheaper than an assumption.
		return nil
	}

	// Deduplicate by IMAGE, not by service. Several services routinely share
	// one image (in this project five run `control-plane` and three run
	// `reliant`), and listing the same missing image once per service turns
	// one fact into nine lines and reads like nine separate problems.
	missingImages := map[string][]string{}
	var imageOrder []string
	for _, s := range entities.Workloads {
		// An artifact a release must carry: an image forge builds for a
		// workload that runs FROM an image (cluster, hosted, build-only).
		// A host or compose workload is never released as an image.
		if s.Image == "" || s.Build.Type == "" || s.OnRuntime(RuntimeHost) || s.OnRuntime(RuntimeCompose) {
			continue
		}
		// Compare by REPOSITORY, which is how the ledger is keyed: the declared
		// reference minus its tag. A workload pinning `repo:e2e` and the ledger
		// entry for `repo` are the same artifact.
		//
		// A HOSTED workload's image is resolved against the platform's push
		// base first (hostedImageForDecl), because that — not the bare name the
		// author wrote — is the address the build pushed to and the key every
		// producer records. Comparing the raw declaration refused a cut whose
		// ledger was correct.
		repo := imageRepository(s.Image)
		if s.OnRuntime(RuntimeHosted) {
			repo = hostedImageForDecl(entities, repo)
		}
		if _, ok := artifacts[repo]; ok {
			continue
		}
		if _, seen := missingImages[repo]; !seen {
			imageOrder = append(imageOrder, repo)
		}
		missingImages[repo] = append(missingImages[repo], s.Name)
	}

	var missing []string
	for _, image := range imageOrder {
		svcs := missingImages[image]
		sort.Strings(svcs)
		missing = append(missing, fmt.Sprintf("%s (image, used by %s)", image, strings.Join(svcs, ", ")))
	}
	for _, fe := range entities.Frontends {
		// A hosted StaticSite ships as an OCI release artifact keyed by the
		// frontend name (buildHostedStaticSites); the hosted deploy pins it
		// as liveDigest, so a release without it cannot deploy the site.
		if frontendIsHosted(fe) {
			// The SAME address the build pushed to, through the one rule
			// (hostedStaticDestination). A bare hosted image resolves
			// against the platform's base, so deriving the key from the
			// declared reference here would look up an entry the push never
			// wrote and refuse a complete build.
			if _, ok := artifacts[hostedStaticDestinationForDecl(entities, imageRepository(fe.Image))]; !ok {
				missing = append(missing, fmt.Sprintf("%s (hosted static site: forge env build %s --push)", fe.Name, envNameOr(env)))
			}
			continue
		}
		// Source-pinned frontends are keyed by frontend name.
		if fe.Source == nil || fe.Source.Repo == "" {
			continue
		}
		if _, ok := artifacts[fe.Name]; !ok {
			missing = append(missing, fmt.Sprintf("%s (frontend)", fe.Name))
		}
	}
	sort.Strings(missing)
	return missing
}

// countOCIArtifacts returns how many of a release's artifacts are container
// images. Used to tell "this release ships no images" apart from "this
// release's images are all variant-mode", which are different problems with
// different answers.
func countOCIArtifacts(r release.Release) int {
	n := 0
	for name, art := range r.Artifacts {
		if _, maps := release.SourcemapsFrontend(name); maps {
			continue // a frontend's source maps are an OCI artifact, not an image
		}
		if art.Kind == release.KindOCI {
			n++
		}
	}
	return n
}

// releaseArtifactKinds returns the sorted distinct kinds present in a release,
// for error messages that name what a release actually holds rather than what
// it lacks.
func releaseArtifactKinds(r release.Release) []string {
	seen := map[string]bool{}
	for _, art := range r.Artifacts {
		seen[string(art.Kind)] = true
	}
	kinds := make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// releaseImageNames returns the sorted image names in a release, for legible
// summary/log output.
func releaseImageNames(r release.Release) []string {
	return r.ArtifactNames()
}

// resolveReleaseDigests flattens a release's artifacts into the bare
// image-name → digest map the deploy/promote paths consume. Only shared
// artifacts resolve here (the MVP); a variant artifact is skipped with no
// error (its per-target resolution is a deferred follow-up). Returns an error
// only if the release carries no resolvable digests at all — a release that
// can't pin anything is a bug, not a silent fall-through.
func resolveReleaseDigests(r release.Release) (map[string]string, error) {
	out := r.SharedDigests()
	if len(out) == 0 {
		// A release that pins nothing is a dead end for promote/deploy: the
		// whole point is to advance content-addressed digests by reference.
		// The single most common way to reach here is a release cut with an
		// EMPTY artifact map — `forge env build --release X` ran but captured no
		// digests (forgot --push, no services built, external builds skipped
		// for a missing build_cwd). The previous message ("carries no shared
		// image digests to pin") read like an internal invariant and left the
		// user with no next step, so distinguish the two cases and, for the
		// empty-artifacts case, spell out exactly why a release ends up empty
		// and what to do about it.
		if len(r.Artifacts) == 0 {
			return nil, fmt.Errorf(
				"release %q was cut with no image digests. This can happen if:\n"+
					"  (1) no docker images were built (check --env and that KCL declares services),\n"+
					"  (2) digests were not captured (re-run the build with --push: forge env build <env> --release <version> --push), or\n"+
					"  (3) all external builds were skipped due to a missing build_cwd.\n"+
					"Inspect the release file with `forge project audit` to see what was recorded",
				r.Version)
		}
		// Artifacts exist but none resolved a shared OCI digest. Two very
		// different causes, and saying "variant-mode" for both would send a
		// reader looking for a feature flag when the real answer is that this
		// release ships no images at all.
		if n := countOCIArtifacts(r); n == 0 {
			return nil, fmt.Errorf(
				"release %q contains no container images to deploy — it has %d artifact(s), all non-OCI (%s). "+
					"A release of only packages or files can be verified with `forge release verify`, but there is nothing for an environment to run",
				r.Version, len(r.Artifacts), strings.Join(releaseArtifactKinds(r), ", "))
		}
		return nil, fmt.Errorf("release %q carries only variant-mode artifacts; shared image digests are required to promote/deploy (variant promotion is not yet supported)", r.Version)
	}
	return out, nil
}
