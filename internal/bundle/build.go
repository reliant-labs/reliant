package bundle

// Building a bundle: the immutable record of what was shipped, and the SOURCE
// THE RECONCILER APPLIES.
//
// One bundle is one OCI image manifest (doc §4.1):
//
//	manifest   artifactType application/vnd.forge.bundle.v1
//	 ├─ config  application/vnd.forge.bundle.config.v1+json   the BundleDoc
//	 └─ layer   ….bundle.manifests.v1.tar+gzip                manifests/<cluster>/<NNN>-<kind>-<name>.yaml
//
// IT IS FLUX'S SOURCE, WHICH IS WHY THE LAYOUT IS A CONTRACT. A Flux
// OCIRepository selects the one layer by media type with
// `operation: extract`, and a Kustomization per target cluster applies the
// YAML under [release.BundleClusterPath]'s answer for that cluster. forge
// writes those paths; the control plane reads them off the BundleDoc. Neither
// side re-derives them — see release.BundleClusterPath on why a second
// spelling fails silently and green.
//
// EXACTLY ONE LAYER, AND NO CHART LAYER. A layerSelector picks one layer, so a
// second one is invisible to the reconciler: anything in it would be recorded
// as shipped and never applied. The bundle therefore carries the env's own
// objects and nothing else.
//
// PLATFORM CHARTS ARE DELIBERATELY NOT IN HERE, and that is a safety property
// rather than a simplification. Every forge.HelmChart is by declaration a
// platform DEPENDENCY — Flux itself, the CNPG operator, Envoy Gateway with
// its Gateway API CRDs, cert-manager — and the hub Kustomization prunes what
// its path no longer carries. A bundle that owned those objects could delete
// its own reconciler, orphan every Postgres cluster whose CRD it removed, or
// revoke every certificate, and it would do so from a render that merely
// failed to select a chart. Platform infra stays on forge's explicit
// cluster-bootstrap path (`forge env deploy <env> --target <chart>`), where it
// is applied by a human naming it, against a cluster, once.
//
// EVERY BYTE IS A FUNCTION OF THE INPUT. The tar entries are sorted, their
// mtimes, uid/gid and modes are fixed, the gzip header carries no time, and
// the manifest's `created` annotation is pinned — so Build on the same render
// twice produces the same digest, and a re-push of an unchanged bundle is
// idempotent instead of cutting a second record of the same deploy.
// [BundleDoc.CreatedAt] is the one input that is a clock, and it is the
// CALLER's: Build never reads one, because a function that did could not be
// determinism-tested and its output would differ run to run by construction.
//
// The manifest layer is the stream a deploy APPLIES. Doc §4.4: the apply path
// reads the bundle's manifests rather than re-rendering, so "what was
// applied" and "what was recorded" are the same bytes. That is why the
// filenames carry the render's index — the apply order is a property of the
// stream (a Namespace before the objects in it) and a directory listing would
// lose it.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"gopkg.in/yaml.v3"
	"oras.land/oras-go/v2/content"

	"github.com/reliant-labs/forge/pkg/release"
)

// Annotation keys on a bundle's OCI manifest (doc §4.1). The `org.*` ones are
// OCI standard, so a registry UI shows them; the `dev.forge.*` ones are what
// the retention policy and a human browsing a repository read.
const (
	AnnotationEnv          = "dev.forge.env"
	AnnotationProject      = "dev.forge.project"
	AnnotationRelease      = "dev.forge.release"
	AnnotationConfigDigest = "dev.forge.config-digest"
	AnnotationDirty        = "dev.forge.dirty"
	AnnotationWorktreeKey  = "dev.forge.worktree-key"
)

// manifestsPrefix is the directory the manifest layer's entries live under.
// pkg/release owns it, because the reconciler's path is built from it.
const manifestsPrefix = release.BundleManifestsPrefix

// fixedModTime pins every tar entry's timestamp. Unix zero rather than the
// build time: the bundle's digest must depend on the render and nothing else.
var fixedModTime = time.Unix(0, 0).UTC()

// fixedCreated pins the OCI manifest's `created` annotation, for the same
// reason. The bundle's real creation time is in the BundleDoc, where it is
// the caller's input rather than a clock read mid-build.
const fixedCreated = "1970-01-01T00:00:00Z"

const (
	tarFileMode = 0o644
	tarDirMode  = 0o755
)

