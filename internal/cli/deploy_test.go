package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// writeKCLFixture writes a JSON fixture file the RenderKCL helper
// picks up via FORGE_KCL_RENDER_FIXTURE. Returns the fixture path so
// the test can pass it through t.Setenv.
func writeKCLFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "render.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

// TestExpectedClusterForEnv_DevDefault confirms the dev default
// of `k3d-<project>` so existing projects don't need to declare a
// K8sCluster to benefit from the guard.
func TestExpectedClusterForEnv_DevDefault(t *testing.T) {
	// Empty KCL render → fall through to the dev default.
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{}`))
	cfg := &config.ProjectConfig{Name: "cp-forge"}
	got := expectedClusterForEnv(context.Background(), cfg, "dev")
	want := "k3d-cp-forge"
	if got != want {
		t.Errorf("dev default: want %q, got %q", want, got)
	}
}

// TestExpectedClusterForEnv_KCLDeclaration confirms that
// `forge.K8sCluster.cluster` in rendered KCL takes precedence over
// the dev default.
func TestExpectedClusterForEnv_KCLDeclaration(t *testing.T) {
	body := `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "gke_acme-prod_us-central1_cluster-1"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, body))
	cfg := &config.ProjectConfig{Name: "cp-forge"}
	got := expectedClusterForEnv(context.Background(), cfg, "prod")
	want := "gke_acme-prod_us-central1_cluster-1"
	if got != want {
		t.Errorf("explicit declaration: want %q, got %q", want, got)
	}
}

// TestExpectedClusterForEnv_NoDeclaration returns empty for non-dev
// envs without an explicit cluster — the guard is skipped (with a
// notice), preserving backwards compatibility.
func TestExpectedClusterForEnv_NoDeclaration(t *testing.T) {
	body := `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "cluster"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, body))
	cfg := &config.ProjectConfig{Name: "cp-forge"}
	got := expectedClusterForEnv(context.Background(), cfg, "staging")
	if got != "" {
		t.Errorf("no declaration should return empty, got %q", got)
	}
}

// TestExpectedClusterForEnv_DevExplicitOverride confirms that even for
// dev, an explicit KCL declaration wins over the k3d-<project> default —
// supports projects with a non-default k3d cluster name.
func TestExpectedClusterForEnv_DevExplicitOverride(t *testing.T) {
	body := `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "runtime": {
          "type": "cluster",
          "cluster": "k3d-my-custom-name"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, body))
	cfg := &config.ProjectConfig{Name: "cp-forge"}
	got := expectedClusterForEnv(context.Background(), cfg, "dev")
	want := "k3d-my-custom-name"
	if got != want {
		t.Errorf("dev explicit override: want %q, got %q", want, got)
	}
}

// TestDeployDryRunHelpMentionsGuard documents that dry-run still runs
// the env-cluster guard — the change in this commit. A user reading
// `forge env deploy --help` should see that.
func TestDeployDryRunHelpMentionsGuard(t *testing.T) {
	cmd := newDeployCmd()
	f := cmd.Flags().Lookup("dry-run")
	if f == nil {
		t.Fatal("--dry-run flag not registered")
	}
	if !strings.Contains(f.Usage, "guard") {
		t.Errorf("--dry-run usage should mention the env-cluster guard, got %q", f.Usage)
	}
}

// TestDeployTargetArchFlagRegistered confirms `forge env deploy
// --target-arch` is wired so users can cross-compile from a Mac/arm64
// host onto an amd64 cluster without editing forge.yaml.
func TestDeployTargetArchFlagRegistered(t *testing.T) {
	cmd := newDeployCmd()
	f := cmd.Flags().Lookup("target-arch")
	if f == nil {
		t.Fatal("--target-arch flag not registered on deploy command")
	}
	if f.DefValue != "" {
		t.Errorf("--target-arch default = %q, want empty", f.DefValue)
	}
}

