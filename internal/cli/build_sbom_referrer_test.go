package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/internal/sbomoci"
	"github.com/reliant-labs/forge/internal/sbomoci/ocitest"
	"github.com/reliant-labs/forge/pkg/release"
)

// attachCall is one attach the hook asked for.
type attachCall struct {
	subject string
	opts    sbomoci.AttachOptions
}

// useReferrerRegistry points the referrer hook at the real sbomoci.Attach
// with an anonymous credential (an ocitest.Registry asks for none), and
// records each call. It mutates package vars: the test must not be parallel.
func useReferrerRegistry(t *testing.T) *[]attachCall {
	t.Helper()
	prevAttach, prevCred := sbomReferrerAttach, sbomRegistryCredential
	var (
		mu    sync.Mutex
		calls []attachCall
	)
	sbomReferrerAttach = func(ctx context.Context, subject string, data []byte, opts sbomoci.AttachOptions) (string, error) {
		mu.Lock()
		calls = append(calls, attachCall{subject, opts})
		mu.Unlock()
		opts.Registry.Credential = func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil }
		return sbomoci.Attach(ctx, subject, data, opts)
	}
	sbomRegistryCredential = func(string, string, *KCLEntities, string) (auth.CredentialFunc, error) { return nil, nil }
	t.Cleanup(func() { sbomReferrerAttach, sbomRegistryCredential = prevAttach, prevCred })
	return &calls
}

// generatedFor is the hook input for an image name whose SBOM is data.
func generatedFor(dir, name string, art release.Artifact, data []byte) sbomGenerated {
	art.SBOM = &release.SBOMRef{Digest: sbom.Digest(data), Format: release.SBOMFormatCycloneDXJSON,
		Generator: "syft@v1.57.2", Source: release.SBOMSourceRegistry, Status: release.SBOMStatusOK}
	return sbomGenerated{ProjectDir: dir, Env: "prod", Version: "v2.0.0", Name: name, Artifact: art, Bytes: data, Built: true,
		CutAt: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
}

func splitSubject(subject string) (name, digest string) {
	name, digest, _ = strings.Cut(subject, "@")
	return name, digest
}

func TestAttachSBOMReferrer_AttachesBesideTheImage(t *testing.T) {
	calls := useReferrerRegistry(t)
	f := ocitest.New(t, nil)
	name, digest := splitSubject(f.PutSubject("acme/api", "v1"))
	data := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
	in := generatedFor(t.TempDir(), name, sbomTestImage(digest), data)

	ref, err := attachSBOMReferrer(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Referrer, name+"@sha256:") || ref.Digest != in.Artifact.SBOM.Digest ||
		ref.Status != release.SBOMStatusOK || ref.Reason != "" {
		t.Fatalf("ref = %+v", ref)
	}
	if len(*calls) != 1 {
		t.Fatalf("attach called %d times", len(*calls))
	}
	c := (*calls)[0]
	if c.subject != name+"@"+digest || !c.opts.Created.Equal(in.CutAt) || c.opts.Release != "v2.0.0" || c.opts.Generator != "syft@v1.57.2" {
		t.Errorf("attach call = %+v", c)
	}
	got, source, err := sbomoci.FetchWith(context.Background(), nil, release.Artifact{SBOM: &ref}, name+"@"+digest,
		sbomoci.RegistryOptions{Credential: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil }})
	if err != nil || source != sbomoci.SourceReferrer || !bytes.Equal(got, data) {
		t.Errorf("the recorded referrer does not read back: (%q, %q, %v)", got, source, err)
	}
}

