package stsresize

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
)

func buildFor(t *testing.T, spec JobSpec) Objects {
	t.Helper()
	objs, err := Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func oneSet(size string) JobSpec {
	return JobSpec{Namespace: "observability-data", StatefulSets: []*appsv1.StatefulSet{sts(template("data", size))}}
}

func TestBuild_TheJobIsAFluxSafePreRolloutOneShot(t *testing.T) {
	job := buildFor(t, oneSet("100Gi")).Job
	if !strings.HasPrefix(job.Name, "sts-volume-resize-") || len(job.Name) != len("sts-volume-resize-")+10 {
		t.Fatalf("name %q does not end in a 10-hex spec hash", job.Name)
	}
	if job.Spec.TTLSecondsAfterFinished != nil {
		t.Fatal("a TTL re-runs a Job every period under a reconciler")
	}
	if job.Annotations["forge.dev/deploy-phase"] != "pre-rollout" {
		t.Fatalf("annotations = %v", job.Annotations)
	}
	if job.Spec.Template.Spec.RestartPolicy != "Never" {
		t.Fatal("a pod that failed must not be retried inside the Job: the BackoffLimit retries a transient failure with a fresh pod")
	}
}

// The script exits 2 on a refusal and the Job must fail AT ONCE on it: a
// retried refusal wastes three pods and hides the reason behind a backoff.
func TestBuild_ARefusalFailsTheJobAtOnce(t *testing.T) {
	spec := buildFor(t, oneSet("100Gi")).Job.Spec
	if spec.PodFailurePolicy == nil || len(spec.PodFailurePolicy.Rules) != 1 {
		t.Fatalf("podFailurePolicy = %+v", spec.PodFailurePolicy)
	}
	r := spec.PodFailurePolicy.Rules[0]
	if r.Action != batchv1.PodFailurePolicyActionFailJob || r.OnExitCodes == nil ||
		len(r.OnExitCodes.Values) != 1 || r.OnExitCodes.Values[0] != exitRefused {
		t.Fatalf("rule = %+v", r)
	}
	if *r.OnExitCodes.ContainerName != spec.Template.Spec.Containers[0].Name {
		t.Fatal("the rule names a container the pod does not have, so it would never match")
	}
	if !strings.Contains(Script, "exit 2") || !strings.Contains(Script, "refuse()") {
		t.Fatal("the script no longer exits 2 on a refusal, which the failure policy keys on")
	}
}

func TestBuild_ScopedRBAC(t *testing.T) {
	a, b := sts(template("data", "10Gi")), sts(template("data", "10Gi"))
	a.Name, b.Name = "zeta", "alpha"
	objs := buildFor(t, JobSpec{Namespace: "observability-data", StatefulSets: []*appsv1.StatefulSet{a, b}})
	rules := objs.Role.Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %+v", rules)
	}
	sets := rules[0]
	if strings.Join(sets.ResourceNames, ",") != "alpha,zeta" || strings.Join(sets.Verbs, ",") != "get,delete" {
		t.Fatalf("StatefulSet rule = %+v: must be pinned to the named StatefulSets with get+delete only", sets)
	}
	claims := rules[1]
	if strings.Join(claims.Verbs, ",") != "get,list,patch" || len(claims.ResourceNames) != 0 {
		t.Fatalf("claim rule = %+v", claims)
	}
	for _, r := range rules {
		for _, v := range r.Verbs {
			if v == "*" || v == "create" || v == "update" {
				t.Fatalf("verb %q is more than the Job needs", v)
			}
		}
	}
	if objs.RoleBinding.Subjects[0].Name != objs.ServiceAccount.Name || objs.RoleBinding.RoleRef.Name != objs.Role.Name {
		t.Fatal("the binding must tie this Job's ServiceAccount to this Role")
	}
	if objs.Job.Spec.Template.Spec.ServiceAccountName != objs.ServiceAccount.Name {
		t.Fatal("the pod does not run as the ServiceAccount the Role is bound to")
	}
}

