package release

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

func (e *ClosureError) Error() string {
	return fmt.Sprintf("%v: explicit dependency closure required: %v (%v)", ErrInvalid, e.Required, e.Reasons)
}
func (e *ClosureError) Unwrap() error { return ErrInvalid }
func bad(f string, args ...any) error {
	return fmt.Errorf("%w: composition: %s", ErrInvalid, fmt.Sprintf(f, args...))
}
func clone(s Snapshot) Snapshot {
	b, _ := json.Marshal(s)
	var out Snapshot
	_ = json.Unmarshal(b, &out)
	return out
}
func compositionSortedKeys(m map[ConsumerKey]bool) []ConsumerKey {
	out := make([]ConsumerKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cluster != out[j].Cluster {
			return out[i].Cluster < out[j].Cluster
		}
		return out[i].Workload < out[j].Workload
	})
	return out
}
func ownersSelected(r SnapshotResource, selected map[ConsumerKey]bool) bool {
	for _, o := range r.Owners {
		if selected[o] {
			return true
		}
	}
	return false
}
func sameResource(a, b SnapshotResource) bool {
	// Owners/dependency edges are semantic sets; specs remain original bytes.
	a = copyJSON(a)
	b = copyJSON(b)
	sortSet(a.Owners)
	sortSet(b.Owners)
	sortSet(a.DependsOn)
	sortSet(b.DependsOn)
	return reflect.DeepEqual(a, b)
}

