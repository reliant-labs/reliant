package fluxdelivery_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/toolcache"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The oracle: the REAL template engine, flux-operator's own
// `build resourceset`, renders every fixture, and its output must equal
// Render's. This is what licenses Render as the writer's pre-write check —
// it proves the in-process renderer is the engine, not a resemblance of it.
//
// The CLI is pinned to the operator version the hub runs, downloaded from the
// release and checked against its published SHA-256, and cached in forge's
// tool cache. Bump fluxOperatorVersion and the sums together (the release's
// flux-operator_<v>_checksums.txt).
const fluxOperatorVersion = "0.62.1"

var fluxOperatorSHA256 = map[string]string{
	"darwin_amd64": "228cbce18426fe7e6b413a38726a48ded4921734013dcd7a02d0543281d8e0ce",
	"darwin_arm64": "3a245269899e20a193c474f74dc1b8ca2088dac1afd489b88424448755144f5c",
	"linux_amd64":  "7bb9e54d370039d8701d4271b38aed57d6c89668432b7caddce69f4e26e24c1b",
	"linux_arm64":  "4f427dd86747472f83144487404b8f77296660356d0b90859a14abbe6065a8e8",
}

func TestRenderMatchesFluxOperator(t *testing.T) {
	testkit.Slow(t, "downloads and runs the pinned flux-operator CLI on every fixture",
		testkit.Inputs("pkg/fluxdelivery/**"))
	bin := fluxOperatorBinary(t)
	dir := t.TempDir()

	for _, fx := range fixtures() {
		rs, err := fluxdelivery.ResourceSet(fx.in())
		if err != nil {
			t.Fatal(err)
		}
		want, err := fluxdelivery.Render(rs)
		if err != nil {
			t.Fatalf("%s: Render: %v", fx.name, err)
		}
		got := oracleBuild(t, bin, dir, fx.name, rs, nil)
		assertSameObjects(t, fx.name, got, want)
	}
	// The self-hosted bindings: in-cluster trees, with and without identities.
	for name, identities := range map[string]bool{"self_hosted_identities": true, "self_hosted_no_identities": false} {
		rs, err := fluxdelivery.ResourceSet(selfHosted(identities))
		if err != nil {
			t.Fatal(err)
		}
		want, err := fluxdelivery.Render(rs)
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		assertSameObjects(t, name, oracleBuild(t, bin, dir, name, rs, nil), want)
	}

	// The self-hosted route: the same input through a Static
	// ResourceSetInputProvider.
	in := flatConnected()
	values, err := in.Map()
	if err != nil {
		t.Fatal(err)
	}
	selfHosted := map[string]any{
		"apiVersion": fluxdelivery.APIVersion,
		"kind":       fluxdelivery.KindResourceSet,
		"metadata":   map[string]any{"name": "forge-delivery", "namespace": fxNS},
		"spec": map[string]any{
			"serviceAccountName": fluxdelivery.ServiceAccount,
			"inputsFrom": []any{map[string]any{
				"apiVersion": fluxdelivery.APIVersion, "kind": "ResourceSetInputProvider",
				"selector": map[string]any{"matchLabels": map[string]any{fluxdelivery.InputLabel: "true"}},
			}},
			"resourcesTemplate": fluxdelivery.Template,
		},
	}
	provider := map[string]any{
		"apiVersion": fluxdelivery.APIVersion,
		"kind":       "ResourceSetInputProvider",
		"metadata": map[string]any{
			"name": "delivery-env-prod", "namespace": fxNS,
			"labels": map[string]any{fluxdelivery.InputLabel: "true"},
		},
		"spec": map[string]any{"type": "Static", "defaultValues": values},
	}
	want, err := fluxdelivery.Render(selfHosted, fluxdelivery.Provider{Name: "delivery-env-prod", Inputs: []map[string]any{values}})
	if err != nil {
		t.Fatal(err)
	}
	assertSameObjects(t, "self_hosted_provider", oracleBuild(t, bin, dir, "self_hosted_provider", selfHosted, provider), want)

	// The schema gate is the engine's, not only Render's.
	bad := flatConnected()
	bad.Schema = "forge.dev/flux-delivery/v2"
	rs, err := fluxdelivery.ResourceSet(bad)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runOracle(bin, dir, "schema_v2", rs, nil); err == nil || !strings.Contains(out, "forge.dev/flux-delivery/v2") {
		t.Errorf("flux-operator rendered a v2 input (err %v):\n%s", err, out)
	}
}

