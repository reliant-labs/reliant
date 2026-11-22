package sbomoci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/sbomoci/ocitest"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestMain gives every registry client in these tests a transport that does
// not retry: a refused request must fail at once, not back off.
func TestMain(m *testing.M) {
	registryHTTPClient = &http.Client{Timeout: 10 * time.Second}
	os.Exit(m.Run())
}

// testRegistry reaches an ocitest.Registry with no credential.
func testRegistry() RegistryOptions {
	return RegistryOptions{
		Credential: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil },
	}
}

func testAttach() AttachOptions {
	return AttachOptions{Registry: testRegistry(), Generator: "syft@v1.57.2", Release: "v1.2.3",
		Created: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}
}

func sbomDoc(name string) []byte {
	return []byte(fmt.Sprintf(`{"bomFormat":"CycloneDX","specVersion":"1.6","metadata":{"component":{"name":%q}}}`, name))
}

// listed is what the registry lists as subject's SBOM referrers, read by a
// fresh client (auto mode, as any reader would).
func listed(t *testing.T, subject string) []ocispec.Descriptor {
	t.Helper()
	ref, d, err := parseSubject(subject)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := openRepository(ref.Registry+"/"+ref.Repository, testRegistry())
	if err != nil {
		t.Fatal(err)
	}
	desc, err := resolveSubject(context.Background(), repo, d)
	if err != nil {
		t.Fatal(err)
	}
	out, err := listReferrers(context.Background(), repo, desc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func referrerDigests(t *testing.T, subject string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, d := range listed(t, subject) {
		out[d.Digest.String()] = true
	}
	return out
}

func mustAttach(t *testing.T, subject string, sbom []byte) string {
	t.Helper()
	ref, err := Attach(context.Background(), subject, sbom, testAttach())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return ref
}

// fallbackTag is the OCI tag-schema index tag for subject.
func fallbackTag(subject string) string {
	_, d, _ := strings.Cut(subject, "@")
	return strings.Replace(d, ":", "-", 1)
}

func TestAttach_TagFallback(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/web/static.v1", "v1")
	sbom := sbomDoc("web")

	referrer := mustAttach(t, subject, sbom)

	repo, _, _ := strings.Cut(subject, "@")
	if !strings.HasPrefix(referrer, repo+"@sha256:") {
		t.Fatalf("referrer %q is not in the subject's repository %q", referrer, repo)
	}
	if tags := f.Tags("acme/web/static.v1"); !contains(tags, fallbackTag(subject)) {
		t.Fatalf("no fallback index tag %s; tags %v", fallbackTag(subject), tags)
	}
	got := listed(t, subject)
	if len(got) != 1 {
		t.Fatalf("listed %d referrers, want 1: %+v", len(got), got)
	}
	a := got[0].Annotations
	if a[AnnotationSBOMDigest] != digest.FromBytes(sbom).String() || a[AnnotationGenerator] != "syft@v1.57.2" ||
		a[AnnotationRelease] != "v1.2.3" || a[ocispec.AnnotationCreated] != "2026-11-20T00:00:00Z" {
		t.Errorf("referrer annotations = %v", a)
	}
	if got[0].ArtifactType != ArtifactType {
		t.Errorf("artifactType = %q, want %q", got[0].ArtifactType, ArtifactType)
	}
	if !strings.HasSuffix(referrer, got[0].Digest.String()) {
		t.Errorf("Attach returned %s, registry lists %s", referrer, got[0].Digest)
	}
}

func TestAttach_ReferrersAPI(t *testing.T) {
	t.Parallel()
	// GAR: a referrers API that answers `"manifests": null` while empty.
	f := ocitest.New(t, func(f *ocitest.Registry) { f.ReferrersAPI, f.NullManifests = true, true })
	subject := f.PutSubject("acme/api", "v1")

	referrer := mustAttach(t, subject, sbomDoc("api"))

	if tags := f.Tags("acme/api"); contains(tags, fallbackTag(subject)) {
		t.Errorf("a registry with the referrers API got a fallback tag: %v", tags)
	}
	if got := referrerDigests(t, subject); len(got) != 1 || !got[strings.SplitN(referrer, "@", 2)[1]] {
		t.Errorf("listed %v, want exactly %s", got, referrer)
	}
}

// The hosted registry's tenant token cannot DELETE. Without SkipReferrersGC
// the second attach to a subject fails after moving the tag.
func TestAttach_SecondSBOMWhenDeleteRefused(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, func(f *ocitest.Registry) { f.RefuseDelete = true })
	subject := f.PutSubject("org/proj/api", "v1")

	first := mustAttach(t, subject, sbomDoc("first"))
	second := mustAttach(t, subject, sbomDoc("second"))

	got := referrerDigests(t, subject)
	for _, r := range []string{first, second} {
		if !got[strings.SplitN(r, "@", 2)[1]] {
			t.Errorf("%s not listed; listed %v", r, got)
		}
	}
	if n := f.DeleteCount(); n != 0 {
		t.Errorf("attach sent %d DELETE(s); it must never garbage-collect the old index", n)
	}
}

func TestAttach_SameSBOMTwiceIsANoOp(t *testing.T) {
	t.Parallel()
	for _, api := range []bool{false, true} {
		t.Run(fmt.Sprintf("referrersAPI=%v", api), func(t *testing.T) {
			t.Parallel()
			f := ocitest.New(t, func(f *ocitest.Registry) { f.ReferrersAPI = api })
			subject := f.PutSubject("acme/api", "v1")
			sbom := sbomDoc("api")

			first := mustAttach(t, subject, sbom)
			before := len(f.Requests())
			// Different informational options: still the SBOM already there.
			again, err := Attach(context.Background(), subject, sbom, AttachOptions{Registry: testRegistry(), Generator: "syft@v9"})
			if err != nil {
				t.Fatal(err)
			}
			if again != first {
				t.Errorf("re-attach returned %s, want the existing %s", again, first)
			}
			for _, r := range f.Requests()[before:] {
				if strings.HasPrefix(r, http.MethodPut) || strings.HasPrefix(r, http.MethodPost) {
					t.Errorf("re-attach wrote to the registry: %s", r)
				}
			}
			if n := len(listed(t, subject)); n != 1 {
				t.Errorf("listed %d referrers after a re-attach, want 1", n)
			}
		})
	}
}

// The tag fallback is an unconditional read-modify-write: concurrent attaches
// lost index entries in every phase-0 trial. All must be listed.
func TestAttach_ConcurrentAttachesAreAllListed(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, func(f *ocitest.Registry) { f.RefuseDelete = true })
	subject := f.PutSubject("org/proj/api", "v1")

	const n = 6
	refs := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refs[i], errs[i] = Attach(context.Background(), subject, sbomDoc(fmt.Sprint("component-", i)), testAttach())
		}()
	}
	wg.Wait()
	got := referrerDigests(t, subject)
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("attach %d: %v", i, errs[i])
		}
		if !got[strings.SplitN(refs[i], "@", 2)[1]] {
			t.Errorf("attach %d's referrer %s not listed", i, refs[i])
		}
	}
	if len(got) != n {
		t.Errorf("listed %d referrers, want %d", len(got), n)
	}
}

