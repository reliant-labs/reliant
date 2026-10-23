package releasecompose

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func dg(c string) string { return "sha256:" + strings.Repeat(c, 64) }
func fixture(t *testing.T) release.Snapshot {
	t.Helper()
	source := release.Source{Repo: "github.com/acme/app", Ref: "main", Commit: strings.Repeat("a", 40)}
	a := release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.test/reliant", Content: dg("a"), Platforms: []string{"linux/amd64"}, Origin: release.ArtifactOrigin{Release: "v1", Source: &source}}
	id, err := release.ArtifactIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	s := release.Snapshot{Declaration: &release.Shape{Kind: release.EnvSelfManaged}, Schema: release.SnapshotSchema, Project: "app", Environment: "prod", Source: source, PolicyDigest: dg("b"), Artifacts: map[release.ArtifactID]release.SnapshotArtifact{id: a}}
	for _, name := range []string{"api", "worker", "workspace-base", "pre-pull"} {
		key := release.ConsumerKey{Cluster: "cluster", Workload: name}
		slot := "container:" + name
		if name == "workspace-base" {
			slot = "build-only"
		}
		if name == "pre-pull" {
			slot = "input:workspace"
		}
		s.Consumers = append(s.Consumers, release.Consumer{DependenciesDeclared: true, Key: key, Slots: []string{slot}})
		s.Selected = append(s.Selected, key)
		s.Closure = append(s.Closure, key)
		s.Bindings = append(s.Bindings, release.ConsumerBinding{Consumer: key, Slot: slot, Artifact: id})
		s.Decisions = append(s.Decisions, release.CompositionDecision{Consumer: key, Action: "build", Reason: "bootstrap"})
		if strings.HasPrefix(slot, "container:") {
			spec := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: " + name + "\n  namespace: app\nspec:\n  template:\n    metadata:\n      annotations:\n        checksum/config: old-checksum\n    spec:\n      containers:\n        - name: " + name + "\n          image: registry.test/reliant@" + dg("a") + "\n")
			s.Resources = append(s.Resources, release.SnapshotResource{Key: release.ResourceKey{Cluster: "cluster", Group: "apps", Kind: "Deployment", Namespace: "app", Name: name}, Spec: spec, Digest: release.SpecDigest(spec), Tree: "manifests/cluster/base/rollout", Phase: "rollout", Owners: []release.ConsumerKey{key}})
		}
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	return s
}
func candidate(t *testing.T, base release.Snapshot) release.Snapshot {
	c := clone(base)
	old := c.Bindings[1].Artifact
	a := c.Artifacts[old]
	a.Content = dg("c")
	a.Origin.Release = "v2"
	id, e := release.ArtifactIdentity(a)
	if e != nil {
		t.Fatal(e)
	}
	c.Artifacts[id] = a
	c.Bindings[1].Artifact = id
	c.Resources[1].Spec = bytes.ReplaceAll(c.Resources[1].Spec, []byte(dg("a")), []byte(dg("c")))
	c.Resources[1].Digest = release.SpecDigest(c.Resources[1].Spec)
	return c
}
func input(t *testing.T, b, c release.Snapshot, selected ...release.ConsumerKey) Input {
	t.Helper()
	d, e := b.Digest()
	if e != nil {
		t.Fatal(e)
	}
	return Input{Base: &b, Candidate: c, BaseRef: &release.SnapshotBase{PromotionID: "promotion-1", SnapshotDigest: d, BundleDigest: dg("d")}, Selected: selected}
}

func TestWorkerOnlyPreservesFullBase(t *testing.T) {
	b := fixture(t)
	c := candidate(t, b)
	worker := b.Consumers[1].Key
	// The checkout also changes API resources. They must NOT leak into result.
	c.Resources[0].Spec = bytes.ReplaceAll(c.Resources[0].Spec, []byte("old-checksum"), []byte("new-checksum"))
	c.Resources[0].Digest = release.SpecDigest(c.Resources[0].Spec)
	out, e := Compose(input(t, b, c, worker))
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Bindings) != 4 || len(out.Artifacts) != 2 {
		t.Fatalf("incomplete result: %+v", out)
	}
	if !bytes.Equal(out.Resources[0].Spec, b.Resources[0].Spec) {
		t.Fatal("carried API bytes changed")
	}
	if bytes.Equal(out.Resources[1].Spec, b.Resources[1].Spec) {
		t.Fatal("worker did not change")
	}
	for _, name := range []string{"api", "workspace-base", "pre-pull"} {
		for _, binding := range out.Bindings {
			if binding.Consumer.Workload == name {
				a := out.Artifacts[binding.Artifact]
				if a.Content != dg("a") || a.Origin.Release != "v1" {
					t.Fatal("carried origin lost")
				}
			}
		}
	}
	if err := release.ValidateSnapshotBindings(out); err != nil {
		t.Fatal(err)
	}
}

func TestComposeFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Input)
	}{
		{"missing base", func(in *Input) { in.Base = nil; in.BaseRef = nil }},
		{"unknown compatibility", func(in *Input) { in.Candidate.Consumers[0].DependenciesDeclared = false }},
		{"unsafe reuse", func(in *Input) { in.Candidate.Decisions[1].Action = "reuse" }},
		{"wrong base digest", func(in *Input) { in.BaseRef.SnapshotDigest = dg("f") }},
		{"foreign base", func(in *Input) { in.Candidate.Environment = "other" }},
		{"unknown selector", func(in *Input) { in.Selected = []release.ConsumerKey{{Cluster: "cluster", Workload: "unknown"}} }},
		{"deletion", func(in *Input) { in.Candidate.Resources = in.Candidate.Resources[:1] }},
		{"mutable prerequisite", func(in *Input) { in.Candidate.Prerequisites = map[string]string{"secret": "latest"} }},
		{"changed prerequisite", func(in *Input) { in.Candidate.Prerequisites = map[string]string{"secret": dg("e")} }},
		{"shared config", func(in *Input) {
			r := &in.Candidate.Resources[1]
			r.Owners = append(r.Owners, in.Candidate.Consumers[0].Key)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := fixture(t)
			c := candidate(t, b)
			in := input(t, b, c, b.Consumers[1].Key)
			test.mutate(&in)
			if _, e := Compose(in); !errors.Is(e, release.ErrInvalid) {
				t.Fatalf("expected refusal, got %v", e)
			}
		})
	}
}

func TestConservativeCohortAndExactEvidence(t *testing.T) {
	b := fixture(t)
	b.Consumers[1].DependsOn = []release.ConsumerDependency{{Consumer: b.Consumers[0].Key, Contract: "temporal-api/v1"}}
	c := candidate(t, b)
	in := input(t, b, c, b.Consumers[1].Key)
	_, e := Compose(in)
	var closure *ClosureError
	if !errors.As(e, &closure) || len(closure.Required) != 1 || closure.Required[0] != b.Consumers[0].Key {
		t.Fatalf("missing peer cohort: %v", e)
	}
	// Report authenticity is an admission concern. Here exact subjects bind it
	// to this old/new pair and ensure a different pair cannot reuse it.
	ba, _ := release.ConsumerDigest(b, b.Consumers[1].Key)
	ca, _ := release.ConsumerDigest(c, b.Consumers[1].Key)
	bb, _ := release.ConsumerDigest(b, b.Consumers[0].Key)
	in.Candidate.Evidence = []release.CompatibilityEvidence{{Consumer: b.Consumers[1].Key, Peer: b.Consumers[0].Key, Contract: "temporal-api/v1", BaseConsumer: ba, CandidateConsumer: ca, BasePeer: bb, CandidatePeer: bb, PolicyDigest: b.PolicyDigest, Validator: "pair-test/v1", ReportDigest: dg("e")}}
	if _, e := Compose(in); e != nil {
		t.Fatal(e)
	}
	in.Candidate.Evidence[0].CandidateConsumer = dg("f")
	if _, e := Compose(in); e == nil {
		t.Fatal("stale compatibility evidence accepted")
	}
	in.Candidate.Evidence = nil
	in.Selected = append(in.Selected, b.Consumers[0].Key)
	if _, e := Compose(in); e != nil {
		t.Fatal(e)
	}
}

func TestFullBootstrapAndCanonicalIdentity(t *testing.T) {
	b := fixture(t)
	out, e := Compose(Input{Candidate: b, Selected: b.Selected})
	if e != nil {
		t.Fatal(e)
	}
	d, _ := out.Digest()
	out.Bindings[0], out.Bindings[1] = out.Bindings[1], out.Bindings[0]
	out.Resources[0], out.Resources[1] = out.Resources[1], out.Resources[0]
	d2, _ := out.Digest()
	if d != d2 {
		t.Fatal("semantic ordering changed identity")
	}
	blob, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	var round release.Snapshot
	if e := json.Unmarshal(blob, &round); e != nil {
		t.Fatal(e)
	}
	d3, _ := round.Digest()
	if d != d3 {
		t.Fatal("wire roundtrip changed identity")
	}
	out.PolicyDigest = dg("e")
	d4, _ := out.Digest()
	if d == d4 {
		t.Fatal("policy omitted from identity")
	}
}
