package v1alpha1

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestGatingJobRefusesPodLevelFields: a job with `before` runs as an
// initContainer INSIDE the pods it gates, under their identity and
// placement, so every field that describes a pod of its own is refused by
// Validate itself, the gate the control plane admits with, and not only by
// the renderer.
func TestGatingJobRefusesPodLevelFields(t *testing.T) {
	gating := func(mut func(*WorkloadSpec)) WorkloadSpec {
		s := WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"migrate"}, Before: []string{BeforeAll}}
		mut(&s)
		return s
	}
	rule := []PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}}}
	cases := map[string]func(*WorkloadSpec){
		"namespacedRBAC":            func(s *WorkloadSpec) { s.NamespacedRBAC = rule },
		"serviceAccount":            func(s *WorkloadSpec) { s.ServiceAccount = "existing" },
		"serviceAccountAnnotations": func(s *WorkloadSpec) { s.ServiceAccountAnnotations = map[string]string{"a": "b"} },
		"sidecars":                  func(s *WorkloadSpec) { s.Sidecars = []Container{{Name: "proxy", Image: "i"}} },
		"volumes": func(s *WorkloadSpec) {
			s.Volumes = []Volume{{Name: "v", MountPath: "/v", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}
		},
		"nodeSelector":   func(s *WorkloadSpec) { s.NodeSelector = map[string]string{"pool": "a"} },
		"tolerations":    func(s *WorkloadSpec) { s.Tolerations = []Toleration{{Key: "k", Operator: TolerationOpExists}} },
		"podAnnotations": func(s *WorkloadSpec) { s.PodAnnotations = map[string]string{"a": "b"} },
	}
	for field, mut := range cases {
		t.Run(field, func(t *testing.T) {
			// Full is where these fields are otherwise legal, so it is the
			// profile that proves the refusal is structural.
			err := gating(mut).Validate(ProfileFull)
			mustFail(t, err, field+" is not allowed on a job with before: it runs as an initContainer in the pods it gates")
			mustFail(t, err, "drop before to run it as a standalone Job")
			// The same field on a STANDALONE job has a pod of its own.
			standalone := gating(mut)
			standalone.Before = nil
			if err := standalone.Validate(ProfileFull); err != nil {
				t.Errorf("a standalone job may set %s: %v", field, err)
			}
		})
	}
}

func obj(name string, spec WorkloadSpec) Workload {
	return Workload{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
}

// TestWorkloadObjectValidate: the object-level entrypoint checks what only
// metadata.name can tell (the rendered object names), then delegates to the
// spec, so an admitted Workload is one the renderer can render.
func TestWorkloadObjectValidate(t *testing.T) {
	if err := obj("api", svc()).Validate(ProfileRestricted); err != nil {
		t.Fatalf("a valid workload object must validate: %v", err)
	}
	standalone := WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}}
	gating := WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{BeforeAll}}
	name52, name53 := strings.Repeat("a", 52), strings.Repeat("a", 53)
	if err := obj(name52, standalone).Validate(ProfileFull); err != nil {
		t.Errorf("a %d-character standalone job name must be allowed: %v", MaxStandaloneJobNameLength, err)
	}
	// A gating job renders no Job of its own, and a service no hashed
	// name, so the 63-character label limit is theirs.
	for _, s := range []WorkloadSpec{gating, svc()} {
		if err := obj(strings.Repeat("a", 63), s).Validate(ProfileFull); err != nil {
			t.Errorf("a 63-character %s name must be allowed: %v", s.EffectiveKind(), err)
		}
	}

	cases := map[string]struct {
		w    Workload
		want string
	}{
		"empty name":           {obj("", svc()), `name "" must be an RFC-1123 label of at most 63 characters`},
		"name not a DNS label": {obj("API_Server", svc()), `name "API_Server" must be an RFC-1123 label`},
		"name over 63":         {obj(strings.Repeat("a", 64), svc()), "at most 63 characters"},
		"standalone job over 52": {obj(name53, standalone),
			"a standalone job's name is at most 52 characters: its Job is named <name>-<10-char spec hash>"},
		"sidecar named like the workload": {obj("api", func() WorkloadSpec {
			s := svc()
			s.Sidecars = []Container{{Name: "api", Image: "i"}}
			return s
		}()), `sidecars[api]: a sidecar may not be named "api", the workload's own name, which its main container uses`},
		"spec errors are included": {obj("api", WorkloadSpec{}), "image is required"},
		"profile reaches the spec": {obj("api", WorkloadSpec{Kind: KindCron, Image: pinnedImage, Args: []string{"x"}, Schedule: "@daily"}),
			`kind "cron" is not allowed under the restricted profile`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { mustFail(t, c.w.Validate(ProfileRestricted), c.want) })
	}

	// One pass reports the object AND the spec errors together.
	err := obj("Bad Name", WorkloadSpec{}).Validate(ProfileFull)
	mustFail(t, err, "RFC-1123")
	mustFail(t, err, "image is required")
}
