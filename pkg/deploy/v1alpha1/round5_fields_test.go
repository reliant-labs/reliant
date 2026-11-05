package v1alpha1

import (
	"fmt"
	"strings"
	"testing"
)

var getPods = []PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}

// TestClusterRBACOnAnyScheduledKind: clusterRBAC is a Full-only tier any
// scheduled kind may take (workspace-proxy resolves resources cluster-wide).
// It ADDS to the namespaced tier rather than replacing it, so both on one
// workload is valid — including on an operator, whose namespace-local grants
// belong in its Role, not widened into its ClusterRole.
func TestClusterRBACOnAnyScheduledKind(t *testing.T) {
	for _, s := range []WorkloadSpec{
		{Kind: KindService, Image: pinnedImage, ClusterRBAC: getPods},
		{Kind: KindWorker, Image: pinnedImage, ClusterRBAC: getPods},
		{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, ClusterRBAC: getPods},
		{Kind: KindCron, Image: pinnedImage, Args: []string{"x"}, Schedule: "@daily", ClusterRBAC: getPods},
	} {
		mustPass(t, s, ProfileFull)
		mustFail(t, s.Validate(ProfileRestricted), "clusterRBAC: not allowed under the restricted profile")
	}
	mustPass(t, WorkloadSpec{Kind: KindWorker, Image: pinnedImage, ClusterRBAC: getPods, NamespacedRBAC: getPods}, ProfileFull)
	mustPass(t, WorkloadSpec{Kind: KindOperator, Image: pinnedImage, CRDs: []string{"W"}, ClusterRBAC: getPods, NamespacedRBAC: getPods}, ProfileFull)
	gating := WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"api"}, ClusterRBAC: getPods}
	mustFail(t, gating.Validate(ProfileFull), "clusterRBAC is not allowed on a job with before")
	if !strings.Contains(FullOnlyReason("clusterRBAC"), "Kubernetes API access") {
		t.Errorf("clusterRBAC's Restricted refusal must say why: %q", FullOnlyReason("clusterRBAC"))
	}
}

// TestTerminationGracePeriodOverride (N1): Full-only, 0..3600, pod-bearing
// long-running and batch kinds only.
func TestTerminationGracePeriodOverride(t *testing.T) {
	ok := WorkloadSpec{Kind: KindWorker, Image: pinnedImage, TerminationGracePeriodSeconds: ptr(int32(60))}
	mustPass(t, ok, ProfileFull)
	mustFail(t, ok.Validate(ProfileRestricted), "terminationGracePeriodSeconds: not allowed under the restricted profile")
	mustPass(t, WorkloadSpec{Kind: KindWorker, Image: pinnedImage, TerminationGracePeriodSeconds: ptr(int32(0))}, ProfileFull)
	for _, v := range []int32{-1, 3601} {
		mustFail(t, WorkloadSpec{Kind: KindWorker, Image: pinnedImage, TerminationGracePeriodSeconds: ptr(v)}.Validate(ProfileFull), "terminationGracePeriodSeconds must be 0-3600")
	}
	mustFail(t, WorkloadSpec{Kind: KindTool, Image: pinnedImage, TerminationGracePeriodSeconds: ptr(int32(10))}.Validate(ProfileFull), `terminationGracePeriodSeconds is not supported for kind "tool"`)
	mustFail(t, WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"api"}, TerminationGracePeriodSeconds: ptr(int32(10))}.Validate(ProfileFull),
		"terminationGracePeriodSeconds is not allowed on a job with before")
}

// TestDeploymentStrategy: allowed under every profile (it trades only the
// author's own availability), on the kinds that render a Deployment, and
// never RollingUpdate alongside a ReadWriteOnce volume.
func TestDeploymentStrategy(t *testing.T) {
	for _, k := range []WorkloadKind{KindService, KindWorker, KindOperator} {
		s := WorkloadSpec{Kind: k, Image: pinnedImage, Strategy: StrategyRecreate}
		if k == KindOperator {
			s.CRDs = []string{"W"}
		}
		mustPass(t, s, ProfileFull)
	}
	mustPass(t, WorkloadSpec{Kind: KindWorker, Image: pinnedImage, Strategy: StrategyRecreate}, ProfileRestricted)
	mustFail(t, WorkloadSpec{Kind: KindWorker, Image: pinnedImage, Strategy: "recreate"}.Validate(ProfileFull),
		`strategy "recreate" must be RollingUpdate or Recreate`)
	for _, k := range []WorkloadKind{KindJob, KindCron, KindTool} {
		s := WorkloadSpec{Kind: k, Image: pinnedImage, Args: []string{"x"}, Strategy: StrategyRecreate}
		if k == KindCron {
			s.Schedule = "@daily"
		}
		mustFail(t, s.Validate(ProfileFull), fmt.Sprintf("strategy is not supported for kind %q", k))
	}
	// A ReadWriteOnce volume cannot be mounted by a surge pod, so an
	// explicit RollingUpdate beside storage would deadlock the rollout.
	mustFail(t, WorkloadSpec{Kind: KindService, Image: pinnedImage, StorageGiB: 10, Strategy: StrategyRollingUpdate}.Validate(ProfileFull),
		"strategy RollingUpdate is not allowed with storageGiB")
	mustPass(t, WorkloadSpec{Kind: KindService, Image: pinnedImage, StorageGiB: 10, Strategy: StrategyRecreate}, ProfileFull)
}

