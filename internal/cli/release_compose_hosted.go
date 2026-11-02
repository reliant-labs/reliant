package cli

import (
	"context"
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedSiteResultKind marks a buildResult that is a hosted frontend's pushed
// site release, so a workload of the same name can never be matched to it.
const hostedSiteResultKind = "hosted-static"

// compositionHostedSites builds and pushes every SELECTED hosted frontend of a
// snapshot composition — the frontends the platform publishes from their
// sealed StaticSite record — and returns this invocation's outputs.
//
// A hosted frontend ships as an OCI site release, and the release its record
// serves must be the one THIS composition built: the snapshot binds the
// frontend slot to that exact artifact, and the record is rendered from the
// binding (hostedBundleObjects). A carried (unselected) frontend is not
// rebuilt; its binding, and so its record, are the base's.
func compositionHostedSites(ctx context.Context, run *compositionRun, entities *KCLEntities, opts buildOptions) ([]buildResult, error) {
	selected := map[release.ConsumerKey]bool{}
	for _, k := range run.Selected {
		selected[k] = true
	}
	frontends := compositionFrontendEntities(run, entities)
	var sites []FrontendEntity
	for _, c := range run.Declaration.Consumers {
		if !selected[c.Key] || !platformPublishedFrontend(c.Key) || !consumerHasSlot(c, "frontend") {
			continue
		}
		found := false
		for _, f := range frontends.Frontends {
			if f.Name == c.Key.Workload && frontendIsHosted(f) {
				sites, found = append(sites, f), true
			}
		}
		if !found {
			return nil, fmt.Errorf("selected hosted frontend %s has no declaration to build", c.Key.Workload)
		}
	}
	if len(sites) == 0 {
		return nil, nil
	}
	if !opts.pushPlan.push {
		names := make([]string, 0, len(sites))
		for _, f := range sites {
			names = append(names, f.Name)
		}
		return nil, errHostedSiteMustPush(opts.env, names)
	}
	var out []buildResult
	for _, f := range sites {
		repository, digest, err := pushHostedStaticSite(ctx, projectDirForKCL(), frontends, opts, f)
		if err != nil {
			return nil, err
		}
		if !release.ValidDigest(digest) {
			return nil, fmt.Errorf("hosted static site %s: push returned no immutable digest", f.Name)
		}
		fmt.Printf("[build]   %s release %s\n", f.Name, digest)
		out = append(out, buildResult{name: f.Name + " (hosted static site)", kind: hostedSiteResultKind, workload: f.Name, repository: repository, digest: digest})
	}
	return out, nil
}

func consumerHasSlot(c release.Consumer, slot string) bool {
	for _, s := range c.Slots {
		if s == slot {
			return true
		}
	}
	return false
}

// hostedSiteArtifact is a selected hosted frontend's binding: the exact site
// release this invocation pushed, attributed to the source it was built from.
// It is platform-neutral (a site release has no os/architecture), which the
// snapshot permits only for a hosted frontend's slot.
func hostedSiteArtifact(name string, results []buildResult, source release.Source, version string) (release.SnapshotArtifact, error) {
	var out *buildResult
	for i, r := range results {
		if r.err != nil || r.kind != hostedSiteResultKind || r.workload != name {
			continue
		}
		if out != nil {
			return release.SnapshotArtifact{}, fmt.Errorf("selected hosted frontend %s has conflicting site releases", name)
		}
		out = &results[i]
	}
	if out == nil || out.repository == "" || !release.ValidDigest(out.digest) {
		return release.SnapshotArtifact{}, fmt.Errorf("selected hosted frontend %s lacks this invocation's pushed site release", name)
	}
	return release.SnapshotArtifact{Kind: release.KindOCI, Location: out.repository, Content: out.digest,
		Origin: release.ArtifactOrigin{Release: version, Source: &source}}, nil
}

// snapshotHostedPins is a composition's hosted pins, read from the sealed
// snapshot's bindings alone — never from the release ledger or the env's
// current promotion, which name what is LIVE, not what this candidate binds.
// Every hosted service the group would publish must have an OCI binding; a
// hosted database has no artifact.
func snapshotHostedPins(s release.Snapshot, group deploytarget.ServiceGroup) (map[string]string, map[string]release.SnapshotArtifact, error) {
	digests := map[string]string{}
	bound := map[string]release.SnapshotArtifact{}
	for _, svc := range group.Services {
		if svc.Hosted == nil || svc.Hosted.Tier == deploytarget.HostedTierDatabase {
			continue
		}
		slot := "container:" + svc.Name
		if svc.Hosted.Tier == deploytarget.HostedTierStatic {
			slot = "frontend"
		}
		key := release.ConsumerKey{Cluster: release.BundleHostedCluster, Workload: svc.Name}
		var artifact *release.SnapshotArtifact
		for _, b := range s.Bindings {
			if b.Consumer != key || b.Slot != slot {
				continue
			}
			a, ok := s.Artifacts[b.Artifact]
			if !ok {
				return nil, nil, fmt.Errorf("hosted %s: snapshot binding names a missing artifact", svc.Name)
			}
			artifact = &a
		}
		if artifact == nil {
			return nil, nil, fmt.Errorf("hosted %s: the snapshot binds no %s slot", svc.Name, slot)
		}
		if artifact.Kind != release.KindOCI {
			return nil, nil, fmt.Errorf("hosted %s: the snapshot binds its %s slot to a %s artifact, not a built release; select %s to rebuild it",
				svc.Name, slot, artifact.Kind, svc.Name)
		}
		artifactKey := deploytarget.HostedArtifactKey(svc)
		digests[artifactKey] = artifact.Content
		bound[svc.Name] = *artifact
	}
	return digests, bound, nil
}

// checkHostedRecordsMatchSnapshot is the closing check: every rendered record
// addresses exactly the artifact the snapshot binds, so the record a platform
// applies and the binding a reader audits cannot name different bytes.
func checkHostedRecordsMatchSnapshot(records []deploytarget.HostedRecord, bound map[string]release.SnapshotArtifact) error {
	for _, r := range records {
		a, ok := bound[r.Name]
		if !ok {
			continue
		}
		var doc struct {
			Spec struct {
				Image             string `json:"image"`
				ReleaseRepository string `json:"releaseRepository"`
				LiveDigest        string `json:"liveDigest"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(r.YAML, &doc); err != nil {
			return fmt.Errorf("hosted %s %s: %w", r.Kind, r.Name, err)
		}
		got := doc.Spec.Image
		if r.Kind == "StaticSite" {
			got = doc.Spec.ReleaseRepository + "@" + doc.Spec.LiveDigest
		}
		if want := a.Location + "@" + a.Content; got != want {
			return fmt.Errorf("hosted %s %s addresses %q, but the snapshot binds %q", r.Kind, r.Name, got, want)
		}
	}
	return nil
}