// BuildInput is one env's render plus the facts that identify it.
type BuildInput struct {
	// Snapshot is a complete composition. Its recorded object bytes, never a
	// new checkout render, are packaged verbatim and checked against the layer.
	Snapshot *release.Snapshot
	// Project and Env name the bundle. Both required: a bundle is one
	// env's render, and a reader that could not tell which env it
	// described would have an artifact it could not place.
	Project string
	Env     string
	// Release is the version pinned, or "" for an unreleased bundle (dev,
	// or a Preview deploy) pinned to the build's own digests.
	Release string
	// Pins is the pin set the render resolved: images by artifact key,
	// and source pins for source-built components.
	Pins release.BundlePins
	// Provenance is where the render came from. Captured by the caller
	// (release.CaptureProvenance), never by this package: provenance is a
	// property of a checkout and this package never sees one.
	Provenance release.Provenance
	// Reader is what the backend this bundle is recorded in decodes. The
	// document leaves out every optional part it does not name
	// (release.BundleReader.Limit): a control plane strict-decodes the
	// document, so a field it predates fails the whole record. Nil writes
	// every part — the machine ledger is read by this same forge.
	Reader *release.BundleReader
	// Shape is the declaration half of the projection, exactly as
	// [ProjectShape] takes it. Build runs the projection ITSELF, from the
	// same parse it packages, rather than accepting a finished shape —
	// which is what makes "the shape describes these manifests" true by
	// construction instead of by a caller's discipline.
	Shape ShapeInput
	// CreatedAt is the bundle's creation time. Required, and the caller's:
	// see the determinism note at the top of this file.
	CreatedAt time.Time
	// Reconciled says a reconciler (Flux) may apply this bundle, so it must be
	// safe to apply repeatedly: a Job with a TTL or without a spec hash is then
	// REFUSED rather than built. False for an env forge applies itself (a
	// local or ephemeral lifecycle, or a declared applier of forge): a
	// one-shot apply neither re-runs a TTL'd Job on a timer nor re-reconciles
	// an unhashed one, and refusing there would break builds that work today.
	// The fixes that change nothing at runtime are made either way.
	Reconciled bool
	// ReconciledUnits narrows the refusal to the delivery units a reconciler
	// applies, for an env that moves onto Flux one unit at a time: an object in
	// a unit forge still applies directly is not refused, so the first unit to
	// move is not held up by a Job in one that has not. Consulted only when
	// Reconciled is false; Reconciled true is every unit. The fixes that change
	// nothing at runtime are made for every unit regardless.
	ReconciledUnits []string
	// Units declares the delivery units' order (see release.DeliveryUnit).
	// An object's unit is its forge.dev/delivery-unit label; a unit nobody
	// declares waits for the default unit. Every real cluster is split into
	// trees by it; see release.PlanTrees.
	Units []release.DeliveryUnit
	// Observability is the dashboards and the metrics schema the bundle
	// carries beside its manifests (see [release.BundleObservabilityPrefix]).
	// Nothing applies them, so they neither enter the shape nor the config
	// digest; they do enter the layer, and so the bundle's digest.
	Observability []ObservabilityFile
}

// ObservabilityFile is one file under the bundle's observability/ directory.
type ObservabilityFile struct {
	// Path is slash-separated and under [release.BundleObservabilityPrefix].
	Path string
	Data []byte
}

const (
	// maxObservabilityFileBytes and maxObservabilityBytes bound what a project
	// can put in a bundle. They sit far under the control plane's 8 MiB layer
	// bound so a bundle forge builds is one the platform records.
	maxObservabilityFileBytes = 1 << 20
	maxObservabilityBytes     = 4 << 20
)

// observabilityEntries validates the files and turns them into layer entries.
func observabilityEntries(files []ObservabilityFile) ([]tarEntry, error) {
	var out []tarEntry
	seen := map[string]bool{}
	total := 0
	for _, f := range files {
		clean := path.Clean(f.Path)
		switch {
		case clean != f.Path || strings.HasPrefix(clean, "/") || strings.Contains(clean, ".."):
			return nil, fmt.Errorf("%w: observability path %q is not a clean relative path", release.ErrInvalid, f.Path)
		case clean != release.BundleFrontendDeclarationsPath && !strings.HasPrefix(clean, release.BundleObservabilityPrefix+"/"):
			return nil, fmt.Errorf("%w: observability path %q is not under %s/", release.ErrInvalid, f.Path, release.BundleObservabilityPrefix)
		case seen[clean]:
			return nil, fmt.Errorf("%w: observability path %q appears twice", release.ErrInvalid, f.Path)
		case len(f.Data) > maxObservabilityFileBytes:
			return nil, fmt.Errorf("%w: %s is %d bytes, over the %d-byte bound for one observability file", release.ErrInvalid, f.Path, len(f.Data), maxObservabilityFileBytes)
		}
		seen[clean] = true
		total += len(f.Data)
		out = append(out, tarEntry{name: clean, data: f.Data})
	}
	if total > maxObservabilityBytes {
		return nil, fmt.Errorf("%w: the observability files total %d bytes, over the %d-byte bound", release.ErrInvalid, total, maxObservabilityBytes)
	}
	return out, nil
}

