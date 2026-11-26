package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCovC_ListPodsInNamespace(t *testing.T) {
	logPath := covCInstallTools(t, map[string]string{"kubectl": `case "$*" in
  *"-n broken"*) exit 1;;
  *) cat <<'EOF2'
api-7d9f   1/1   Running             0               3h
worker-1   0/1   CrashLoopBackOff    4 (2m ago)      10m
short      1/1   Running
EOF2
;;
esac`})
	got := listPodsInNamespace(context.Background(), "dev-ns")
	if len(got) != 2 {
		t.Fatalf("pods = %+v; a row with fewer than five columns is dropped", got)
	}
	if got[0] != (podStatusEntry{Name: "api-7d9f", Ready: "1/1", Status: "Running", Restart: "0", Age: "3h"}) {
		t.Errorf("row 0 = %+v", got[0])
	}
	if got[1].Restart != "4" || got[1].Age != "(2m ago) 10m" || got[1].Status != "CrashLoopBackOff" {
		t.Errorf("kubectl's '4 (2m ago)' restart column is folded into AGE so the table stays five columns: %+v", got[1])
	}
	if calls := covCCalls(t, logPath); calls[0] != "kubectl get pods -n dev-ns --no-headers" {
		t.Errorf("argv = %v", calls)
	}
	if got := listPodsInNamespace(context.Background(), "broken"); got != nil {
		t.Errorf("a kubectl failure is non-fatal and empty: %v", got)
	}
}

func TestCovC_ListSiblingNamespaces(t *testing.T) {
	covCInstallTools(t, map[string]string{"kubectl": `case "$*" in
  *"managed-by=forge"*) printf 'proj-dev\nproj-dev-wt-a\n\n  proj-dev-wt-b  \n';;
  *) exit 1;;
esac`})
	got := listSiblingNamespaces(context.Background(), "k3d-proj", "proj-dev")
	if !slices.Equal(got, []string{"proj-dev-wt-a", "proj-dev-wt-b"}) {
		t.Fatalf("siblings = %v; want every forge namespace but the primary", got)
	}
	covCInstallTools(t, map[string]string{"kubectl": `exit 1`})
	if got := listSiblingNamespaces(context.Background(), "k3d-proj", "p"); got != nil {
		t.Errorf("= %v", got)
	}
}

func TestCovC_DevNamespace(t *testing.T) {
	deployEnabledProject(t)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"project":"p","env":"dev"}`))
	if got := devNamespace("cluster"); got != testProjectName+"-dev" {
		t.Errorf("default = %q; want <project>-dev", got)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{"workloads":[{"name":"s","kind":"service",
	  "runtime":{"type":"cluster","cluster":"c","namespace":"declared-ns"},"spec":{"kind":"service"}}]}}`))
	if got := devNamespace("cluster"); got != "declared-ns" {
		t.Errorf("declared = %q", got)
	}
	t.Chdir(t.TempDir())
	if got := devNamespace("fallback"); got != "fallback-dev" {
		t.Errorf("outside a project the cluster name stands in, got %q", got)
	}
}

func TestCovC_RunDevStatusHumanOutput(t *testing.T) {
	deployEnabledProject(t)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"project":"p","env":"dev"}`))
	if err := os.MkdirAll("deploy", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join("deploy", "k3d.yaml")
	if err := os.WriteFile(cfg, []byte("apiVersion: k3d.io/v1alpha5\nkind: Simple\nmetadata:\n  name: statuscl\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origList := listK3dClustersFn
	t.Cleanup(func() { listK3dClustersFn = origList })
	ctx := context.Background()

	t.Run("a down cluster says how to start it", func(t *testing.T) {
		listK3dClustersFn = func(context.Context) ([]k3dClusterListEntry, error) { return nil, nil }
		covCInstallTools(t, map[string]string{"kubectl": `exit 1`})
		var err error
		out := captureStdout(t, func() { err = runDevStatus(ctx, cfg, false) })
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Cluster statuscl: down", "kubectl context (current): (none)", "Config: " + cfg + " (found)", "Cluster is down — run `forge cluster up`"} {
			if !strings.Contains(out, want) {
				t.Errorf("out lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Pods:") {
			t.Errorf("a down cluster lists no pods:\n%s", out)
		}
	})
	t.Run("a running cluster lists pods, ingress state and sibling namespaces", func(t *testing.T) {
		listK3dClustersFn = func(context.Context) ([]k3dClusterListEntry, error) {
			return []k3dClusterListEntry{{Name: testProjectName, ServersRunning: 1, ServersCount: 1}}, nil
		}
		covCInstallTools(t, map[string]string{"kubectl": `case "$*" in
  *"current-context"*) echo "k3d-other";;
  *"get pods"*) echo "api-1   1/1   Running   0   5m";;
  *"get namespaces"*) printf 'p-dev\nsibling-ns\n';;
