package cli

// AN ENV DEPLOY'S EVIDENCE SNAPSHOT.
//
// A release cut by a plain `forge env build` / `forge env deploy` has no
// snapshot intents, so its bundle never carried a snapshot, and the promotion
// that shipped it froze nothing: `forge release compose` then refused that env
// as a base ("has no sealed complete snapshot"), and the control plane's
// status guards had no desired state to compare against. An evidence seal
// closes that without changing what such a release IS: when the env's control
// plane advertises it (snapshot_features: evidence-seal), the bundle seals a
// complete snapshot of exactly the bytes it carries, the control plane
// verifies it as it verifies a composition's and records an evidence seal, and
// the promotion freezes it. It decides nothing: no CAS, no expected-current,
// no change to which bundle ships, and the legacy deploy path, --target,
// --frontends-only and promotion of one release across envs are untouched.
//
// A SEAL NEVER COSTS THE ENV ANYTHING. The snapshot carries every asset the
// legacy bundle would, the service catalog (the RPC explorer's routes)
// included, so a bundle with a catalog seals only on a control plane that
// also advertises evidence-seal-service-catalog, and only when the snapshot
// with its catalog fits the 1 MiB config bound. Otherwise it is today's
// legacy bundle, catalog and all.
//
// THE BINDINGS ARE READ FROM THE RENDER, never from a pin map: every slot's
// artifact is the exact image reference its resource carries (a hosted
// Workload's spec.image, a StaticSite's release, a pod template's container),
// and the composer's own ValidateSnapshotBindings then proves each binding
// reproduces those bytes. A slot this forge cannot bind with certainty (a
// mutable tag, an unresolvable registry descriptor, an image the release does
// not record) seals NOTHING: the deploy writes the legacy bundle and says why
// in one line. Sealing never fails a deploy.
//
// Contract: /home/workspace/briefings/snapshot-evidence-contract.md
// (control-plane#2321).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// maxSnapshotManifestsLayerBytes is the control plane's bound on a snapshot
// bundle's manifests layer, sent inline with RecordBundle.
const maxSnapshotManifestsLayerBytes = 8 << 20

// evidenceSeal is a sealed evidence bundle, ready to place and record.
type evidenceSeal struct {
	Snapshot release.Snapshot
	Digest   string
	Bundle   bundle.Bundle
	// Layer is the manifests layer RecordBundle carries for a snapshot.
	Layer []byte
}

// carriesServiceCatalog reports whether a bundle's files include the
// service catalog.
func carriesServiceCatalog(files []bundle.ObservabilityFile) bool {
	for _, f := range files {
		if f.Path == release.BundleServiceCatalogPath {
			return true
		}
	}
	return false
}

// evidenceSkipNoted holds the (env, reason) pairs whose "no snapshot" note
// this process has printed: a deploy writes its bundle more than once (plan,
// then write), and the reason is worth saying once.
var evidenceSkipNoted sync.Map

// sealEvidenceSnapshotFn is a seam so a test can state the seal outcome
// without standing up a registry and a release.
var sealEvidenceSnapshotFn = sealEvidenceSnapshot

// sealEvidenceSnapshot seals the env's rendered state into a v2 bundle, or
// says why it did not. A nil seal with an empty reason is "not applicable"
// (no capable control plane, an unreleased or unpushed bundle, a release cut
// as a snapshot release), which is the normal state of most deploys and
// prints nothing. A nil seal WITH a reason is a capable env that could not be
// sealed; the caller prints the reason once and writes the legacy bundle.
func sealEvidenceSnapshot(ctx context.Context, projectDir, env string, ledger envLedger, doc envShapeDoc, in bundleBuildInputs, input bundle.BuildInput) (*evidenceSeal, string) {
	if !ledger.Hosted || in.Release == "" || !in.Pushed || doc.Shape.Kind == release.EnvLocal {
		return nil, ""
	}
	store, ok := ledger.Releases.(*hostedStore)
	if !ok {
		return nil, ""
	}
	features, err := evidenceSealSupported(ctx, store.client)
	if err != nil || !features.Seal {
		return nil, ""
	}
	rel, err := ledger.Releases.Get(ctx, in.Release)
	if err != nil || rel == nil {
		return nil, ""
	}
	// A release cut as a snapshot release seals by intent (release compose);
	// its env deploys write no bundle here (ensureHostedReleaseBundle).
	if len(rel.SnapshotIntents) > 0 || len(rel.Catalog) > 0 {
		return nil, ""
	}
	// The snapshot carries exactly the assets the legacy bundle would, the
	// service catalog included: a seal never costs the env its RPC
	// explorer's routes. So a bundle with a catalog seals only on a control
	// plane that takes one in a snapshot.
	if carriesServiceCatalog(input.Observability) && !features.ServiceCatalog {
		return nil, "its control plane does not take a service catalog in a snapshot (" + snapshotFeatureEvidenceSealServiceCatalog + ")"
	}
	if store.project != "" && store.project != doc.Project {
		return nil, fmt.Sprintf("the bundle's project %q is not the control plane project %q", doc.Project, store.project)
	}
	seal, err := buildEvidenceSnapshot(ctx, projectDir, env, doc, *rel, input)
	if err != nil {
		return nil, err.Error()
	}
	return seal, ""
}

