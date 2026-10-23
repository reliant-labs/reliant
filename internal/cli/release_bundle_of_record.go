package cli

// ONE BUNDLE PER (ENV, RELEASE): the bundle recorded first is the release's
// bundle, and every later write for the same release reuses it verbatim.
//
// WHY. A bundle is what a reviewer inspects and what a Flux applies, so "the
// bundle a plan was computed from" and "the bundle that deploys" must be one
// object. On 2026-10-07 they were not: the cut recorded sha256:a2564b5253b2,
// the next --plan-only recorded sha256:4645278bf09a for the same release, and
// that second one is what deployed. Pulled and compared, the two carried
// byte-identical manifests (one layer, sha256:7627821e8cb5) and differed only
// in metadata: the plan's carried the release's source pins, and it named a
// different forge as the renderer. So every plan re-recorded, and an approval
// was bound to whichever binary computed it.
//
// THE IDENTITY IS THE RENDERED OBJECTS, not the bundle digest. A bundle also
// records who rendered it (provenance.forge_version) and when, and those are
// facts about the first render, not differences in what ships. Two renders
// whose objects match are the same bundle, and the recorded one is reused —
// its digest, its record, its provenance. Two renders whose objects differ are
// two different deploys, and writing the second under the same release would
// make the release mean whichever was written last. That is refused, naming
// the objects that differ, unless the caller re-records on purpose.
//
// WHERE THE RECORD IS FOUND:
//   - a pushed HOSTED bundle: the registry tag release-<version> in the env's
//     bundle repository. The control plane has no (env, release) lookup, and
//     the registry is where the bytes are, so the tag is set right after a
//     successful record and read back here.
//   - otherwise: the machine ledger's oldest bundle row for (env, release),
//     which carries the shape it recorded.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// releaseBundle is a release's bundle of record for one env.
type releaseBundle struct {
	Digest    string
	Reference string
	Pushed    bool
	Objects   []release.ShapeObject
	// RenderedBy is the forge that rendered it, for the refusal message.
	RenderedBy string
	Commit     string
	// blobs are the recorded bundle's own bytes, for a hosted bundle: a
	// reuse re-sends them to RecordBundle, which is idempotent on (env,
	// digest) and keeps "the control plane holds this record" true even if
	// the first record was lost. Empty for a machine-ledger row.
	blobs bundleBlobs
	// fromLedgerRow is true when Objects came from a machine-ledger row,
	// which records the env's PROJECTED shape; a fetched bundle carries the
	// shape bundle.Build sealed. Each is compared with its own kind.
	fromLedgerRow bool
}

// bundleRegistry is what a bundle repository must offer for the release tag:
// read a bundle back, and point a tag at one. *remote.Repository does; a test
// target that only accepts pushes does not, and then there is no tag to keep.
type bundleRegistry interface {
	bundle.Resolver
	Tag(ctx context.Context, desc ocispec.Descriptor, reference string) error
}

// releaseBundleTag is the registry tag a release's bundle is kept under. OCI
// tags allow [A-Za-z0-9_.-]; anything else in a version is mapped to '_'.
func releaseBundleTag(version string) string {
	var b strings.Builder
	b.WriteString("release-")
	for _, r := range version {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	tag := b.String()
	if len(tag) > 128 {
		tag = tag[:128]
	}
	return tag
}

// findReleaseBundle looks up env's bundle of record for version. found is
// false when there is none yet. An error means the lookup itself failed; the
// caller then cannot vouch for immutability and says so.
func findReleaseBundle(ctx context.Context, projectDir, env, version string, ledger envLedger, in bundleBuildInputs) (releaseBundle, bool, error) {
	if ledger.Hosted && in.Pushed {
		base := bundlePushBaseFor(ctx, projectDir, env)
		if base == "" {
			return releaseBundle{}, false, nil
		}
		repo := bundle.Repository(base, env)
		target, err := bundlePushTarget(ctx, projectDir, env, repo)
		if err != nil {
			return releaseBundle{}, false, err
		}
		reg, ok := target.(bundleRegistry)
		if !ok {
			return releaseBundle{}, false, nil
		}
		fetched, err := bundle.Fetch(ctx, reg, releaseBundleTag(version))
		if errors.Is(err, errdef.ErrNotFound) {
			return releaseBundle{}, false, nil
		}
		if err != nil {
			return releaseBundle{}, false, err
		}
		manifest, config, err := fetchBundleBlobs(ctx, reg, releaseBundleTag(version), fetched.Digest)
		if err != nil {
			return releaseBundle{}, false, err
		}
		return releaseBundle{
			Digest: fetched.Digest, Reference: repo + "@" + fetched.Digest, Pushed: true,
			Objects:    fetched.Doc.Shape.Objects,
			RenderedBy: fetched.Doc.Provenance.ForgeVersion, Commit: fetched.Doc.Provenance.Commit,
			blobs: bundleBlobs{Repository: repo, Manifest: manifest, Config: config},
		}, true, nil
	}
	if ledger.Hosted {
		// A hosted bundle that was not pushed has no record to find: the
		// machine ledger is not evidence about a hosted env.
		return releaseBundle{}, false, nil
	}
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return releaseBundle{}, false, err
	}
	rows, err := store.Bundles(env)
	if err != nil {
		return releaseBundle{}, false, err
	}
	for _, row := range rows { // oldest first: the first recorded is the release's
		if row.Release != version {
			continue
		}
		return releaseBundle{
			Digest: row.Digest, Reference: row.Reference, Pushed: !strings.HasPrefix(row.Reference, "oci-layout:"),
			Objects:    row.Shape.Objects,
			RenderedBy: row.Provenance.ForgeVersion, Commit: row.Provenance.Commit,
			fromLedgerRow: true,
		}, true, nil
	}
	return releaseBundle{}, false, nil
}

