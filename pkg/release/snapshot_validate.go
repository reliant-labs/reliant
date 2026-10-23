package release

import (
	"fmt"
	"path"
	"strings"
)

func snapshotBad(f string, args ...any) error {
	return fmt.Errorf("%w: snapshot: %s", ErrInvalid, fmt.Sprintf(f, args...))
}
func validSlot(s string) bool {
	if s == "frontend" || s == "build-only" {
		return true
	}
	for _, p := range []string{"container:", "init:", "input:"} {
		if strings.HasPrefix(s, p) {
			name := strings.TrimPrefix(s, p)
			return name != "" && !strings.ContainsAny(name, "/: \t\n")
		}
	}
	return false
}

// Validate rejects incomplete inventories and ambiguous ownership. It never
// guesses an absent binding from a repository name or live mutable tag.
func (s Snapshot) Validate() error {
	if s.Schema != SnapshotSchema || s.Project == "" || s.Environment == "" || !ValidDigest(s.PolicyDigest) {
		return snapshotBad("schema, project, environment and policy digest required")
	}
	if s.Declaration != nil {
		if len(s.Declaration.Objects) != 0 {
			return snapshotBad("declaration must not duplicate resource object inventory")
		}
		if err := s.Declaration.Validate(); err != nil {
			return err
		}
	}
	if s.Source.Repo == "" || !ValidObjectID(s.Source.Commit) {
		return snapshotBad("resolved composition source required")
	}
	if len(s.Consumers) == 0 || len(s.Consumers) > 10000 || len(s.Artifacts) == 0 || len(s.Artifacts) > 10000 || len(s.Resources) > 50000 {
		return snapshotBad("empty or oversized inventory")
	}
	if s.Base != nil && (s.Base.PromotionID == "" || !ValidDigest(s.Base.SnapshotDigest) || !ValidDigest(s.Base.BundleDigest)) {
		return snapshotBad("incomplete base reference")
	}
	consumers := map[ConsumerKey]Consumer{}
	slots := map[ConsumerKey]map[string]bool{}
	for _, c := range s.Consumers {
		if c.Key.Workload == "" {
			return snapshotBad("empty consumer workload")
		}
		if _, ok := consumers[c.Key]; ok {
			return snapshotBad("duplicate consumer %v", c.Key)
		}
		consumers[c.Key] = c
		slots[c.Key] = map[string]bool{}
		if len(c.Slots) == 0 {
			return snapshotBad("consumer %v has no slots", c.Key)
		}
		for _, slot := range c.Slots {
			if !validSlot(slot) || slots[c.Key][slot] {
				return snapshotBad("invalid/duplicate slot %v/%s", c.Key, slot)
			}
			slots[c.Key][slot] = true
		}
	}
	for _, c := range s.Consumers {
		seen := map[ConsumerKey]bool{}
		for _, d := range c.DependsOn {
			if _, ok := consumers[d.Consumer]; !ok || d.Consumer == c.Key || d.Contract == "" || seen[d.Consumer] {
				return snapshotBad("invalid peer dependency of %v", c.Key)
			}
			seen[d.Consumer] = true
		}
	}
	validateKeys := func(keys []ConsumerKey, name string) (map[ConsumerKey]bool, error) {
		out := map[ConsumerKey]bool{}
		for _, k := range keys {
			if _, ok := consumers[k]; !ok || out[k] {
				return nil, snapshotBad("unknown/duplicate %s consumer %v", name, k)
			}
			out[k] = true
		}
		return out, nil
	}
	selected, e := validateKeys(s.Selected, "selected")
	if e != nil {
		return e
	}
	closure, e := validateKeys(s.Closure, "closure")
	if e != nil {
		return e
	}
	if len(selected) == 0 {
		return snapshotBad("empty selection")
	}
	for k := range selected {
		if !closure[k] {
			return snapshotBad("selected consumer missing from approved closure")
		}
	}
	if s.BootstrapFromPromotionID != "" && (s.Base != nil || len(selected) != len(consumers) || len(closure) != len(consumers)) {
		return snapshotBad("legacy adoption requires full selection/closure and no snapshot base")
	}
	if s.Base == nil && len(selected) != len(consumers) {
		return snapshotBad("bootstrap must select every consumer")
	}
	for id, a := range s.Artifacts {
		actual, e := ArtifactIdentity(a)
		if e != nil {
			return e
		}
		if actual != id {
			return snapshotBad("artifact ID mismatch %s", id)
		}
		if a.Origin.Release == "" {
			return snapshotBad("artifact %s lacks origin release", id)
		}
		if a.Kind == KindOCI && len(a.Platforms) == 0 {
			return snapshotBad("artifact %s lacks verified platforms", id)
		}
		seen := map[string]bool{}
		for _, p := range a.Platforms {
			if len(strings.Split(p, "/")) < 2 || seen[p] {
				return snapshotBad("invalid/duplicate platform %s", p)
			}
			seen[p] = true
		}
		if a.Kind == KindGit {
			if a.Origin.Source == nil || a.Origin.Source.Repo != a.Location || a.Origin.Source.Commit != a.Content {
				return snapshotBad("git origin must match immutable source")
			}
		} else if a.Origin.Source == nil && a.Origin.Receipt == nil && a.Origin.External == nil {
			return snapshotBad("artifact %s lacks build provenance or explicit external verification", id)
		}
		if external := a.Origin.External; external != nil {
			if a.Kind != KindOCI || external.Verifier == "" || external.DescriptorDigest != a.Content || a.Origin.Source != nil || a.Origin.Receipt != nil {
				return snapshotBad("invalid/mixed external registry origin")
			}
		}
		if a.Origin.Source != nil && (a.Origin.Source.Repo == "" || !ValidObjectID(a.Origin.Source.Commit)) {
			return snapshotBad("unresolved artifact origin")
		}
		if r := a.Origin.Receipt; r != nil {
			if err := r.Validate(); err != nil {
				return err
			}
			d, e := BuildInputDigest(r.Inputs)
			if e != nil {
				return e
			}
			if d != r.InputDigest {
				return snapshotBad("receipt input mismatch")
			}
			matched := false
			for name, out := range r.Outputs {
				if name == "" || !ValidDigest(out) {
					return snapshotBad("invalid receipt output")
				}
				matched = matched || out == a.Content
			}
			if !matched {
				return snapshotBad("receipt does not attest artifact output")
			}
		}
	}
	bound := map[ConsumerKey]map[string]bool{}
	for _, b := range s.Bindings {
		if !slots[b.Consumer][b.Slot] {
			return snapshotBad("unknown consumer/slot %v/%s", b.Consumer, b.Slot)
		}
		if bound[b.Consumer] == nil {
			bound[b.Consumer] = map[string]bool{}
		}
		if bound[b.Consumer][b.Slot] {
			return snapshotBad("duplicate binding %v/%s", b.Consumer, b.Slot)
		}
		bound[b.Consumer][b.Slot] = true
		if _, ok := s.Artifacts[b.Artifact]; !ok {
			return snapshotBad("missing artifact %s", b.Artifact)
		}
	}
	for key, ss := range slots {
		for slot := range ss {
			if !bound[key][slot] {
				return snapshotBad("missing binding %v/%s", key, slot)
			}
		}
	}
	resources := map[ResourceKey]bool{}
	total := 0
	for _, r := range s.Resources {
		if resources[r.Key] || r.Key.Kind == "" || r.Key.Name == "" || r.Key.Cluster == "" {
			return snapshotBad("invalid/duplicate resource key %v", r.Key)
		}
		resources[r.Key] = true
		if r.Order < 0 {
			return snapshotBad("negative resource apply order")
		}
		if r.Key.Kind == "Secret" {
			return snapshotBad("Secret values are forbidden; use immutable prerequisites")
		}
		total += len(r.Spec)
		if len(r.Spec) == 0 || len(r.Spec) > 4<<20 || total > 32<<20 || SpecDigest(r.Spec) != r.Digest {
			return snapshotBad("resource %v empty, oversized or digest mismatch", r.Key)
		}
		prefix := BundleClusterPath(r.Key.Cluster)
		if r.Phase != "pre" && r.Phase != "rollout" && r.Phase != "post" {
			return snapshotBad("unknown resource delivery phase")
		}
		if r.Tree != prefix && (!strings.HasPrefix(r.Tree, prefix+"/") || len(strings.Split(strings.TrimPrefix(r.Tree, prefix+"/"), "/")) != 2 || !strings.HasSuffix(r.Tree, "/"+r.Phase)) {
			return snapshotBad("resource destination/tree disagreement")
		}
		if r.Tree == "" || path.Clean(r.Tree) != r.Tree || strings.HasPrefix(r.Tree, "/") || strings.Contains(r.Tree, "..") || r.Phase == "" {
			return snapshotBad("invalid delivery tree/phase")
		}
		if len(r.Owners) == 0 {
			return snapshotBad("resource %v has no declared owner", r.Key)
		}
		owners := map[ConsumerKey]bool{}
		for _, owner := range r.Owners {
			if _, ok := consumers[owner]; !ok || owners[owner] || owner.Cluster != r.Key.Cluster {
				return snapshotBad("invalid resource owner %v", owner)
			}
			owners[owner] = true
		}
	}
	for _, r := range s.Resources {
		seen := map[ResourceKey]bool{}
		for _, d := range r.DependsOn {
			if !resources[d] || seen[d] || d == r.Key {
				return snapshotBad("missing/duplicate/self resource dependency")
			}
			seen[d] = true
		}
	}
	// Cycles cannot be ordered by delivery engines; refuse before packaging.
	visiting := map[ResourceKey]bool{}
	done := map[ResourceKey]bool{}
	edges := map[ResourceKey][]ResourceKey{}
	for _, r := range s.Resources {
		edges[r.Key] = r.DependsOn
	}
	var visit func(ResourceKey) error
	visit = func(k ResourceKey) error {
		if visiting[k] {
			return snapshotBad("resource dependency cycle")
		}
		if done[k] {
			return nil
		}
		visiting[k] = true
		for _, d := range edges[k] {
			if e := visit(d); e != nil {
				return e
			}
		}
		delete(visiting, k)
		done[k] = true
		return nil
	}
	for k := range resources {
		if e := visit(k); e != nil {
			return e
		}
	}
	assetPaths := map[string]bool{}
	assetBytes := 0
	for _, a := range s.Assets {
		if (a.Path != BundleFrontendDeclarationsPath && !strings.HasPrefix(a.Path, BundleObservabilityPrefix+"/")) || path.Clean(a.Path) != a.Path || strings.Contains(a.Path, "..") || assetPaths[a.Path] || SpecDigest(a.Data) != a.Digest {
			return snapshotBad("invalid/duplicate snapshot asset")
		}
		assetPaths[a.Path] = true
		assetBytes += len(a.Data)
		if len(a.Data) > 1<<20 || assetBytes > 4<<20 {
			return snapshotBad("oversized snapshot assets")
		}
	}
	seenSecrets := map[BundleSecretRef]bool{}
	for _, ref := range s.SecretRefs {
		if ref.Name == "" || ref.Cluster == "" || seenSecrets[ref] {
			return snapshotBad("invalid/duplicate secret prerequisite reference")
		}
		seenSecrets[ref] = true
	}
	for name, d := range s.Prerequisites {
		if name == "" || !ValidDigest(d) {
			return snapshotBad("unfrozen prerequisite %q", name)
		}
	}
	decisions := map[ConsumerKey]bool{}
	for _, d := range s.Decisions {
		if _, ok := consumers[d.Consumer]; !ok || decisions[d.Consumer] || d.Reason == "" {
			return snapshotBad("invalid/duplicate decision")
		}
		if d.Action != "build" && d.Action != "reuse" && d.Action != "carry" {
			return snapshotBad("unknown decision action")
		}
		if d.Action == "carry" && selected[d.Consumer] {
			return snapshotBad("selected consumer cannot be carry")
		}
		if d.Action != "carry" && !selected[d.Consumer] {
			return snapshotBad("unselected consumer must be carry")
		}
		decisions[d.Consumer] = true
	}
	if len(decisions) != len(consumers) {
		return snapshotBad("one decision per consumer required")
	}
	seenEvents := map[string]bool{}
	for _, event := range s.BuildEvents {
		artifact, exists := s.Artifacts[event.Artifact]
		if !exists || !selected[event.Consumer] {
			return snapshotBad("build event requires selected consumer and catalog artifact")
		}
		bound := false
		for _, binding := range s.Bindings {
			bound = bound || (binding.Consumer == event.Consumer && binding.Artifact == event.Artifact)
		}
		if !bound {
			return snapshotBad("build event artifact not bound to consumer")
		}
		artifact.Origin = event.Origin
		// Reuse the same provenance validator over an isolated one-artifact
		// manifest; events themselves are excluded to avoid recursion.
		probe := s
		probe.BuildEvents = nil
		probe.Artifacts = map[ArtifactID]SnapshotArtifact{}
		for id, a := range s.Artifacts {
			probe.Artifacts[id] = a
		}
		probe.Artifacts[event.Artifact] = artifact
		if err := probe.Validate(); err != nil {
			return err
		}
		key, _ := semanticDigest("forge.build-event/v1", event)
		if seenEvents[key] {
			return snapshotBad("duplicate build event")
		}
		seenEvents[key] = true
	}
	for _, e := range s.Evidence {
		if _, ok := consumers[e.Consumer]; !ok {
			return snapshotBad("unknown evidence consumer")
		}
		if _, ok := consumers[e.Peer]; !ok {
			return snapshotBad("unknown evidence peer")
		}
		if e.Contract == "" || e.Validator == "" || e.PolicyDigest != s.PolicyDigest || !ValidDigest(e.ReportDigest) || !ValidDigest(e.BaseConsumer) || !ValidDigest(e.CandidateConsumer) || !ValidDigest(e.BasePeer) || !ValidDigest(e.CandidatePeer) {
			return snapshotBad("incomplete compatibility evidence")
		}
	}
	return nil
}