func TestBuild_PodIsLockedDown(t *testing.T) {
	pod := buildFor(t, oneSet("1Gi")).Job.Spec.Template.Spec
	c := pod.Containers[0]
	if !*pod.SecurityContext.RunAsNonRoot || !*c.SecurityContext.ReadOnlyRootFilesystem || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("security context = %+v / %+v", pod.SecurityContext, c.SecurityContext)
	}
	if len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatal("capabilities must be dropped")
	}
	if !strings.Contains(c.Image, "@sha256:") {
		t.Fatalf("default image %q must be pinned by digest", c.Image)
	}
	if pod.EnableServiceLinks == nil || *pod.EnableServiceLinks {
		t.Fatal("the pod must render enableServiceLinks: false, as every forge-rendered pod does")
	}
}

func TestBuild_HashTracksTheDeclaredVolumesAndNothingElse(t *testing.T) {
	name := func(spec JobSpec) string { return buildFor(t, spec).Job.Name }
	base := name(oneSet("100Gi"))
	if base != name(oneSet("100Gi")) {
		t.Fatal("the same declaration must name the same Job")
	}
	if base == name(oneSet("200Gi")) {
		t.Fatal("a changed declared size must be a new Job: its spec is immutable and it has to run again")
	}
	withImage := oneSet("100Gi")
	withImage.Image = "mirror/k8s:1"
	if base == name(withImage) {
		t.Fatal("a different image is a different pod spec, so a different Job")
	}
	// A pod template change must NOT rename the Job: Build is never given it.
	other := oneSet("100Gi")
	other.StatefulSets[0].Spec.Template.Spec.Containers = nil
	other.StatefulSets[0].Spec.Replicas = ptr(int32(5))
	if base != name(other) {
		t.Fatal("only the declared volumes may move the hash; a replica or pod template change re-ran the resize")
	}
}

func TestBuild_NamesPerUnit(t *testing.T) {
	cases := map[string]string{
		"":                      "sts-volume-resize",
		"base":                  "sts-volume-resize",
		"infra":                 "sts-volume-resize-infra",
		"Hub_Base.v2":           "sts-volume-resize-hub-base-v2",
		strings.Repeat("u", 60): "",
	}
	for unit, want := range cases {
		got := Name(unit)
		if want == "" {
			if len(got) > 63-11 || !strings.HasPrefix(got, "sts-volume-resize-") {
				t.Errorf("a long unit must fold to a bounded name, got %q", got)
			}
			continue
		}
		if got != want {
			t.Errorf("Name(%q) = %q, want %q", unit, got, want)
		}
	}
	// Two units' objects must not collide in one namespace.
	if Name("a") == Name("b") || Name("a") == Name("") {
		t.Fatal("units must not share object names")
	}
}

func TestBuild_Refusals(t *testing.T) {
	cases := map[string]JobSpec{
		"no namespace":      {StatefulSets: []*appsv1.StatefulSet{sts(template("data", "1Gi"))}},
		"no StatefulSet":    {Namespace: "ns"},
		"no templates":      {Namespace: "ns", StatefulSets: []*appsv1.StatefulSet{sts()}},
		"another namespace": {Namespace: "ns", StatefulSets: []*appsv1.StatefulSet{sts(template("data", "1Gi"))}},
	}
	for name, spec := range cases {
		if _, err := Build(spec); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestObjects_ManifestsAreCleanDocuments(t *testing.T) {
	docs, err := buildFor(t, oneSet("100Gi")).Manifests()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, d := range docs {
		kinds = append(kinds, d["kind"].(string))
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), `"creationTimestamp":null`) || strings.Contains(string(raw), `"status"`) {
			t.Fatalf("%s carries typed-object noise: %s", d["kind"], raw)
		}
	}
	if strings.Join(kinds, ",") != "ServiceAccount,Role,RoleBinding,NetworkPolicy,Job" {
		t.Fatalf("apply order = %v: the identity must precede the Job that runs as it", kinds)
	}
}

// The script is the one thing in the Job that no compiler checks.
func TestScript_IsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	cmd := exec.Command(sh, "-n")
	cmd.Stdin = strings.NewReader(Script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}
