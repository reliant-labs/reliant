package release

import (
	"reflect"
	"strings"
	"testing"
)

func job(name string, spec map[string]any) map[string]any {
	return map[string]any{"kind": "Job", "metadata": map[string]any{"name": name, "namespace": "ns"}, "spec": spec}
}

func rules(fs []FluxFinding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule+":"+f.Name)
	}
	return out
}

// The three control-plane Jobs that re-ran every 24 h under a reconciler.
func TestCheckFluxSafetyRefusesTTLdJobs(t *testing.T) {
	ttl := map[string]any{"ttlSecondsAfterFinished": 86400}
	got := rules(CheckFluxSafety([]any{
		job("temporal-schema-1023848b73", ttl),
		job("temporal-namespace-040a6cfe06", ttl),
		job("openbao-bootstrap-df3aa129ed", ttl),
		job("control-plane-migrate-b2e57763c4", map[string]any{"backoffLimit": 4}), // clean
	}))
	want := []string{
		"flux-job-ttl:openbao-bootstrap-df3aa129ed",
		"flux-job-ttl:temporal-namespace-040a6cfe06",
		"flux-job-ttl:temporal-schema-1023848b73",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings %v, want %v", got, want)
	}
	// ttl 0 is still a TTL: it deletes the Job the moment it finishes.
	if fs := CheckFluxSafety([]any{job("x-1023848b73", map[string]any{"ttlSecondsAfterFinished": 0})}); len(fs) != 1 || fs[0].Rule != RuleJobTTL {
		t.Errorf("ttl 0 findings = %v", fs)
	}
}

func TestCheckFluxSafetyRefusesUnhashedJobs(t *testing.T) {
	for name, wantFinding := range map[string]bool{
		"migrate":                          true, // no hash at all
		"control-plane-migrate":            true,
		"migrate-v2":                       true,  // a version, not a hash
		"migrate-abc":                      true,  // too short to be one
		"migrate-b2e57763c4":               false, // forge's ten-hex hash
		"temporal-schema-1023848b73":       false,
		"dbrole-provision-aa11bb22cc33dd4": false,
		"migrate-B2E57763C4":               true, // names are lowercase: not a hash
	} {
		fs := CheckFluxSafety([]any{job(name, nil)})
		if got := len(fs) == 1 && fs[0].Rule == RuleJobUnhashed; got != wantFinding {
			t.Errorf("Job %q: unhashed finding = %v, want %v (%v)", name, got, wantFinding, fs)
		}
	}
}

func TestCheckFluxSafetyIgnoresWhatItDoesNotJudge(t *testing.T) {
	if fs := CheckFluxSafety([]any{
		"not a mapping", nil,
		map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "api"}},
		map[string]any{"kind": "CronJob", "metadata": map[string]any{"name": "backup"}, "spec": map[string]any{"ttlSecondsAfterFinished": 5}},
	}); len(fs) != 0 {
		t.Errorf("findings = %v", fs)
	}
}

func TestCheckFluxSafetyRefusesForceOnData(t *testing.T) {
	forced := func(kind string) map[string]any {
		return map[string]any{"kind": kind, "metadata": map[string]any{
			"name": "x-1023848b73", "annotations": map[string]any{AnnotationFluxForce: "Enabled"}}}
	}
	for _, kind := range []string{"PersistentVolumeClaim", "StatefulSet", "Namespace", "CustomResourceDefinition", "Secret", "PersistentVolume"} {
		if fs := CheckFluxSafety([]any{forced(kind)}); len(fs) != 1 || fs[0].Rule != RuleForceData {
			t.Errorf("%s: findings = %v: force on a kind that holds data must be refused", kind, fs)
		}
	}
	for _, kind := range []string{"StorageClass", "RuntimeClass", "Deployment", "Service", "Job"} {
		if fs := CheckFluxSafety([]any{forced(kind)}); len(fs) != 0 {
			t.Errorf("%s: force on a recreatable kind was refused: %v", kind, fs)
		}
	}
}

