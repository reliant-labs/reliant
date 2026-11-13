// File: internal/cli/deploy_namespace_check_test.go
//
// Tests for `checkNamespaceReferences`. The check is pure (entities +
// projectName + resolvedNamespace → error) so these tests skip the
// filesystem and the KCL renderer entirely and exercise the heuristic
// across the cases we care about:
//
//   - exact match: no error
//   - project-prefixed mismatch (the cp-forge-dev smoke regression):
//     loud error naming the offending env var + suggesting both fixes
//   - foreign-namespace reference (shared `nats-system`): legitimate,
//     no error
//   - mixed (foreign + project-prefixed match): no error
//   - mixed (foreign + project-prefixed mismatch): error mentions ONLY
//     the project-prefixed value
//   - operator + cronjob env_vars are scanned too
//   - cluster deploy env_vars are scanned
//   - empty / nil entities: no error

package cli

import (
	"strings"
	"testing"
)

// withKCLEnv sets literal spec.env values from KCLEnvVar pairs.
func withKCLEnv(envs ...KCLEnvVar) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		for _, e := range envs {
			withEnv(e.Name, e.Value)(w)
		}
	}
}

func mkServiceWithEnv(name string, envs ...KCLEnvVar) WorkloadEntity {
	return hostWL(name, withKCLEnv(envs...))
}

func mkClusterService(name string, envs ...KCLEnvVar) WorkloadEntity {
	return clusterWL(name, "k3d-dev", "myapp-dev", withKCLEnv(envs...))
}

func TestCheckNamespaceReferences_NoEnvVars(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{{Name: "api"}},
	}
	if err := checkNamespaceReferences(entities, "myapp", "myapp-dev"); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestCheckNamespaceReferences_ExactMatch(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			mkServiceWithEnv("api",
				KCLEnvVar{Name: "NATS_URL", Value: "nats://nats.myapp-dev.svc.cluster.local:4222"},
				KCLEnvVar{Name: "TEMPORAL_HOST", Value: "temporal.myapp-dev.svc.cluster.local:7233"},
			),
		},
	}
	if err := checkNamespaceReferences(entities, "myapp", "myapp-dev"); err != nil {
		t.Errorf("matched namespace should not error, got: %v", err)
	}
}

// TestCheckNamespaceReferences_ProjectPrefixedMismatch is the cp-forge-dev
// regression: KCL hardcoded `cp-forge-dev` but forge.yaml resolved to
// `cp-forge-dev-host`. The pod CrashLoops with DNS errors; the deploy
// step itself silently succeeds. This must error LOUD.
func TestCheckNamespaceReferences_ProjectPrefixedMismatch(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			mkServiceWithEnv("api",
				KCLEnvVar{Name: "NATS_URL", Value: "nats://nats.cp-forge-dev.svc.cluster.local:4222"},
			),
		},
	}
	err := checkNamespaceReferences(entities, "cp-forge", "cp-forge-dev-host")
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	msg := err.Error()
	// Must name BOTH namespaces — operator needs to see what's pointed
	// at what so they can pick the fix.
	if !strings.Contains(msg, "cp-forge-dev-host") {
		t.Errorf("error must name resolved namespace, got: %s", msg)
	}
	if !strings.Contains(msg, "cp-forge-dev") {
		t.Errorf("error must name referenced namespace, got: %s", msg)
	}
	// Must name the offending env var so grep works.
	if !strings.Contains(msg, "NATS_URL") {
		t.Errorf("error must name offending env var, got: %s", msg)
	}
	// Must suggest BOTH fix paths. Different projects pick different
	// canonical answers (some pin namespace in forge.yaml, some fix
	// the KCL).
	if !strings.Contains(msg, "environments[<env>].namespace") {
		t.Errorf("error must suggest forge.yaml namespace declaration, got: %s", msg)
	}
	if !strings.Contains(msg, "Update the KCL env_var values") {
		t.Errorf("error must suggest fixing KCL literals, got: %s", msg)
	}
}

// TestCheckNamespaceReferences_ForeignNamespaceAllowed: a project
// legitimately consuming `nats.nats-system.svc.cluster.local` (shared
// infra in its own namespace) is fine. The heuristic must not flag
// foreign namespaces.
func TestCheckNamespaceReferences_ForeignNamespaceAllowed(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			mkServiceWithEnv("api",
				KCLEnvVar{Name: "NATS_URL", Value: "nats://nats.nats-system.svc.cluster.local:4222"},
				KCLEnvVar{Name: "VAULT_ADDR", Value: "https://vault.security.svc.cluster.local"},
			),
		},
	}
	if err := checkNamespaceReferences(entities, "myapp", "myapp-dev"); err != nil {
		t.Errorf("foreign namespace should be allowed, got: %v", err)
	}
}

// TestCheckNamespaceReferences_MixedForeignAndProjectMismatch: when
// foreign references coexist with a project-prefixed mismatch, the
// error must call out ONLY the project one — flagging shared infra
// would noise up the message and lose the actual signal.
func TestCheckNamespaceReferences_MixedForeignAndProjectMismatch(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			mkServiceWithEnv("api",
				KCLEnvVar{Name: "NATS_URL", Value: "nats://nats.nats-system.svc.cluster.local:4222"},
				KCLEnvVar{Name: "TEMPORAL_HOST", Value: "temporal.myapp-staging.svc.cluster.local:7233"},
			),
		},
	}
	err := checkNamespaceReferences(entities, "myapp", "myapp-prod")
	if err == nil {
		t.Fatal("expected mismatch error from project-prefixed value, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "nats-system") {
		t.Errorf("foreign namespace should not appear in error, got: %s", msg)
	}
	if !strings.Contains(msg, "myapp-staging") {
		t.Errorf("project-prefixed mismatch must appear in error, got: %s", msg)
	}
}

