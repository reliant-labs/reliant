package vulnpatch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// registryServer is the slice of the OCI distribution API LiveRegistry
// reads: manifest HEADs and the tag list, for one repository.
func registryServer(t *testing.T, repo string, digests map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/"+repo+"/tags/list":
			var tags []string
			for tag := range digests {
				tags = append(tags, tag)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
		case strings.HasPrefix(r.URL.Path, "/v2/"+repo+"/manifests/"):
			d, ok := digests[strings.TrimPrefix(r.URL.Path, "/v2/"+repo+"/manifests/")]
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Header().Set("Docker-Content-Digest", d)
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLiveRegistryOverTheDistributionAPI(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // no ambient credentials
	srv := registryServer(t, "library/alpine", map[string]string{"3.19": dg("1"), "3.19.4": dg("4")})
	repository := strings.TrimPrefix(srv.URL, "http://") + "/library/alpine"
	reg := LiveRegistry()
	ctx := context.Background()

	if d, err := reg.Digest(ctx, repository, "3.19.4"); err != nil || d != dg("4") {
		t.Errorf("Digest = %q, %v", d, err)
	}
	if _, err := reg.Digest(ctx, repository, "3.17"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("a missing tag: %v, want ErrImageNotFound", err)
	}
	tags, err := reg.Tags(ctx, repository)
	if err != nil || len(tags) != 2 {
		t.Errorf("Tags = %v, %v", tags, err)
	}

	// The whole OS refresh against it.
	image := strings.TrimPrefix(srv.URL, "http://") + "/library/alpine"
	root := writeTree(t, map[string]string{"Dockerfile": "FROM " + image + ":3.19.1\n"})
	doc, _ := patch(t, root, []vulnscan.Finding{apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")}, Options{Registry: reg}, nil, nil)
	if got := readFile(t, root, "Dockerfile"); got != "FROM "+image+":3.19.4\n" || doc.Findings[0].Status != StatusBumped {
		t.Errorf("Dockerfile %q, %+v", got, doc.Findings[0])
	}
}

// LiveRegistry.Platforms reads an index's platforms (attestations left
// out), and a single image's from its config.
func TestLiveRegistryPlatforms(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // no ambient credentials
	const repo = "library/alpine"
	blob := func(mediaType string, v any) (string, []byte, string) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return mediaType, b, "sha256:" + fmt.Sprintf("%x", sha256.Sum256(b))
	}
	idxType, idx, idxDigest := blob("application/vnd.oci.image.index.v1+json", map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": dg("a"), "size": 1, "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": dg("b"), "size": 1, "platform": map[string]string{"os": "linux", "architecture": "arm", "variant": "v7"}},
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": dg("c"), "size": 1, "platform": map[string]string{"os": "unknown", "architecture": "unknown"}},
		},
	})
	_, cfg, cfgDigest := blob("application/vnd.oci.image.config.v1+json", map[string]any{"os": "linux", "architecture": "arm64", "variant": "v8"})
	manType, man, manDigest := blob("application/vnd.oci.image.manifest.v1+json", map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cfgDigest, "size": len(cfg)},
		"layers": []any{},
	})
	manifests := map[string][2]any{"3.19": {idxType, idx}, idxDigest: {idxType, idx}, "single": {manType, man}, manDigest: {manType, man}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/v2/"+repo+"/manifests/"):
			m, ok := manifests[strings.TrimPrefix(r.URL.Path, "/v2/"+repo+"/manifests/")]
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
				return
			}
			body := m[1].([]byte)
			w.Header().Set("Content-Type", m[0].(string))
			w.Header().Set("Docker-Content-Digest", "sha256:"+fmt.Sprintf("%x", sha256.Sum256(body)))
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		case r.URL.Path == "/v2/"+repo+"/blobs/"+cfgDigest:
			w.Header().Set("Content-Length", fmt.Sprint(len(cfg)))
			_, _ = w.Write(cfg)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	repository := strings.TrimPrefix(srv.URL, "http://") + "/" + repo
	reg := LiveRegistry()
	ctx := context.Background()
	for _, ref := range []string{"3.19", idxDigest} {
		if pl, index, err := reg.Platforms(ctx, repository, ref); err != nil || !index || strings.Join(pl, ",") != "linux/amd64,linux/arm/v7" {
			t.Errorf("Platforms(%s) = %v %v %v", ref, pl, index, err)
		}
	}
	if pl, index, err := reg.Platforms(ctx, repository, "single"); err != nil || index || strings.Join(pl, ",") != "linux/arm64/v8" {
		t.Errorf("Platforms(single) = %v %v %v", pl, index, err)
	}
	if _, _, err := reg.Platforms(ctx, repository, "3.17"); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("a missing tag: %v, want ErrImageNotFound", err)
	}
}

func TestLoopbackRegistry(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"localhost:5000": true, "127.0.0.1:41234": true, "registry.localhost": true, "[::1]:5000": true,
		"ghcr.io": false, "registry-1.docker.io": false, "us-docker.pkg.dev": false,
	} {
		if got := loopbackRegistry(host); got != want {
			t.Errorf("loopbackRegistry(%q) = %v", host, got)
		}
	}
}

// TestRealRegistryDockerHub reads a real registry: the alpine tag list and
// the digest of alpine:3.19 on Docker Hub. It skips when the network or
// Docker Hub cannot be reached.
func TestRealRegistryDockerHub(t *testing.T) {
	testkit.Slow(t, "reads Docker Hub over the network")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	reg := LiveRegistry()
	d, err := reg.Digest(ctx, alpineRepo, "3.19")
	if err != nil {
		t.Skipf("Docker Hub is not reachable from this host: %v", err)
	}
	if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		t.Errorf("digest %q", d)
	}
	tags, err := reg.Tags(ctx, alpineRepo)
	if err != nil {
		t.Skipf("Docker Hub's tag list is not reachable from this host: %v", err)
	}
	if next, ok := newestTag(imageTag{nums: [3]int{3, 19, 0}, parts: 3}, tags, false); !ok || next.nums[1] != 19 {
		t.Errorf("newest 3.19.x of %d tags = %+v %v", len(tags), next, ok)
	}
}
