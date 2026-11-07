//go:build integration

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real Distribution DELETE + offline GC + restart against a disposable volume.
// The protecting Kubernetes API is empty; no existing machine policy is loaded.
func TestRegistryMaintenanceEndToEnd(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("k3d-forge-storage-test-%d", time.Now().UnixNano())
	volume := name + "-data"
	run := func(args ...string) []byte {
		t.Helper()
		b, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if e != nil {
			t.Fatalf("docker %v: %v: %s", args, e, b)
		}
		return b
	}
	run("volume", "create", volume)
	t.Cleanup(func() {
		for _, n := range []string{name + "-forge-gc", name + "-forge-retention", name} {
			_ = exec.Command("docker", "rm", "-f", n).Run()
		}
		_ = exec.Command("docker", "volume", "rm", volume).Run()
	})
	run("run", "-d", "--name", name, "-p", "127.0.0.1::5000", "-v", volume+":/var/lib/registry", "registry:2")
	p := DefaultPolicy()
	p.RegistryKeep = 2
	r := Runner{Policy: p, PolicyPath: filepath.Join(t.TempDir(), "storage.json"), Command: func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command == "kubectl" {
			// The scan asks which resources exist before listing them; an empty
			// cluster still has to answer both questions.
			for _, a := range args {
				if a == "api-resources" {
					return []byte("pods\nworkspaces.workspaces.reliant.dev\n"), nil
				}
			}
			return []byte(`{"items":[]}`), nil
		}
		return Exec(ctx, command, args...)
	}}
	base, err := r.endpoint(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err = registryRequest(ctx, base, "/v2/", http.MethodGet); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, url string, body []byte, kind string) *http.Response {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(body)))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", kind)
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %s", method, url, resp.Status)
		}
		return resp
	}
	digests := map[string]string{}
	for _, tag := range []string{"old", "kept", "latest"} {
		config := []byte(fmt.Sprintf(`{"architecture":"amd64","os":"linux","config":{"Labels":{"test":"%s"}},"rootfs":{"type":"layers","diff_ids":[]}}`, tag))
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
		resp := request("POST", base+"/v2/app/blobs/uploads/", nil, "")
		request("PUT", resp.Header.Get("Location")+"&digest="+digest, config, "application/octet-stream")
		body, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.docker.distribution.manifest.v2+json", "config": map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "size": len(config), "digest": digest}, "layers": []any{}})
		request("PUT", base+"/v2/app/manifests/"+tag, body, "application/vnd.docker.distribution.manifest.v2+json")
		digests[tag] = fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	}
	// The newest-version window still orders pushes, while old exceeds TTL.
	run("exec", name, "touch", "-d", "2000-01-01", "/var/lib/registry/docker/registry/v2/repositories/app/_manifests/tags/old/current/link")
	reg := Registry{Container: name, Repositories: []string{"app"}, Aliases: []string{strings.TrimPrefix(base, "http://")}, Contexts: []string{"k3d-test"}}
	if err = r.RegistryGC(ctx, reg, false); err != nil {
		t.Fatal(err)
	}
	if _, err = registryRequest(ctx, base, "/v2/app/manifests/"+digests["old"], http.MethodGet); err != nil {
		t.Fatal("preview removed old manifest")
	}
	if err = r.RegistryGC(ctx, reg, true); err != nil {
		t.Fatal(err)
	}
	// RegistryGC stops and restarts the registry. This test publishes it on an
	// EPHEMERAL host port (127.0.0.1::5000), and Docker assigns a new one on
	// `docker start`, so the pre-GC URL is dead: re-read the published port.
	// (k3d registries are created with a fixed host port, which survives the
	// restart — this re-resolution is the test's, not the product's.)
	if base, err = r.endpoint(ctx, name); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err = registryRequest(ctx, base, "/v2/", http.MethodGet); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("registry did not come back after GC: %v", err)
	}
	// Deleted means a 404 from a live registry — not merely "the request
	// failed", which an unreachable registry would also satisfy.
	_, err = registryRequest(ctx, base, "/v2/app/manifests/"+digests["old"], http.MethodGet)
	if err == nil {
		t.Fatal("expired manifest survived")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expired manifest check did not get a 404 from the registry: %v", err)
	}
	for _, tag := range []string{"kept", "latest"} {
		if _, err = registryRequest(ctx, base, "/v2/app/manifests/"+digests[tag], http.MethodGet); err != nil {
			t.Fatal(err)
		}
	}
}