// Bundle is a built bundle, in memory: its identity, its document, and the
// blobs that constitute it.
//
// Held in memory rather than written to a path because the two things a
// caller does with it — push it to a registry, write it into a local OCI
// layout — both want the blobs, and a bundle is small by design (doc §4.1
// measures control-plane prod at 246 KB).
type Bundle struct {
	// Digest is the OCI manifest digest: the bundle's identity.
	Digest string
	// Doc is the config blob, decoded. [Bundle.ConfigBlob] is the bytes.
	Doc release.BundleDoc
	// Manifest is the OCI image manifest, as raw JSON — the bytes Digest
	// is over, so a consumer verifies against exactly what was hashed
	// rather than against a re-marshalling of the struct.
	Manifest []byte
	// GuardedObjects is how many of the layer's objects were stamped
	// prune-exempt (prune_guard.go): the ones a reconciler must never
	// delete as a side effect of a render that came out short.
	//
	// Carried so the build can SAY so. A guard nobody sees is one nobody
	// notices has stopped working, and the day it matters is the day a
	// PersistentVolumeClaim is already gone.
	GuardedObjects int
	// blobs are the config and layer blobs, by descriptor.
	blobs []blob
}

type blob struct {
	desc ocispec.Descriptor
	data []byte
}

// ConfigBlob is the BundleDoc's canonical bytes — what
// [release.DecodeBundleDoc] reads and what the config descriptor's digest is
// over.
func (b Bundle) ConfigBlob() []byte {
	for _, bl := range b.blobs {
		if bl.desc.MediaType == release.BundleConfigMediaType {
			return bl.data
		}
	}
	return nil
}

// Layer returns the blob of a given media type.
func (b Bundle) Layer(mediaType string) ([]byte, bool) {
	for _, bl := range b.blobs {
		if bl.desc.MediaType == mediaType {
			return bl.data, true
		}
	}
	return nil, false
}