// A writer in another process races the fallback index: our index write is
// lost. The attach must notice when it verifies, and re-attach.
func TestAttach_ReattachesWhenTheIndexWriteIsLost(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	first := mustAttach(t, subject, sbomDoc("first"))

	f.LoseIndexWrites(2)
	second := mustAttach(t, subject, sbomDoc("second"))

	got := referrerDigests(t, subject)
	for _, r := range []string{first, second} {
		if !got[strings.SplitN(r, "@", 2)[1]] {
			t.Errorf("%s not listed after verify-and-reattach; listed %v", r, got)
		}
	}
	if lost := f.IndexWritesToLose(); lost != 0 {
		t.Fatalf("the fake lost only %d of 2 index writes: the test did not exercise re-attach", 2-f.IndexWritesToLose())
	}
}

func TestAttach_SkipsBundleRepositories(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("org/proj/bundle.v1/prod", "v1")

	ref, err := Attach(context.Background(), subject, sbomDoc("bundle"), testAttach())
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("Attach to a bundle.v1 repository = (%q, %v), want ErrSkipped", ref, err)
	}
	reason, ok := SkipReason(err)
	if !ok || !strings.Contains(reason, "bundle") || !strings.Contains(reason, "ledger") {
		t.Errorf("SkipReason = %q, %v", reason, ok)
	}
	if n := len(f.Requests()); n != 0 {
		t.Errorf("a skipped attach sent %d request(s)", n)
	}
	if _, ok := SkipReason(errors.New("other")); ok {
		t.Error("SkipReason claimed an unrelated error")
	}
}

