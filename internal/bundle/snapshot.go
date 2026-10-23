package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/release"
)

// SnapshotInventory captures the final candidate resource bytes, after the
// normal safety transforms. Explicit owner annotations win; unannotated objects
// are conservatively owned by every consumer on the destination, never guessed
// independent from a coincidental object name.
func SnapshotInventory(ctx context.Context, in BuildInput, declaration release.Snapshot) (release.Snapshot, error) {
	// Inventory ownership/provenance must not alias a caller's candidate edits.
	encoded, err := json.Marshal(declaration)
	if err != nil {
		return release.Snapshot{}, err
	}
	var isolated release.Snapshot
	if err := json.Unmarshal(encoded, &isolated); err != nil {
		return release.Snapshot{}, err
	}
	declaration = isolated
	in.Snapshot = nil
	built, err := Build(ctx, in)
	if err != nil {
		return release.Snapshot{}, err
	}
	layer, ok := built.Layer(release.BundleManifestsLayer)
	if !ok {
		return release.Snapshot{}, fmt.Errorf("%w: missing manifest layer", release.ErrInvalid)
	}
	resources, err := release.ReadSnapshotResources(layer)
	if err != nil {
		return release.Snapshot{}, err
	}
	clusters := map[string]string{}
	owners := map[string][]release.ConsumerKey{}
	for _, c := range declaration.Consumers {
		seg := strings.TrimPrefix(release.BundleClusterPath(c.Key.Cluster), release.BundleManifestsPrefix+"/")
		if old, ok := clusters[seg]; ok && old != c.Key.Cluster {
			return release.Snapshot{}, fmt.Errorf("%w: ambiguous destination path", release.ErrInvalid)
		}
		clusters[seg] = c.Key.Cluster
		owners[c.Key.Cluster] = append(owners[c.Key.Cluster], c.Key)
	}
	for i := range resources {
		r := &resources[i]
		cluster, exists := clusters[r.Key.Cluster]
		if !exists {
			return release.Snapshot{}, fmt.Errorf("%w: undeclared resource cluster", release.ErrInvalid)
		}
		r.Key.Cluster = cluster
		var body map[string]any
		if err := yaml.Unmarshal(r.Spec, &body); err != nil {
			return release.Snapshot{}, err
		}
		meta, _ := body["metadata"].(map[string]any)
		ann, _ := meta["annotations"].(map[string]any)
		if raw, _ := ann["forge.dev/release-owners"].(string); raw != "" {
			var names []string
			if err := json.Unmarshal([]byte(raw), &names); err != nil {
				return release.Snapshot{}, fmt.Errorf("release-owners: %w", err)
			}
			for _, name := range names {
				r.Owners = append(r.Owners, release.ConsumerKey{Cluster: cluster, Workload: name})
			}
		} else {
			// These are existing explicit Forge workload ownership labels, not
			// inference from an object's name or a container list position.
			labels, _ := meta["labels"].(map[string]any)
			name, _ := labels["app.kubernetes.io/name"].(string)
			managed, _ := labels["app.kubernetes.io/managed-by"].(string)
			if explicit, _ := labels["forge.dev/workload"].(string); explicit != "" {
				name = explicit
				managed = "forge"
			}
			if managed == "forge" {
				for _, owner := range owners[cluster] {
					if owner.Workload == name {
						r.Owners = append(r.Owners, owner)
					}
				}
			}
			if len(r.Owners) == 0 {
				r.Owners = append([]release.ConsumerKey(nil), owners[cluster]...)
			}
		}
		if raw, _ := ann["forge.dev/release-depends-on"].(string); raw != "" {
			if err := json.Unmarshal([]byte(raw), &r.DependsOn); err != nil {
				return release.Snapshot{}, fmt.Errorf("release-depends-on: %w", err)
			}
		}
	}
	declaration.Resources = resources
	shapeDeclaration := built.Doc.Shape.Canonical()
	shapeDeclaration.Objects = nil
	declaration.Declaration = &shapeDeclaration
	declaration.StatefulWorkloads = append([]string(nil), in.Shape.StatefulWorkloads...)
	declaration.DeliveryUnits = in.Units
	declaration.SecretRefs = built.Doc.Secrets
	declaration.Assets = nil
	for _, f := range in.Observability {
		declaration.Assets = append(declaration.Assets, release.SnapshotAsset{Path: f.Path, Data: f.Data, Digest: release.SpecDigest(f.Data)})
	}
	if err := declaration.Validate(); err != nil {
		return release.Snapshot{}, err
	}
	if err := release.ValidateSnapshotLayer(declaration, layer); err != nil {
		return release.Snapshot{}, err
	}
	return declaration, nil
}

// SnapshotStream reconstructs an annotated apply stream without changing a
// single object's serialized bytes. It is not a fresh render from source.
func SnapshotStream(s release.Snapshot) string {
	resources := append([]release.SnapshotResource(nil), s.Resources...)
	sort.Slice(resources, func(i, j int) bool {
		a, b := resources[i], resources[j]
		if a.Tree != b.Tree {
			return a.Tree < b.Tree
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return fmt.Sprint(a.Key) < fmt.Sprint(b.Key)
	})
	var out strings.Builder
	for _, r := range resources {
		fmt.Fprintf(&out, "# cluster: %s\n", r.Key.Cluster)
		out.Write(r.Spec)
		if !bytes.HasSuffix(r.Spec, []byte("\n")) {
			out.WriteByte('\n')
		}
		out.WriteString("---\n")
	}
	return out.String()
}

func packSnapshot(s release.Snapshot, docs []parsedDoc, units []release.DeliveryUnit, extra []tarEntry) ([]byte, []release.BundleClusterTree, []release.BundleTree, error) {
	// Derive delivery topology/deadlines from the same parser as normal bundles,
	// but replace serialization with the approved resource bytes verbatim.
	clusters, trees, err := release.SnapshotDelivery(s)
	if err != nil {
		return nil, nil, nil, err
	}
	var files []tarEntry
	for _, r := range s.Resources {
		// Exact tree placement is part of semantic identity, never recomputed from
		// a changed checkout. Filenames are packaging only, not pod-template bytes.
		name := fmt.Sprintf("%06d-%s-%s.yaml", r.Order, release.BundlePathSegment(strings.ToLower(r.Key.Kind)), release.BundlePathSegment(r.Key.Name))
		files = append(files, tarEntry{name: path.Join(r.Tree, name), data: r.Spec})
	}
	files = append(files, extra...)
	layer, err := writeTarGz(files)
	return layer, clusters, trees, err
}
