package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"oras.land/oras-go/v2/registry/remote/errcode"
)

// flakyImageChecker answers each ref from a script, one answer per call, and
// repeats the last answer once the script runs out. It is a registry having a
// bad minute: the shape of the 2026-10-07 prod release, where Docker Hub was
// answering 500s and one lookup came back "not found" for an image that
// existed and that prod was running.
type flakyImageChecker struct {
	mu      sync.Mutex
	answers map[string][]bool
	calls   map[string]int
}

func (f *flakyImageChecker) ImageExists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	script := f.answers[ref]
	i := f.calls[ref]
	f.calls[ref]++
	if i >= len(script) {
		i = len(script) - 1
	}
	return script[i], nil
}

// imageOnlyManifest is one Deployment and one image, so an image verdict is the
// only thing a preflight over it can report.
const imageOnlyManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:abc123
`

// A single "not found" is not a verdict. The preflight looks twice before it
// blocks a deploy on a missing image, so a registry that answers wrongly once
// does not stop a release.
func TestPreflight_AMissIsRecheckedBeforeItBlocks(t *testing.T) {
	checker := &flakyImageChecker{answers: map[string][]bool{
		"ghcr.io/acme/api:abc123": {false, true},
	}}
	opts := PreflightOpts{
		Manifests: imageOnlyManifest,
		Namespace: "app-prod",
		Images:    checker,
	}
	result, err := PreflightReport(context.Background(), opts)
	if err != nil {
		t.Fatalf("one wrong answer blocked the deploy:\n%v", err)
	}
	if len(result.MissingImages) != 0 {
		t.Errorf("MissingImages = %v, want none", result.MissingImages)
	}
	if got := checker.calls["ghcr.io/acme/api:abc123"]; got != 2 {
		t.Errorf("the registry was asked %d time(s), want 2 (one recheck)", got)
	}
}

// Two consistent "not found" answers still block — the recheck is a second
// look, not a way to wave a missing image through.
func TestPreflight_TwoMissesStillBlock(t *testing.T) {
	checker := &flakyImageChecker{answers: map[string][]bool{
		"ghcr.io/acme/api:abc123": {false},
	}}
	opts := PreflightOpts{Manifests: imageOnlyManifest, Namespace: "app-prod", Images: checker}
	_, err := PreflightReport(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "Images not found") {
		t.Fatalf("a genuinely missing image must block; got %v", err)
	}
}

// fakeRegistry serves the OCI distribution API's manifest HEAD for a few
// repositories, each answering one fixed status.
func fakeRegistry(t *testing.T) (host string) {
	t.Helper()
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/acme/present/manifests/"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/v2/acme/missing/manifests/"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/v2/acme/flaky/manifests/"):
			// Docker Hub on 2026-10-07: a 500 whose text says "not found".
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"code":"UNKNOWN","message":"upstream manifest not found"}]}`))
		case strings.HasPrefix(r.URL.Path, "/v2/acme/throttled/manifests/"):
			w.WriteHeader(http.StatusTooManyRequests)
		case strings.HasPrefix(r.URL.Path, "/v2/acme/private/manifests/"):
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// The verdict is the registry's HTTP status, never its prose: a 5xx or a 429
// is a statement about the registry, whatever its body says.
func TestRegistryImageChecker_VerdictIsTheStatusCode(t *testing.T) {
	host := fakeRegistry(t)
	// An empty docker config: no credential helper of this machine's runs.
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No backoff: the 500 and the 429 are served on purpose.
	previous := registryHTTPClient
	registryHTTPClient = &http.Client{}
	t.Cleanup(func() { registryHTTPClient = previous })
	checker := RegistryImageChecker{DockerConfigDir: cfg}

	cases := []struct {
		repo    string
		present bool
		want    error // nil means present
	}{
		{"acme/present", true, nil},
		{"acme/missing", false, ErrImageNotFound},
		{"acme/flaky", false, ErrImageCheckInconclusive},
		{"acme/throttled", false, ErrImageCheckInconclusive},
		{"acme/private", false, ErrImageCheckAuthDenied},
	}
	for _, tc := range cases {
		// registryRefIsInsecure keys on the host; a loopback test server is
		// plain HTTP, like a local k3d registry.
		ref := strings.Replace(host, "127.0.0.1", "localhost", 1) + "/" + tc.repo + ":v1"
		exists, err := checker.ImageExists(context.Background(), ref)
		if exists != tc.present {
			t.Errorf("%s: exists=%v, want %v (err %v)", tc.repo, exists, tc.present, err)
		}
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: err %v, want none", tc.repo, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", tc.repo, err, tc.want)
		}
	}
}