func TestBundleSegmentIsTheBundlePackages(t *testing.T) {
	t.Parallel()
	if bundleRepositorySegment != bundle.RepositorySegment {
		t.Fatalf("bundleRepositorySegment %q != bundle.RepositorySegment %q", bundleRepositorySegment, bundle.RepositorySegment)
	}
}

func TestAttach_RefusesBadInput(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	for name, tc := range map[string]struct {
		subject string
		sbom    []byte
	}{
		"tag subject":    {f.Host() + "/acme/api:v1", sbomDoc("x")},
		"bare repo":      {f.Host() + "/acme/api", sbomDoc("x")},
		"empty sbom":     {subject, nil},
		"non-JSON sbom":  {subject, []byte("not json")},
		"absent subject": {f.Host() + "/acme/api@" + digest.FromString("nothing").String(), sbomDoc("x")},
	} {
		if _, err := Attach(context.Background(), tc.subject, tc.sbom, testAttach()); err == nil {
			t.Errorf("%s: Attach succeeded", name)
		}
	}
	if n := len(listed(t, subject)); n != 0 {
		t.Errorf("a refused attach left %d referrer(s)", n)
	}
}

// ─── Fetch ──────────────────────────────────────────────────────────────────

func openStore(t *testing.T) (*ledgerfile.Store, string) {
	t.Helper()
	home := t.TempDir()
	s, err := ledgerfile.Open(home, "proj-sbomoci")
	if err != nil {
		t.Fatal(err)
	}
	return s, home
}

func artifactWith(sbom []byte, referrer string) release.Artifact {
	return release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, SBOM: &release.SBOMRef{
		Digest: digest.FromBytes(sbom).String(), Format: release.SBOMFormatCycloneDXJSON,
		Status: release.SBOMStatusOK, Referrer: referrer,
	}}
}

func TestFetch_LedgerFirstWithoutTheNetwork(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	sbom := sbomDoc("api")
	store, _ := openStore(t)
	if _, err := store.PutSBOM(sbom); err != nil {
		t.Fatal(err)
	}
	before := len(f.Requests())

	data, source, err := FetchWith(context.Background(), store, artifactWith(sbom, f.Host()+"/acme/api@"+digest.FromString("r").String()), subject, testRegistry())
	if err != nil || source != SourceLedger || !bytes.Equal(data, sbom) {
		t.Fatalf("Fetch = (%d bytes, %q, %v), want the ledger copy", len(data), source, err)
	}
	if n := len(f.Requests()) - before; n != 0 {
		t.Errorf("a ledger hit sent %d registry request(s)", n)
	}
}

func TestFetch_ReferrerByRecordThenCachedInTheLedger(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	sbom := sbomDoc("api")
	referrer := mustAttach(t, subject, sbom)
	store, _ := openStore(t)

	before := len(f.Requests())
	data, source, err := FetchWith(context.Background(), store, artifactWith(sbom, referrer), subject, testRegistry())
	if err != nil || source != SourceReferrer || !bytes.Equal(data, sbom) {
		t.Fatalf("Fetch = (%d bytes, %q, %v), want the referrer's bytes", len(data), source, err)
	}
	// The recorded referrer served it: the subject's referrers were never
	// listed (no fallback index read, no referrers API call).
	for _, r := range f.Requests()[before:] {
		if strings.Contains(r, fallbackTag(subject)) || strings.Contains(r, "/referrers/") {
			t.Errorf("a valid recorded referrer still ran discovery: %s", r)
		}
	}
	cached, err := store.GetSBOM(digest.FromBytes(sbom).String())
	if err != nil || !bytes.Equal(cached, sbom) {
		t.Fatalf("the fetched referrer was not cached in the ledger: %v", err)
	}
	if _, source, err = FetchWith(context.Background(), store, artifactWith(sbom, referrer), "", testRegistry()); err != nil || source != SourceLedger {
		t.Errorf("second Fetch = (%q, %v), want the ledger", source, err)
	}
}

