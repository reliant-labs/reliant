package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

const covCClusterBundle = `{"output":{"cluster_target":{"cluster":"gke_prod","namespace":"prod-ns"},
  "workloads":[{"name":"api","kind":"service","image":"registry.example/prod/api",
    "build":{"type":"go","cmd":"./cmd/api","output_name":"api"},
    "runtime":{"type":"cluster","cluster":"gke_prod","namespace":"prod-ns"},"spec":{"kind":"service"}}]}}`

func TestCovC_DeclaredContextExistsVerdict(t *testing.T) {
	if err := declaredContextExistsVerdict("prod", "", []string{"a"}); err != nil {
		t.Errorf("no declared cluster has nothing to verify: %v", err)
	}
	if err := declaredContextExistsVerdict("prod", "b", []string{"a", "b"}); err != nil {
		t.Errorf("present: %v", err)
	}
	err := declaredContextExistsVerdict("prod", "gke_prod", []string{"k3d-x", "other"})
	if err == nil {
		t.Fatal("a missing context must refuse")
	}
	for _, want := range []string{`env "prod" declares cluster "gke_prod"`, "available contexts: k3d-x, other", "refusing to deploy", "gcloud container clusters get-credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "forge cluster up") {
		t.Errorf("only a k3d- context gets the create-it hint:\n%v", err)
	}
	err = declaredContextExistsVerdict("dev", "k3d-proj", nil)
	if err == nil || !strings.Contains(err.Error(), "available contexts: (none)") || !strings.Contains(err.Error(), `forge cluster up`) || !strings.Contains(err.Error(), `"k3d-proj"`) {
		t.Errorf("a missing local cluster says how to create it: %v", err)
	}
}

func TestCovC_KubectlContextNames(t *testing.T) {
	covCInstallTools(t, map[string]string{"kubectl": `printf 'k3d-a\n\n  gke_b  \n'`})
	got, err := kubectlContextNames(context.Background())
	if err != nil || !slices.Equal(got, []string{"k3d-a", "gke_b"}) {
		t.Fatalf("= %v, %v", got, err)
	}
	covCInstallTools(t, map[string]string{"kubectl": `exit 1`})
	if _, err := kubectlContextNames(context.Background()); err == nil || !strings.Contains(err.Error(), "is kubectl installed and configured?") {
		t.Errorf("err = %v", err)
	}
}

func TestCovC_ComputeDeployGuard(t *testing.T) {
	ctx := context.Background()
	cfg := &config.ProjectConfig{Name: "proj"}
	current := `case "$*" in *current-context*) echo "k3d-ambient";; *get-contexts*) printf '%s\n' $COVC_CONTEXTS;; esac`

	t.Run("an env that declares no cluster is allowed with a hint", func(t *testing.T) {
		t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{}}`))
		covCInstallTools(t, map[string]string{"kubectl": current})
		g := computeDeployGuard(ctx, cfg, "staging")
		if g.Verdict != deployGuardVerdictAllow || g.Reason != deployGuardReasonNoClusterDeclared || g.CurrentContext != "k3d-ambient" || !strings.Contains(g.Fix, "deploy/kcl/staging/main.k") {
			t.Fatalf("guard = %+v", g)
		}
		out := captureStdout(t, func() { renderDeployGuardText("staging", g) })
		for _, want := range []string{"forge env deploy staging — declared-cluster guard", "declared context: (not declared)", "current context:  k3d-ambient", "verdict: ALLOW (no cluster declared"} {
			if !strings.Contains(out, want) {
				t.Errorf("text lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("dev defaults to the project's k3d cluster", func(t *testing.T) {
		t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{}}`))
		covCInstallTools(t, map[string]string{"kubectl": current})
		t.Setenv("COVC_CONTEXTS", "k3d-proj")
		g := computeDeployGuard(ctx, cfg, "dev")
		if g.Verdict != deployGuardVerdictAllow || g.Reason != deployGuardReasonContextDeclared || g.DeclaredContext != "k3d-proj" {
			t.Fatalf("guard = %+v", g)
		}
		out := captureStdout(t, func() { renderDeployGuardText("dev", g) })
		if !strings.Contains(out, "verdict: ALLOW (declared context exists") {
			t.Errorf("text:\n%s", out)
		}
	})
	t.Run("a declared context missing from the kubeconfig refuses and lists what exists", func(t *testing.T) {
		t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, covCClusterBundle))
		covCInstallTools(t, map[string]string{"kubectl": current})
		t.Setenv("COVC_CONTEXTS", "k3d-x other")
		g := computeDeployGuard(ctx, cfg, "prod")
		if g.Verdict != deployGuardVerdictRefuse || g.Reason != deployGuardReasonDeclaredContextMissing || g.DeclaredContext != "gke_prod" || !slices.Equal(g.AvailableContexts, []string{"k3d-x", "other"}) {
			t.Fatalf("guard = %+v", g)
		}
		out := captureStdout(t, func() { renderDeployGuardText("prod", g) })
		for _, want := range []string{"declared context: gke_prod", "available:        k3d-x, other", "verdict: REFUSE (declared context not in kubeconfig)"} {
			if !strings.Contains(out, want) {
				t.Errorf("text lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("a kubectl that cannot list contexts refuses with its error as the fix", func(t *testing.T) {
		t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, covCClusterBundle))
		covCInstallTools(t, map[string]string{"kubectl": `exit 1`})
		g := computeDeployGuard(ctx, cfg, "prod")
		if g.Verdict != deployGuardVerdictRefuse || g.Reason != deployGuardReasonKubectlUnavailable || !strings.Contains(g.Fix, "kubectl config get-contexts") || g.CurrentContext != "" {
			t.Fatalf("guard = %+v", g)
		}
		out := captureStdout(t, func() { renderDeployGuardText("prod", g) })
		if !strings.Contains(out, "(none — kubectl not configured)") || !strings.Contains(out, "verdict: REFUSE (kubectl not configured)") {
			t.Errorf("text:\n%s", out)
		}
	})
}

