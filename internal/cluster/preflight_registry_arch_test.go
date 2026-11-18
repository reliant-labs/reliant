package cluster

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// archRegistry serves one repository per manifest shape the arch gate must
// read: a Docker schema 2 image (architecture in its config blob — 939b541b's
// shape), an OCI image, an OCI index, a Docker manifest list, and a manifest
// type nobody knows.
func archRegistry(t *testing.T) string {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
	image := func(mediaType string) string {
		return `{"schemaVersion":2,"mediaType":"` + mediaType + `","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":` +
			fmt.Sprint(len(config)) + `,"digest":"` + configDigest + `"},"layers":[]}`
	}
	list := func(mediaType string) string {
		return `{"schemaVersion":2,"mediaType":"` + mediaType + `","manifests":[` +
			`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("1", 64) + `","size":1,"platform":{"architecture":"amd64","os":"linux"}},` +
			`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("2", 64) + `","size":1,"platform":{"architecture":"arm64","os":"linux"}},` +
			`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("3", 64) + `","size":1,"platform":{"architecture":"unknown","os":"unknown"}}]}`
	}
	manifests := map[string][2]string{
		"acme/docker-v2":   {mediaTypeDockerManifest, image(mediaTypeDockerManifest)},
		"acme/oci":         {mediaTypeOCIManifest, image(mediaTypeOCIManifest)},
		"acme/oci-index":   {mediaTypeOCIIndex, list(mediaTypeOCIIndex)},
		"acme/docker-list": {mediaTypeDockerManifestList, list(mediaTypeDockerManifestList)},
		"acme/weird":       {"application/vnd.example.thing+json", `{"schemaVersion":2}`},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for repo, m := range manifests {
			if strings.HasPrefix(r.URL.Path, "/v2/"+repo+"/manifests/") {
				body := []byte(m[1])
				w.Header().Set("Content-Type", m[0])
				w.Header().Set("Docker-Content-Digest", fmt.Sprintf("sha256:%x", sha256.Sum256(body)))
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = w.Write(body)
				return
			}
			if r.URL.Path == "/v2/"+repo+"/blobs/"+configDigest {
				w.Header().Set("Content-Length", fmt.Sprint(len(config)))
				_, _ = w.Write(config)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return strings.Replace(strings.TrimPrefix(srv.URL, "http://"), "127.0.0.1", "localhost", 1)
}

func TestRegistryImageArchChecker_ReadsEveryManifestShape(t *testing.T) {
	host := archRegistry(t)
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := registryHTTPClient
	registryHTTPClient = &http.Client{}
	t.Cleanup(func() { registryHTTPClient = previous })
	checker := RegistryImageArchChecker{DockerConfigDir: cfg}

	for repo, want := range map[string]string{
		"acme/docker-v2":   "amd64",
		"acme/oci":         "amd64",
		"acme/oci-index":   "amd64,arm64",
		"acme/docker-list": "amd64,arm64",
	} {
		archs, err := checker.ImageArchitectures(context.Background(), host+"/"+repo+":v1")
		if err != nil {
			t.Errorf("%s: %v", repo, err)
			continue
		}
		if got := strings.Join(archs, ","); got != want {
			t.Errorf("%s: archs = %s, want %s", repo, got, want)
		}
	}

	// An unknown shape and an absent image are inconclusive — the gate warns —
	// and the message names the media type rather than dumping bytes.
	_, err := checker.ImageArchitectures(context.Background(), host+"/acme/weird:v1")
	if !errors.Is(err, ErrImageCheckInconclusive) || !strings.Contains(err.Error(), "application/vnd.example.thing+json") {
		t.Errorf("unknown manifest: %v; want inconclusive naming the media type", err)
	}
	if strings.Contains(fmt.Sprint(err), "[123 ") {
		t.Errorf("the error dumps manifest bytes: %v", err)
	}
	if _, err := checker.ImageArchitectures(context.Background(), host+"/acme/absent:v1"); !errors.Is(err, ErrImageCheckInconclusive) {
		t.Errorf("absent image: %v; want inconclusive (existence is the existence check's)", err)
	}
}

// An oversized manifest or config is inconclusive, never parsed from its
// first maxManifestBytes: a truncated manifest list can name a subset of its
// platforms, and a truncated config can still carry an architecture.
func TestReadBounded_OversizedDocumentIsInconclusive(t *testing.T) {
	entry := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("1", 64) + `","size":1,"platform":{"architecture":"amd64","os":"linux"}},`
	list := `{"schemaVersion":2,"mediaType":"` + mediaTypeOCIIndex + `","manifests":[` +
		strings.Repeat(entry, maxManifestBytes/len(entry)+1) +
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("2", 64) + `","size":1,"platform":{"architecture":"arm64","os":"linux"}}]}`
	if _, err := readBounded(io.NopCloser(strings.NewReader(list))); !errors.Is(err, ErrImageCheckInconclusive) {
		t.Errorf("oversized manifest list: %v; want inconclusive", err)
	}

	config := `{"architecture":"amd64","os":"linux"}` + strings.Repeat(" ", maxManifestBytes)
	manifest := `{"schemaVersion":2,"mediaType":"` + mediaTypeOCIManifest + `","config":{"mediaType":"application/vnd.oci.image.config.v1+json","size":1,"digest":"sha256:` + strings.Repeat("3", 64) + `"}}`
	_, err := archsFromManifest(mediaTypeOCIManifest, []byte(manifest), func(ocispec.Descriptor) ([]byte, error) {
		return readBounded(io.NopCloser(strings.NewReader(config)))
	})
	if !errors.Is(err, ErrImageCheckInconclusive) {
		t.Errorf("oversized config: %v; want inconclusive", err)
	}

	exact := strings.Repeat(" ", maxManifestBytes)
	if body, err := readBounded(io.NopCloser(strings.NewReader(exact))); err != nil || len(body) != maxManifestBytes {
		t.Errorf("a document of exactly the limit: %d bytes, %v; want it read whole", len(body), err)
	}
}