// buildEvidenceSnapshot is the evidence snapshot of one rendered env: a full
// snapshot with no base, every consumer selected, through buildFullSnapshot.
func buildEvidenceSnapshot(ctx context.Context, projectDir, env string, doc envShapeDoc, rel release.Release, input bundle.BuildInput) (*evidenceSeal, error) {
	p := doc.Provenance
	if p.Dirty {
		return nil, fmt.Errorf("the render is from a dirty checkout, which no commit attributes")
	}
	if p.Repo == "" || !release.ValidObjectID(p.Commit) {
		return nil, fmt.Errorf("the render has no git source to attribute it to")
	}
	consumers, err := compositionConsumers(doc.entities)
	if err != nil {
		return nil, err
	}
	resources, err := bundle.SnapshotResources(ctx, input, consumers)
	if err != nil {
		return nil, err
	}
	refs, err := snapshotRuntimeRefs(resources, consumers)
	if err != nil {
		return nil, err
	}
	candidate := release.Snapshot{
		Schema: release.SnapshotSchema, Project: doc.Project, Environment: env,
		Source:       release.Source{Repo: p.Repo, Commit: p.Commit},
		PolicyDigest: compositionPolicyDigest(),
		Consumers:    consumers,
		Artifacts:    map[release.ArtifactID]release.SnapshotArtifact{},
	}
	for _, c := range consumers {
		candidate.Selected = append(candidate.Selected, c.Key)
		candidate.Decisions = append(candidate.Decisions, release.CompositionDecision{
			Consumer: c.Key, Action: "reuse", Reason: "evidence: the environment as rendered for release " + rel.Version,
		})
	}
	candidate.Closure = candidate.Selected
	if err := bindEvidenceSlots(ctx, &candidate, doc.entities, rel, refs); err != nil {
		return nil, err
	}
	composed, built, err := buildFullSnapshot(ctx, fullSnapshotInput{
		Candidate: candidate, Bundle: input, Selected: candidate.Selected,
		Frontends: doc.entities, ProjectDir: projectDir, Env: env,
	})
	if err != nil {
		var tooLarge *bundle.ConfigTooLargeError
		if errors.As(err, &tooLarge) {
			return nil, fmt.Errorf("its snapshot (with every asset the bundle carries) is %d bytes, over the %d-byte bundle bound", tooLarge.Size, bundle.MaxManifestBytes)
		}
		return nil, err
	}
	layer, ok := built.Layer(release.BundleManifestsLayer)
	if !ok || len(layer) > maxSnapshotManifestsLayerBytes {
		return nil, fmt.Errorf("the snapshot's manifests layer exceeds the %d-byte bound", maxSnapshotManifestsLayerBytes)
	}
	digest, err := composed.Digest()
	if err != nil {
		return nil, err
	}
	return &evidenceSeal{Snapshot: composed, Digest: digest, Bundle: built, Layer: layer}, nil
}

// bindEvidenceSlots binds every consumer slot to the artifact the render
// carries for it. Primary slots first, as populateCompositionBindings does, so
// an auxiliary input of a build-only producer finds the producer's binding.
func bindEvidenceSlots(ctx context.Context, candidate *release.Snapshot, entities *KCLEntities, rel release.Release, refs map[release.ConsumerKey]map[string]string) error {
	type slot struct {
		consumer release.Consumer
		name     string
	}
	var primary, auxiliary []slot
	for _, c := range candidate.Consumers {
		for _, s := range c.Slots {
			if compositionPrimarySlot(entities, c.Key, s) {
				primary = append(primary, slot{c, s})
			} else {
				auxiliary = append(auxiliary, slot{c, s})
			}
		}
	}
	recorded := map[string]bool{}
	for _, a := range rel.Artifacts {
		for _, d := range a.Digests {
			recorded[d] = true
		}
	}
	for _, s := range append(primary, auxiliary...) {
		if err := bindEvidenceSlot(ctx, candidate, entities, rel, refs, s.consumer.Key, s.name); err != nil {
			return err
		}
	}
	// The control plane compares what forge built with what the release
	// recorded; refuse here what it would refuse there.
	for _, a := range candidate.Artifacts {
		if a.Kind == release.KindOCI && a.Origin.External == nil && !recorded[a.Content] {
			return fmt.Errorf("%s@%s is not an image release %s recorded", a.Location, a.Content, rel.Version)
		}
	}
	return nil
}