// TestResolveDeployArch verifies the deploy-side arch resolver. Unlike
// resolveBuildArch in build.go, this one ALWAYS falls back to amd64 (no
// "host arch" branch) since deploy is always building an image for a
// cluster node. Returns the empty string only when the resolved target
// matches the host arch — the signal to skip `--platform`.
func TestResolveDeployArch(t *testing.T) {
	otherArch := "amd64"
	if runtime.GOARCH == "amd64" {
		otherArch = "arm64"
	}

	cases := []struct {
		name     string
		cfgArch  string
		flagArch string
		want     string
	}{
		{
			name:     "no config, no flag → amd64 default",
			cfgArch:  "",
			flagArch: "",
			want: func() string {
				if runtime.GOARCH == "amd64" {
					return ""
				}
				return "amd64"
			}(),
		},
		{
			name:     "cfg arch matches host → empty",
			cfgArch:  runtime.GOARCH,
			flagArch: "",
			want:     "",
		},
		{
			name:     "cfg arch differs from host → cross-compile",
			cfgArch:  otherArch,
			flagArch: "",
			want:     otherArch,
		},
		{
			name:     "flag overrides cfg",
			cfgArch:  runtime.GOARCH,
			flagArch: otherArch,
			want:     otherArch,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveDeployArch(c.cfgArch, c.flagArch)
			if got != c.want {
				t.Errorf("resolveDeployArch(cfg=%q, flag=%q) = %q, want %q",
					c.cfgArch, c.flagArch, got, c.want)
			}
		})
	}
}

// TestEffectiveTargetArch covers the DeployConfig.EffectiveTargetArch
// precedence chain: explicit override > forge.yaml field > "amd64"
// default. This is the project-level reader; the CLI-level resolver
// (resolveDeployArch) layers runtime.GOARCH comparison on top of this.
func TestEffectiveTargetArch(t *testing.T) {
	cases := []struct {
		name     string
		field    string
		override string
		want     string
	}{
		{"empty → amd64", "", "", "amd64"},
		{"field wins over default", "arm64", "", "arm64"},
		{"override wins over field", "arm64", "amd64", "amd64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &config.DeployConfig{TargetArch: c.field}
			if got := d.EffectiveTargetArch(c.override); got != c.want {
				t.Errorf("EffectiveTargetArch(override=%q) with field=%q = %q, want %q",
					c.override, c.field, got, c.want)
			}
		})
	}
}

// Note: TestHostDeploymentSkipSet_DevOnly was removed when
// services[].dev_target moved to the KCL layer in feat/kcl-orchestration.
// The replacement filter reads `deploy: "host"` from rendered KCL — see
// deploy_kcl_test.go for the KCL-side equivalent coverage.

// Note: TestRenderedDeploymentNames and the empty/malformed variant
// were moved to internal/cluster/cluster_test.go alongside the function
// they exercise (cluster.RenderedDeploymentNames) when the cluster
// pipeline was lifted out of internal/cli.