// fetchBundleBlobs reads a pushed bundle's raw manifest and config blob.
func fetchBundleBlobs(ctx context.Context, reg bundleRegistry, tag, digest string) (manifest, config []byte, err error) {
	desc, err := reg.Resolve(ctx, tag)
	if err != nil {
		return nil, nil, err
	}
	if string(desc.Digest) != digest {
		return nil, nil, fmt.Errorf("tag %s moved while it was read (%s, then %s)", tag, shortDigest(digest), shortDigest(string(desc.Digest)))
	}
	if manifest, err = content.FetchAll(ctx, reg, desc); err != nil {
		return nil, nil, err
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, nil, fmt.Errorf("decode bundle manifest %s: %w", shortDigest(digest), err)
	}
	if config, err = content.FetchAll(ctx, reg, m.Config); err != nil {
		return nil, nil, err
	}
	return manifest, config, nil
}

// tagReleaseBundle points the release tag at a pushed, recorded bundle, so the
// next write for the same release finds it. Best-effort: a registry that will
// not take the tag costs immutability checking for this release, not the
// build, and that is said.
func tagReleaseBundle(ctx context.Context, projectDir, env, repo string, manifest []byte, version string, in bundleBuildInputs) {
	target, err := bundlePushTarget(ctx, projectDir, env, repo)
	if err != nil {
		fmt.Fprintf(in.errWriter(), "[bundle] Note: release %s's bundle was not tagged (%v); a later deploy cannot check it is unchanged.\n", version, err)
		return
	}
	reg, ok := target.(bundleRegistry)
	if !ok {
		return
	}
	// The descriptor is the manifest just pushed, described from its own
	// bytes: a registry resolves tags, not necessarily bare digests.
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, manifest)
	if err := reg.Tag(ctx, desc, releaseBundleTag(version)); err != nil {
		fmt.Fprintf(in.errWriter(), "[bundle] Note: release %s's bundle was not tagged (%v); a later deploy cannot check it is unchanged.\n", version, err)
	}
}

// objectChange is one rendered object that differs between two renders.
type objectChange struct {
	Change string // "added", "removed", "changed"
	Key    string
}

// diffBundleObjects compares two renders object by object: identity is
// (cluster, kind, namespace, name), content is the redacted document's hash.
func diffBundleObjects(recorded, rendered []release.ShapeObject) []objectChange {
	key := func(o release.ShapeObject) string {
		k := o.Kind + " "
		if o.Namespace != "" {
			k += o.Namespace + "/"
		}
		k += o.Name
		if o.Cluster != "" {
			k += " (" + o.Cluster + ")"
		}
		return k
	}
	was := map[string]string{}
	for _, o := range recorded {
		was[key(o)] = o.Hash
	}
	now := map[string]string{}
	for _, o := range rendered {
		now[key(o)] = o.Hash
	}
	var out []objectChange
	for k, h := range now {
		switch prev, ok := was[k]; {
		case !ok:
			out = append(out, objectChange{"added", k})
		case prev != h:
			out = append(out, objectChange{"changed", k})
		}
	}
	for k := range was {
		if _, ok := now[k]; !ok {
			out = append(out, objectChange{"removed", k})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// releaseBundleDiffersError refuses a second, different bundle for a release.
type releaseBundleDiffersError struct{ msg string }

func (e *releaseBundleDiffersError) Error() string { return e.msg }

// errReleaseBundleDiffers builds the refusal, naming the objects that differ.
func errReleaseBundleDiffers(env, version string, prior releaseBundle, changes []objectChange) error {
	var b strings.Builder
	fmt.Fprintf(&b, "release %s's bundle for env %s is already recorded (%s), and this checkout renders DIFFERENT objects:\n",
		version, env, shortDigest(prior.Digest))
	const show = 20
	for i, c := range changes {
		if i == show {
			fmt.Fprintf(&b, "    … and %d more\n", len(changes)-show)
			break
		}
		fmt.Fprintf(&b, "    %-8s %s\n", c.Change, c.Key)
	}
	fmt.Fprintf(&b, "  A release's bundle is immutable: what a reviewer approved and what deploys must be the same bytes.\n")
	fmt.Fprintf(&b, "  It was rendered from commit %s by forge %s. Either deploy from that commit with that forge,\n",
		emptyAs(shortSHA(prior.Commit), "(unknown)"), emptyAs(prior.RenderedBy, "(unknown)"))
	fmt.Fprintf(&b, "  cut a new release for this change, or replace the release's bundle on purpose:\n    forge env deploy %s %s --rerecord-bundle", env, version)
	return &releaseBundleDiffersError{msg: b.String()}
}