// Build renders one env's bundle: the manifest layer, the shape, the config
// digest, the BundleDoc, and the OCI manifest that binds them.
//
// It takes a context for symmetry with [Push] and because a large render is
// cancellable work; it performs no IO.
func Build(ctx context.Context, in BuildInput) (Bundle, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	switch {
	case strings.TrimSpace(in.Project) == "":
		return Bundle{}, fmt.Errorf("%w: bundle project is required", release.ErrInvalid)
	case strings.TrimSpace(in.Env) == "":
		return Bundle{}, fmt.Errorf("%w: bundle env is required", release.ErrInvalid)
	case in.CreatedAt.IsZero():
		return Bundle{}, fmt.Errorf("%w: bundle created_at is required; Build never reads a clock", release.ErrInvalid)
	}

	var inputErr error
	in, inputErr = snapshotBuildInput(in)
	if inputErr != nil {
		return Bundle{}, inputErr
	}
	// ONE parse, feeding both halves — see parse.go.
	docs, err := parseStream(in.Shape.Manifests)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}
	docs, secretRefs := splitSecrets(docs)
	secretRefs = mergeSecretRefs(secretRefs, in.Shape.SyncedSecrets)
	if !in.Shape.Kind.Valid() {
		return Bundle{}, fmt.Errorf("%w: bundle %s/%s: environment kind %q", release.ErrInvalid, in.Project, in.Env, in.Shape.Kind)
	}
	// THE PRUNE GUARD, before anything hashes or serializes these bodies.
	// A reconciler applying this layer with `prune: true` deletes whatever
	// the path no longer carries, and the trigger for that is not a human
	// decision — it is a render that merely came out one document short. So
	// every object that holds data is stamped prune-exempt here, where the
	// stamp lands inside the bundle and is covered by its digest.
	//
	// ORDER IS LOAD-BEARING: shapeObjects hashes `doc.body` and
	// packManifests serializes it, so a stamp applied afterwards would
	// make every guarded object's recorded hash describe bytes the layer
	// does not carry — and drift detection compares the live object
	// against that hash, so each one would read as permanently drifted.
	// See prune_guard.go.
	// FLUX-SAFETY, before anything hashes or serializes these bodies, for the
	// same reason as the guard below: a fix applied afterwards would make every
	// fixed object's recorded hash describe bytes the layer does not carry.
	//
	// Two things are fixed here because the fix does not change what an object
	// means: a Deployment's implicit strategy and progressDeadlineSeconds are
	// written down, and an object that needs Flux's recreate policy gets its
	// force annotation. What only the author can fix (a Job's TTL, an unhashed
	// Job name) is refused instead. See pkg/release/fluxsafe.go.
	if err := makeFluxSafe(docs, in.Reconciled, in.ReconciledUnits); err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}
	guarded := guardStatefulObjects(docs, in.Shape.StatefulWorkloads)
	objects, err := shapeObjects(docs, in.Shape.Images, in.Shape.StatefulWorkloads)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}
	shape := in.Shape.shapeWith(objects)
	if in.Snapshot != nil {
		shape, err = release.ProjectSnapshotShape(*in.Snapshot)
		if err != nil {
			return Bundle{}, err
		}
	}
	if err := shape.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}

	extra, err := observabilityEntries(in.Observability)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}
	manifestsLayer, clusterTrees, trees, err := packManifests(docs, in.Units, extra...)
	if err == nil && in.Snapshot != nil {
		manifestsLayer, clusterTrees, trees, err = packSnapshot(*in.Snapshot, docs, in.Units, extra)
	}
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: pack manifests: %w", in.Project, in.Env, err)
	}
	// THE INGEST GATE, run at build so forge refuses to cut a bundle the
	// control plane would refuse to record. It is the same function, over the
	// final layer bytes — what the shared controller would be handed.
	if _, err := release.ValidateManifestsLayer(bytes.NewReader(manifestsLayer)); err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}
	configDigest, err := release.ConfigDigest(shape)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}

	doc := release.BundleDoc{
		Schema:       release.BundleSchema,
		Project:      in.Project,
		Env:          in.Env,
		Release:      in.Release,
		ConfigDigest: configDigest,
		Pins:         in.Pins,
		Provenance:   in.Provenance,
		Shape:        shape,
		// From the SAME parsed, guarded bodies the shape hashed, so a
		// summary describes exactly the bytes its object's Hash names.
		Objects:      objectSummaries(docs, shape),
		ClusterPaths: clusterTrees,
		Trees:        trees,
		Secrets:      secretRefs,
		CreatedAt:    in.CreatedAt.UTC(),
	}
	doc = in.Reader.Limit(doc)
	if in.Snapshot != nil {
		doc.Schema = release.SnapshotBundleSchema
		doc.Snapshot = in.Snapshot
		doc.SnapshotDigest, err = in.Snapshot.Digest()
		if err != nil {
			return Bundle{}, err
		}
		if err := release.ValidateSnapshotLayer(*in.Snapshot, manifestsLayer); err != nil {
			return Bundle{}, err
		}
	}
	if err := doc.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}
	configBlob, err := json.Marshal(doc)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: encode document: %w", in.Project, in.Env, err)
	}

	// F-13, THE LAST GATE. parseStream already redacted every Secret, so
	// this cannot fail unless that path broke — which is exactly why it is
	// checked here, over the FINAL bytes, rather than trusted. A bundle is
	// kept forever and shared; a value in one is a permanent leak, so the
	// invariant is proved against what will actually be written and not
	// against an intermediate value.
	if err := refuseSecretValues(manifestsLayer, configBlob); err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: %w", in.Project, in.Env, err)
	}

	if int64(len(configBlob)) > MaxManifestBytes {
		return Bundle{}, fmt.Errorf("%w: bundle config exceeds %d bytes", release.ErrInvalid, MaxManifestBytes)
	}
	configDesc := content.NewDescriptorFromBytes(release.BundleConfigMediaType, configBlob)
	manifestsDesc := content.NewDescriptorFromBytes(release.BundleManifestsLayer, manifestsLayer)
	blobs := []blob{{configDesc, configBlob}, {manifestsDesc, manifestsLayer}}
	// ONE layer. A Flux layerSelector selects exactly one, so a second
	// would be recorded as shipped and never applied — see the package
	// note on why platform charts are not here.
	layers := []ocispec.Descriptor{manifestsDesc}

	ociManifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: release.BundleArtifactType,
		Config:       configDesc,
		Layers:       layers,
		Annotations:  annotationsFor(doc),
	}
	raw, err := json.Marshal(ociManifest)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle %s/%s: encode OCI manifest: %w", in.Project, in.Env, err)
	}
	manifestDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	return Bundle{
		Digest:         manifestDesc.Digest.String(),
		Doc:            doc,
		Manifest:       raw,
		GuardedObjects: guarded,
		blobs:          append(blobs, blob{manifestDesc, raw}),
	}, nil
}

