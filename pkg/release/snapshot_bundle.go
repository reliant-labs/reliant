package release

// validateSnapshotBundleLayout checks index/config inventory consistency before
// a reconciler can select an incomplete or different delivery tree.
func validateSnapshotBundleLayout(d BundleDoc) error {
	s := d.Snapshot
	if s.Declaration == nil {
		return snapshotBad("sealed bundle requires recorded declaration")
	}
	actualDeclaration := d.Shape.Canonical()
	actualDeclaration.Objects = nil
	want, _ := semanticDigest("forge.declaration/v1", s.Declaration.Canonical())
	actual, _ := semanticDigest("forge.declaration/v1", actualDeclaration.Canonical())
	if want != actual {
		return snapshotBad("bundle declaration differs from recorded snapshot")
	}
	projected, err := ProjectSnapshotShape(*s)
	if err != nil {
		return err
	}
	wantShape, _ := semanticDigest("forge.shape/v1", projected.Canonical())
	actualShape, _ := semanticDigest("forge.shape/v1", d.Shape.Canonical())
	if wantShape != actualShape {
		return snapshotBad("bundle planning projection differs from exact snapshot resources")
	}
	configDigest, err := ConfigDigest(projected)
	if err != nil {
		return err
	}
	if d.ConfigDigest != configDigest {
		return snapshotBad("bundle config digest differs from derived projection")
	}
	clusters, trees, err := SnapshotDelivery(*s)
	if err != nil {
		return err
	}
	wantDelivery, _ := semanticDigest("forge.delivery/v1", struct {
		Clusters []BundleClusterTree
		Trees    []BundleTree
	}{clusters, trees})
	actualClusters := append([]BundleClusterTree(nil), d.ClusterPaths...)
	sortSet(actualClusters)
	actualDelivery, _ := semanticDigest("forge.delivery/v1", struct {
		Clusters []BundleClusterTree
		Trees    []BundleTree
	}{actualClusters, d.Trees})
	if wantDelivery != actualDelivery {
		return snapshotBad("bundle delivery health/dependency metadata differs from exact snapshot")
	}
	clusterCounts := map[string]int{}
	treeCounts := map[string]int{}
	for _, r := range s.Resources {
		clusterCounts[r.Key.Cluster]++
		if !FlatCluster(r.Key.Cluster) {
			treeCounts[r.Tree]++
		}
	}
	if len(clusterCounts) != len(d.ClusterPaths) || len(treeCounts) != len(d.Trees) {
		return snapshotBad("bundle layout does not describe full snapshot inventory")
	}
	for _, c := range d.ClusterPaths {
		if clusterCounts[c.Cluster] != c.Documents {
			return snapshotBad("bundle cluster document count mismatch")
		}
	}
	for _, t := range d.Trees {
		if treeCounts[t.Path] != t.Documents {
			return snapshotBad("bundle tree document count mismatch")
		}
	}
	wantSecrets, _ := semanticDigest("forge.secret-refs/v1", canonicalSnapshot(Snapshot{SecretRefs: s.SecretRefs}).SecretRefs)
	actualSecrets, _ := semanticDigest("forge.secret-refs/v1", canonicalSnapshot(Snapshot{SecretRefs: d.Secrets}).SecretRefs)
	if wantSecrets != actualSecrets {
		return snapshotBad("bundle secret prerequisites disagree with snapshot")
	}
	return nil
}

// CheckSnapshotSeal validates all three immutable associations together. A
// ledger must enforce append-only uniqueness of (release,environment) and org
// scope transactionally after verifying registry descriptors/layer bytes.
func CheckSnapshotSeal(r Release, doc BundleDoc, seal SnapshotSeal) error {
	if err := seal.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	if doc.Snapshot == nil || r.Project != seal.Project || r.Version != seal.Release || doc.Project != seal.Project || doc.Release != seal.Release || doc.Env != seal.Environment || doc.SnapshotDigest != seal.SnapshotDigest || r.SnapshotIntents[seal.Environment] != seal.SnapshotDigest {
		return snapshotBad("cut/seal/bundle snapshot association mismatch")
	}
	if len(r.Catalog) != len(doc.Snapshot.Artifacts) {
		return snapshotBad("cut/seal artifact catalog mismatch")
	}
	// Catalog attestations are frozen; semantic map equality ignores only ordering
	// of verified platform/build input sets, just as Snapshot.Digest does.
	a := canonicalSnapshot(Snapshot{Artifacts: r.Catalog})
	b := canonicalSnapshot(Snapshot{Artifacts: doc.Snapshot.Artifacts})
	ad, _ := semanticDigest("forge.catalog/v1", a.Artifacts)
	bd, _ := semanticDigest("forge.catalog/v1", b.Artifacts)
	if ad != bd {
		return snapshotBad("cut/seal catalog provenance mismatch")
	}
	return nil
}