// The same rule, at the classifier, for the exact answer that blocked the
// release: a registry 500 whose message mentions "not found".
func TestRegistryVerdict_5xxMentioningNotFoundIsInconclusive(t *testing.T) {
	resp := &errcode.ErrorResponse{
		Method: http.MethodHead, StatusCode: http.StatusInternalServerError,
		Errors: errcode.Errors{{Code: "UNKNOWN", Message: "received unexpected HTTP status: 500 Internal Server Error; manifest not found"}},
	}
	exists, err := registryVerdict("registry-1.docker.io", resp)
	if exists || !errors.Is(err, ErrImageCheckInconclusive) || errors.Is(err, ErrImageNotFound) {
		t.Fatalf("a 500 was classified (%v, %v); want inconclusive, never a miss", exists, err)
	}
}

// A blocked image names what the registry said. The 2026-10-07 report listed
// only the ref, so nobody could tell what had actually been answered.
func TestPreflight_MissingImageReportCarriesTheRegistryAnswer(t *testing.T) {
	checker := answerChecker{err: fmt.Errorf("%w: registry-1.docker.io answered HTTP 404 for the manifest", ErrImageNotFound)}
	_, err := PreflightReport(context.Background(), PreflightOpts{
		Manifests: imageOnlyManifest, Namespace: "app-prod", Images: checker,
	})
	if err == nil {
		t.Fatal("a confirmed miss must block")
	}
	if !strings.Contains(err.Error(), "registry: the registry has no such image: registry-1.docker.io answered HTTP 404") {
		t.Errorf("the report does not carry the registry's answer:\n%s", err)
	}
}

// answerChecker gives every ref the same answer.
type answerChecker struct {
	exists bool
	err    error
}

func (a answerChecker) ImageExists(context.Context, string) (bool, error) { return a.exists, a.err }

// fakeRunningImages is a cluster running a fixed set of images.
type fakeRunningImages struct {
	images map[string]string
	calls  *int
}

func (f fakeRunningImages) RunningImages(context.Context, string) (map[string]string, error) {
	if f.calls != nil {
		*f.calls++
	}
	out := map[string]string{}
	for ref, where := range f.images {
		out[normalizeImageRef(ref)] = where
	}
	return out, nil
}

// An image the live target is RUNNING cannot be "missing". prod's temporal
// Deployment was running temporalio/auto-setup:1.26.2 when the preflight
// blocked the release on it.
func TestPreflight_AnImageTheLiveTargetRunsIsNeverMissing(t *testing.T) {
	calls := 0
	opts := PreflightOpts{
		Manifests: imageOnlyManifest,
		Namespace: "app-prod",
		Context:   "gke_prod",
		Images:    answerChecker{err: fmt.Errorf("%w: ghcr.io answered HTTP 404", ErrImageNotFound)},
		// Spelled the way a runtime reports it, not the way the manifest does.
		RunningImages: fakeRunningImages{images: map[string]string{"ghcr.io/acme/api:abc123": "app-prod/api-7d9f"}, calls: &calls},
	}
	result, err := PreflightReport(context.Background(), opts)
	if err != nil {
		t.Fatalf("an image prod is running blocked the deploy:\n%v", err)
	}
	if len(result.ImageWarnings) != 1 || !strings.Contains(result.ImageWarnings[0], "live target is running it (app-prod/api-7d9f)") {
		t.Errorf("ImageWarnings = %q; want one naming the registry's answer and the running pod", result.ImageWarnings)
	}
	if calls != 1 {
		t.Errorf("the cluster was listed %d time(s), want exactly 1", calls)
	}

	// Same for an auth-denied lookup nothing could resolve.
	opts.Images = answerChecker{err: fmt.Errorf("%w: ghcr.io answered 403", ErrImageCheckAuthDenied)}
	if _, err := PreflightReport(context.Background(), opts); err != nil {
		t.Errorf("an auth-denied image prod is running blocked the deploy:\n%v", err)
	}
}