// TestIsLocalCluster covers the local-cluster guard recognizer that
// gates plaintext dotenv Secret projection. Local dev contexts pass;
// remote/prod names and the empty string are rejected.
func TestIsLocalCluster(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"k3d-myproject", true},
		{"kind-myproject", true},
		{"docker-desktop", true},
		{"minikube", true},
		{"rancher-desktop", true},
		{"colima", true},
		{"orbstack", true},
		{"K3D-MyProject", true}, // case-insensitive
		{"  k3d-dev  ", true},   // trimmed
		{"prod-cluster", false},
		{"gke_proj_us-central1_prod", false},
		{"arn:aws:eks:us-east-1:123:cluster/prod", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLocalCluster(c.name); got != c.want {
			t.Errorf("isLocalCluster(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// k8sGroup is a tiny helper for the declarative-context tests: a
// k8s-cluster ServiceGroup carrying the KCL-declared cluster.
func k8sGroup(cluster, namespace string) deploytarget.ServiceGroup {
	return deploytarget.ServiceGroup{
		ProviderID: "k8s-cluster",
		Cluster:    cluster,
		Namespace:  namespace,
	}
}

// TestResolveGroupContext_DeclaredIsTheOnlySource is the core of the
// declarative-only model: the kubectl context is the group's declared
// cluster (KCL forge.K8sCluster.cluster), NOT whatever context is
// currently active — and there is no CLI override to consult.
func TestResolveGroupContext_DeclaredIsTheOnlySource(t *testing.T) {
	g := k8sGroup("gke_reliant-labs-475814_us-central1_prod", "cp-forge-prod")
	if got := resolveGroupContext(g); got != g.Cluster {
		t.Errorf("declared cluster should be the context: want %q, got %q", g.Cluster, got)
	}
}

// TestResolveGroupContext_NoClusterEmpty confirms a host-only / compose
// group with no declared cluster yields an empty context. Empty is NOT a
// silent fall-back to the active context — the cluster.KubectlApply
// chokepoint refuses an empty context on any k8s write (see
// TestKubectlApply_RefusesEmptyContext in internal/cluster).
func TestResolveGroupContext_NoClusterEmpty(t *testing.T) {
	g := deploytarget.ServiceGroup{ProviderID: "compose"}
	if got := resolveGroupContext(g); got != "" {
		t.Errorf("no declared cluster should yield empty context, got %q", got)
	}
}

// TestApplyOptsBuilder_ContextFromDeclaredCluster proves the full
// builder path: the cluster.ApplyOpts the K8sCluster provider feeds into
// every kubectl call carries the KCL-declared cluster as its Context.
// There is no override parameter to bypass it.
func TestApplyOptsBuilder_ContextFromDeclaredCluster(t *testing.T) {
	const declared = "gke_reliant-labs-475814_us-central1_prod"
	group := k8sGroup(declared, "cp-forge-prod")
	builder := applyOptsBuilderFromContext(applyOptsContext{
		MainK: "deploy/kcl/prod/main.k", ImageTag: "v1.2.3", FallbackNamespace: "fallback-ns", Env: "prod",
		Groups: []deploytarget.ServiceGroup{group},
	})
	opts := builder(group)
	if opts.Context != declared {
		t.Errorf("ApplyOpts.Context should be the declared cluster: want %q, got %q", declared, opts.Context)
	}
	if opts.Namespace != "cp-forge-prod" {
		t.Errorf("ApplyOpts.Namespace = %q, want the group namespace", opts.Namespace)
	}
	// Single-cluster env: no second cluster to isolate, so no per-group
	// manifest scoping — the apply stays byte-identical to the pre-fix path.
	if opts.ClusterScope != nil {
		t.Errorf("single-cluster env must not engage multi-cluster scoping; got %+v", opts.ClusterScope)
	}
}

// TestDeclaredEnvContext picks the first declared cluster for the
// env-wide consumers (secrets pre-apply / empty-groups apply);
// host-only envs yield empty. There is no override to consult.
func TestDeclaredEnvContext(t *testing.T) {
	groups := []deploytarget.ServiceGroup{
		{ProviderID: "external"},
		k8sGroup("gke_first", "ns"),
		k8sGroup("gke_second", "ns2"),
	}
	if got := declaredEnvContext(nil, groups); got != "gke_first" {
		t.Errorf("env context should be the first declared cluster: want %q, got %q", "gke_first", got)
	}
	if got := declaredEnvContext(nil, []deploytarget.ServiceGroup{{ProviderID: "compose"}}); got != "" {
		t.Errorf("no k8s cluster declared should yield empty, got %q", got)
	}
}

// TestDeclaredContextExistsVerdict_Present allows the deploy when the
// declared cluster IS a kubectl context in the kubeconfig.
func TestDeclaredContextExistsVerdict_Present(t *testing.T) {
	available := []string{"k3d-cp-forge", "gke_reliant-labs-475814_us-central1_prod"}
	if err := declaredContextExistsVerdict("prod", "gke_reliant-labs-475814_us-central1_prod", available); err != nil {
		t.Errorf("expected nil when declared context exists, got %v", err)
	}
}

// TestDeclaredContextExistsVerdict_Missing is the fail-fast guard that
// makes wrong-cluster deploys impossible: a declared cluster with no
// matching kubectl context refuses, naming the env, the cluster, and the
// available contexts.
func TestDeclaredContextExistsVerdict_Missing(t *testing.T) {
	available := []string{"k3d-cp-forge", "gke_other_us-central1_staging"}
	err := declaredContextExistsVerdict("prod", "gke_reliant-labs-475814_us-central1_prod", available)
	if err == nil {
		t.Fatal("expected error when declared context is absent, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"prod",
		"gke_reliant-labs-475814_us-central1_prod",
		"refusing to deploy",
		"k3d-cp-forge",                  // available list
		"gke_other_us-central1_staging", // available list
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got:\n%s", want, msg)
		}
	}
}

// TestDeclaredContextExistsVerdict_NoDeclaration is a no-op when the env
// declares no cluster (host-only / compose) — the guard is skipped.
func TestDeclaredContextExistsVerdict_NoDeclaration(t *testing.T) {
	if err := declaredContextExistsVerdict("staging", "", []string{"k3d-cp-forge"}); err != nil {
		t.Errorf("expected nil when no cluster declared, got %v", err)
	}
}
