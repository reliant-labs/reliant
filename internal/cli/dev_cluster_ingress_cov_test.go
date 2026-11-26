package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// covCSetHome points os.UserHomeDir at dir on every platform.
func covCSetHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func TestCovC_IngressCacheDirLivesUnderTheUserCache(t *testing.T) {
	home := t.TempDir()
	covCSetHome(t, home)
	got, err := ingressCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".cache", "forge", "ingress"); got != want {
		t.Fatalf("cache dir = %q; want %q", got, want)
	}
}

func TestCovC_FetchCachedCRDYAML(t *testing.T) {
	var hits atomic.Int32
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("kind: CustomResourceDefinition\n"))
	}))
	defer srv.Close()
	ctx := context.Background()

	t.Run("cold fetch downloads once and later calls hit the cache", func(t *testing.T) {
		home := t.TempDir()
		covCSetHome(t, home)
		hits.Store(0)
		var path string
		var err error
		out := captureStdout(t, func() { path, err = fetchCachedCRDYAML(ctx, "crds-v1.yaml", srv.URL, "test CRDs v1") })
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if want := filepath.Join(home, ".cache", "forge", "ingress", "crds-v1.yaml"); path != want {
			t.Fatalf("path = %q; want %q", path, want)
		}
		if b, _ := os.ReadFile(path); string(b) != "kind: CustomResourceDefinition\n" {
			t.Fatalf("cached body = %q", b)
		}
		if !strings.Contains(out, "Fetching test CRDs v1 from upstream") {
			t.Errorf("cold fetch is announced; out = %q", out)
		}
		if _, err := fetchCachedCRDYAML(ctx, "crds-v1.yaml", srv.URL, "test CRDs v1"); err != nil || hits.Load() != 1 {
			t.Fatalf("second call: err %v, server hits %d; want the cache to answer", err, hits.Load())
		}
		if tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp")); len(tmps) != 0 {
			t.Errorf("temp files left behind: %v", tmps)
		}
	})
	t.Run("non-200 is an error and leaves no cache entry", func(t *testing.T) {
		home := t.TempDir()
		covCSetHome(t, home)
		status = http.StatusNotFound
		defer func() { status = http.StatusOK }()
		var err error
		captureStdout(t, func() { _, err = fetchCachedCRDYAML(ctx, "crds-v2.yaml", srv.URL, "x") })
		if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Fatalf("err = %v; want the status code", err)
		}
		if _, statErr := os.Stat(filepath.Join(home, ".cache", "forge", "ingress", "crds-v2.yaml")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("a failed download must not be cached: %v", statErr)
		}
	})
	t.Run("unreachable server", func(t *testing.T) {
		covCSetHome(t, t.TempDir())
		dead := httptest.NewServer(http.NotFoundHandler())
		url := dead.URL
		dead.Close()
		var err error
		captureStdout(t, func() { _, err = fetchCachedCRDYAML(ctx, "crds-v3.yaml", url, "x") })
		if err == nil || !strings.Contains(err.Error(), "download "+url) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("malformed url", func(t *testing.T) {
		covCSetHome(t, t.TempDir())
		var err error
		captureStdout(t, func() { _, err = fetchCachedCRDYAML(ctx, "crds-v4.yaml", "http://bad host/\x7f", "x") })
		if err == nil {
			t.Fatal("a URL that cannot form a request must fail")
		}
	})
	t.Run("cache dir that cannot be created", func(t *testing.T) {
		home := t.TempDir()
		covCSetHome(t, home)
		// A file where the .cache directory should be.
		if err := os.WriteFile(filepath.Join(home, ".cache"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := fetchCachedCRDYAML(ctx, "c.yaml", srv.URL, "x")
		if err == nil || !strings.Contains(err.Error(), "create cache dir") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_CRDFetchersUseVersionPinnedURLsAndCacheNames(t *testing.T) {
	if got := certManagerCRDsURL("v1.17.2"); got != "https://github.com/cert-manager/cert-manager/releases/download/v1.17.2/cert-manager.crds.yaml" {
		t.Errorf("cert-manager URL = %q", got)
	}
	// A pre-seeded cache answers without any network: this proves each fetcher keys
	// its cache file on the pinned version.
	home := t.TempDir()
	covCSetHome(t, home)
	dir := filepath.Join(home, ".cache", "forge", "ingress")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gateway-api-crds-v1.2.0.yaml", "cert-manager-crds-v1.17.2.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := fetchGatewayAPICRDs(context.Background(), "v1.2.0"); err != nil || filepath.Base(got) != "gateway-api-crds-v1.2.0.yaml" {
		t.Errorf("gateway = %q, %v", got, err)
	}
	if got, err := fetchCertManagerCRDs(context.Background(), "v1.17.2"); err != nil || filepath.Base(got) != "cert-manager-crds-v1.17.2.yaml" {
		t.Errorf("cert-manager = %q, %v", got, err)
	}
}

func TestCovC_KubeContextArgs(t *testing.T) {
	if got := kubeContextArgs(""); got != nil {
		t.Errorf("empty context must inherit the pinned one; got %v", got)
	}
	if got := kubeContextArgs("k3d-cp"); !slices.Equal(got, []string{"--context", "k3d-cp"}) {
		t.Errorf("got %v", got)
	}
}

func TestCovC_KubectlApplyBytes(t *testing.T) {
	logPath := covCInstallTools(t, map[string]string{"kubectl": `cat > "$COVC_LOG.stdin"; [ -n "$COVC_FAIL" ] && exit 1; exit 0`})
	if err := kubectlApplyBytes(context.Background(), "k3d-cp", []byte("kind: Secret\n")); err != nil {
		t.Fatal(err)
	}
	if calls := covCCalls(t, logPath); len(calls) != 1 || calls[0] != "kubectl --context k3d-cp apply -f -" {
		t.Fatalf("calls = %v", calls)
	}
	if b, _ := os.ReadFile(logPath + ".stdin"); string(b) != "kind: Secret\n" {
		t.Fatalf("manifest piped to kubectl = %q", b)
	}
	t.Setenv("COVC_FAIL", "1")
	if err := kubectlApplyBytes(context.Background(), "", []byte("x")); err == nil || !strings.Contains(err.Error(), "kubectl apply") {
		t.Fatalf("err = %v", err)
	}
}

func TestCovC_MergeK3dConfig(t *testing.T) {
	const userCfg = "apiVersion: k3d.io/v1alpha5\nkind: Simple\nmetadata:\n  name: cp\nservers: 1\n"
	write := func(t *testing.T, dir, name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("nothing to project hands back the user's own file", func(t *testing.T) {
		dir := t.TempDir()
		p := write(t, dir, "k3d.yaml", userCfg)
		got, cleanup, err := mergeK3dConfig(p, k3dConfigOverlay{})
		defer cleanup()
		if err != nil || got.path != p || got.temporary {
			t.Fatalf("= %+v, %v", got, err)
		}
	})
	t.Run("host ports without a fragment file is not an error and not a merge", func(t *testing.T) {
		dir := t.TempDir()
		p := write(t, dir, "k3d.yaml", userCfg)
		got, cleanup, err := mergeK3dConfig(p, k3dConfigOverlay{HostPorts: true})
		defer cleanup()
		if err != nil || got.temporary {
			t.Fatalf("= %+v, %v", got, err)
		}
	})
	t.Run("fragment, api port and CIDRs are projected into a temp file that cleanup removes", func(t *testing.T) {
		dir := t.TempDir()
		p := write(t, dir, "k3d.yaml", userCfg)
		write(t, dir, "k3d-ports.yaml", "ports:\n  - port: 19190:19190\n    nodeFilters:\n      - loadbalancer\n")
		got, cleanup, err := mergeK3dConfig(p, k3dConfigOverlay{HostPorts: true, APIPort: 6443, ClusterCIDR: "10.52.0.0/16", ServiceCIDR: "10.53.0.0/16"})
		if err != nil {
			t.Fatal(err)
		}
		if !got.temporary || got.path == p {
			t.Fatalf("merged config must be a separate temp file: %+v", got)
		}
		b, err := os.ReadFile(got.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"19190:19190", "hostPort", "6443", "--cluster-cidr=10.52.0.0/16", "--service-cidr=10.53.0.0/16", "server:*"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("merged config lacks %q:\n%s", want, b)
			}
		}
		if orig, _ := os.ReadFile(p); string(orig) != userCfg {
			t.Errorf("the user's own file was modified:\n%s", orig)
		}
		cleanup()
		if _, err := os.Stat(got.path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cleanup left the temp file behind: %v", err)
		}
	})
	t.Run("unreadable user file", func(t *testing.T) {
		_, cleanup, err := mergeK3dConfig(filepath.Join(t.TempDir(), "absent.yaml"), k3dConfigOverlay{APIPort: 6443})
		defer cleanup()
		if err == nil || !strings.Contains(err.Error(), "read ") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a config that disagrees with the declared api_port is refused", func(t *testing.T) {
		dir := t.TempDir()
		p := write(t, dir, "k3d.yaml", userCfg+"kubeAPI:\n  hostPort: \"7000\"\n")
		_, cleanup, err := mergeK3dConfig(p, k3dConfigOverlay{APIPort: 6443})
		defer cleanup()
		if err == nil || !strings.Contains(err.Error(), "declares api_port = 6443") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a config that already carries the declared api_port is returned untouched", func(t *testing.T) {
		dir := t.TempDir()
		p := write(t, dir, "k3d.yaml", userCfg+"kubeAPI:\n  hostPort: \"6443\"\n")
		got, cleanup, err := mergeK3dConfig(p, k3dConfigOverlay{APIPort: 6443})
		defer cleanup()
		if err != nil || got.temporary || got.path != p {
			t.Fatalf("= %+v, %v", got, err)
		}
	})
	t.Run("an unreadable fragment is reported", func(t *testing.T) {
		dir := t.TempDir()
		p := write(t, dir, "k3d.yaml", userCfg)
		write(t, dir, "k3d-ports.yaml", "ports: [not: valid: yaml")
		_, cleanup, err := mergeK3dConfig(p, k3dConfigOverlay{HostPorts: true})
		defer cleanup()
		if err == nil || !strings.Contains(err.Error(), "merge k3d config") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_K3dPortHost(t *testing.T) {
	tests := []struct {
		name  string
		entry any
		want  int
		ok    bool
	}{
		{"host:cluster pair", map[string]any{"port": "18080:80"}, 18080, true},
		{"padded", map[string]any{"port": " 18080 :80"}, 18080, true},
		{"bare port has no cluster side", map[string]any{"port": "18080"}, 0, false},
		{"not a number", map[string]any{"port": "abc:80"}, 0, false},
		{"port is an int, not a string", map[string]any{"port": 18080}, 0, false},
		{"not a map", "18080:80", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := k3dPortHost(tc.entry)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("= %d, %v; want %d, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCovC_RenderedGatewayHostPorts(t *testing.T) {
	orig := renderKCLRawHook
	t.Cleanup(func() { renderKCLRawHook = orig })
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`{"project":"p","env":"dev","gateways":[
			{"name":"a","listeners":[{"name":"x","port":28080,"protocol":"HTTP"},{"name":"y","port":0,"protocol":"HTTP"}]},
			{"name":"b","listeners":[{"name":"z","port":28443,"protocol":"HTTPS"},{"name":"dup","port":28080,"protocol":"HTTP"}]}]}`), nil
	}
	got, err := renderedGatewayHostPorts(context.Background(), "proj", "dev")
	if err != nil || len(got) != 2 || !got[28080] || !got[28443] {
		t.Fatalf("= %v, %v; want the two distinct positive listener ports", got, err)
	}
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) { return nil, errors.New("kcl failed") }
	if _, err := renderedGatewayHostPorts(context.Background(), "proj", "dev"); err == nil {
		t.Fatal("a render failure must be returned so the caller can warn")
	}
}