// The happy path never lists the cluster: the downgrade costs a kubectl call
// only when an image would otherwise block.
func TestPreflight_PresentImagesNeverListTheCluster(t *testing.T) {
	calls := 0
	_, err := PreflightReport(context.Background(), PreflightOpts{
		Manifests: imageOnlyManifest, Namespace: "app-prod", Context: "gke_prod",
		Images:        answerChecker{exists: true},
		RunningImages: fakeRunningImages{calls: &calls},
	})
	if err != nil || calls != 0 {
		t.Fatalf("err %v, cluster listed %d time(s); want nil and 0", err, calls)
	}
}

// Only RUNNING containers count. A pod in ImagePullBackOff names the image in
// its spec, and counting it would call a missing image running.
func TestRunningImagesFromPodList_OnlyRunningContainers(t *testing.T) {
	raw := []byte(`{"items":[
	  {"metadata":{"namespace":"prod","name":"temporal-1"},
	   "spec":{"containers":[{"name":"temporal","image":"temporalio/auto-setup:1.26.2"}]},
	   "status":{"containerStatuses":[{"name":"temporal","image":"docker.io/temporalio/auto-setup:1.26.2",
	     "imageID":"docker.io/temporalio/auto-setup@sha256:abc","state":{"running":{}}}]}},
	  {"metadata":{"namespace":"prod","name":"api-2"},
	   "spec":{"containers":[{"name":"api","image":"ghcr.io/acme/api:never-pushed"}]},
	   "status":{"containerStatuses":[{"name":"api","image":"ghcr.io/acme/api:never-pushed",
	     "imageID":"","state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}
	]}`)
	got, err := runningImagesFromPodList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got[normalizeImageRef("temporalio/auto-setup:1.26.2")] != "prod/temporal-1" {
		t.Errorf("the running temporal image is not indexed: %v", got)
	}
	if _, ok := got[normalizeImageRef("ghcr.io/acme/api:never-pushed")]; ok {
		t.Errorf("an image stuck in ImagePullBackOff was counted as running: %v", got)
	}
	if got["@sha256:abc"] != "prod/temporal-1" {
		t.Errorf("the resolved digest is not indexed: %v", got)
	}
}

func TestNormalizeImageRef(t *testing.T) {
	same := [][2]string{
		{"nats:2.10.24-alpine", "docker.io/library/nats:2.10.24-alpine"},
		{"temporalio/auto-setup:1.26.2", "docker.io/temporalio/auto-setup:1.26.2"},
		{"index.docker.io/temporalio/ui:2.35.0", "temporalio/ui:2.35.0"},
		{"redis", "docker.io/library/redis:latest"},
		{"localhost:5000/api:dev", "localhost:5000/api:dev"},
	}
	for _, p := range same {
		if a, b := normalizeImageRef(p[0]), normalizeImageRef(p[1]); a != b {
			t.Errorf("%q → %q, %q → %q; want the same image", p[0], a, p[1], b)
		}
	}
	if got := normalizeImageRef("us-docker.pkg.dev/p/r/api:v1@sha256:abc"); got != "us-docker.pkg.dev/p/r/api:v1@sha256:abc" {
		t.Errorf("a fully qualified ref changed: %q", got)
	}
}
