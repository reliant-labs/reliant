package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func semanticDigest(domain string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte(domain+"\x00"), b...))
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

// SecretPrerequisiteKey names an immutable provider-version identity in
// Snapshot.Prerequisites. The identity is never a hash of the secret value.
func SecretPrerequisiteKey(ref BundleSecretRef) string {
	b, _ := json.Marshal(ref)
	return "secret:" + string(b)
}

// SpecDigest addresses the exact original serialized bytes, not canonical YAML.
func SpecDigest(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }

// ArtifactIdentity excludes attestations but includes the immutable location.
func ArtifactIdentity(a SnapshotArtifact) (ArtifactID, error) {
	if !a.Kind.Valid() || strings.TrimSpace(a.Location) == "" || strings.TrimSpace(a.Content) == "" {
		return "", fmt.Errorf("%w: artifact kind, location and content required", ErrInvalid)
	}
	switch a.Kind {
	case KindOCI:
		if !ValidDigest(a.Content) || strings.Contains(a.Location, "@") || strings.Contains(strings.Split(a.Location, "/")[len(strings.Split(a.Location, "/"))-1], ":") {
			return "", fmt.Errorf("%w: OCI artifact requires untagged repository and immutable digest", ErrInvalid)
		}
	case KindGit:
		if !ValidObjectID(a.Content) {
			return "", fmt.Errorf("%w: git artifact requires resolved commit", ErrInvalid)
		}
	default:
		if !strings.HasPrefix(a.Content, "sha") && !strings.HasPrefix(a.Content, "h1:") {
			return "", fmt.Errorf("%w: artifact requires native integrity hash", ErrInvalid)
		}
	}
	d, e := semanticDigest("forge.artifact/v1", struct {
		Kind     Kind   `json:"kind"`
		Location string `json:"location"`
		Content  string `json:"content"`
	}{a.Kind, a.Location, a.Content})
	return ArtifactID(d), e
}

func sortSet[T any](xs []T) {
	sort.Slice(xs, func(i, j int) bool {
		a, _ := json.Marshal(xs[i])
		b, _ := json.Marshal(xs[j])
		return string(a) < string(b)
	})
}
func copyJSON[T any](v T) T { b, _ := json.Marshal(v); var c T; _ = json.Unmarshal(b, &c); return c }

// BuildInputDigest names an effective recipe/output contract, not a workload.
func BuildInputDigest(in BuildInputs) (string, error) {
	if in.Recipe == "" || !ValidDigest(in.ContextDigest) || !ValidDigest(in.RecipeDigest) || !ValidDigest(in.ConfigDigest) || len(in.Sources) == 0 || len(in.Tools) == 0 || len(in.Platforms) == 0 || len(in.Outputs) == 0 {
		return "", fmt.Errorf("%w: incomplete build inputs", ErrInvalid)
	}
	for _, s := range in.Sources {
		if s.Repo == "" || !ValidObjectID(s.Commit) {
			return "", fmt.Errorf("%w: unresolved build source", ErrInvalid)
		}
	}
	for name, d := range in.Tools {
		if name == "" || !ValidDigest(d) {
			return "", fmt.Errorf("%w: tool identity required", ErrInvalid)
		}
	}
	for name, d := range in.External {
		if name == "" || !ValidDigest(d) {
			return "", fmt.Errorf("%w: external input identity required", ErrInvalid)
		}
	}
	for _, values := range [][]string{in.Platforms, in.Outputs} {
		seen := map[string]bool{}
		for _, v := range values {
			if v == "" || seen[v] {
				return "", fmt.Errorf("%w: empty/duplicate build contract member", ErrInvalid)
			}
			seen[v] = true
		}
	}
	in = copyJSON(in)
	sortSet(in.Sources)
	sort.Strings(in.Platforms)
	sort.Strings(in.Outputs)
	return semanticDigest("forge.build-inputs/v1", in)
}

func canonicalSnapshot(s Snapshot) Snapshot {
	s = copyJSON(s)
	if s.Declaration != nil {
		canonical := s.Declaration.Canonical()
		s.Declaration = &canonical
	}
	sort.Strings(s.StatefulWorkloads)
	for i := range s.DeliveryUnits {
		sort.Strings(s.DeliveryUnits[i].After)
	}
	sortSet(s.DeliveryUnits)
	sortSet(s.Assets)
	sortSet(s.SecretRefs)
	sortSet(s.Selected)
	sortSet(s.Closure)
	for id, a := range s.Artifacts {
		sort.Strings(a.Platforms)
		if a.Origin.Receipt != nil {
			sortSet(a.Origin.Receipt.Inputs.Sources)
			sort.Strings(a.Origin.Receipt.Inputs.Platforms)
			sort.Strings(a.Origin.Receipt.Inputs.Outputs)
		}
		s.Artifacts[id] = a
	}
	for i := range s.Consumers {
		sort.Strings(s.Consumers[i].Slots)
		sortSet(s.Consumers[i].DependsOn)
	}
	sortSet(s.Consumers)
	sortSet(s.Bindings)
	for i := range s.Resources {
		sortSet(s.Resources[i].Owners)
		sortSet(s.Resources[i].DependsOn)
	}
	sortSet(s.Resources)
	sortSet(s.BuildEvents)
	sortSet(s.Evidence)
	sortSet(s.Decisions)
	return s
}

// Digest validates and hashes complete semantic state. Clock/actor/labels cannot
// affect it because they are not fields in the snapshot vocabulary.
func (s Snapshot) Digest() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return semanticDigest("forge.snapshot/v1", canonicalSnapshot(s))
}

// ConsumerDigest binds compatibility evidence to exact declarations, artifacts
// (including original provenance), slots and all resources this consumer owns.
// It excludes snapshot lineage and evidence, avoiding a hash cycle.
func ConsumerDigest(s Snapshot, key ConsumerKey) (string, error) {
	var c *Consumer
	for i := range s.Consumers {
		if s.Consumers[i].Key == key {
			c = &s.Consumers[i]
			break
		}
	}
	if c == nil {
		return "", fmt.Errorf("%w: unknown consumer %v", ErrInvalid, key)
	}
	subset := Snapshot{Consumers: []Consumer{*c}, Artifacts: map[ArtifactID]SnapshotArtifact{}}
	for _, b := range s.Bindings {
		if b.Consumer == key {
			subset.Bindings = append(subset.Bindings, b)
			subset.Artifacts[b.Artifact] = s.Artifacts[b.Artifact]
		}
	}
	for _, r := range s.Resources {
		for _, owner := range r.Owners {
			if owner == key {
				subset.Resources = append(subset.Resources, r)
				break
			}
		}
	}
	return semanticDigest("forge.consumer/v1", canonicalSnapshot(subset))
}

// Validate enforces complete immutable seal addressing. Matching to a cut and
// verified bundle and checking org scope happens inside the ledger transaction.
func (s SnapshotSeal) Validate() error {
	if s.Release == "" || s.Project == "" || s.Environment == "" || !ValidDigest(s.SnapshotDigest) || !ValidDigest(s.BundleDigest) {
		return fmt.Errorf("%w: incomplete snapshot seal", ErrInvalid)
	}
	return nil
}