func TestCovC_VerifyKubectlContextAndDeclaredContexts(t *testing.T) {
	ctx := context.Background()
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, covCClusterBundle))
	covCInstallTools(t, map[string]string{"kubectl": `printf '%s\n' $COVC_CONTEXTS`})
	cfg := &config.ProjectConfig{Name: "proj"}

	t.Setenv("COVC_CONTEXTS", "gke_prod")
	out := captureStdout(t, func() {
		if err := verifyKubectlContext(ctx, cfg, "prod"); err != nil {
			t.Errorf("verify: %v", err)
		}
	})
	if !strings.Contains(out, "kubectl context: gke_prod (declared by env prod") {
		t.Errorf("out = %q", out)
	}
	t.Setenv("COVC_CONTEXTS", "elsewhere")
	if err := verifyKubectlContext(ctx, cfg, "prod"); err == nil || !strings.Contains(err.Error(), "no such kubectl context exists") {
		t.Errorf("err = %v", err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{}}`))
	out = captureStdout(t, func() {
		if err := verifyKubectlContext(ctx, cfg, "staging"); err != nil {
			t.Errorf("an env with no declared cluster skips the guard: %v", err)
		}
	})
	if !strings.Contains(out, "declared-cluster guard skipped") {
		t.Errorf("out = %q", out)
	}

	groups := []deploytarget.ServiceGroup{
		{ProviderID: "k8s-cluster", Cluster: "b-ctx"}, {ProviderID: "k8s-cluster", Cluster: "a-ctx"},
		{ProviderID: "k8s-cluster", Cluster: "a-ctx"}, {ProviderID: "compose"}, {ProviderID: "k8s-cluster"},
	}
	t.Setenv("COVC_CONTEXTS", "a-ctx b-ctx")
	if err := verifyDeclaredContextsExist(ctx, "prod", groups); err != nil {
		t.Errorf("all present: %v", err)
	}
	t.Setenv("COVC_CONTEXTS", "a-ctx")
	err := verifyDeclaredContextsExist(ctx, "prod", groups)
	if err == nil || !strings.Contains(err.Error(), `declares cluster "b-ctx"`) {
		t.Errorf("the first missing context (sorted) is named: %v", err)
	}
	if err := verifyDeclaredContextsExist(ctx, "prod", []deploytarget.ServiceGroup{{ProviderID: "compose"}}); err != nil {
		t.Errorf("no cluster groups, no kubectl needed: %v", err)
	}
	covCInstallTools(t, map[string]string{"kubectl": `exit 1`})
	if err := verifyDeclaredContextsExist(ctx, "prod", groups); err == nil {
		t.Error("kubectl failure must surface")
	}
}

func TestCovC_DeclaredRepositoryLookups(t *testing.T) {
	ctx := context.Background()
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, covCClusterBundle))
	dests := declaredImageDestinationsForEnv(ctx, "prod")
	if len(dests) != 1 || dests[0].repository != "registry.example/prod/api" || dests[0].workload != "api" {
		t.Fatalf("destinations = %+v", dests)
	}
	if got := declaredProjectRepositoryForEnv(ctx, "prod", "api"); got != "registry.example/prod/api" {
		t.Errorf("repository = %q", got)
	}
	if got := declaredProjectRepositoryForEnv(ctx, "prod", "other"); got != "" {
		t.Errorf("an image no workload declares has no repository: %q", got)
	}
	if got := declaredImageDestinationsForEnv(ctx, ""); got != nil {
		t.Errorf("empty env = %v", got)
	}
	prev := renderKCLRawHook
	t.Cleanup(func() { renderKCLRawHook = prev })
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", "")
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) { return nil, errors.New("kcl failed") }
	if got := declaredImageDestinationsForEnv(ctx, "prod"); got != nil {
		t.Errorf("a render failure yields no destinations: %v", got)
	}
}

func TestCovC_ResolveDeployNamespacePrecedence(t *testing.T) {
	ctx := context.Background()
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, covCClusterBundle))
	if got := resolveDeployNamespace(ctx, "explicit", "prod", "proj"); got != "explicit" {
		t.Errorf("flag = %q", got)
	}
	if got := resolveDeployNamespace(ctx, "", "prod", "proj"); got != "prod-ns" {
		t.Errorf("declared = %q", got)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{}}`))
	if got := resolveDeployNamespace(ctx, "", "staging", "proj"); got != "proj-staging" {
		t.Errorf("convention = %q", got)
	}
}