func TestFetch_DiscoversByDigestWhenNoReferrerIsRecorded(t *testing.T) {
	t.Parallel()
	for _, api := range []bool{false, true} {
		t.Run(fmt.Sprintf("referrersAPI=%v", api), func(t *testing.T) {
			t.Parallel()
			f := ocitest.New(t, func(f *ocitest.Registry) { f.ReferrersAPI, f.NullManifests = api, api })
			subject := f.PutSubject("acme/api", "v1")
			sbom := sbomDoc("wanted")
			mustAttach(t, subject, sbomDoc("other generator's"))
			mustAttach(t, subject, sbom)

			data, source, err := FetchWith(context.Background(), nil, artifactWith(sbom, ""), subject, testRegistry())
			if err != nil || source != SourceReferrer || !bytes.Equal(data, sbom) {
				t.Fatalf("Fetch = (%q, %q, %v), want the matching referrer", data, source, err)
			}
		})
	}
}

// A referrer another tool attached carries no dev.forge.sbom.digest: it is
// matched by reading its layers.
func TestFetch_DiscoversAReferrerWithoutTheDigestAnnotation(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	sbom := sbomDoc("from ci")

	ref, d, _ := parseSubject(subject)
	repo, err := openRepository(ref.Registry+"/"+ref.Repository, testRegistry())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	subjectDesc, err := resolveSubject(ctx, repo, d)
	if err != nil {
		t.Fatal(err)
	}
	layer := ocispec.Descriptor{MediaType: ArtifactType, Digest: digest.FromBytes(sbom), Size: int64(len(sbom))}
	desc, raw, err := referrerManifest(subjectDesc, layer, layer.Digest, AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(AnnotationSBOMDigest), []byte("org.example.other"), 1)
	desc.Digest, desc.Size = digest.FromBytes(raw), int64(len(raw))
	for _, b := range []struct {
		d    ocispec.Descriptor
		data []byte
	}{{layer, sbom}, {ocispec.DescriptorEmptyJSON, ocispec.DescriptorEmptyJSON.Data}, {desc, raw}} {
		if err := repo.Push(ctx, b.d, bytes.NewReader(b.data)); err != nil {
			t.Fatal(err)
		}
	}

	data, source, err := FetchWith(ctx, nil, artifactWith(sbom, ""), subject, testRegistry())
	if err != nil || source != SourceReferrer || !bytes.Equal(data, sbom) {
		t.Fatalf("Fetch = (%q, %q, %v)", data, source, err)
	}
}

