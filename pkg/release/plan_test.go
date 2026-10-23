package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func obj(kind, name, hash string) ShapeObject {
	return ShapeObject{Cluster: "c1", Kind: kind, Namespace: "ns", Name: name, Hash: digest(hash)}
}

func shapeOf(objs ...ShapeObject) Shape {
	return Shape{Kind: EnvSelfManaged, Objects: objs}
}

func noDrift() *DriftObservation { return &DriftObservation{} }

func codesOf(p Plan) map[string]FindingClass {
	out := map[string]FindingClass{}
	for _, f := range p.Findings {
		out[f.Code+"@"+f.Subject] = f.Class
	}
	return out
}

// THE STOP-CLASS RULES. These table tests are the acceptance tests for every
// backend that recomputes a plan (the CLI and the control plane both call
// BuildPlan): a row here is a promise about which changes a general --yes can
// never approve.
func TestBuildPlan_StopClassRules(t *testing.T) {
	lb := obj("Service", "web", "a")
	lb.Identity = map[string]string{IdentityType: "LoadBalancer", IdentityLoadBalancerIP: "34.1.2.3"}
	lbNewIP := lb
	lbNewIP.Hash = digest("b")
	lbNewIP.Identity = map[string]string{IdentityType: "LoadBalancer", IdentityLoadBalancerIP: "34.9.9.9"}
	lbToClusterIP := lb
	lbToClusterIP.Hash = digest("c")
	lbToClusterIP.Identity = map[string]string{IdentityType: "ClusterIP"}
	clusterIP := obj("Service", "api", "d")
	clusterIP.Identity = map[string]string{IdentityType: "ClusterIP"}
	clusterIPToLB := clusterIP
	clusterIPToLB.Hash = digest("e")
	clusterIPToLB.Identity = map[string]string{IdentityType: "LoadBalancer"}
	dbDeployment := obj("Deployment", "pg", "f")
	dbDeployment.Stateful = true

	cases := []struct {
		name      string
		live      []ShapeObject
		candidate []ShapeObject
		wantStop  []string
		wantClass map[string]FindingClass // subset check, keyed code@subject
	}{
		{
			name:     "PVC removed is a stateful deletion",
			live:     []ShapeObject{obj("PersistentVolumeClaim", "data", "a")},
			wantStop: []string{FindingStatefulDeletion},
		},
		{
			name:     "StatefulSet removed is a stateful deletion",
			live:     []ShapeObject{obj("StatefulSet", "pg", "a")},
			wantStop: []string{FindingStatefulDeletion},
		},
		{
			name:     "Secret removed is a stateful deletion",
			live:     []ShapeObject{obj("Secret", "creds", "a")},
			wantStop: []string{FindingStatefulDeletion},
		},
		{
			name:     "an object marked stateful (a declared database) removed is a stateful deletion",
			live:     []ShapeObject{dbDeployment},
			wantStop: []string{FindingStatefulDeletion},
		},
		{
			name:      "a plain Deployment removed is a warn, not a stop",
			live:      []ShapeObject{obj("Deployment", "worker", "a")},
			wantClass: map[string]FindingClass{FindingObjectRemoved + "@c1/Deployment/ns/worker": ClassWarn},
		},
		{
			name:     "a LoadBalancer Service removed releases its address",
			live:     []ShapeObject{lb},
			wantStop: []string{FindingLBIdentityChange},
		},
		{
			name:      "a LoadBalancer's IP changing is an identity change",
			live:      []ShapeObject{lb},
			candidate: []ShapeObject{lbNewIP},
			wantStop:  []string{FindingLBIdentityChange},
		},
		{
			name:      "a LoadBalancer becoming ClusterIP is an identity change",
			live:      []ShapeObject{lb},
			candidate: []ShapeObject{lbToClusterIP},
			wantStop:  []string{FindingLBIdentityChange},
		},
		{
			name:      "a ClusterIP becoming a LoadBalancer gains an address and loses none",
			live:      []ShapeObject{clusterIP},
			candidate: []ShapeObject{clusterIPToLB},
		},
		{
			name:      "an added PVC is info",
			candidate: []ShapeObject{obj("PersistentVolumeClaim", "data", "a")},
			wantClass: map[string]FindingClass{FindingObjectAdded + "@c1/PersistentVolumeClaim/ns/data": ClassInfo},
		},
		{
			name:      "a changed StatefulSet is not a deletion",
			live:      []ShapeObject{obj("StatefulSet", "pg", "a")},
			candidate: []ShapeObject{obj("StatefulSet", "pg", "b")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := shapeOf(tc.live...)
			p, err := BuildPlan(PlanInput{
				EnvironmentID: "env-1", BundleID: "b-1",
				Candidate: shapeOf(tc.candidate...), Live: &live, Drift: noDrift(),
			})
			if err != nil {
				t.Fatal(err)
			}
			got := p.StopCodes()
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tc.wantStop) {
				t.Fatalf("stop codes = %v, want %v (findings %+v)", got, tc.wantStop, p.Findings)
			}
			classes := codesOf(p)
			for k, want := range tc.wantClass {
				if classes[k] != want {
					t.Errorf("finding %s class = %q, want %q (all: %v)", k, classes[k], want, classes)
				}
			}
		})
	}
}