// annotationsFor is the manifest's annotation set (doc §4.1). Empty values
// are omitted rather than written blank: an annotation that is present and
// empty reads as "recorded as nothing", and a registry UI would show it.
func annotationsFor(doc release.BundleDoc) map[string]string {
	out := map[string]string{
		ocispec.AnnotationCreated:  fixedCreated,
		AnnotationEnv:              doc.Env,
		AnnotationProject:          doc.Project,
		AnnotationConfigDigest:     doc.ConfigDigest,
		AnnotationDirty:            fmt.Sprint(doc.Provenance.Dirty),
		ocispec.AnnotationRevision: doc.Provenance.Commit,
		ocispec.AnnotationSource:   doc.Provenance.Repo,
	}
	if doc.Release != "" {
		out[AnnotationRelease] = doc.Release
	}
	if key := doc.Provenance.Worktree.Key; key != "" {
		out[AnnotationWorktreeKey] = key
	}
	for k, v := range out {
		if v == "" {
			delete(out, k)
		}
	}
	return out
}

// ─── The manifest layer ─────────────────────────────────────────────────────

// manifestEntryName is the layer path of one document on one cluster.
//
// A flat cluster (the unclustered and hosted trees) keeps the v1 shape,
// `manifests/<cluster>/<NNN>-<kind>-<name>.yaml`. Every other cluster is split
// by delivery unit and phase,
// `manifests/<cluster>/<unit>/<phase>/<NNN>-<kind>-<name>.yaml`. The tree path
// is [release.BundleTreePath]'s, the same string the reconciler sets as
// Kustomization.spec.path.
//
// The NNN is the document's position in the render, zero-padded, because the
// apply ORDER within a tree is part of the stream's meaning — a Namespace must
// land before the objects in it — and kustomize applies a generated
// kustomization's resources in lexicographic order. The index is global to the
// render, so it is unique across trees and stable when objects move.
func manifestEntryName(doc parsedDoc, cluster string) string {
	dir := release.BundleClusterPath(cluster)
	if !release.FlatCluster(cluster) {
		dir = release.BundleTreePath(cluster, docUnit(doc), doc.phase)
	}
	return fmt.Sprintf("%s/%03d-%s-%s.yaml", dir, doc.index,
		pathSegment(strings.ToLower(doc.meta.kind)), pathSegment(doc.meta.name))
}

// pathSegment sanitizes one path component. pkg/release owns the rule, so
// forge and the control plane cannot disagree about a cluster's path; this is
// the local spelling for the kind and name halves of a filename.
func pathSegment(s string) string { return release.BundlePathSegment(s) }

