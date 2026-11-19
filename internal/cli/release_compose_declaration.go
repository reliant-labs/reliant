package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// compositionConsumers extracts explicit stable runtime slots. Pure declarations
// come from the render; image resolution/provenance is supplied by the builder.
func compositionConsumers(e *KCLEntities) ([]release.Consumer, error) {
	if e == nil {
		return nil, fmt.Errorf("snapshot requires a rendered environment")
	}
	var out []release.Consumer
	for _, w := range e.Workloads {
		cluster := ""
		switch w.Runtime.Type {
		case RuntimeCluster:
			cluster = w.Runtime.Cluster.Cluster
		case RuntimeHosted:
			cluster = release.BundleHostedCluster
		case RuntimeBuildOnly:
			cluster = "build-only"
		default:
			return nil, fmt.Errorf("snapshot consumer %q uses unsupported runtime %q", w.Name, w.Runtime.Type)
		}
		c := release.Consumer{Key: release.ConsumerKey{Cluster: cluster, Workload: w.Name}, DependenciesDeclared: w.ReleaseDependencies != nil}
		if w.Runtime.Type == RuntimeBuildOnly {
			c.Slots = []string{"build-only"}
		} else {
			c.Slots = []string{"container:" + w.Name}
		}
		for _, sidecar := range w.Spec.Sidecars {
			prefix := "container:"
			if sidecar.Native {
				prefix = "init:"
			}
			c.Slots = append(c.Slots, prefix+sidecar.Name)
		}
		for name := range w.ReleaseInputs {
			c.Slots = append(c.Slots, "input:"+name)
		}
		out = append(out, c)
	}
	// A source-built frontend has its own immutable source slot. Containerized
	// frontends already appear as workloads, never infer them from repository.
	for _, fe := range e.Frontends {
		c := release.Consumer{Key: compositionFrontendKey(fe), Slots: []string{"frontend"}, DependenciesDeclared: fe.ReleaseDependencies != nil}
		out = append(out, c)
	}
	for _, m := range e.ReleaseManifestConsumers {
		c := release.Consumer{Key: release.ConsumerKey{Cluster: m.Cluster, Workload: m.Name}, DependenciesDeclared: m.ReleaseDependencies != nil}
		for slot := range m.ReleaseInputs {
			c.Slots = append(c.Slots, slot)
		}
		for peer, contract := range m.ReleaseDependencies {
			c.DependsOn = append(c.DependsOn, release.ConsumerDependency{Consumer: release.ConsumerKey{Cluster: m.Cluster, Workload: peer}, Contract: contract})
		}
		out = append(out, c)
	}
	if e.OpenBao != nil {
		provider := e.OpenBao
		if provider.ReleaseOwner == "" || provider.Cluster == "" || len(provider.ReleaseInputs) == 0 {
			return nil, fmt.Errorf("native OpenBao provider lacks compiled snapshot image contract")
		}
		key := release.ConsumerKey{Cluster: provider.Cluster, Workload: provider.ReleaseOwner}
		for _, consumer := range out {
			if consumer.Key == key {
				return nil, fmt.Errorf("native OpenBao consumer %q collides with an authored consumer", key.Workload)
			}
		}
		if provider.ReleaseInputs["container:openbao"] == "" || provider.ReleaseInputs["container:converge"] == "" {
			return nil, fmt.Errorf("native OpenBao provider lacks required server or convergence image input")
		}
		consumer := release.Consumer{Key: key, DependenciesDeclared: true}
		for slot := range provider.ReleaseInputs {
			if slot != "container:openbao" && slot != "container:converge" && slot != "init:cloud-sql-proxy" {
				return nil, fmt.Errorf("native OpenBao provider declares unknown runtime slot %q", slot)
			}
			consumer.Slots = append(consumer.Slots, slot)
		}
		out = append(out, consumer)
	}
	for i := range out {
		for _, fe := range e.Frontends {
			if fe.Name != out[i].Key.Workload {
				continue
			}
			for peer, contract := range fe.ReleaseDependencies {
				keys := compositionDependencyKeys(out, peer)
				if len(keys) != 1 || contract == "" {
					return nil, fmt.Errorf("frontend %q has unknown dependency %q", fe.Name, peer)
				}
				out[i].DependsOn = append(out[i].DependsOn, release.ConsumerDependency{Consumer: keys[0], Contract: contract})
			}
		}
		for _, w := range e.Workloads {
			if w.Name != out[i].Key.Workload {
				continue
			}
			for peer, contract := range w.ReleaseDependencies {
				keys := compositionDependencyKeys(out, peer)
				if len(keys) != 1 || contract == "" {
					return nil, fmt.Errorf("consumer %q has unknown/ambiguous dependency %q", w.Name, peer)
				}
				out[i].DependsOn = append(out[i].DependsOn, release.ConsumerDependency{Consumer: keys[0], Contract: contract})
			}
		}
	}
	return out, nil
}