// --yes cannot approve a stop: only naming the code can, and naming one code
// does not approve another.
func TestPlan_Unacknowledged(t *testing.T) {
	live := shapeOf(obj("PersistentVolumeClaim", "data", "a"), func() ShapeObject {
		o := obj("Service", "web", "b")
		o.Identity = map[string]string{IdentityType: "LoadBalancer"}
		return o
	}())
	p, err := BuildPlan(PlanInput{EnvironmentID: "e", BundleID: "b", Candidate: shapeOf(), Live: &live, Drift: noDrift()})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Unacknowledged(nil); !reflect.DeepEqual(got, []string{FindingLBIdentityChange, FindingStatefulDeletion}) {
		t.Fatalf("nothing acknowledged: %v", got)
	}
	if got := p.Unacknowledged([]string{FindingStatefulDeletion}); !reflect.DeepEqual(got, []string{FindingLBIdentityChange}) {
		t.Fatalf("one acknowledged: %v", got)
	}
	if got := p.Unacknowledged([]string{" stateful_deletion ", FindingLBIdentityChange}); len(got) != 0 {
		t.Fatalf("both acknowledged: %v", got)
	}
}

func TestBuildPlan_ImageAndConfigSplit(t *testing.T) {
	l := obj("Deployment", "api", "a")
	l.ConfigHash = digest("1")
	l.Images = map[string]string{"control-plane": digest("7")}
	imageOnly := l
	imageOnly.Hash = digest("b")
	imageOnly.Images = map[string]string{"control-plane": digest("8")}
	configOnly := l
	configOnly.Hash = digest("c")
	configOnly.ConfigHash = digest("2")

	live := shapeOf(l)
	for _, tc := range []struct {
		name string
		cand ShapeObject
		want []string
	}{
		{"image only", imageOnly, []string{FindingImageChanged}},
		{"config only", configOnly, []string{FindingConfigChanged}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := BuildPlan(PlanInput{EnvironmentID: "e", BundleID: "b", Candidate: shapeOf(tc.cand), Live: &live, Drift: noDrift()})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range p.Findings {
				got = append(got, f.Code)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("codes = %v, want %v", got, tc.want)
			}
		})
	}
}

// Empty and unknown must never render alike (F-20): no live record, and no
// drift observation, are both WARN findings rather than silence.
func TestBuildPlan_UnknownsAreWarnings(t *testing.T) {
	p, err := BuildPlan(PlanInput{EnvironmentID: "e", BundleID: "b", Candidate: shapeOf(obj("Deployment", "api", "a"))})
	if err != nil {
		t.Fatal(err)
	}
	var unknown []string
	for _, f := range p.Findings {
		if f.Code == FindingUnknown {
			if f.Class != ClassWarn {
				t.Errorf("unknown %s is %s, want warn", f.Section, f.Class)
			}
			unknown = append(unknown, f.Section)
		}
	}
	if !reflect.DeepEqual(unknown, []string{SectionObjects, SectionDrift}) {
		t.Fatalf("unknown sections = %v", unknown)
	}
}