// TestPriorityClassName: Full-only, pod-bearing kinds only, a DNS-1123
// subdomain of at most 253 characters.
func TestPriorityClassName(t *testing.T) {
	ok := WorkloadSpec{Kind: KindWorker, Image: pinnedImage, PriorityClassName: "reliant-platform"}
	mustPass(t, ok, ProfileFull)
	mustFail(t, ok.Validate(ProfileRestricted), "priorityClassName: not allowed under the restricted profile")
	for _, v := range []string{"Reliant-Platform", "-leading", "has_underscore", strings.Repeat("a", 254)} {
		mustFail(t, WorkloadSpec{Kind: KindWorker, Image: pinnedImage, PriorityClassName: v}.Validate(ProfileFull),
			"priorityClassName")
	}
	mustFail(t, WorkloadSpec{Kind: KindTool, Image: pinnedImage, PriorityClassName: "reliant-platform"}.Validate(ProfileFull),
		`priorityClassName is not supported for kind "tool"`)
	mustFail(t, WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"api"}, PriorityClassName: "reliant-platform"}.Validate(ProfileFull),
		"priorityClassName is not allowed on a job with before")
	if !strings.Contains(FullOnlyReason("priorityClassName"), "preempt") {
		t.Errorf("priorityClassName's Restricted refusal must say why: %q", FullOnlyReason("priorityClassName"))
	}
}

// TestActiveDeadlineSeconds (N2): Restricted-allowed, job/cron only, > 0.
func TestActiveDeadlineSeconds(t *testing.T) {
	job := WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, ActiveDeadlineSeconds: ptr(int64(60))}
	mustPass(t, job, ProfileRestricted)
	mustPass(t, WorkloadSpec{Kind: KindCron, Image: pinnedImage, Args: []string{"x"}, Schedule: "@daily", ActiveDeadlineSeconds: ptr(int64(600))}, ProfileFull)
	// A gating job's deadline bounds its initContainer: refused, since an
	// initContainer has no deadline of its own (the Job-level one would be
	// the gated pod's).
	mustFail(t, WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"api"}, ActiveDeadlineSeconds: ptr(int64(60))}.Validate(ProfileFull),
		"activeDeadlineSeconds is not allowed on a job with before")
	mustFail(t, WorkloadSpec{Kind: KindService, Image: pinnedImage, ActiveDeadlineSeconds: ptr(int64(60))}.Validate(ProfileFull), `activeDeadlineSeconds is not supported for kind "service"`)
	for _, v := range []int64{0, -5} {
		mustFail(t, WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, ActiveDeadlineSeconds: ptr(v)}.Validate(ProfileFull), "activeDeadlineSeconds must be positive")
	}
	if FieldProfiles["activeDeadlineSeconds"] != ProfileRestricted {
		t.Errorf("activeDeadlineSeconds must be Restricted-allowed, got %s", FieldProfiles["activeDeadlineSeconds"])
	}
}

// TestPodSecurityOverride (N3): Full-only; only uid/gid/fsGroup/RO rootfs;
// runAsNonRoot stays forced, so uid/gid/fsGroup 0 is refused.
func TestPodSecurityOverride(t *testing.T) {
	ok := WorkloadSpec{Kind: KindService, Image: pinnedImage, SecurityContext: &PodSecurity{
		RunAsUser: ptr(int64(1000)), RunAsGroup: ptr(int64(1000)), FSGroup: ptr(int64(1000)), ReadOnlyRootFilesystem: ptr(false),
	}}
	mustPass(t, ok, ProfileFull)
	mustFail(t, ok.Validate(ProfileRestricted), "securityContext: not allowed under the restricted profile")
	for field, sc := range map[string]*PodSecurity{
		"runAsUser":  {RunAsUser: ptr(int64(0))},
		"runAsGroup": {RunAsGroup: ptr(int64(0))},
		"fsGroup":    {FSGroup: ptr(int64(-1))},
	} {
		mustFail(t, WorkloadSpec{Image: pinnedImage, SecurityContext: sc}.Validate(ProfileFull), "securityContext."+field)
	}
	mustFail(t, WorkloadSpec{Kind: KindTool, Image: pinnedImage, SecurityContext: &PodSecurity{RunAsUser: ptr(int64(1000))}}.Validate(ProfileFull), `securityContext is not supported for kind "tool"`)
	mustFail(t, WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"api"}, SecurityContext: &PodSecurity{RunAsUser: ptr(int64(1000))}}.Validate(ProfileFull),
		"securityContext is not allowed on a job with before")
}

// TestEnableServiceLinks: Full-only, pod-bearing kinds only, and nothing on a
// gating job (it has no pod of its own).
func TestEnableServiceLinks(t *testing.T) {
	ok := WorkloadSpec{Kind: KindWorker, Image: pinnedImage, EnableServiceLinks: true}
	mustPass(t, ok, ProfileFull)
	mustFail(t, ok.Validate(ProfileRestricted), "enableServiceLinks: not allowed under the restricted profile")
	mustFail(t, WorkloadSpec{Kind: KindTool, Image: pinnedImage, EnableServiceLinks: true}.Validate(ProfileFull), `enableServiceLinks is not supported for kind "tool"`)
	mustFail(t, WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"api"}, EnableServiceLinks: true}.Validate(ProfileFull),
		"enableServiceLinks is not allowed on a job with before")
	mustPass(t, WorkloadSpec{Kind: KindWorker, Image: pinnedImage}, ProfileRestricted)
}
