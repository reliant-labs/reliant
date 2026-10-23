package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReadSnapshotResources validates the archive with the existing shared ingest
// gate, then returns every desired object with its exact serialized bytes and
// delivery placement. It does not infer ownership; callers must declare it.
func ReadSnapshotResources(layer []byte) ([]SnapshotResource, error) {
	summary, err := ValidateManifestsLayer(bytes.NewReader(layer))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, manifestsLayerMaxStream+1))
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, e := io.ReadAll(io.LimitReader(tr, ManifestsLayerMaxFileBytes+1))
		if e != nil {
			return nil, e
		}
		files[h.Name] = b
	}
	var out []SnapshotResource
	for _, o := range summary.Objects {
		group := ""
		if p := strings.Split(o.APIVersion, "/"); len(p) == 2 {
			group = p[0]
		}
		clusterSegment := strings.Split(o.Path, "/")[1]
		// Summary.Cluster is the sanitized path segment, not the actual destination.
		// Callers map it to the declared destination; validation below does the same.
		phase := o.Phase
		if phase == "" {
			phase = "rollout"
		}
		orderText := strings.SplitN(path.Base(o.Path), "-", 2)[0]
		order, err := strconv.Atoi(orderText)
		if err != nil || order < 0 {
			return nil, snapshotBad("manifest filename does not preserve apply order")
		}
		out = append(out, SnapshotResource{Order: order, Key: ResourceKey{Cluster: clusterSegment, Group: group, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}, Spec: files[o.Path], Digest: SpecDigest(files[o.Path]), Tree: path.Dir(o.Path), Phase: phase})
	}
	return out, nil
}

// ReadSnapshotAssets validates and reads the non-applied layer inventory.
func ReadSnapshotAssets(layer []byte) ([]SnapshotAsset, error) {
	summary, err := ValidateManifestsLayer(bytes.NewReader(layer))
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, name := range summary.Artifacts {
		wanted[name] = true
	}
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, manifestsLayerMaxStream+1))
	var out []SnapshotAsset
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if !wanted[h.Name] {
			continue
		}
		b, e := io.ReadAll(io.LimitReader(tr, ManifestsLayerMaxFileBytes+1))
		if e != nil {
			return nil, e
		}
		out = append(out, SnapshotAsset{Path: h.Name, Data: b, Digest: SpecDigest(b)})
	}
	return out, nil
}

// ValidateSnapshotLayer proves exact config/layer agreement, including named
// runtime container slots. Registry callers must additionally verify OCI
// manifest/config/layer descriptor digests and admission/authorization.
func ValidateSnapshotLayer(s Snapshot, layer []byte) error {
	if err := s.Validate(); err != nil {
		return err
	}
	actual, err := ReadSnapshotResources(layer)
	if err != nil {
		return err
	}
	if len(actual) != len(s.Resources) {
		return snapshotBad("snapshot/layer resource count mismatch")
	}
	clusters := map[string]string{}
	for _, c := range s.Consumers {
		segment := strings.TrimPrefix(BundleClusterPath(c.Key.Cluster), BundleManifestsPrefix+"/")
		if old, ok := clusters[segment]; ok && old != c.Key.Cluster {
			return snapshotBad("ambiguous cluster path")
		}
		clusters[segment] = c.Key.Cluster
	}
	expected := map[ResourceKey]SnapshotResource{}
	for _, r := range s.Resources {
		expected[r.Key] = r
	}
	for _, r := range actual {
		cluster, ok := clusters[r.Key.Cluster]
		if !ok {
			return snapshotBad("layer contains undeclared destination")
		}
		r.Key.Cluster = cluster
		want, ok := expected[r.Key]
		if !ok || !bytes.Equal(r.Spec, want.Spec) || r.Tree != want.Tree || r.Phase != want.Phase || r.Order != want.Order {
			return snapshotBad("snapshot/layer bytes or placement disagree for %v", r.Key)
		}
		delete(expected, r.Key) // duplicate objects may not hide a missing one
	}
	assets, err := ReadSnapshotAssets(layer)
	if err != nil {
		return err
	}
	if len(assets) != len(s.Assets) {
		return snapshotBad("snapshot/layer asset count mismatch")
	}
	byPath := map[string]SnapshotAsset{}
	for _, a := range s.Assets {
		byPath[a.Path] = a
	}
	for _, a := range assets {
		want, ok := byPath[a.Path]
		if !ok || !bytes.Equal(want.Data, a.Data) || want.Digest != a.Digest {
			return snapshotBad("snapshot/layer asset bytes mismatch")
		}
	}
	return ValidateSnapshotBindings(s)
}