// Compose validates scope/base integrity, overlays selected inventory, preserves
// unselected spec bytes and provenance, and checks the complete dependency graph.
// Admission and compatibility-report authority are verified by the caller; this
// function checks exact subjects/policy and never treats absence as permission.
func ComposeSnapshot(in CompositionInput) (Snapshot, error) {
	c := clone(in.Candidate)
	if err := c.Validate(); err != nil {
		return Snapshot{}, err
	}
	if err := ValidateSnapshotBindings(c); err != nil {
		return Snapshot{}, err
	}
	selected := map[ConsumerKey]bool{}
	known := map[ConsumerKey]bool{}
	for _, v := range c.Consumers {
		known[v.Key] = true
	}
	for _, k := range in.Selected {
		if !known[k] || selected[k] {
			return Snapshot{}, bad("unknown/duplicate selector %v", k)
		}
		selected[k] = true
	}
	if len(selected) == 0 {
		return Snapshot{}, bad("empty selection")
	}
	c.Selected = compositionSortedKeys(selected)
	c.Closure = compositionSortedKeys(selected)
	c.Base = in.BaseRef
	if in.Base == nil {
		if in.BaseRef != nil || len(selected) != len(c.Consumers) {
			return Snapshot{}, bad("partial composition needs a verified base; bootstrap must select all")
		}
		if err := c.Validate(); err != nil {
			return Snapshot{}, err
		}
		return c, nil
	}
	b := clone(*in.Base)
	if err := b.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("base: %w", err)
	}
	if err := ValidateSnapshotBindings(b); err != nil {
		return Snapshot{}, fmt.Errorf("base: %w", err)
	}
	if in.BaseRef == nil || in.BaseRef.PromotionID == "" || !ValidDigest(in.BaseRef.BundleDigest) {
		return Snapshot{}, bad("exact pre-build base promotion/bundle required")
	}
	digest, err := b.Digest()
	if err != nil {
		return Snapshot{}, err
	}
	if digest != in.BaseRef.SnapshotDigest {
		return Snapshot{}, bad("base digest mismatch")
	}
	if b.Project != c.Project || b.Environment != c.Environment {
		return Snapshot{}, bad("foreign project/environment base")
	}
	baseConsumers := map[ConsumerKey]Consumer{}
	for _, v := range b.Consumers {
		baseConsumers[v.Key] = v
	}
	// Removing a consumer requires an explicit deletion primitive, not omission.
	for k := range baseConsumers {
		if !known[k] {
			return Snapshot{}, bad("consumer deletion is not declared: %v", k)
		}
	}
	for k := range known {
		if _, ok := baseConsumers[k]; !ok && !selected[k] {
			return Snapshot{}, bad("new unselected consumer %v", k)
		}
	}
	partial := len(selected) != len(known)
	if partial {
		if b.Declaration == nil || c.Declaration == nil {
			return Snapshot{}, bad("partial composition requires complete recorded declaration")
		}
		baseDecl := canonicalSnapshot(b).Declaration
		candidateDecl := canonicalSnapshot(c).Declaration
		bd, _ := semanticDigest("forge.declaration/v1", baseDecl)
		cd, _ := semanticDigest("forge.declaration/v1", candidateDecl)
		if bd != cd || !reflect.DeepEqual(canonicalSnapshot(b).StatefulWorkloads, canonicalSnapshot(c).StatefulWorkloads) {
			return Snapshot{}, bad("shared shape declaration change requires full selection")
		}
		// An undeclared consumer may be a peer of any selected one. Unknown
		// compatibility conservatively requires the complete cohort.
		for _, s := range []Snapshot{b, c} {
			for _, consumer := range s.Consumers {
				if !consumer.DependenciesDeclared {
					return Snapshot{}, bad("consumer %v has undeclared compatibility; full selection required", consumer.Key)
				}
			}
		}
	}
	if partial && (!reflect.DeepEqual(b.DeliveryUnits, c.DeliveryUnits) || !reflect.DeepEqual(b.Assets, c.Assets) || !reflect.DeepEqual(b.SecretRefs, c.SecretRefs)) {
		return Snapshot{}, bad("shared delivery topology/assets/secret references change requires full selection")
	}
	if partial {
		for _, ref := range b.SecretRefs {
			if !ValidDigest(b.Prerequisites[SecretPrerequisiteKey(ref)]) {
				return Snapshot{}, bad("secret prerequisite %s has no verified immutable provider identity; full selection required", SecretPrerequisiteKey(ref))
			}
		}
	}
	if partial && !reflect.DeepEqual(b.Prerequisites, c.Prerequisites) {
		return Snapshot{}, bad("changed/unverifiable prerequisites require full selection")
	}
	if partial && b.PolicyDigest != c.PolicyDigest {
		return Snapshot{}, bad("policy change requires full selection")
	}
	out := c
	out.Consumers = nil
	out.Bindings = nil
	out.Resources = nil
	out.Artifacts = map[ArtifactID]SnapshotArtifact{}
	out.Decisions = nil
	for _, v := range c.Consumers {
		if selected[v.Key] {
			out.Consumers = append(out.Consumers, v)
		} else {
			out.Consumers = append(out.Consumers, baseConsumers[v.Key])
		}
	}
	for _, pair := range []struct {
		s    Snapshot
		want bool
	}{{b, false}, {c, true}} {
		for _, v := range pair.s.Bindings {
			if selected[v.Consumer] == pair.want {
				out.Bindings = append(out.Bindings, v)
				artifact := pair.s.Artifacts[v.Artifact]
				if original, exists := out.Artifacts[v.Artifact]; exists {
					oldOrigin, _ := semanticDigest("forge.origin/v1", original.Origin)
					newOrigin, _ := semanticDigest("forge.origin/v1", artifact.Origin)
					if oldOrigin != newOrigin {
						if pair.want {
							out.BuildEvents = appendBuildEvent(out.BuildEvents, ArtifactBuildEvent{Consumer: v.Consumer, Artifact: v.Artifact, Origin: artifact.Origin})
						}
						// One byte identity has one original catalog origin. A selected
						// reproducible rebuild is a separate event, not rewritten history.
						continue
					}
				}
				out.Artifacts[v.Artifact] = artifact
			}
		}
	}
	required := map[ConsumerKey]bool{}
	var reasons []string
	needOwners := func(r SnapshotResource, why string) {
		for _, o := range r.Owners {
			if !selected[o] {
				required[o] = true
			}
		}
		reasons = append(reasons, why)
	}
	br := map[ResourceKey]SnapshotResource{}
	cr := map[ResourceKey]SnapshotResource{}
	for _, r := range b.Resources {
		br[r.Key] = r
	}
	for _, r := range c.Resources {
		cr[r.Key] = r
	}
	for _, r := range b.Resources {
		next, exists := cr[r.Key]
		// Ownership cannot be dropped to bypass a shared-resource cohort.
		touched := ownersSelected(r, selected) || (exists && ownersSelected(next, selected))
		if !touched {
			out.Resources = append(out.Resources, r)
			continue
		}
		if !exists {
			return Snapshot{}, bad("resource deletion requires explicit reviewed deletion, missing %v", r.Key)
		}
		if !sameResource(r, next) {
			needOwners(r, fmt.Sprintf("shared resource %v", r.Key))
			needOwners(next, fmt.Sprintf("candidate resource %v", r.Key))
		}
		out.Resources = append(out.Resources, next)
	}
	for _, r := range c.Resources {
		if _, exists := br[r.Key]; !exists {
			if !ownersSelected(r, selected) {
				return Snapshot{}, bad("new unselected resource %v", r.Key)
			}
			needOwners(r, fmt.Sprintf("new shared resource %v", r.Key))
			out.Resources = append(out.Resources, r)
		}
	}
	// Resource edges carry transitive operational dependencies (config,
	// Kubernetes RBAC, migration and routes). A changed endpoint cannot silently affect a carried
	// owner's resource, even if the changed object is singly owned.
	for _, inventory := range []Snapshot{b, c} {
		for _, r := range inventory.Resources {
			for _, target := range r.DependsOn {
				before, had := br[target]
				after, has := cr[target]
				targetChanged := !had || !has || !sameResource(before, after)
				rBefore, rHad := br[r.Key]
				rAfter, rHas := cr[r.Key]
				sourceChanged := !rHad || !rHas || !sameResource(rBefore, rAfter)
				if (targetChanged || sourceChanged) && (ownersSelected(r, selected) || ownersSelected(before, selected) || ownersSelected(after, selected)) {
					needOwners(r, fmt.Sprintf("resource dependency %v -> %v", r.Key, target))
					if had {
						needOwners(before, fmt.Sprintf("dependency target %v", target))
					}
					if has {
						needOwners(after, fmt.Sprintf("dependency target %v", target))
					}
				}
			}
		}
	}
	for _, v := range c.Decisions {
		if selected[v.Consumer] {
			if v.Action == "carry" {
				return Snapshot{}, bad("selected candidate decision cannot carry")
			}
			if v.Action == "reuse" {
				for _, binding := range c.Bindings {
					if binding.Consumer == v.Consumer {
						receipt := c.Artifacts[binding.Artifact].Origin.Receipt
						if receipt == nil || !receipt.Inputs.Cacheable {
							return Snapshot{}, bad("automatic reuse requires declared cacheable effective build receipt for %v", v.Consumer)
						}
					}
				}
			}
			out.Decisions = append(out.Decisions, v)
		}
	}
	for _, k := range compositionSortedKeys(known) {
		if !selected[k] {
			out.Decisions = append(out.Decisions, CompositionDecision{Consumer: k, Action: "carry", Reason: "retain verified base bytes and original artifact provenance"})
		}
	}
	// A dependency is symmetric for safety: changing either endpoint while the
	// other is carried needs pair-specific compatibility evidence.
	edgeSeen := map[string]bool{}
	for _, s := range []Snapshot{b, c} {
		for _, v := range s.Consumers {
			for _, dep := range v.DependsOn {
				if selected[v.Key] == selected[dep.Consumer] {
					continue
				}
				key := fmt.Sprint(v.Key, dep.Consumer, dep.Contract)
				if edgeSeen[key] {
					continue
				}
				edgeSeen[key] = true
				if compatible(b, out, v.Key, dep.Consumer, dep.Contract) {
					continue
				}
				peer := dep.Consumer
				if !selected[v.Key] {
					peer = v.Key
				}
				required[peer] = true
				reasons = append(reasons, fmt.Sprintf("%v requires %v contract %s", v.Key, dep.Consumer, dep.Contract))
			}
		}
	}
	if len(required) > 0 {
		sort.Strings(reasons)
		return Snapshot{}, &ClosureError{Required: compositionSortedKeys(required), Reasons: reasons}
	}
	if err := out.Validate(); err != nil {
		return Snapshot{}, err
	}
	if err := ValidateSnapshotBindings(out); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

func appendBuildEvent(events []ArtifactBuildEvent, event ArtifactBuildEvent) []ArtifactBuildEvent {
	want, _ := semanticDigest("forge.build-event/v1", event)
	for _, old := range events {
		hash, _ := semanticDigest("forge.build-event/v1", old)
		if hash == want {
			return events
		}
	}
	return append(events, event)
}

func compatible(base, candidate Snapshot, a, b ConsumerKey, contract string) bool {
	ba, ea := ConsumerDigest(base, a)
	bb, eb := ConsumerDigest(base, b)
	ca, ec := ConsumerDigest(candidate, a)
	cb, ed := ConsumerDigest(candidate, b)
	if ea != nil || eb != nil || ec != nil || ed != nil {
		return false
	}
	for _, e := range candidate.Evidence {
		if e.Consumer == a && e.Peer == b && e.Contract == contract && e.BaseConsumer == ba && e.BasePeer == bb && e.CandidateConsumer == ca && e.CandidatePeer == cb && e.PolicyDigest == candidate.PolicyDigest && e.Validator != "" && ValidDigest(e.ReportDigest) {
			return true
		}
	}
	return false
}