func bindEvidenceSlot(ctx context.Context, candidate *release.Snapshot, entities *KCLEntities, rel release.Release, refs map[release.ConsumerKey]map[string]string, key release.ConsumerKey, slot string) error {
	ref := refs[key][slot]
	if slot == "frontend" {
		// A hosted frontend binds the site release its StaticSite serves;
		// selectedFrontendArtifact reads it as a result of this invocation.
		var results []buildResult
		if platformPublishedFrontend(key) {
			repository, digest, ok := strings.Cut(ref, "@")
			if !ok {
				return fmt.Errorf("hosted frontend %s has no StaticSite record naming its release", key.Workload)
			}
			results = []buildResult{{kind: hostedSiteResultKind, workload: key.Workload, repository: repository, digest: digest}}
		}
		artifact, err := selectedFrontendArtifact(candidate, entities, buildOptions{release: rel.Version}, results, key)
		if err != nil {
			return err
		}
		return addEvidenceBinding(candidate, key, slot, artifact, "")
	}
	var w *WorkloadEntity
	for i := range entities.Workloads {
		if entities.Workloads[i].Name == key.Workload {
			w = &entities.Workloads[i]
		}
	}
	built := false
	requiredPlatform := ""
	if w != nil {
		built = w.Build.Type != "" && compositionPrimarySlot(entities, key, slot)
		if w.Runtime.Type == RuntimeHosted {
			requiredPlatform = "linux/" + w.Runtime.HostedPlatform()
		} else if w.Runtime.Cluster != nil && w.Runtime.Cluster.Platform != "" {
			requiredPlatform = "linux/" + w.Runtime.Cluster.Platform
		}
		if ref == "" && strings.HasPrefix(slot, "input:") {
			ref = w.ReleaseInputs[strings.TrimPrefix(slot, "input:")]
		}
		if ref == "" && slot == "build-only" {
			ref = releaseImageRef(rel, w.Image)
		}
	}
	for _, m := range entities.ReleaseManifestConsumers {
		if ref == "" && m.Name == key.Workload && m.Cluster == key.Cluster {
			ref = m.ReleaseInputs[slot]
		}
	}
	if provider := entities.OpenBao; ref == "" && provider != nil && provider.ReleaseOwner == key.Workload && provider.Cluster == key.Cluster {
		ref = provider.ReleaseInputs[slot]
	}
	if ref == "" {
		return fmt.Errorf("%s/%s carries no image this deploy can bind", key.Workload, slot)
	}
	if !strings.Contains(ref, "@sha256:") {
		return fmt.Errorf("%s/%s runs %s, a mutable tag rather than a digest", key.Workload, slot, ref)
	}
	artifact, bound, err := compositionInputArtifact(candidate, entities, key, slot, ref)
	if err != nil {
		return err
	}
	if !bound {
		var source *release.Source
		if built {
			source = releaseBuildSource(rel, candidate.Source)
		}
		artifact, err = resolveCompositionArtifact(ctx, ref, rel.Version, nil, source)
		if err != nil {
			return err
		}
	}
	if requiredPlatform != "" {
		found := false
		for _, platform := range artifact.Platforms {
			found = found || platform == requiredPlatform
		}
		if !found {
			return fmt.Errorf("%s/%s does not provide required platform %s", key.Workload, slot, requiredPlatform)
		}
	}
	// The tag the resource carries is kept, so the binding reproduces its
	// exact image reference (release.BindingReference).
	return addEvidenceBinding(candidate, key, slot, artifact, declaredImageTag(ref))
}