func TestFetch_VerifiesTheDigest(t *testing.T) {
	t.Parallel()
	t.Run("referrer holds other bytes", func(t *testing.T) {
		t.Parallel()
		f := ocitest.New(t, nil)
		subject := f.PutSubject("acme/api", "v1")
		referrer := mustAttach(t, subject, sbomDoc("attached"))
		_, _, err := FetchWith(context.Background(), nil, artifactWith(sbomDoc("recorded"), referrer), subject, testRegistry())
		if !errors.Is(err, ErrSBOMUnavailable) {
			t.Fatalf("Fetch of a digest no referrer holds = %v, want ErrSBOMUnavailable", err)
		}
	})
	t.Run("registry serves damaged bytes", func(t *testing.T) {
		t.Parallel()
		sbom := sbomDoc("api")
		f := ocitest.New(t, func(f *ocitest.Registry) { f.CorruptBlob = digest.FromBytes(sbom) })
		subject := f.PutSubject("acme/api", "v1")
		referrer := mustAttach(t, subject, sbom)
		store, _ := openStore(t)
		_, _, err := FetchWith(context.Background(), store, artifactWith(sbom, referrer), subject, testRegistry())
		if !errors.Is(err, ErrSBOMUnavailable) {
			t.Fatalf("Fetch of damaged bytes = %v, want ErrSBOMUnavailable", err)
		}
		if _, err := store.GetSBOM(digest.FromBytes(sbom).String()); !errors.Is(err, ledgerfile.ErrSBOMNotFound) {
			t.Errorf("damaged bytes reached the ledger: %v", err)
		}
	})
	t.Run("damaged ledger copy is passed over and repaired", func(t *testing.T) {
		t.Parallel()
		f := ocitest.New(t, nil)
		subject := f.PutSubject("acme/api", "v1")
		sbom := sbomDoc("api")
		referrer := mustAttach(t, subject, sbom)
		store, home := openStore(t)
		if _, err := store.PutSBOM(sbom); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "proj-sbomoci", "sboms", fallbackTag("x@"+digest.FromBytes(sbom).String())+".cdx.json")
		if err := os.WriteFile(path, []byte(`{"tampered":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		data, source, err := FetchWith(context.Background(), store, artifactWith(sbom, referrer), subject, testRegistry())
		if err != nil || source != SourceReferrer || !bytes.Equal(data, sbom) {
			t.Fatalf("Fetch = (%d bytes, %q, %v), want the referrer", len(data), source, err)
		}
		if got, err := store.GetSBOM(digest.FromBytes(sbom).String()); err != nil || !bytes.Equal(got, sbom) {
			t.Errorf("ledger copy not repaired: %v", err)
		}
	})
}

// subjectDescriptor resolves subject the way an attach does.
func subjectDescriptor(t *testing.T, subject string) ocispec.Descriptor {
	t.Helper()
	ref, d, err := parseSubject(subject)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := openRepository(ref.Registry+"/"+ref.Repository, testRegistry())
	if err != nil {
		t.Fatal(err)
	}
	desc, err := resolveSubject(context.Background(), repo, d)
	if err != nil {
		t.Fatal(err)
	}
	return desc
}

// pushManifest pushes blobs, then manifest as a mediaType manifest, to
// repository (`<host>/<repo>`) and returns `<repository>@<digest>`.
func pushManifest(t *testing.T, repository, mediaType string, manifest any, blobs ...[]byte) string {
	t.Helper()
	repo, err := openRepository(repository, testRegistry())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, b := range blobs {
		if err := repo.Blobs().Push(ctx, ocispec.Descriptor{MediaType: "application/octet-stream", Digest: digest.FromBytes(b), Size: int64(len(b))}, bytes.NewReader(b)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(raw), Size: int64(len(raw))}
	if err := repo.Manifests().Push(ctx, desc, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	return repository + "@" + desc.Digest.String()
}

// sbomReferrerShape is a well-formed SBOM referrer of subject holding sbom,
// for a test to bend one way.
func sbomReferrerShape(subject ocispec.Descriptor, sbom []byte) ocispec.Manifest {
	subj := ocispec.Descriptor{MediaType: subject.MediaType, Digest: subject.Digest, Size: subject.Size}
	m := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest, ArtifactType: ArtifactType, Config: ocispec.DescriptorEmptyJSON,
		Layers:  []ocispec.Descriptor{{MediaType: ArtifactType, Digest: digest.FromBytes(sbom), Size: int64(len(sbom))}},
		Subject: &subj,
	}
	m.SchemaVersion = 2
	return m
}

// A recorded referrer is a claim the release record makes. Each of these
// holds a layer with exactly the recorded digest, so only the checks on the
// manifest itself stand between it and the ledger.
func TestFetch_RefusesARecordedReferrerThatIsNotThisSubjectsSBOM(t *testing.T) {
	t.Parallel()
	empty := ocispec.DescriptorEmptyJSON.Data
	cases := map[string]struct {
		forge func(t *testing.T, f *ocitest.Registry, subject string, sbom []byte) string
		want  string
	}{
		"in another repository": {func(t *testing.T, f *ocitest.Registry, subject string, sbom []byte) string {
			// Same subject digest, same layer: only the repository differs.
			return pushManifest(t, f.Host()+"/acme/other", ocispec.MediaTypeImageManifest, sbomReferrerShape(subjectDescriptor(t, subject), sbom), sbom, empty)
		}, "not the subject's"},
		"of another subject": {func(t *testing.T, f *ocitest.Registry, _ string, sbom []byte) string {
			// A genuine attach of the same bytes to a different image.
			return mustAttach(t, f.PutSubject("acme/api", "v2"), sbom)
		}, "its subject is"},
		"naming no subject": {func(t *testing.T, f *ocitest.Registry, subject string, sbom []byte) string {
			m := sbomReferrerShape(subjectDescriptor(t, subject), sbom)
			m.Subject = nil
			return pushManifest(t, f.Host()+"/acme/api", ocispec.MediaTypeImageManifest, m, sbom, empty)
		}, "names no subject"},
		"not CycloneDX": {func(t *testing.T, f *ocitest.Registry, subject string, sbom []byte) string {
			m := sbomReferrerShape(subjectDescriptor(t, subject), sbom)
			m.ArtifactType = "application/spdx+json"
			return pushManifest(t, f.Host()+"/acme/api", ocispec.MediaTypeImageManifest, m, sbom, empty)
		}, `artifact type is "application/spdx+json"`},
		"a plain image manifest": {func(t *testing.T, f *ocitest.Registry, _ string, sbom []byte) string {
			config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
			m := ocispec.Manifest{
				MediaType: ocispec.MediaTypeImageManifest,
				Config:    ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(config), Size: int64(len(config))},
				Layers:    []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageLayer, Digest: digest.FromBytes(sbom), Size: int64(len(sbom))}},
			}
			m.SchemaVersion = 2
			return pushManifest(t, f.Host()+"/acme/api", ocispec.MediaTypeImageManifest, m, sbom, config)
		}, "artifact type is \"" + ocispec.MediaTypeImageConfig + "\""},
		"a docker manifest": {func(t *testing.T, f *ocitest.Registry, subject string, sbom []byte) string {
			const docker = "application/vnd.docker.distribution.manifest.v2+json"
			m := sbomReferrerShape(subjectDescriptor(t, subject), sbom)
			m.MediaType = docker
			return pushManifest(t, f.Host()+"/acme/api", docker, m, sbom, empty)
		}, "not an OCI image or artifact manifest"},
	}
	for name, tc := range cases {
		for _, api := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/referrersAPI=%v", name, api), func(t *testing.T) {
				t.Parallel()
				f := ocitest.New(t, func(f *ocitest.Registry) { f.ReferrersAPI = api })
				subject := f.PutSubject("acme/api", "v1")
				sbom := sbomDoc("api")
				referrer := tc.forge(t, f, subject, sbom)
				store, _ := openStore(t)

				data, _, err := FetchWith(context.Background(), store, artifactWith(sbom, referrer), subject, testRegistry())
				if !errors.Is(err, ErrSBOMUnavailable) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Fetch = (%d bytes, %v), want ErrSBOMUnavailable saying %q", len(data), err, tc.want)
				}
				if _, err := store.GetSBOM(digest.FromBytes(sbom).String()); !errors.Is(err, ledgerfile.ErrSBOMNotFound) {
					t.Errorf("a refused referrer's bytes reached the ledger: %v", err)
				}
			})
		}
	}
}

// A refused recorded referrer is not the end: discovery on the subject still
// finds the genuine one.
func TestFetch_FallsThroughToDiscoveryPastARefusedReferrer(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	sbom := sbomDoc("api")
	mustAttach(t, subject, sbom)
	forged := pushManifest(t, f.Host()+"/acme/other", ocispec.MediaTypeImageManifest, sbomReferrerShape(subjectDescriptor(t, subject), sbom), sbom, ocispec.DescriptorEmptyJSON.Data)
	store, _ := openStore(t)

	data, source, err := FetchWith(context.Background(), store, artifactWith(sbom, forged), subject, testRegistry())
	if err != nil || source != SourceReferrer || !bytes.Equal(data, sbom) {
		t.Fatalf("Fetch = (%d bytes, %q, %v), want the discovered referrer", len(data), source, err)
	}
	if _, err := store.GetSBOM(digest.FromBytes(sbom).String()); err != nil {
		t.Errorf("the discovered copy was not cached: %v", err)
	}
}

// A referrer in the OCI 1.1 release candidates' artifact manifest shape
// (content in blobs) is accepted under the same checks.
func TestFetch_AcceptsAnArtifactManifestReferrer(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	sbom := sbomDoc("api")
	desc := subjectDescriptor(t, subject)
	referrer := pushManifest(t, f.Host()+"/acme/api", mediaTypeArtifactManifest, map[string]any{
		"mediaType":    mediaTypeArtifactManifest,
		"artifactType": ArtifactType,
		"blobs":        []ocispec.Descriptor{{MediaType: ArtifactType, Digest: digest.FromBytes(sbom), Size: int64(len(sbom))}},
		"subject":      ocispec.Descriptor{MediaType: desc.MediaType, Digest: desc.Digest, Size: desc.Size},
	}, sbom)

	data, source, err := FetchWith(context.Background(), nil, artifactWith(sbom, referrer), subject, testRegistry())
	if err != nil || source != SourceReferrer || !bytes.Equal(data, sbom) {
		t.Fatalf("Fetch = (%d bytes, %q, %v), want the artifact manifest's blob", len(data), source, err)
	}
}

// A listing is not proof either: on a tag-fallback registry any writer can
// put any descriptor in a subject's index. Here the index of v1 is replaced
// by v2's, which lists a genuine SBOM referrer — of v2.
func TestFetch_DiscoveryHoldsListedReferrersToTheSubject(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	other := f.PutSubject("acme/api", "v2")
	sbom := sbomDoc("api")
	mustAttach(t, other, sbom)

	repo, err := openRepository(f.Host()+"/acme/api", testRegistry())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	idxDesc, rc, err := repo.FetchReference(ctx, fallbackTag(other))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PushReference(ctx, idxDesc, bytes.NewReader(idx), fallbackTag(subject)); err != nil {
		t.Fatal(err)
	}
	if len(listed(t, subject)) != 1 {
		t.Fatalf("the forged index lists %d referrer(s) of %s, want 1", len(listed(t, subject)), subject)
	}

	_, _, err = FetchWith(ctx, nil, artifactWith(sbom, ""), subject, testRegistry())
	if !errors.Is(err, ErrSBOMUnavailable) || !strings.Contains(err.Error(), "its subject is") {
		t.Fatalf("Fetch through a forged listing = %v, want ErrSBOMUnavailable naming the subject mismatch", err)
	}
}

// With no subject a recorded referrer cannot be checked, so it is not
// followed — not one registry request — and only the ledger copy serves.
func TestFetch_DoesNotFollowARecordedReferrerWithoutASubject(t *testing.T) {
	t.Parallel()
	f := ocitest.New(t, nil)
	subject := f.PutSubject("acme/api", "v1")
	sbom := sbomDoc("api")
	referrer := mustAttach(t, subject, sbom)
	store, _ := openStore(t)
	before := len(f.Requests())

	_, _, err := FetchWith(context.Background(), store, artifactWith(sbom, referrer), "", testRegistry())
	if !errors.Is(err, ErrSBOMUnavailable) || !errors.Is(err, errUnverifiableReferrer) {
		t.Fatalf("Fetch with no subject = %v, want ErrSBOMUnavailable saying the referrer was not followed", err)
	}
	if n := len(f.Requests()) - before; n != 0 {
		t.Errorf("an unverifiable referrer was followed: %d registry request(s)", n)
	}
	if _, err := store.PutSBOM(sbom); err != nil {
		t.Fatal(err)
	}
	if data, source, err := FetchWith(context.Background(), store, artifactWith(sbom, referrer), "", testRegistry()); err != nil || source != SourceLedger || !bytes.Equal(data, sbom) {
		t.Errorf("Fetch with a ledger copy = (%q, %v), want the ledger", source, err)
	}
}

func TestFetch_NothingToRead(t *testing.T) {
	t.Parallel()
	if _, _, err := Fetch(context.Background(), nil, release.Artifact{}, ""); !errors.Is(err, ErrNoSBOM) {
		t.Errorf("no SBOMRef: %v, want ErrNoSBOM", err)
	}
	unavailable := release.Artifact{SBOM: &release.SBOMRef{Status: release.SBOMStatusUnavailable, Reason: "syft missing"}}
	if _, _, err := Fetch(context.Background(), nil, unavailable, ""); !errors.Is(err, ErrNoSBOM) {
		t.Errorf("unavailable SBOMRef: %v, want ErrNoSBOM", err)
	}
	store, _ := openStore(t)
	if _, _, err := FetchWith(context.Background(), store, artifactWith(sbomDoc("x"), ""), "", testRegistry()); !errors.Is(err, ErrSBOMUnavailable) {
		t.Errorf("not in the ledger, no referrer, no subject: %v, want ErrSBOMUnavailable", err)
	}
	// GAR's null list: discovery finds nothing, and says so.
	f := ocitest.New(t, func(f *ocitest.Registry) { f.ReferrersAPI, f.NullManifests = true, true })
	subject := f.PutSubject("acme/api", "v1")
	if _, _, err := FetchWith(context.Background(), store, artifactWith(sbomDoc("x"), ""), subject, testRegistry()); !errors.Is(err, ErrSBOMUnavailable) {
		t.Errorf("subject with no referrers: %v, want ErrSBOMUnavailable", err)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