func TestAttachSBOMReferrer_LeavesAloneWhatItDoesNotAttach(t *testing.T) {
	calls := useReferrerRegistry(t)
	f := ocitest.New(t, nil)
	name, digest := splitSubject(f.PutSubject("acme/api", "v1"))
	data := []byte(`{"bomFormat":"CycloneDX"}`)

	off := t.TempDir()
	if err := os.WriteFile(filepath.Join(off, "forge.yaml"), []byte("name: shop\nvuln:\n  attach_sbom: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	variant := release.Artifact{Kind: release.KindOCI, Mode: release.ModeVariant, Digests: map[string]string{"prod": digest}}
	file := release.Artifact{Kind: release.KindFile, Version: "v2.0.0", Integrity: sha("f")}
	for label, in := range map[string]sbomGenerated{
		"vuln.attach_sbom: false": generatedFor(off, name, sbomTestImage(digest), data),
		"a variant image":         generatedFor(t.TempDir(), name, variant, data),
		"a file artifact":         generatedFor(t.TempDir(), "bin/api", file, data),
	} {
		ref, err := attachSBOMReferrer(context.Background(), in)
		if err != nil || ref != *in.Artifact.SBOM {
			t.Errorf("%s: (%+v, %v), want the ref unchanged", label, ref, err)
		}
	}
	if len(*calls) != 0 || len(f.Requests()) != 0 {
		t.Errorf("attached anyway: %d call(s), %d request(s)", len(*calls), len(f.Requests()))
	}
}

func TestAttachSBOMReferrer_BundleRepositoryIsSkippedWithAReason(t *testing.T) {
	useReferrerRegistry(t)
	f := ocitest.New(t, nil)
	name, digest := splitSubject(f.PutSubject("org/proj/bundle.v1/prod", "v1"))
	in := generatedFor(t.TempDir(), name, sbomTestImage(digest), []byte(`{"bomFormat":"CycloneDX"}`))

	ref, err := attachSBOMReferrer(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Status != release.SBOMStatusOK || ref.Referrer != "" || !strings.Contains(ref.Reason, "bundle") || ref.Digest != in.Artifact.SBOM.Digest {
		t.Errorf("ref = %+v, want status ok, no referrer, the skip reason", ref)
	}
}

// Through the seam: the cut records the referrer, and an attach that fails
// warns and leaves the SBOM recorded without one.
func TestGenerateReleaseSBOMs_AttachesReferrers(t *testing.T) {
	dir := newLedgerTestProject(t, "sbomref")
	useFakeSyft(t, &cutSyft{}, "")
	useReferrerRegistry(t)
	f := ocitest.New(t, func(r *ocitest.Registry) { r.RefuseDelete = true })
	name, digest := splitSubject(f.PutSubject("acme/api", "v1"))
	missing := f.Host() + "/acme/gone"
	_, pgDigest := splitSubject(f.PutSubject("library/postgres", "16"))
	postgres := f.Host() + "/library/postgres"
	artifacts := map[string]release.Artifact{name: sbomTestImage(digest), missing: sbomTestImage(sha("9")), postgres: sbomTestImage(pgDigest)}
	var warn bytes.Buffer
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Env: "prod", Version: "v3.0.0",
		CreatedAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		Built:     map[string]bool{name: true, missing: true}}, artifacts, &warn)

	got := artifacts[name].SBOM
	if got == nil || got.Status != release.SBOMStatusOK || !strings.HasPrefix(got.Referrer, name+"@sha256:") {
		t.Fatalf("%s: SBOM %+v", name, got)
	}
	lost := artifacts[missing].SBOM
	if lost == nil || lost.Status != release.SBOMStatusOK || !lost.HasSBOM() || lost.Referrer != "" {
		t.Errorf("%s: SBOM %+v, want recorded without a referrer", missing, lost)
	}
	if !strings.Contains(warn.String(), "not attached beside the image") || !strings.Contains(warn.String(), missing) {
		t.Errorf("the failed attach was not warned:\n%s", warn.String())
	}
	// A pinned third-party image: recorded, said why, and never warned.
	pg := artifacts[postgres].SBOM
	if pg == nil || pg.Status != release.SBOMStatusOK || pg.Referrer != "" ||
		pg.Reason != "referrer not attached: "+postgres+"@"+pgDigest+" is not pushed by this project" {
		t.Errorf("%s: SBOM %+v", postgres, pg)
	}
	if strings.Contains(warn.String(), "postgres") {
		t.Errorf("a third-party image was warned about:\n%s", warn.String())
	}
	if err := (release.Release{Version: "v3.0.0", Artifacts: artifacts}).Validate(); err != nil {
		t.Errorf("the recorded artifacts no longer validate: %v", err)
	}
	// The ledger copy is there whatever the registry did.
	store := testStore(t, dir)
	for _, n := range []string{name, missing} {
		if _, err := store.GetSBOM(artifacts[n].SBOM.Digest); err != nil {
			t.Errorf("%s: ledger copy: %v", n, err)
		}
	}
}

// Only a subject this project pushed gets an attach attempt.
func TestAttachSBOMReferrer_OnlyWhereTheProjectPushed(t *testing.T) {
	calls := useReferrerRegistry(t)
	prevName := hostedProjectName
	hostedProjectName = func() string { return "shop" }
	t.Cleanup(func() { hostedProjectName = prevName })
	f := ocitest.New(t, nil)
	data := []byte(`{"bomFormat":"CycloneDX"}`)
	hosted := &KCLEntities{ControlPlane: &ControlPlaneEntity{RegistryHost: f.Host()}}
	base := platformPushBase(hosted)
	if base == "" {
		t.Fatal("no push base for the test control plane")
	}
	builds := func(spec string) *KCLEntities {
		w := WorkloadEntity{Name: "api", Image: "api"}
		w.Spec.Image = spec
		return &KCLEntities{Workloads: []WorkloadEntity{w}}
	}
	pinned := func(spec string) *KCLEntities {
		w := WorkloadEntity{Name: "db"} // forge does not build it
		w.Spec.Image = spec
		return &KCLEntities{Workloads: []WorkloadEntity{w}}
	}
	for _, tc := range []struct {
		label    string
		repo     string
		entities func(name string) *KCLEntities
		attached bool
	}{
		{"under the platform push base", strings.TrimPrefix(base, f.Host()+"/") + "/api", func(string) *KCLEntities { return hosted }, true},
		{"the repository a built workload resolves to", "acme/api", func(name string) *KCLEntities { return builds(name + ":v1") }, true},
		{"a pinned third-party image", "library/postgres", func(name string) *KCLEntities { return pinned(name + ":16") }, false},
		{"no declaration at all", "vendor/agent", func(string) *KCLEntities { return nil }, false},
	} {
		name, digest := splitSubject(f.PutSubject(tc.repo, "v1"))
		in := generatedFor(t.TempDir(), name, sbomTestImage(digest), data)
		in.Built, in.Entities = false, tc.entities(name)
		before := len(*calls)

		ref, err := attachSBOMReferrer(context.Background(), in)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if got := len(*calls) > before; got != tc.attached {
			t.Errorf("%s: attach attempted = %v, want %v", tc.label, got, tc.attached)
		}
		if tc.attached && !strings.HasPrefix(ref.Referrer, name+"@sha256:") {
			t.Errorf("%s: ref = %+v, want a referrer", tc.label, ref)
		}
		if !tc.attached && (ref.Referrer != "" || ref.Status != release.SBOMStatusOK ||
			ref.Reason != "referrer not attached: "+name+"@"+digest+" is not pushed by this project") {
			t.Errorf("%s: ref = %+v", tc.label, ref)
		}
	}
}

// The subject's repository comes from what each caller records, and the
// cut's three OCI kinds keep the repository they had.
func TestSBOMSubjectRepository(t *testing.T) {
	t.Parallel()
	oci := func(uri string) release.Artifact {
		return release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, URI: uri, Digests: map[string]string{release.SharedVariant: sha("a")}}
	}
	for _, tc := range []struct {
		label, name string
		art         release.Artifact
		want        string
	}{
		{"cut: a built image (key is the repository, no URI)", "ghcr.io/acme/api", oci(""), "ghcr.io/acme/api"},
		{"cut: a hosted static site (key is the repository, no URI)", "registry.reliantapi.com/org-1/shop/web/static.v1", oci(""), "registry.reliantapi.com/org-1/shop/web/static.v1"},
		{"cut: a hosted backend (URI is its push-base namespace)", "registry.reliantapi.com/org-1/shop/api", oci("registry.reliantapi.com/org-1/shop"), "registry.reliantapi.com/org-1/shop/api"},
		{"cut: a mirrored backend (URI is the namespace)", "registry.reliantapi.com/org-1/shop/mirror/docker.io/library/redis", oci("registry.reliantapi.com/org-1/shop/mirror/docker.io/library"), "registry.reliantapi.com/org-1/shop/mirror/docker.io/library/redis"},
		{"record: workload key, URI with a tag", "api", oci("ghcr.io/acme/api:v1"), "ghcr.io/acme/api"},
		{"record: workload key, bare URI", "api", oci("us-docker.pkg.dev/p/r/api"), "us-docker.pkg.dev/p/r/api"},
		{"record: workload key, URI with a digest", "api", oci("localhost:5000/api@" + sha("b")), "localhost:5000/api"},
		{"record: workload key, URI on a port with a tag", "api", oci("localhost:5000/acme/api:v1"), "localhost:5000/acme/api"},
		{"no repository anywhere", "api", oci(""), ""},
		{"a URI with no registry host", "api", oci("acme/api:v1"), ""},
	} {
		if got := sbomSubjectRepository(tc.name, tc.art, nil); got != tc.want {
			t.Errorf("%s: sbomSubjectRepository(%q, URI %q) = %q, want %q", tc.label, tc.name, tc.art.URI, got, tc.want)
		}
	}
}