func TestBuildPlan_SecretsAndDrift(t *testing.T) {
	live := Shape{Kind: EnvPersistent, Secrets: []ShapeSecret{{Name: "OLD", Provider: "hosted"}}}
	cand := Shape{Kind: EnvPersistent, Secrets: []ShapeSecret{
		{Name: "OLD", Provider: "hosted"}, {Name: "SET", Provider: "hosted"}, {Name: "MISSING", Provider: "hosted"},
		{Name: "EXTERNAL", Provider: "external"},
	}}
	p, err := BuildPlan(PlanInput{
		EnvironmentID: "e", BundleID: "b", Candidate: cand, Live: &live,
		SecretPresence: map[string]bool{"SET": true, "MISSING": false},
		Drift:          &DriftObservation{Drifted: []ObjectKey{{Cluster: "c1", Kind: "Deployment", Namespace: "ns", Name: "api"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	classes := codesOf(p)
	if classes[FindingSecretNeeded+"@SET"] != ClassInfo || classes[FindingSecretNeeded+"@MISSING"] != ClassWarn {
		t.Fatalf("secret classes wrong: %v", classes)
	}
	// A secret whose presence was never read is unverifiable, not missing:
	// an env mixing a managed store and an external provider must not read
	// every external secret as MISSING.
	for _, f := range p.Findings {
		if f.Subject == "EXTERNAL" && strings.Contains(f.Detail, "MISSING") {
			t.Fatalf("an unread secret was reported missing: %+v", f)
		}
	}
	if _, ok := classes[FindingSecretNeeded+"@OLD"]; ok {
		t.Fatal("an already-declared secret is not newly needed")
	}
	if classes[FindingDrift+"@c1/Deployment/ns/api"] != ClassWarn {
		t.Fatalf("drift not reported: %v", classes)
	}
	if !p.LiveBasis.DriftObserved {
		t.Fatal("drift must be part of the basis, so drift appearing invalidates an approval")
	}
}

// Presence over EVERY declared secret, against the last applied declaration.
// Prod's 2026-10-10 plans carried 28 `secret_needed` warns for Secrets that
// had existed for months: the steady state must be silent, so the one secret
// that actually went missing is the finding a reviewer reads.
func TestBuildPlan_SecretPresenceAgainstTheLastApply(t *testing.T) {
	declared := []ShapeSecret{
		{Name: "steady", Provider: "external"},
		{Name: "deleted-since", Provider: "external"},
		{Name: "unread", Provider: "external"},
	}
	live := Shape{Kind: EnvSelfManaged, Secrets: declared}
	cand := Shape{Kind: EnvSelfManaged, Secrets: append(append([]ShapeSecret(nil), declared...),
		ShapeSecret{Name: "new-present", Provider: "external"},
		ShapeSecret{Name: "new-missing", Provider: "external"})}
	p, err := BuildPlan(PlanInput{
		EnvironmentID: "e", BundleID: "b", Candidate: cand, Live: &live, Drift: noDrift(),
		SecretPresence: map[string]bool{
			"steady": true, "deleted-since": false,
			"new-present": true, "new-missing": false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PlanFinding{}
	for _, f := range p.Findings {
		if f.Code == FindingSecretNeeded {
			got[f.Subject] = f
		}
	}
	if _, ok := got["steady"]; ok {
		t.Errorf("a secret declared at the last apply and still present is not a finding: %+v", got["steady"])
	}
	if _, ok := got["unread"]; ok {
		t.Errorf("an already-declared secret nobody could read must stay silent, not warn every deploy: %+v", got["unread"])
	}
	if f := got["deleted-since"]; f.Class != ClassWarn || !strings.Contains(f.Detail, "MISSING") {
		t.Errorf("a declared secret that is now missing = %+v, want a warn naming it MISSING", f)
	}
	if f := got["new-present"]; f.Class != ClassInfo {
		t.Errorf("a newly declared secret that is present = %+v, want info", f)
	}
	if f := got["new-missing"]; f.Class != ClassWarn || !strings.Contains(f.Detail, "MISSING") {
		t.Errorf("a newly declared secret that is missing = %+v, want a warn naming it MISSING", f)
	}
	if len(got) != 3 {
		t.Errorf("secret findings = %v, want exactly deleted-since, new-present, new-missing", got)
	}
}

func TestBuildPlan_KindChangeRefused(t *testing.T) {
	live := Shape{Kind: EnvLocal}
	_, err := BuildPlan(PlanInput{EnvironmentID: "e", BundleID: "b", Candidate: Shape{Kind: EnvSelfManaged}, Live: &live})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a kind change must be refused, got %v", err)
	}
}

func TestBuildPlan_ConfigIdentical(t *testing.T) {
	live := shapeOf(obj("Deployment", "api", "a"))
	p, err := BuildPlan(PlanInput{
		EnvironmentID: "e", BundleID: "b", Candidate: live, Live: &live, Drift: noDrift(),
		CandidateConfigDigest: digest("9"), Basis: PlanBasis{AppliedConfigDigest: digest("9")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.ConfigIdentical || len(p.Findings) != 0 {
		t.Fatalf("identical config: identical=%v findings=%v", p.ConfigIdentical, p.Findings)
	}
}

// THE DIGEST CONTRACT: the same plan computed twice — at two times, with its
// objects in a different order, with its prose reworded — has one digest;
// anything that changes what was approved changes it.
func TestPlanDigest(t *testing.T) {
	live := shapeOf(obj("PersistentVolumeClaim", "data", "a"), obj("Deployment", "api", "b"))
	cand := shapeOf(obj("Deployment", "api", "c"), obj("Deployment", "web", "d"))
	in := PlanInput{EnvironmentID: "e", BundleID: "b", Candidate: cand, Live: &live, Drift: noDrift(),
		Basis: PlanBasis{CurrentPromotionID: "p1", AppliedBundleID: "b0"}}
	base, err := BuildPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidDigest(base.Digest) {
		t.Fatalf("digest %q", base.Digest)
	}

	t.Run("stable across time, order and prose", func(t *testing.T) {
		reordered := in
		reordered.Candidate = shapeOf(cand.Objects[1], cand.Objects[0])
		p, err := BuildPlan(reordered)
		if err != nil {
			t.Fatal(err)
		}
		p.ComputedAt = time.Now()
		for i := range p.Findings {
			p.Findings[i].Detail = "reworded"
		}
		if ok, err := p.VerifyDigest(); err != nil || !ok {
			t.Fatalf("verify after prose change: ok=%v err=%v", ok, err)
		}
		if p.Digest != base.Digest {
			t.Fatalf("digest moved: %s vs %s", p.Digest, base.Digest)
		}
	})

	t.Run("a different live basis is a different plan", func(t *testing.T) {
		moved := in
		moved.Basis.CurrentPromotionID = "p2"
		p, _ := BuildPlan(moved)
		if p.Digest == base.Digest {
			t.Fatal("a plan computed against another promotion must not match")
		}
	})

	t.Run("drift appearing is a different plan", func(t *testing.T) {
		drifted := in
		drifted.Drift = &DriftObservation{Drifted: []ObjectKey{{Cluster: "c1", Kind: "Deployment", Namespace: "ns", Name: "api"}}}
		p, _ := BuildPlan(drifted)
		if p.Digest == base.Digest {
			t.Fatal("drift must invalidate an approval")
		}
	})

	t.Run("tampering with a finding breaks verification", func(t *testing.T) {
		p := base
		p.Findings = append([]PlanFinding(nil), base.Findings...)
		p.Findings[0].Class = ClassInfo
		if ok, _ := p.VerifyDigest(); ok {
			t.Fatal("a downgraded finding must not verify")
		}
	})
}

// The plan's JSON is the wire shape (controlplane.v1.DeployPlan field names)
// and round-trips; an unknown class is refused on decode.
func TestPlan_JSON(t *testing.T) {
	live := shapeOf(obj("Secret", "s", "a"))
	p, err := BuildPlan(PlanInput{EnvironmentID: "e", BundleID: "b", Candidate: shapeOf(), Live: &live, Drift: noDrift()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"digest"`, `"environment_id"`, `"bundle_id"`, `"live_basis"`, `"config_identical"`, `"findings"`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("wire JSON lacks %s: %s", field, b)
		}
	}
	var back Plan
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if ok, err := back.VerifyDigest(); err != nil || !ok {
		t.Fatalf("round-tripped plan does not verify: %v %v", ok, err)
	}
	if err := json.Unmarshal([]byte(`{"code":"x","class":"maybe"}`), &PlanFinding{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown class must be refused, got %v", err)
	}
}

// The prod StorageClass case: with nothing recorded to diff against, the plan
// cannot say whether workspace-ssd already exists with different immutable
// fields, so it must say the apply MAY recreate it — and only for the kinds the
// apply is allowed to recreate.
func TestBuildPlan_NamesRecreatableKindsItCannotDiff(t *testing.T) {
	sc := ShapeObject{Cluster: "daemon", Kind: "StorageClass", Name: "workspace-ssd", Hash: digest("a")}
	pvc := ShapeObject{Cluster: "daemon", Kind: "PersistentVolumeClaim", Namespace: "ns", Name: "data", Hash: digest("b")}
	p, err := BuildPlan(PlanInput{EnvironmentID: "prod", BundleID: "b", Candidate: shapeOf(sc, pvc), Drift: noDrift()})
	if err != nil {
		t.Fatal(err)
	}
	detail := map[string]string{}
	for _, f := range p.Findings {
		if f.Code == FindingObjectAdded {
			detail[f.Subject] = f.Detail
		}
	}
	if !strings.Contains(detail[sc.Key().String()], "DELETED AND RECREATED") {
		t.Errorf("a StorageClass the plan cannot diff must be flagged as possibly recreated, got %q", detail[sc.Key().String()])
	}
	if detail[pvc.Key().String()] != "" {
		t.Errorf("a PersistentVolumeClaim is never recreated and must carry no such note, got %q", detail[pvc.Key().String()])
	}
}

// The note is prose: it must not change the digest an approval names, or two
// forge versions would compute different digests for the same plan.
func TestBuildPlan_RecreateNoteIsNotDigested(t *testing.T) {
	sc := ShapeObject{Cluster: "daemon", Kind: "StorageClass", Name: "workspace-ssd", Hash: digest("a")}
	p, err := BuildPlan(PlanInput{EnvironmentID: "prod", BundleID: "b", Candidate: shapeOf(sc), Drift: noDrift()})
	if err != nil {
		t.Fatal(err)
	}
	stripped := p
	stripped.Findings = append([]PlanFinding(nil), p.Findings...)
	for i := range stripped.Findings {
		stripped.Findings[i].Detail = ""
	}
	a, _ := p.computeDigest()
	b, _ := stripped.computeDigest()
	if a != b {
		t.Errorf("Detail must not be digested: %s vs %s", a, b)
	}
}

// With a recorded live config the same object that DIFFERS is also named, since
// a changed StorageClass is exactly the case an immutable field bites.
func TestBuildPlan_NamesRecreatableKindsThatChange(t *testing.T) {
	live := ShapeObject{Cluster: "daemon", Kind: "StorageClass", Name: "workspace-ssd", Hash: digest("c")}
	cand := ShapeObject{Cluster: "daemon", Kind: "StorageClass", Name: "workspace-ssd", Hash: digest("d")}
	lv := shapeOf(live)
	p, err := BuildPlan(PlanInput{EnvironmentID: "prod", BundleID: "b", Candidate: shapeOf(cand), Live: &lv, Drift: noDrift()})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range p.Findings {
		if f.Code == FindingObjectChanged && strings.Contains(f.Detail, "DELETED AND RECREATED") {
			found = true
		}
	}
	if !found {
		t.Errorf("a changed StorageClass must be flagged as possibly recreated: %+v", p.Findings)
	}
}