// packManifests writes the redacted documents into the deterministic tar.gz
// that is layer 0.
//
// The documents are re-SERIALIZED from the parsed, redacted bodies rather
// than copied from the render's text. That is the F-13 requirement: the
// rendered text of a Secret contains its value, so a layer built by copying
// text would ship one however carefully the shape was redacted. Re-
// serializing costs a canonical YAML encoding and buys the guarantee that no
// unredacted byte can reach a bundle.
// It returns the layer AND the per-cluster trees it wrote, which is what the
// BundleDoc publishes. The trees come from the entries actually written rather
// than from the env's declared cluster list: a declared cluster with no
// documents routed to it must not get a Kustomization, because an empty path
// reconciles green over an env nothing applied.
func packManifests(docs []parsedDoc, units []release.DeliveryUnit, extra ...tarEntry) ([]byte, []release.BundleClusterTree, []release.BundleTree, error) {
	type entry struct {
		name    string
		cluster string
		data    []byte
		object  release.TreeObject
	}
	var entries []entry
	for _, doc := range docs {
		encoded, err := encodeDocument(doc.body)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("encode %s %s: %w", doc.meta.kind, doc.meta.name, err)
		}
		clusters := doc.clusters
		if len(clusters) == 0 {
			clusters = []string{""}
		}
		for _, cluster := range clusters {
			entries = append(entries, entry{
				name: manifestEntryName(doc, cluster), cluster: cluster, data: encoded,
				object: release.TreeObject{
					Cluster: cluster, Unit: docUnit(doc), Phase: doc.phase,
					Kind: doc.meta.kind, Namespace: doc.meta.namespace, Name: doc.meta.name,
					DeadlineSeconds: docDeadlineSeconds(doc),
				},
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	files := make([]tarEntry, 0, len(entries))
	counts := map[string]int{}
	var order []string
	var treeObjects []release.TreeObject
	for i, e := range entries {
		// Two documents that render to the same (cluster, tree, index, kind,
		// name) would silently collapse into one entry, and the layer
		// would carry fewer objects than the shape claims. The shape's
		// own duplicate check would not catch it: these are per-cluster
		// pairs, and the names are sanitized, so two different cluster
		// contexts can sanitize to the same segment.
		if i > 0 && entries[i-1].name == e.name {
			return nil, nil, nil, fmt.Errorf("two rendered documents both map to %s", e.name)
		}
		files = append(files, tarEntry{name: e.name, data: e.data})
		if _, seen := counts[e.cluster]; !seen {
			order = append(order, e.cluster)
		}
		counts[e.cluster]++
		if !release.FlatCluster(e.cluster) {
			treeObjects = append(treeObjects, e.object)
		}
	}
	trees, err := release.PlanTrees(treeObjects, units)
	if err != nil {
		return nil, nil, nil, err
	}
	// The observability files ride in the same layer but are not documents:
	// they are not counted in any cluster tree and no Kustomization path
	// reaches them.
	files = append(files, extra...)
	layer, err := writeTarGz(files)
	if err != nil {
		return nil, nil, nil, err
	}
	sort.Strings(order)
	clusterTrees := make([]release.BundleClusterTree, 0, len(order))
	for _, cluster := range order {
		clusterTrees = append(clusterTrees, release.BundleClusterTree{
			Cluster:   cluster,
			Path:      release.BundleClusterPath(cluster),
			Documents: counts[cluster],
		})
	}
	return layer, clusterTrees, trees, nil
}

// encodeDocument serializes one document back to YAML, deterministically.
//
// yaml.v3 emits a map's keys in sorted order, so the bytes are a function of
// the document's content rather than of its decode order — the same property
// [release.HashDocument] relies on, for the same reason.
func encodeDocument(body any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type tarEntry struct {
	name string
	data []byte
}

// writeTarGz writes a deterministic tar.gz: sorted entries, fixed mtimes,
// zero uid/gid, fixed modes, and no gzip header name or time.
//
// Every one of those is a thing that would otherwise vary run to run and
// change the digest — which would make a bundle's identity a function of when
// and where it was built, so an unchanged render would cut a new record on
// every build and "has this already been deployed" would always answer no.
func writeTarGz(entries []tarEntry) ([]byte, error) {
	sorted := append([]tarEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)

	// Parent directories are written explicitly, once each and in order,
	// so the archive is well-formed for an extractor that does not create
	// them implicitly.
	seen := map[string]bool{}
	for _, e := range sorted {
		for _, dir := range parentDirs(e.name) {
			if seen[dir] {
				continue
			}
			seen[dir] = true
			if err := tw.WriteHeader(&tar.Header{
				Name: dir + "/", Typeflag: tar.TypeDir, Mode: tarDirMode,
				ModTime: fixedModTime, Format: tar.FormatPAX,
			}); err != nil {
				return nil, err
			}
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: e.name, Typeflag: tar.TypeReg, Mode: tarFileMode,
			Size: int64(len(e.data)), ModTime: fixedModTime, Format: tar.FormatPAX,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parentDirs lists an entry's ancestor directories, outermost first.
func parentDirs(name string) []string {
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return nil
	}
	var out []string
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

// docDeadlineSeconds is the time allowance an object states for itself: a
// Deployment's progressDeadlineSeconds, a Job's activeDeadlineSeconds. 0 means
// it states none and the default for its kind applies (release.TreeBudget).
func docDeadlineSeconds(doc parsedDoc) int {
	body, ok := doc.body.(map[string]any)
	if !ok {
		return 0
	}
	spec, _ := body["spec"].(map[string]any)
	field := "progressDeadlineSeconds"
	if doc.meta.kind == "Job" {
		field = "activeDeadlineSeconds"
	}
	switch n := spec[field].(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