// The cut's hosted backend in an env with no push base: keyed by its bare
// artifact name, with its namespace as URI. A record of a workload of the same
// name keeps its URI whole.
func TestSBOMSubjectRepository_HostedBackendWithoutAPushBase(t *testing.T) {
	t.Parallel()
	w := WorkloadEntity{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHosted}}
	w.Spec.Image = "ghcr.io/acme/api:v3"
	e := &KCLEntities{Workloads: []WorkloadEntity{w}}
	if key := hostedArtifactKey(e, w); key != "api" {
		t.Fatalf("hostedArtifactKey = %q; the fixture no longer models a no-push-base env", key)
	}
	art := release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, URI: "ghcr.io/acme", Digests: map[string]string{release.SharedVariant: sha("a")}}
	if got := sbomSubjectRepository("api", art, e); got != "ghcr.io/acme/api" {
		t.Errorf("cut hosted backend: %q, want ghcr.io/acme/api", got)
	}
	art.URI = "ghcr.io/acme/api:v3"
	if got := sbomSubjectRepository("api", art, e); got != "ghcr.io/acme/api" {
		t.Errorf("record of the same workload: %q, want ghcr.io/acme/api", got)
	}
}

// A record keys its artifact by workload: the subject, the referrer and the
// owned-subject filter all follow URI, never the workload name.
func TestAttachSBOMReferrer_NameIsNotTheRepository(t *testing.T) {
	calls := useReferrerRegistry(t)
	f := ocitest.New(t, nil)
	data := []byte(`{"bomFormat":"CycloneDX"}`)
	repo, digest := splitSubject(f.PutSubject("acme/api", "v1"))
	art := sbomTestImage(digest)
	art.URI = repo + ":v1"

	// Pushed by this pipeline.
	in := generatedFor(t.TempDir(), "api", art, data)
	ref, err := attachSBOMReferrer(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Referrer, repo+"@sha256:") {
		t.Fatalf("ref = %+v, want a referrer in %s", ref, repo)
	}
	if c := (*calls)[len(*calls)-1]; c.subject != repo+"@"+digest {
		t.Errorf("attached to %s, want %s", c.subject, repo+"@"+digest)
	}

	// Not built here, but the repository a workload this env builds resolves
	// to: owned by its URI repository, though the name matches nothing.
	w := WorkloadEntity{Name: "api", Image: "api"}
	w.Spec.Image = repo + ":v2"
	in.Built, in.Entities = false, &KCLEntities{Workloads: []WorkloadEntity{w}}
	before := len(*calls)
	if _, err := attachSBOMReferrer(context.Background(), in); err != nil || len(*calls) != before+1 {
		t.Errorf("a workload-built repository under another name: err %v, attach attempted %v", err, len(*calls) > before)
	}

	// A pinned third-party image under a workload name: the reason names
	// the URI repository.
	pgRepo, pgDigest := splitSubject(f.PutSubject("library/postgres", "16"))
	pg := sbomTestImage(pgDigest)
	pg.URI = pgRepo + ":16"
	db := generatedFor(t.TempDir(), "db", pg, data)
	db.Built, db.Entities = false, in.Entities
	before = len(*calls)
	ref, err = attachSBOMReferrer(context.Background(), db)
	if err != nil || len(*calls) != before || ref.Referrer != "" ||
		ref.Reason != "referrer not attached: "+pgRepo+"@"+pgDigest+" is not pushed by this project" {
		t.Errorf("third-party image under a workload name: (%+v, %v)", ref, err)
	}

	// No repository recorded at all: said, not attempted.
	bare := generatedFor(t.TempDir(), "api", sbomTestImage(digest), data)
	before = len(*calls)
	if ref, err := attachSBOMReferrer(context.Background(), bare); err != nil || len(*calls) != before ||
		!strings.Contains(ref.Reason, "no registry repository") {
		t.Errorf("no repository: (%+v, %v)", ref, err)
	}
}