esac`})
		var err error
		out := captureStdout(t, func() { err = runDevStatus(ctx, "deploy/missing.yaml", false) })
		if err != nil {
			t.Fatal(err)
		}
		// The cluster name falls back to the project name when the config file is missing.
		for _, want := range []string{"Cluster " + testProjectName + ": up",
			"(expected k3d-" + testProjectName + " — run `kubectl config use-context k3d-" + testProjectName + "`)",
			"(missing)", "NAME", "api-1", "Ingress URLs:", "(ingress feature disabled)",
			"Sibling dev namespaces on this cluster:", "- sibling-ns"} {
			if !strings.Contains(out, want) {
				t.Errorf("out lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("the matching kubectl context is called out, and an empty cluster says (none)", func(t *testing.T) {
		listK3dClustersFn = func(context.Context) ([]k3dClusterListEntry, error) {
			return []k3dClusterListEntry{{Name: "statuscl", ServersRunning: 1, ServersCount: 1}}, nil
		}
		covCInstallTools(t, map[string]string{"kubectl": `case "$*" in *"current-context"*) echo "k3d-statuscl";; *) exit 0;; esac`})
		out := captureStdout(t, func() { _ = runDevStatus(ctx, cfg, false) })
		if !strings.Contains(out, "k3d-statuscl (matches expected)") || !strings.Contains(out, "  (none)") {
			t.Errorf("out:\n%s", out)
		}
	})
	t.Run("json carries the pods and siblings", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `case "$*" in
  *"get pods"*) echo "api-1   1/1   Running   0   5m";;
  *"get namespaces"*) printf 'sibling-ns\n';;
esac`})
		var err error
		out := captureStdout(t, func() { err = runDevStatus(ctx, cfg, true) })
		if err != nil {
			t.Fatal(err)
		}
		var got devStatusSummary
		if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
			t.Fatalf("%v\n%s", jerr, out)
		}
		if !got.Cluster.Exists || got.Cluster.Name != "statuscl" || len(got.Pods) != 1 || !slices.Equal(got.Siblings, []string{"sibling-ns"}) {
			t.Errorf("summary = %+v", got)
		}
	})
	t.Run("an unreadable cluster list reads as not existing", func(t *testing.T) {
		listK3dClustersFn = func(context.Context) ([]k3dClusterListEntry, error) { return nil, errors.New("k3d missing") }
		covCInstallTools(t, map[string]string{"kubectl": `exit 1`})
		out := captureStdout(t, func() { _ = runDevStatus(ctx, cfg, false) })
		if !strings.Contains(out, "Cluster statuscl: down") {
			t.Errorf("out:\n%s", out)
		}
	})
}

func TestCovC_RunDevStatusIngressSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: statusproj\nmodule_path: example.com/x\nfeatures:\n  ingress: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	markDeployProject(t, dir)
	t.Chdir(dir)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{
	  "gateways":[{"name":"public","host":"app.localtest.me","listeners":[{"name":"https","port":8443,"protocol":"HTTPS"}]}],
	  "http_routes":[{"name":"web","gateway":"public","listener":"https","service":"web","port":80,"path":"/"}]}}`))
	orig := listK3dClustersFn
	t.Cleanup(func() { listK3dClustersFn = orig })
	listK3dClustersFn = func(context.Context) ([]k3dClusterListEntry, error) { return nil, nil }
	var err error
	out := captureStdout(t, func() { err = runDevStatus(context.Background(), "deploy/k3d.yaml", true) })
	if err != nil {
		t.Fatal(err)
	}
	var got devStatusSummary
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("%v\n%s", jerr, out)
	}
	if len(got.IngressURLs) != 1 || got.IngressURLs[0].URL != "https://app.localtest.me:8443/" || got.IngressURLs[0].Route != "web" {
		t.Fatalf("ingress urls = %+v", got.IngressURLs)
	}
}