// ValidateSnapshotBindings checks all runtime container images against exact
// workload/slot identities. Every image-bearing resource needs known owners;
// no bare repository or container-list-position fallback is permitted.
func ValidateSnapshotBindings(s Snapshot) error {
	bindings := map[ConsumerKey]map[string]SnapshotArtifact{}
	for _, b := range s.Bindings {
		if bindings[b.Consumer] == nil {
			bindings[b.Consumer] = map[string]SnapshotArtifact{}
		}
		bindings[b.Consumer][b.Slot] = s.Artifacts[b.Artifact]
	}
	seen := map[ConsumerKey]map[string]bool{}
	for _, r := range s.Resources {
		var body map[string]any
		if err := yaml.Unmarshal(r.Spec, &body); err != nil {
			return snapshotBad("invalid resource bytes: %v", err)
		}
		meta, _ := body["metadata"].(map[string]any)
		api, _ := body["apiVersion"].(string)
		kind, _ := body["kind"].(string)
		name, _ := meta["name"].(string)
		ns, _ := meta["namespace"].(string)
		group := ""
		if p := strings.Split(api, "/"); len(p) == 2 {
			group = p[0]
		}
		if name != r.Key.Name || kind != r.Key.Kind || ns != r.Key.Namespace || group != r.Key.Group {
			return snapshotBad("resource key/body mismatch %v", r.Key)
		}
		spec, _ := body["spec"].(map[string]any)
		if api == "forge.dev/v1alpha1" && kind == "Workload" {
			// Managed Workload's primary image is spec.image, not a Pod
			// template. Its explicit sole owner names the primary container.
			// Hosted observation has one image measurement; do not infer
			// additional runtime slots even when their digests happen to agree.
			if len(r.Owners) != 1 || r.Owners[0].Cluster != r.Key.Cluster || r.Owners[0].Workload != name {
				return snapshotBad("managed Workload requires its exact sole consumer owner")
			}
			owner := r.Owners[0]
			slot := "container:" + owner.Workload
			runtimeSlots := 0
			for candidate := range bindings[owner] {
				if strings.HasPrefix(candidate, "container:") || strings.HasPrefix(candidate, "init:") {
					runtimeSlots++
				}
			}
			a, exists := bindings[owner][slot]
			image, _ := spec["image"].(string)
			if !exists || runtimeSlots != 1 || a.Kind != KindOCI || image != a.Location+"@"+a.Content {
				return snapshotBad("managed Workload image/primary binding mismatch for %v", owner)
			}
			for _, field := range []string{"sidecars", "initContainers", "containers"} {
				if value, present := spec[field]; present && value != nil {
					list, ok := value.([]any)
					if !ok || len(list) != 0 {
						return snapshotBad("managed Workload supports only its declared primary runtime slot")
					}
				}
			}
			if seen[owner] == nil {
				seen[owner] = map[string]bool{}
			}
			seen[owner][slot] = true
			continue
		}
		if kind == "CronJob" {
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
				container, ok := raw.(map[string]any)
				if !ok {
					return snapshotBad("invalid container")
				}
				name, _ := container["name"].(string)
				image, _ := container["image"].(string)
				slot := list.prefix + name
				matched := false
				for _, owner := range r.Owners {
					a, exists := bindings[owner][slot]
					if exists {
						if a.Kind != KindOCI || image != a.Location+"@"+a.Content {
							return snapshotBad("image/binding mismatch for %v/%s", owner, slot)
						}
						if seen[owner] == nil {
							seen[owner] = map[string]bool{}
						}
						seen[owner][slot] = true
						matched = true
					}
				}
				if !matched {
					return snapshotBad("undeclared runtime image slot %s in %v", slot, r.Key)
				}
			}
		}
	}
	for key, slots := range bindings {
		for slot := range slots {
			if (strings.HasPrefix(slot, "container:") || strings.HasPrefix(slot, "init:")) && !seen[key][slot] {
				return fmt.Errorf("%w: consumer %v slot %s has no runtime resource", ErrInvalid, key, slot)
			}
		}
	}
	return nil
}