func TestRegistryCredentialFor_PlatformHostTakesTheControlPlaneIdentity(t *testing.T) {
	const host = "registry.example.test"
	e := &KCLEntities{ControlPlane: &ControlPlaneEntity{RegistryHost: host}}
	dir := t.TempDir()
	key := dir + "\x00prod"
	bundleRegistryAuths.Store(key, bundleRegistryAuth{Host: host, credential: func(context.Context) (string, error) { return "rlat_test", nil }})
	t.Cleanup(func() { bundleRegistryAuths.Delete(key) })

	cred, err := registryCredentialFor(dir, "prod", e, host)
	if err != nil || cred == nil {
		t.Fatalf("platform host: (%v, %v), want the control-plane credential", cred, err)
	}
	if c, err := cred(context.Background(), host); err != nil || c.Password != "rlat_test" {
		t.Errorf("credential = (%+v, %v)", c, err)
	}
	for _, other := range []string{"ghcr.io", "localhost:5000", "127.0.0.1:5000"} {
		if cred, err := registryCredentialFor(dir, "prod", e, other); err != nil || cred != nil {
			t.Errorf("%s: (%v, %v), want the docker store (nil)", other, cred != nil, err)
		}
	}
	if cred, err := registryCredentialFor(dir, "prod", nil, host); err != nil || cred != nil {
		t.Errorf("no control plane: (%v, %v), want the docker store (nil)", cred != nil, err)
	}
}