func TestCovC_IsInsecureRegistryAndImageExists(t *testing.T) {
	for ref, want := range map[string]bool{
		"localhost:5050/app:v1": true, "127.0.0.1:5000/app": true, "registry.localhost:5000/app": true,
		"ghcr.io/acme/app:v1": false, "gcr.io/p/app": false, "localhost.evil.com/app": false, "app": false,
	} {
		if got := isInsecureRegistry(ref); got != want {
			t.Errorf("isInsecureRegistry(%q) = %v; want %v", ref, got, want)
		}
	}
	logPath := covCInstallTools(t, map[string]string{"docker": `case "$*" in *missing*) exit 1;; esac`})
	ctx := context.Background()
	if !imageExistsInRegistry(ctx, "ghcr.io/acme/app:v1") || imageExistsInRegistry(ctx, "ghcr.io/acme/missing:v1") {
		t.Error("existence follows docker manifest inspect's exit status")
	}
	imageExistsInRegistry(ctx, "localhost:5050/app:v1")
	calls := covCCalls(t, logPath)
	if calls[0] != "docker manifest inspect ghcr.io/acme/app:v1" || calls[2] != "docker manifest inspect --insecure localhost:5050/app:v1" {
		t.Errorf("a local registry is queried with --insecure, a remote one is not: %v", calls)
	}
}

func TestCovC_ResolveDeployArch(t *testing.T) {
	host := runtime.GOARCH
	other := "arm64"
	if host == "arm64" {
		other = "amd64"
	}
	for name, tc := range map[string]struct{ cfg, flag, want string }{
		"flag beats config":                    {other, host, ""},
		"config used when flag empty":          {other, "", other},
		"native arch needs no cross build":     {host, "", ""},
		"default amd64 on a non-amd64 host":    {"", "", map[bool]string{true: "", false: "amd64"}[host == "amd64"]},
		"flag differing from the host is kept": {"", other, other},
	} {
		if got := resolveDeployArch(tc.cfg, tc.flag); got != tc.want {
			t.Errorf("%s: = %q; want %q", name, got, tc.want)
		}
	}
}