func oracleBuild(t *testing.T, bin, dir, name string, rs, provider map[string]any) []*unstructured.Unstructured {
	t.Helper()
	out, err := runOracle(bin, dir, name, rs, provider)
	if err != nil {
		t.Fatalf("%s: flux-operator build resourceset: %v\n%s", name, err, out)
	}
	reader := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	var objs []*unstructured.Unstructured
	for {
		o := &unstructured.Unstructured{}
		if err := reader.Decode(o); err != nil {
			if err == io.EOF {
				return objs
			}
			t.Fatalf("%s: reading flux-operator's output: %v\n%s", name, err, out)
		}
		if len(o.Object) > 0 {
			objs = append(objs, o)
		}
	}
}

func runOracle(bin, dir, name string, rs, provider map[string]any) (string, error) {
	rsFile := filepath.Join(dir, name+".rs.yaml")
	raw, err := yaml.Marshal(rs)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(rsFile, raw, 0o644); err != nil {
		return "", err
	}
	args := []string{"build", "resourceset", "-f", rsFile}
	if provider != nil {
		pFile := filepath.Join(dir, name+".rsip.yaml")
		raw, err := yaml.Marshal(provider)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(pFile, raw, 0o644); err != nil {
			return "", err
		}
		args = append(args, "--inputs-from-provider", pFile)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// A build is offline: no cluster, and nothing of the caller's
	// environment, so a developer's kubeconfig cannot change the result.
	cmd.Env = []string{"HOME=" + dir, "KUBECONFIG=" + filepath.Join(dir, "no-kubeconfig")}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// assertSameObjects compares as the API server would store the objects:
// through JSON, so an int and a float of one value, or a nil and an absent
// map, are not differences.
func assertSameObjects(t *testing.T, name string, got, want []*unstructured.Unstructured) {
	t.Helper()
	norm := func(objs []*unstructured.Unstructured) []any {
		var out []any
		for _, o := range objs {
			b, _ := json.Marshal(o.Object)
			var v any
			_ = json.Unmarshal(b, &v)
			out = append(out, v)
		}
		return out
	}
	g, w := norm(got), norm(want)
	if !reflect.DeepEqual(g, w) {
		gj, _ := json.MarshalIndent(g, "", "  ")
		wj, _ := json.MarshalIndent(w, "", "  ")
		t.Errorf("%s: flux-operator and Render disagree.\n--- flux-operator ---\n%s\n--- Render ---\n%s", name, gj, wj)
	}
}

// fluxOperatorBinary is the pinned CLI from forge's tool cache, installed on
// first use.
func fluxOperatorBinary(t *testing.T) string {
	t.Helper()
	platform := runtime.GOOS + "_" + runtime.GOARCH
	sum, ok := fluxOperatorSHA256[platform]
	if !ok {
		t.Skipf("flux-operator %s publishes no CLI for %s", fluxOperatorVersion, platform)
	}
	kindDir, err := toolcache.KindDir(toolcache.KindPinned)
	if err != nil {
		t.Fatal(err)
	}
	ready := func(dir string) bool {
		out, err := exec.Command(filepath.Join(dir, "flux-operator"), "version", "--client").CombinedOutput()
		return err == nil && strings.Contains(string(out), fluxOperatorVersion)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir, err := toolcache.Ensure(ctx, kindDir, "flux-operator-"+fluxOperatorVersion+"-"+platform, ready,
		func(ctx context.Context, stage string) error {
			return installFluxOperator(ctx, platform, sum, stage)
		})
	if err != nil {
		t.Fatalf("installing flux-operator %s: %v", fluxOperatorVersion, err)
	}
	if !ready(dir) {
		t.Fatalf("the installed flux-operator in %s does not report %s", dir, fluxOperatorVersion)
	}
	return filepath.Join(dir, "flux-operator")
}

func installFluxOperator(ctx context.Context, platform, sum, stage string) error {
	url := fmt.Sprintf("https://github.com/controlplaneio-fluxcd/flux-operator/releases/download/v%[1]s/flux-operator_%[1]s_%[2]s.tar.gz",
		fluxOperatorVersion, platform)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	archive, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(archive)
	if got := hex.EncodeToString(digest[:]); got != sum {
		return fmt.Errorf("%s has sha256 %s, the release publishes %s", url, got, sum)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s holds no flux-operator binary", url)
		}
		if err != nil {
			return err
		}
		if h.Name != "flux-operator" {
			continue
		}
		f, err := os.OpenFile(filepath.Join(stage, "flux-operator"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 512<<20)); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}
}
