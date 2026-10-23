package deploytarget

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func staticReadyStatus(digest string) func(int) string {
	return func(int) string {
		return fmt.Sprintf(`{"deployments":[{"deployment":{"id":"dep-web","name":"web","observed":{"state":"DEPLOY_OBSERVED_STATE_READY","imageDigest":%q}},"desiredDigest":%q}]}`, digest, digest)
	}
}

// staticSiteArtifact is the fixture site's release repository: the declared
// image plus the platform's layout segment, which is what `forge build
// --push` records and the ledger is keyed by.
var staticSiteArtifact = HostedStaticRepository("ghcr.io/acme/web")

func staticGroup(release string, digests map[string]string) ServiceGroup {
	keep := int32(5)
	return ServiceGroup{
		Env: "prod", ProviderID: HostedProviderID,
		Hosted: &HostedTarget{Endpoint: "https://cp.example", Release: release, Digests: digests, PushBase: "ghcr.io/acme"},
		Services: []ResolvedService{{Name: "web", Hosted: &HostedWorkload{
			// A site's artifact key IS the repository it was pushed to, so
			// it carries a registry host exactly as buildHostedGroup's does.
			Tier: HostedTierStatic, Artifact: staticSiteArtifact,
			Static: &v1alpha1.StaticSiteSpec{BasePath: "/app", KeepReleases: &keep},
		}}},
	}
}