// TestCheckNamespaceReferences_OperatorsAndCronjobs: the env of an
// operator and a cron workload is scanned too — they share the same
// in-cluster DNS resolution path so the same foot-gun applies.
func TestCheckNamespaceReferences_OperatorsAndCronjobs(t *testing.T) {
	scaler := mkClusterService("scaler", KCLEnvVar{Name: "METRICS_URL", Value: "http://metrics.myapp-dev.svc.cluster.local"})
	scaler.Kind = "operator"
	reaper := mkClusterService("nightly-reaper", KCLEnvVar{Name: "DB_URL", Value: "postgres://postgres.myapp-dev.svc.cluster.local/db"})
	reaper.Kind = "cron"
	entities := &KCLEntities{Workloads: []WorkloadEntity{scaler, reaper}}
	err := checkNamespaceReferences(entities, "myapp", "myapp-prod")
	if err == nil {
		t.Fatal("expected mismatch error from operator/cronjob env_vars, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, `operator "scaler" (cluster)`) {
		t.Errorf("error must name owning operator, got: %s", msg)
	}
	if !strings.Contains(msg, `cron "nightly-reaper" (cluster)`) {
		t.Errorf("error must name owning cronjob, got: %s", msg)
	}
}

// TestCheckNamespaceReferences_ClusterDeployEnvVarsScanned: KCL
// renders env_vars on the K8sCluster deploy block too (not just at
// the top-level WorkloadEntity). Missing this case would silently
// skip the bulk of the failure surface — cluster-deploy is where
// service-to-service DNS lives.
func TestCheckNamespaceReferences_ClusterDeployEnvVarsScanned(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			mkClusterService("api",
				KCLEnvVar{Name: "NATS_URL", Value: "nats://nats.myapp-staging.svc.cluster.local"},
			),
		},
	}
	err := checkNamespaceReferences(entities, "myapp", "myapp-prod")
	if err == nil {
		t.Fatal("expected mismatch error from cluster-deploy env_vars, got nil")
	}
	if !strings.Contains(err.Error(), `service "api" (cluster)`) {
		t.Errorf("error owner label should name the workload and its cluster runtime, got: %v", err)
	}
}

// TestCheckNamespaceReferences_DeduplicatesByOwnerAndName: a project
// with many services all wrong the same way should produce one
// readable error, not N copies of the same offence.
func TestCheckNamespaceReferences_DeduplicatesByOwnerAndName(t *testing.T) {
	dupValue := "nats://nats.myapp-dev.svc.cluster.local"
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			mkServiceWithEnv("api",
				KCLEnvVar{Name: "NATS_URL", Value: dupValue},
				KCLEnvVar{Name: "NATS_URL", Value: dupValue}, // exact dup
			),
		},
	}
	err := checkNamespaceReferences(entities, "myapp", "myapp-prod")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// The same (owner, name, namespace) triple must only appear once.
	if got := strings.Count(err.Error(), "NATS_URL"); got != 1 {
		t.Errorf("duplicate env var entries should collapse, got %d NATS_URL lines:\n%s", got, err.Error())
	}
}

// TestCheckNamespaceReferences_GuardClauses confirms the check is a
// no-op when any of the required inputs is missing. The caller in
// runDeploy already gates on hasK8sServices, but the function must
// also be safe to call with zero values for unit-test simplicity.
func TestCheckNamespaceReferences_GuardClauses(t *testing.T) {
	if err := checkNamespaceReferences(nil, "myapp", "myapp-prod"); err != nil {
		t.Errorf("nil entities should not error, got: %v", err)
	}
	entities := &KCLEntities{Workloads: []WorkloadEntity{mkServiceWithEnv("api",
		KCLEnvVar{Name: "X", Value: "x.myapp-dev.svc.cluster.local"},
	)}}
	if err := checkNamespaceReferences(entities, "", "myapp-prod"); err != nil {
		t.Errorf("empty projectName should not error, got: %v", err)
	}
	if err := checkNamespaceReferences(entities, "myapp", ""); err != nil {
		t.Errorf("empty resolvedNamespace should not error, got: %v", err)
	}
}

func TestIsProjectPrefixed(t *testing.T) {
	cases := []struct {
		ns, project string
		want        bool
	}{
		{"myapp", "myapp", true},          // bare match
		{"myapp-dev", "myapp", true},      // canonical <project>-<env>
		{"myapp-prod", "myapp", true},     // another env
		{"myapp-dev-host", "myapp", true}, // multi-segment env
		{"myapp2", "myapp", false},        // hyphen boundary matters
		{"nats-system", "myapp", false},   // foreign infra
		{"shared", "myapp", false},        // foreign infra
		{"", "myapp", false},              // defensive: empty namespace
	}
	for _, c := range cases {
		got := isProjectPrefixed(c.ns, c.project)
		if got != c.want {
			t.Errorf("isProjectPrefixed(%q, %q) = %v, want %v", c.ns, c.project, got, c.want)
		}
	}
}