// snapshotFrontendCluster keys a frontend forge publishes itself (Firebase,
// bucket, build-only): its output is outside the bundle, so it has no sealed
// destination.
const snapshotFrontendCluster = "frontend"

// compositionFrontendKey is a frontend's consumer identity, and the one rule
// every composition reader keys frontends by. A hosted frontend's sealed
// record (its StaticSite) is in the hosted tree (hostedBundleObjects), so it
// is keyed there, as a hosted workload is; the snapshot inventory then finds
// that record's destination declared.
func compositionFrontendKey(fe FrontendEntity) release.ConsumerKey {
	if frontendIsHosted(fe) {
		return release.ConsumerKey{Cluster: release.BundleHostedCluster, Workload: fe.Name}
	}
	return release.ConsumerKey{Cluster: snapshotFrontendCluster, Workload: fe.Name}
}

// compositionDependencyKeys retains every matching runtime slot: callers must
// reject ambiguous workload names rather than silently select a placement.
func compositionDependencyKeys(consumers []release.Consumer, workload string) []release.ConsumerKey {
	var keys []release.ConsumerKey
	for _, c := range consumers {
		if c.Key.Workload == workload {
			keys = append(keys, c.Key)
		}
	}
	return keys
}

// resolveCompositionArtifact verifies the descriptor and platforms even for an
// already-digest-pinned external image. A registry outage is an error, never tag
// fallback or proof of absence. Receipts attest actual builder inputs separately.
func resolveCompositionArtifact(ctx context.Context, ref, version string, receipt *release.BuildReceipt, source *release.Source) (release.SnapshotArtifact, error) {
	digest, platforms, err := externalImageDigestResolver(ctx, ref)
	if err != nil {
		return release.SnapshotArtifact{}, fmt.Errorf("verify snapshot artifact %s: %w", ref, err)
	}
	if !release.ValidDigest(digest) || len(platforms) == 0 {
		return release.SnapshotArtifact{}, fmt.Errorf("snapshot artifact %s lacks immutable digest/platforms", ref)
	}
	if i := strings.Index(ref, "@"); i >= 0 && ref[i+1:] != digest {
		return release.SnapshotArtifact{}, fmt.Errorf("registry descriptor differs from pinned digest")
	}
	// A valid OCI reference may carry BOTH a tag and a digest. Normalize its
	// identity with the same canonical helper the build/deploy paths use.
	repo := imageRepository(ref)
	a := release.SnapshotArtifact{Kind: release.KindOCI, Location: repo, Content: digest, Platforms: platforms, Origin: release.ArtifactOrigin{Release: version, Source: source, Receipt: receipt}}
	if receipt == nil && source == nil {
		a.Origin.External = &release.ExternalArtifact{Verifier: "forge.registry-descriptor/v1", DescriptorDigest: digest}
	}
	if receipt != nil {
		matched := false
		for _, d := range receipt.Outputs {
			matched = matched || d == digest
		}
		if !matched {
			return a, fmt.Errorf("builder receipt does not attest resolved digest")
		}
	}
	return a, nil
}