// TestHostedStaticDeployCallOrderAndPinnedRelease: a hosted StaticSite is
// published as ensure env → ensure deployment{tier STATIC, spec.liveDigest =
// the BOUND release digest} → publish → status, and nothing in the spec names
// a bucket or a cdn (the platform owns both). The release's repository IS
// published — see TestHostedStaticPublishesTheRecordedReleaseRepository.
//
// MUTATIONS VERIFIED RED:
//   - dropping `spec.LiveDigest = digest` in planHostedWith → liveDigest absent;
//   - wireTier returning "" for static → tier mismatch;
//   - hostedGroupHasPinnedArtifact ignoring static → an unbound static-only
//     env publishes (TestHostedStaticUnboundRefused).
func TestHostedStaticDeployCallOrderAndPinnedRelease(t *testing.T) {
	cp := &fakeCP{status: staticReadyStatus(digestB)}
	p := HostedProvider{Client: cp, PollInterval: time.Millisecond}
	if err := p.Deploy(context.Background(), staticGroup("v2", map[string]string{staticSiteArtifact: digestB})); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	want := "EnsureEnvironment,GetStatus"
	if got := strings.Join(cp.procs(), ","); got != want {
		t.Fatalf("call order = %s, want %s (no per-deployment RPC)", got, want)
	}
	recs, err := HostedRecords(staticGroup("v2", map[string]string{staticSiteArtifact: digestB}))
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	var doc struct {
		Kind string         `json:"kind"`
		Spec map[string]any `json:"spec"`
	}
	if err := yaml.Unmarshal(recs[0].YAML, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Kind != "StaticSite" || recs[0].Name != "web" {
		t.Fatalf("record = %s %s", doc.Kind, recs[0].Name)
	}
	spec := doc.Spec
	if spec["liveDigest"] != digestB {
		t.Errorf("spec.liveDigest = %v, want the bound release %s", spec["liveDigest"], digestB)
	}
	if spec["basePath"] != "/app" || spec["keepReleases"] != float64(5) {
		t.Errorf("spec lost the declared deploy fields: %v", spec)
	}
	for _, forbidden := range []string{"bucket", "cdn"} {
		if _, ok := spec[forbidden]; ok {
			t.Errorf("hosted static spec carries %q; the platform owns it", forbidden)
		}
	}
}

// TestHostedStaticUnboundRefused: a static-only env with no promoted release
// has no digest to serve and is refused with zero RPCs.
func TestHostedStaticUnboundRefused(t *testing.T) {
	cp := &fakeCP{status: staticReadyStatus(digestB)}
	err := HostedProvider{Client: cp}.Deploy(context.Background(), staticGroup("", nil))
	if err == nil || !strings.Contains(err.Error(), "no promoted release") {
		t.Fatalf("err = %v, want the unbound refusal", err)
	}
	if n := len(cp.procs()); n != 0 {
		t.Fatalf("%d RPCs before the refusal", n)
	}
}

// TestHostedStaticReleaseMissingSiteRefused: a release that pins the backend
// but not the site refuses the whole env before any write.
func TestHostedStaticReleaseMissingSiteRefused(t *testing.T) {
	cp := &fakeCP{status: staticReadyStatus(digestB)}
	err := HostedProvider{Client: cp}.Deploy(context.Background(), staticGroup("v2", map[string]string{"api": digestA}))
	if err == nil || !strings.Contains(err.Error(), `static site artifact "`+staticSiteArtifact+`"`) {
		t.Fatalf("err = %v", err)
	}
	if n := len(cp.procs()); n != 0 {
		t.Fatalf("%d RPCs before the refusal", n)
	}
}

// TestPackStaticSiteTreeIsDeterministic: the same tree packs to the same bytes
// (so the same release digest) regardless of mtimes, and a symlink is refused.
// Mutation: dropping the sort or keeping ModTime → the two archives differ.
func TestPackStaticSiteTreeIsDeterministic(t *testing.T) {
	mk := func(mtime time.Time) string {
		dir := t.TempDir()
		for name, body := range map[string]string{"index.html": "<h1>hi</h1>", "assets/app.js": "x()", "config.js": "c"} {
			p := filepath.Join(dir, name)
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_ = os.Chtimes(p, mtime, mtime)
		}
		return dir
	}
	a, err := PackStaticSiteTree(mk(time.Unix(1000, 0)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := PackStaticSiteTree(mk(time.Unix(99999, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("identical trees packed to different bytes; the release digest would change with mtimes")
	}
	names := tarNames(t, a)
	if strings.Join(names, ",") != "assets/app.js,config.js,index.html" {
		t.Errorf("entries = %v", names)
	}

	link := mk(time.Unix(1, 0))
	if err := os.Symlink("/etc/passwd", filepath.Join(link, "leak")); err != nil {
		t.Skip("symlinks unsupported")
	}
	if _, err := PackStaticSiteTree(link); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink packed: %v", err)
	}
	if _, err := PackStaticSiteTree(t.TempDir()); err == nil {
		t.Fatal("an empty site packed")
	}
}

// TestPushStaticSiteArtifactShape: the pushed manifest carries the site
// artifactType and exactly one layer of the site media type, and re-pushing
// the same layer yields the same digest.
func TestPushStaticSiteArtifactShape(t *testing.T) {
	store := memory.New()
	layer := []byte("layer-bytes")
	d1, err := pushStaticSite(context.Background(), store, layer)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := pushStaticSite(context.Background(), store, layer)
	if err != nil || d1 != d2 {
		t.Fatalf("re-push: %v, %s != %s", err, d1, d2)
	}
	rc, err := store.Fetch(context.Background(), manifestDescFor(t, store, d1))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	var m ocispec.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.ArtifactType != StaticSiteArtifactType || len(m.Layers) != 1 || m.Layers[0].MediaType != StaticSiteLayerMediaType {
		t.Fatalf("manifest = %+v", m)
	}
	// The frontend's OWN declared reference, plus the platform's layout
	// segment. The site name is already in the reference the author wrote, so
	// it is not appended again — static.v1 is the only thing forge adds.
	if got := HostedStaticRepository("reg.example/org1/web/"); got != "reg.example/org1/web/static.v1" {
		t.Errorf("repository = %s", got)
	}
}

func tarNames(t *testing.T, gz []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name)
	}
}

// manifestDescFor finds the pushed manifest's full descriptor (the memory
// store keys content by digest AND size) by walking predecessors of the layer.
func manifestDescFor(t *testing.T, store *memory.Store, d string) ocispec.Descriptor {
	t.Helper()
	layerDesc := ocispec.Descriptor{MediaType: StaticSiteLayerMediaType, Digest: godigest.FromBytes([]byte("layer-bytes")), Size: int64(len("layer-bytes"))}
	preds, err := store.Predecessors(context.Background(), layerDesc)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range preds {
		if p.Digest.String() == d {
			return p
		}
	}
	t.Fatalf("manifest %s not found among %v", d, preds)
	return ocispec.Descriptor{}
}