func TestFindingsAreSortedAndNameTheFix(t *testing.T) {
	fs := CheckFluxSafety([]any{
		job("z-1023848b73", map[string]any{"ttlSecondsAfterFinished": 1}),
		job("a-1023848b73", map[string]any{"ttlSecondsAfterFinished": 1}),
	})
	if len(fs) != 2 || fs[0].Name != "a-1023848b73" {
		t.Fatalf("not sorted: %v", rules(fs))
	}
	if s := fs[0].String(); !strings.Contains(s, "ns/a-1023848b73") || !strings.Contains(s, "remove ttlSecondsAfterFinished") {
		t.Errorf("String() = %q", s)
	}
}

func deployment(spec map[string]any) map[string]any {
	return map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "api"}, "spec": spec}
}

func TestSetExplicitRollout(t *testing.T) {
	// Nothing stated: both are written, with Kubernetes' own defaults.
	d := deployment(map[string]any{"replicas": 2})
	if !SetExplicitRollout(d) {
		t.Fatal("an implicit Deployment was not changed")
	}
	spec := d["spec"].(map[string]any)
	want := map[string]any{"type": "RollingUpdate", "rollingUpdate": map[string]any{"maxSurge": "25%", "maxUnavailable": "25%"}}
	if !reflect.DeepEqual(spec["strategy"], want) {
		t.Errorf("strategy = %v", spec["strategy"])
	}
	if spec["progressDeadlineSeconds"] != 600 {
		t.Errorf("progressDeadlineSeconds = %v", spec["progressDeadlineSeconds"])
	}
	if spec["replicas"] != 2 {
		t.Error("an unrelated field was disturbed")
	}
	// Idempotent: a second pass changes nothing.
	if SetExplicitRollout(d) {
		t.Error("the fix is not idempotent")
	}
}

func TestSetExplicitRolloutNeverOverridesTheAuthor(t *testing.T) {
	// Recreate gains no rollingUpdate (Kubernetes refuses the combination).
	rec := deployment(map[string]any{"strategy": map[string]any{"type": "Recreate"}, "progressDeadlineSeconds": 120})
	if SetExplicitRollout(rec) {
		t.Error("a fully stated Deployment was changed")
	}
	if _, has := rec["spec"].(map[string]any)["strategy"].(map[string]any)["rollingUpdate"]; has {
		t.Error("Recreate was given rollingUpdate parameters")
	}
	if rec["spec"].(map[string]any)["progressDeadlineSeconds"] != 120 {
		t.Error("the author's deadline was overwritten")
	}
	// Stated rollingUpdate parameters are kept.
	custom := deployment(map[string]any{"strategy": map[string]any{"type": "RollingUpdate",
		"rollingUpdate": map[string]any{"maxSurge": 1, "maxUnavailable": 0}}})
	SetExplicitRollout(custom)
	ru := custom["spec"].(map[string]any)["strategy"].(map[string]any)["rollingUpdate"].(map[string]any)
	if ru["maxSurge"] != 1 || ru["maxUnavailable"] != 0 {
		t.Errorf("rollingUpdate = %v", ru)
	}
	// Only Deployments.
	for _, kind := range []string{"StatefulSet", "DaemonSet", "Job", "Service"} {
		other := map[string]any{"kind": kind, "spec": map[string]any{}}
		if SetExplicitRollout(other) {
			t.Errorf("%s was changed", kind)
		}
	}
}

func TestForceRecreateKindIsASubsetOfRecreatable(t *testing.T) {
	for kind := range forceKinds {
		if !RecreatableKind(kind) {
			t.Errorf("%s is force-annotated but direct apply may not recreate it", kind)
		}
	}
	for _, kind := range []string{"PersistentVolumeClaim", "StatefulSet", "Namespace", "CustomResourceDefinition", "Service"} {
		if ForceRecreateKind(kind) {
			t.Errorf("%s must never be stamped force", kind)
		}
	}
	// The StorageClass is the case that motivated the stamp.
	if !ForceRecreateKind("StorageClass") {
		t.Error("StorageClass is not force-annotated")
	}
}