func TestCovC_FallbackRegistriesYAMLAndGitSHA(t *testing.T) {
	p, err := writeFallbackRegistriesYAML()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p)
	b, _ := os.ReadFile(p)
	if string(b) != fallbackRegistriesYAML || !strings.Contains(string(b), `"localhost:5050"`) {
		t.Errorf("registries.yaml = %s", b)
	}
	if filepath.Ext(p) != ".yaml" {
		t.Errorf("name = %s", p)
	}
}

func TestCovC_EnsureDevCluster(t *testing.T) {
	ctx := context.Background()
	t.Run("k3d missing explains how to install it", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		var err error
		captureStdout(t, func() { err = ensureDevCluster(ctx) })
		if err == nil || !strings.Contains(err.Error(), "k3d not available") || !strings.Contains(err.Error(), "https://k3d.io") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an existing cluster is left alone with the mirror tip", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"k3d": `echo '[{"name":"dev"}]'`})
		var err error
		out := captureStdout(t, func() { err = ensureDevCluster(ctx) })
		if err != nil || !strings.Contains(out, "k3d cluster found") || !strings.Contains(out, "localhost:5050") {
			t.Fatalf("err %v out %q", err, out)
		}
		if len(covCCalls(t, logPath)) != 1 {
			t.Errorf("only the list call is expected: %v", covCCalls(t, logPath))
		}
	})
	t.Run("no clusters creates the default shape with the fallback registry config", func(t *testing.T) {
		covCIsolateKubeconfig(t)
		t.Chdir(t.TempDir())
		origStorage, origKmsg := addClusterStorageArgsFn, addK3dKmsgArgsFn
		t.Cleanup(func() { addClusterStorageArgsFn, addK3dKmsgArgsFn = origStorage, origKmsg })
		addClusterStorageArgsFn = func(a []string) ([]string, error) { return a, nil }
		addK3dKmsgArgsFn = func(a []string) ([]string, error) { return a, nil }
		logPath := covCInstallTools(t, map[string]string{"k3d": `case "$*" in "cluster list"*) echo '[]';; esac`})
		var err error
		captureStdout(t, func() { err = ensureDevCluster(ctx) })
		if err != nil {
			t.Fatal(err)
		}
		calls := covCCalls(t, logPath)
		create := calls[len(calls)-1]
		for _, want := range []string{"cluster create dev", "--registry-create dev-registry:0.0.0.0:5050", "--registry-config", "--servers 1", "--no-lb"} {
			if !strings.Contains(create, want) {
				t.Errorf("create argv lacks %q: %s", want, create)
			}
		}
	})
	t.Run("a failing create is reported", func(t *testing.T) {
		covCIsolateKubeconfig(t)
		t.Chdir(t.TempDir())
		covCInstallTools(t, map[string]string{"k3d": `case "$*" in "cluster list"*) echo '[]';; *) echo boom >&2; exit 1;; esac`})
		var err error
		captureStdout(t, func() { err = ensureDevCluster(ctx) })
		if err == nil || !strings.Contains(err.Error(), "failed to create k3d cluster") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a project k3d.yaml is the cluster's source of truth", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.MkdirAll("deploy", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("deploy", "k3d.yaml"), []byte("apiVersion: k3d.io/v1alpha5\nkind: Simple\nmetadata:\n  name: mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		origStorage, origKmsg := addClusterStorageArgsFn, addK3dKmsgArgsFn
		t.Cleanup(func() { addClusterStorageArgsFn, addK3dKmsgArgsFn = origStorage, origKmsg })
		addClusterStorageArgsFn = func(a []string) ([]string, error) { return a, nil }
		addK3dKmsgArgsFn = func(a []string) ([]string, error) { return a, nil }
		args, cleanup, err := devClusterCreateArgs()
		defer cleanup()
		if err != nil || len(args) != 4 || args[2] != "--config" {
			t.Fatalf("args = %v, %v", args, err)
		}
		boom := errors.New("no disk")
		addClusterStorageArgsFn = func([]string) ([]string, error) { return nil, boom }
		if _, _, err := devClusterCreateArgs(); !errors.Is(err, boom) {
			t.Errorf("a storage-arg failure is returned: %v", err)
		}
	})
}