func addEvidenceBinding(candidate *release.Snapshot, key release.ConsumerKey, slot string, artifact release.SnapshotArtifact, tag string) error {
	id, err := release.ArtifactIdentity(artifact)
	if err != nil {
		return fmt.Errorf("binding %s/%s artifact %s@%s: %w", key.Workload, slot, artifact.Location, artifact.Content, err)
	}
	if prior, ok := candidate.Artifacts[id]; ok && (prior.Origin.External == nil) != (artifact.Origin.External == nil) {
		return fmt.Errorf("%s@%s is both built and external", artifact.Location, artifact.Content)
	}
	if _, ok := candidate.Artifacts[id]; !ok {
		candidate.Artifacts[id] = artifact
	}
	candidate.Bindings = append(candidate.Bindings, release.ConsumerBinding{Consumer: key, Slot: slot, Artifact: id, Tag: tag})
	return nil
}

// releaseBuildSource is where release rel's images were built from, or the
// render's own source for a release that predates provenance.
func releaseBuildSource(rel release.Release, fallback release.Source) *release.Source {
	if p := rel.Provenance; p != nil && p.Repo != "" && release.ValidObjectID(p.Commit) {
		return &release.Source{Repo: p.Repo, Commit: p.Commit}
	}
	s := fallback
	return &s
}

// releaseImageRef is the `repo@digest` release rel recorded for image's
// repository, or "" when it recorded none or more than one.
func releaseImageRef(rel release.Release, image string) string {
	repository := imageRepository(image)
	ref := ""
	for name, a := range rel.Artifacts {
		digest, ok := a.SharedDigest()
		if !ok {
			continue
		}
		at := imageRepository(a.URI)
		if a.URI == "" {
			at = name
		}
		if at != repository {
			continue
		}
		if ref != "" {
			return ""
		}
		ref = repository + "@" + digest
	}
	return ref
}

// snapshotRuntimeRefs is, per consumer and slot, the exact image reference
// the rendered resources carry: the inverse of release.ValidateSnapshotBindings,
// read by the same rules (a managed Workload's spec.image is its sole owner's
// primary slot; a StaticSite serves its frontend's site release; a pod
// template's containers and init containers are the slots of the same name of
// every owner that declares one). Two resources carrying different references
// for one slot are refused: a binding names one artifact.
func snapshotRuntimeRefs(resources []release.SnapshotResource, consumers []release.Consumer) (map[release.ConsumerKey]map[string]string, error) {
	declared := map[release.ConsumerKey]map[string]bool{}
	for _, c := range consumers {
		declared[c.Key] = map[string]bool{}
		for _, s := range c.Slots {
			declared[c.Key][s] = true
		}
	}
	refs := map[release.ConsumerKey]map[string]string{}
	set := func(owner release.ConsumerKey, slot, ref string) error {
		if refs[owner] == nil {
			refs[owner] = map[string]string{}
		}
		if prior, ok := refs[owner][slot]; ok && prior != ref {
			return fmt.Errorf("%s/%s carries both %s and %s", owner.Workload, slot, prior, ref)
		}
		refs[owner][slot] = ref
		return nil
	}
	for _, r := range resources {
		var body map[string]any
		if err := yaml.Unmarshal(r.Spec, &body); err != nil {
			return nil, err
		}
		api, _ := body["apiVersion"].(string)
		kind, _ := body["kind"].(string)
		meta, _ := body["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		spec, _ := body["spec"].(map[string]any)
		switch {
		case api == "forge.dev/v1alpha1" && kind == "Workload":
			image, _ := spec["image"].(string)
			if len(r.Owners) == 1 && image != "" {
				if err := set(r.Owners[0], "container:"+r.Owners[0].Workload, image); err != nil {
					return nil, err
				}
			}
			continue
		case api == "forge.dev/v1alpha1" && kind == "StaticSite":
			repository, _ := spec["releaseRepository"].(string)
			digest, _ := spec["liveDigest"].(string)
			if repository != "" && digest != "" {
				if err := set(release.ConsumerKey{Cluster: r.Key.Cluster, Workload: name}, "frontend", repository+"@"+digest); err != nil {
					return nil, err
				}
			}
			continue
		case kind == "CronJob":
			jt, _ := spec["jobTemplate"].(map[string]any)
			spec, _ = jt["spec"].(map[string]any)
		}
		if kind != "Pod" {
			tpl, _ := spec["template"].(map[string]any)
			spec, _ = tpl["spec"].(map[string]any)
		}
		for _, list := range []struct{ field, prefix string }{{"containers", "container:"}, {"initContainers", "init:"}} {
			containers, _ := spec[list.field].([]any)
			for _, raw := range containers {
				container, _ := raw.(map[string]any)
				cname, _ := container["name"].(string)
				image, _ := container["image"].(string)
				for _, owner := range r.Owners {
					if declared[owner][list.prefix+cname] && image != "" {
						if err := set(owner, list.prefix+cname, image); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}
	return refs, nil
}
