package cli

// AN ENV DEPLOY RECORDS A SNAPSHOT OF THE ENVIRONMENT WHEN ITS CONTROL PLANE
// TAKES ONE (env_build_evidence.go; contract: control-plane#2321).
//
// These drive the real `forge env build` / `forge env deploy` against
// fakeDeployService, extended by fakeEvidenceSeals into a control plane that
// advertises (or withholds, or switches off) the evidence-seal feature and
// verifies a snapshot bundle the way control-plane's RecordBundle does. What
// they pin:
//
//   - capable: the bundle the deploy records is a v2 snapshot bundle the
//     server's own checks accept, describing the whole env (no base, every
//     consumer selected), and the deploy stays on the legacy path;
//   - not capable: today's legacy bundle, untouched;
//   - a bundle with a service catalog seals it only when the control plane
//     also takes one (evidence-seal-service-catalog), and never loses it:
//     without the feature, or over the bound, it is the legacy bundle;
//   - switched off (snapshot_refused) and an older replica (a plain
//     InvalidArgument): exactly one retry with the legacy bundle, and the
//     deploy succeeds;
//   - --target is unaffected, and the release routes off its intents, never
//     off the bundle's snapshot;
//   - the resulting promotion is a base `release compose` accepts.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

// fakeEvidenceSeals is the evidence half of a control plane. Its methods run
// under fakeDeployService.mu.
type fakeEvidenceSeals struct {
	// advertise names evidence-seal in snapshot_features.
	advertise bool
	// disabled is the operator switch (§6a): every snapshot on an intent-less
	// release is refused with snapshot_refused.
	disabled bool
	// plainRefusal is an older replica: a plain InvalidArgument, no reason.
	plainRefusal bool
	// catalog names evidence-seal-service-catalog beside evidence-seal.
	catalog bool

	// attempts counts RecordBundle calls that carried a snapshot; sealed
	// holds the ones accepted; legacy counts the ones that carried none.
	attempts int
	sealed   []release.BundleDoc
	legacy   int
	// invalid is every snapshot the server's checks refused: a defect in
	// forge's builder, which no test here expects.
	invalid []string
	fake    *fakeDeployService
}

func (e *fakeEvidenceSeals) capabilities() map[string]any {
	caps := map[string]any{
		"bundleDocumentFeatures": release.BundleDocFeatures(),
		"snapshotSchemaVersions": []string{release.SnapshotSchema},
		"immutableSnapshotSeals": true,
		"frozenPromotionBundles": true,
	}
	if e.advertise {
		features := []string{snapshotFeatureEvidenceSeal}
		if e.catalog {
			features = append(features, snapshotFeatureEvidenceSealServiceCatalog)
		}
		caps["snapshotFeatures"] = features
	}
	return caps
}

// recordBundle handles a RecordBundle whose config carries a snapshot and
// reports whether it answered. A bundle with none is the fake's usual path.
func (e *fakeEvidenceSeals) recordBundle(w http.ResponseWriter, body map[string]any) bool {
	str := func(k string) string { s, _ := body[k].(string); return s }
	config, _ := base64.StdEncoding.DecodeString(str("config"))
	var probe struct {
		Snapshot *release.Snapshot `json:"snapshot"`
	}
	_ = json.Unmarshal(config, &probe)
	if probe.Snapshot == nil {
		e.legacy++
		return false
	}
	e.attempts++
	refuse := func(reason, msg string) bool {
		if reason != "" {
			w.Header().Set(cloud.ReasonHeader, reason)
		}
		connectErr(w, http.StatusBadRequest, "invalid_argument", msg)
		return true
	}
	switch {
	case e.disabled:
		return refuse("snapshot_refused", "snapshot bundle refused: evidence seals are disabled on this control plane")
	case e.plainRefusal:
		return refuse("", "cut/seal/bundle snapshot association mismatch")
	}
	doc, err := e.verify(config, str("manifestsLayer"))
	if err != nil {
		e.invalid = append(e.invalid, err.Error())
		return refuse("snapshot_refused", "snapshot bundle refused: "+err.Error())
	}
	manifest, _ := base64.StdEncoding.DecodeString(str("manifest"))
	digest := release.SpecDigest(manifest)
	recs, err := hostedRecordsFromBundleBlobs(digest, str("repository"), e.fake.layerFetch)
	if err != nil {
		return refuse("", err.Error())
	}
	if e.fake.bundles == nil {
		e.fake.bundles = map[string]map[string]*fakeDeployment{}
	}
	e.fake.bundles[str("environmentId")] = recs
	e.sealed = append(e.sealed, doc)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"bundle": map[string]any{
			"id": "bundle-" + digest[7:15], "environmentId": str("environmentId"), "releaseVersion": doc.Release,
			"digest": digest, "reference": str("repository") + "@" + digest, "createdAt": "2026-09-23T00:00:00Z",
			"snapshot": doc.Snapshot, "snapshotDigest": doc.SnapshotDigest,
		},
		"created": true,
	})
	return true
}

// verify is control-plane's evidence check (contract §3): the strict decode
// and v2 layout, the inline layer, a snapshot that describes the whole env on
// its own, and every built image recorded by the release.
func (e *fakeEvidenceSeals) verify(config []byte, layerB64 string) (release.BundleDoc, error) {
	doc, err := release.DecodeBundleDoc(config)
	if err != nil {
		return doc, err
	}
	if err := doc.Validate(); err != nil {
		return doc, err
	}
	if doc.Schema != release.SnapshotBundleSchema || doc.Release == "" {
		return doc, fmt.Errorf("snapshot requires cut release")
	}
	layer, _ := base64.StdEncoding.DecodeString(layerB64)
	if len(layer) == 0 {
		return doc, fmt.Errorf("snapshot manifests_layer is required")
	}
	s := *doc.Snapshot
	if err := release.ValidateSnapshotLayer(s, layer); err != nil {
		return doc, err
	}
	if s.Base != nil || s.BootstrapFromPromotionID != "" || len(s.Evidence) != 0 {
		return doc, fmt.Errorf("an evidence snapshot describes the whole environment: it names no base promotion and adopts none")
	}
	if err := release.ValidateSnapshotTransition(nil, s); err != nil {
		return doc, err
	}
	if s.Project != doc.Project || s.Environment != doc.Env {
		return doc, fmt.Errorf("snapshot scope/digest mismatch")
	}
	rel, ok := e.fake.releases[doc.Release]
	if !ok {
		return doc, fmt.Errorf("that version has not been built")
	}
	if len(rel.SnapshotRelease) > 0 {
		return doc, fmt.Errorf("cut/seal/bundle snapshot association mismatch")
	}
	recorded := map[string]bool{}
	for _, a := range rel.Artifacts {
		recorded[a.Digest] = true
	}
	for id, a := range s.Artifacts {
		if a.Kind == release.KindOCI && a.Origin.External == nil && !recorded[a.Content] {
			return doc, fmt.Errorf("snapshot binds built images the release does not record: [%s]", id)
		}
	}
	return doc, nil
}

// evidenceFixture is the hosted e2e project of TestHostedCLIEndToEnd, under
// git (an evidence snapshot names the commit it renders), with its release
// cut and ready to deploy.
type evidenceFixture struct {
	fake *fakeDeployService
	ev   *fakeEvidenceSeals
	dir  string
}

func newEvidenceFixture(t *testing.T, ev *fakeEvidenceSeals, apiFields string) *evidenceFixture {
	t.Helper()
	return newEvidenceFixtureWithCatalog(t, ev, apiFields, nil)
}

// newEvidenceFixtureWithCatalog is newEvidenceFixture whose project has
// generated a service catalog (catalog; nil for none), as a Go service does.
func newEvidenceFixtureWithCatalog(t *testing.T, ev *fakeEvidenceSeals, apiFields string, catalog []byte) *evidenceFixture {
	t.Helper()
	fake := newFakeDeployService(map[string]string{})
	if ev != nil {
		ev.fake = fake
		fake.evidence = ev
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := writeHostedProject(t, srv.URL, apiFields)
	initGitRepo(t, dir, map[string]string{".gitignore": ".forge/\nbin/\ngen/\n**/kcl.mod.lock\n"})
	if catalog != nil {
		// gen/ is ignored, as a generated catalog is: the tree stays clean.
		if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(servicecatalog.GeneratedPath)), catalog, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/example/acme.git")
	t.Chdir(dir)
	t.Setenv("ACME_CP_TOKEN", "rlat_e2e")
	t.Setenv("FORGE_HOME", t.TempDir())
	prevPoll := hostedPollInterval
	hostedPollInterval = time.Millisecond
	t.Cleanup(func() { hostedPollInterval = prevPoll })
	stubHostedRegistry(t)
	stubHostedBundleRegistry(t, fake)
	// The snapshot verifies each bound image's registry descriptor; the
	// fixture's only image is pinned, so its descriptor is its digest.
	prevResolve := externalImageDigestResolver
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		if at := strings.LastIndex(ref, "@"); at >= 0 {
			return ref[at+1:], []string{"linux/amd64", "linux/arm64"}, nil
		}
		return "", nil, fmt.Errorf("fixture registry resolves only pinned refs, not %s", ref)
	}
	t.Cleanup(func() { externalImageDigestResolver = prevResolve })
	if out, err := runForge(t, "env", "build", "hosted", "--release", "v1", "--no-build"); err != nil {
		t.Fatalf("release cut: %v\n%s", err, out)
	}
	return &evidenceFixture{fake: fake, ev: ev, dir: dir}
}

func (f *evidenceFixture) deploy(t *testing.T, args ...string) {
	t.Helper()
	argv := append([]string{"env", "deploy", "hosted", "v1"}, args...)
	argv = append(argv, "--yes", "--no-wait")
	if out, err := runForge(t, argv...); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	if list := f.fake.promotions[f.fake.envs["hosted"]]; len(list) == 0 || list[len(list)-1].ReleaseVersion != "v1" {
		t.Fatalf("the deploy promoted nothing: %+v", list)
	}
}

func TestEnvDeployRecordsAnEvidenceSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL, runs git and the whole CLI against an httptest control plane; runs in task test")
	}
	t.Run("a capable control plane gets a whole-environment snapshot", func(t *testing.T) {
		ev := &fakeEvidenceSeals{advertise: true}
		f := newEvidenceFixture(t, ev, hostedOnBandSpec)
		f.deploy(t)
		if len(ev.invalid) != 0 {
			t.Fatalf("the control plane's checks refused forge's snapshot: %v", ev.invalid)
		}
		// A deploy records its release's bundle more than once (planned,
		// then written, then re-sent as the bundle of record); each is the
		// same sealed bytes.
		if ev.attempts == 0 || len(ev.sealed) != ev.attempts {
			t.Fatalf("snapshot records = %d attempted, %d sealed; want every one sealed", ev.attempts, len(ev.sealed))
		}
		doc := ev.sealed[0]
		for _, again := range ev.sealed[1:] {
			if again.SnapshotDigest != doc.SnapshotDigest {
				t.Fatalf("one deploy sealed two snapshots: %s and %s", doc.SnapshotDigest, again.SnapshotDigest)
			}
		}
		s := doc.Snapshot
		if doc.Release != "v1" || doc.Env != "hosted" || s.Environment != "hosted" || s.Project != "acme" {
			t.Errorf("sealed %s/%s release %s; snapshot %s/%s", doc.Project, doc.Env, doc.Release, s.Project, s.Environment)
		}
		if len(s.Selected) != len(s.Consumers) || len(s.Closure) != len(s.Consumers) {
			t.Errorf("an evidence snapshot selects every consumer: %d selected of %d", len(s.Selected), len(s.Consumers))
		}
		requireAPIBinding(t, *s, testHostedAPIImage, hostedTestDigest)
		// What the platform runs is still the release's image.
		if d := f.fake.bundles[f.fake.envs["hosted"]]["api"]; d == nil || d.Spec["image"] != testHostedAPIImage+"@"+hostedTestDigest {
			t.Errorf("published deployment = %+v", d)
		}

		// The promotion that froze it is a composition base.
		promotion := release.Promotion{ID: "promotion-evidence", Env: "hosted", Release: "v1", Kind: release.KindPromote,
			Snapshot: s, SnapshotDigest: doc.SnapshotDigest, BundleID: "bundle-1", BundleDigest: "sha256:" + strings.Repeat("b", 64)}
		got, err := captureCompositionBase(context.Background(), envLedger{Bindings: currentBindingStub{promotion}}, "hosted", "current", false)
		if err != nil || got == nil || got.ID != promotion.ID {
			t.Fatalf("captureCompositionBase(evidence promotion) = %v, %v; want it accepted as the base", got, err)
		}

		// Deploying the release again re-sends its sealed bundle of record,
		// manifests layer included, and is the same idempotent record.
		attempts := ev.attempts
		f.deploy(t)
		if len(ev.invalid) != 0 || ev.attempts <= attempts || len(ev.sealed) != ev.attempts {
			t.Fatalf("re-deploy: %d snapshot records, refused %v; want the sealed bundle re-sent whole", ev.attempts, ev.invalid)
		}

		// The operator then switches evidence seals off: the next deploy of
		// the release falls back to its legacy bytes and still succeeds.
		ev.advertise, ev.disabled = false, true
		legacyBefore := ev.legacy
		f.deploy(t)
		if ev.legacy <= legacyBefore {
			t.Fatal("with evidence seals switched off, the re-deploy recorded no legacy bundle")
		}
	})

	t.Run("a control plane that does not advertise it gets the legacy bundle", func(t *testing.T) {
		ev := &fakeEvidenceSeals{advertise: false}
		f := newEvidenceFixture(t, ev, hostedOnBandSpec)
		f.deploy(t)
		if ev.attempts != 0 || ev.legacy == 0 {
			t.Fatalf("snapshot attempts = %d, legacy records = %d; want none and some", ev.attempts, ev.legacy)
		}
		requireNoManifestsLayer(t, f.fake)
	})

	t.Run("no capabilities RPC at all gets the legacy bundle", func(t *testing.T) {
		f := newEvidenceFixture(t, nil, hostedOnBandSpec)
		f.deploy(t)
		requireNoManifestsLayer(t, f.fake)
	})

	for name, ev := range map[string]*fakeEvidenceSeals{
		"the operator switch (snapshot_refused)":     {advertise: true, disabled: true},
		"an older replica (a plain InvalidArgument)": {advertise: true, plainRefusal: true},
	} {
		t.Run(name+" falls back once to the legacy bundle", func(t *testing.T) {
			f := newEvidenceFixture(t, ev, hostedOnBandSpec)
			legacyBefore := ev.legacy
			f.deploy(t)
			// One refused attempt, then the legacy bundle (re-sent as the
			// release's bundle of record by the deploy's later write).
			if ev.attempts != 1 || len(ev.sealed) != 0 || ev.legacy <= legacyBefore {
				t.Fatalf("snapshot attempts = %d, sealed = %d, legacy records = %d; want exactly one refused attempt, then legacy records",
					ev.attempts, len(ev.sealed), ev.legacy-legacyBefore)
			}
			if d := f.fake.bundles[f.fake.envs["hosted"]]["api"]; d == nil || d.Spec["image"] != testHostedAPIImage+"@"+hostedTestDigest {
				t.Errorf("published deployment = %+v", d)
			}
		})
	}

	t.Run("a bundle with a service catalog seals it when the control plane takes one", func(t *testing.T) {
		ev := &fakeEvidenceSeals{advertise: true, catalog: true}
		f := newEvidenceFixtureWithCatalog(t, ev, hostedOnBandSpec, evidenceCatalog(t, 0))
		f.deploy(t)
		if len(ev.invalid) != 0 || ev.attempts == 0 || len(ev.sealed) != ev.attempts {
			t.Fatalf("snapshot records = %d attempted, %d sealed, refused %v; want every one sealed", ev.attempts, len(ev.sealed), ev.invalid)
		}
		found := false
		for _, a := range ev.sealed[0].Snapshot.Assets {
			found = found || a.Path == release.BundleServiceCatalogPath
		}
		if !found {
			t.Fatal("the sealed snapshot dropped the service catalog")
		}
		requireEveryRecordCarriesTheCatalog(t, f.fake)
	})

	t.Run("a bundle with a service catalog on a control plane without evidence-seal-service-catalog is legacy, catalog intact", func(t *testing.T) {
		ev := &fakeEvidenceSeals{advertise: true}
		f := newEvidenceFixtureWithCatalog(t, ev, hostedOnBandSpec, evidenceCatalog(t, 0))
		f.deploy(t)
		if ev.attempts != 0 || ev.legacy == 0 {
			t.Fatalf("snapshot attempts = %d, legacy records = %d; want no seal attempt (so no refusal) and the legacy bundle", ev.attempts, ev.legacy)
		}
		requireNoManifestsLayer(t, f.fake)
		requireEveryRecordCarriesTheCatalog(t, f.fake)
	})

	t.Run("a snapshot that would exceed the bound with its service catalog is legacy, catalog intact", func(t *testing.T) {
		ev := &fakeEvidenceSeals{advertise: true, catalog: true}
		// Under the 1 MiB asset bound raw, over the 1 MiB config bound once
		// a snapshot inlines it (base64); a legacy bundle carries it in its
		// layer, which is bounded far higher.
		f := newEvidenceFixtureWithCatalog(t, ev, hostedOnBandSpec, evidenceCatalog(t, 900<<10))
		f.deploy(t)
		if ev.attempts != 0 || ev.legacy == 0 {
			t.Fatalf("snapshot attempts = %d, legacy records = %d; an oversize snapshot must not be sent", ev.attempts, ev.legacy)
		}
		requireEveryRecordCarriesTheCatalog(t, f.fake)
	})

	t.Run("a snapshot over the bundle bound is not sealed", func(t *testing.T) {
		ev := &fakeEvidenceSeals{advertise: true}
		// One resource whose bytes are small in a legacy bundle (the layer)
		// and over 1 MiB inline in a snapshot's config blob.
		f := newEvidenceFixture(t, ev, strings.Replace(hostedOnBandSpec, `env = {GREETING = forge.ManagedSecret {name = "GREETING"}}`,
			`env = {GREETING = forge.ManagedSecret {name = "GREETING"}`+evidencePadding()+`}`, 1))
		f.deploy(t)
		if ev.attempts != 0 || ev.legacy == 0 {
			t.Fatalf("snapshot attempts = %d, legacy records = %d; an oversize snapshot must not be sent", ev.attempts, ev.legacy)
		}
	})
}

// evidencePadding is env vars under the per-value bound whose sum is over the
// 1 MiB config bound once a snapshot inlines the record (base64).
func evidencePadding() string {
	var b strings.Builder
	for i := 0; i < 32; i++ {
		fmt.Fprintf(&b, ", PADDING_%d = \"%s\"", i, strings.Repeat("x", 30000))
	}
	return b.String()
}

// The release routes the deploy, never the bundle: a release cut without
// intents is never a snapshot release, whatever its bundles carry.
func TestEvidenceSealedReleaseStaysOnTheLegacyDeployPath(t *testing.T) {
	plain := &release.Release{Version: "v1", Artifacts: map[string]release.Artifact{}}
	if sealedSnapshotRelease(plain) || sealedSnapshotRelease(nil) {
		t.Fatal("a release with no snapshot intents is not a snapshot release")
	}
	if !sealedSnapshotRelease(&release.Release{Version: "v2", SnapshotIntents: map[string]string{"prod": "sha256:" + strings.Repeat("a", 64)}}) {
		t.Fatal("a release with snapshot intents is a snapshot release")
	}
	handled, err := dispatchSnapshotPromotion(context.Background(), "prod", "v1", envLedger{Releases: fixedReleaseStub{plain}}, promoteOptions{})
	if handled || err != nil {
		t.Fatalf("dispatchSnapshotPromotion(intent-less release) = %v, %v; want the legacy promote", handled, err)
	}
}

func requireAPIBinding(t *testing.T, s release.Snapshot, repository, digest string) {
	t.Helper()
	for _, b := range s.Bindings {
		if b.Consumer.Workload != "api" || b.Slot != "container:api" {
			continue
		}
		a := s.Artifacts[b.Artifact]
		if a.Kind != release.KindOCI || a.Location != repository || a.Content != digest || a.Origin.Release != "v1" {
			t.Fatalf("api/container:api binds %+v, want %s@%s of release v1", a, repository, digest)
		}
		return
	}
	t.Fatal("the snapshot has no api/container:api binding")
}

// evidenceCatalog is a valid service catalog of at least size bytes.
func evidenceCatalog(t *testing.T, size int) []byte {
	t.Helper()
	c := servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "github.com/example/acme",
		Processes: []servicecatalog.Process{}, Services: []servicecatalog.Service{}, Bindings: []servicecatalog.Binding{},
		Clients: []servicecatalog.Client{}, Diagnostics: []servicecatalog.Diagnostic{}}
	for {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) >= size {
			return raw
		}
		c.Components = append(c.Components, servicecatalog.Component{ID: "c", Name: "c", Kind: "service", Type: strings.Repeat("t", 400),
			Methods: []servicecatalog.CodeMethod{}, ImplementationCandidates: []servicecatalog.Implementation{}})
	}
}

// requireEveryRecordCarriesTheCatalog reads each bundle the deploy recorded
// back from the registry the control plane fetches it from, and requires its
// layer to carry the service catalog.
func requireEveryRecordCarriesTheCatalog(t *testing.T, fake *fakeDeployService) {
	t.Helper()
	n := 0
	for _, b := range fake.bodies {
		if !strings.HasSuffix(b.Path, "/RecordBundle") {
			continue
		}
		n++
		manifest, _ := base64.StdEncoding.DecodeString(b.Body["manifest"].(string))
		repository, _ := b.Body["repository"].(string)
		layer, err := fake.layerFetch(repository, release.SpecDigest(manifest))
		if err != nil {
			t.Fatal(err)
		}
		assets, err := release.ReadSnapshotAssets(layer)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, a := range assets {
			found = found || a.Path == release.BundleServiceCatalogPath
		}
		if !found {
			t.Errorf("recorded bundle %s does not carry the service catalog", release.SpecDigest(manifest))
		}
	}
	if n == 0 {
		t.Fatal("the deploy recorded no bundle")
	}
}

func requireNoManifestsLayer(t *testing.T, fake *fakeDeployService) {
	t.Helper()
	n := 0
	for _, b := range fake.bodies {
		if !strings.HasSuffix(b.Path, "/RecordBundle") {
			continue
		}
		n++
		if _, ok := b.Body["manifestsLayer"]; ok {
			t.Error("a legacy RecordBundle carried a manifests layer")
		}
		raw, _ := base64.StdEncoding.DecodeString(b.Body["config"].(string))
		if doc, err := release.DecodeBundleDoc(raw); err != nil || doc.Snapshot != nil || doc.Schema != release.BundleSchema {
			t.Errorf("a legacy RecordBundle carried schema %q snapshot=%v (%v)", doc.Schema, doc.Snapshot != nil, err)
		}
	}
	if n == 0 {
		t.Fatal("the deploy recorded no bundle")
	}
}

type currentBindingStub struct{ current release.Promotion }

func (s currentBindingStub) Current(context.Context, string) (release.Promotion, bool, error) {
	return s.current, true, nil
}
func (currentBindingStub) Append(context.Context, release.Promotion, appendGuard) (release.Promotion, error) {
	return release.Promotion{}, nil
}
func (currentBindingStub) Location() string { return "https://control-plane.test" }

type fixedReleaseStub struct{ r *release.Release }

func (s fixedReleaseStub) Cut(context.Context, release.Release) (bool, error)    { return true, nil }
func (s fixedReleaseStub) Get(context.Context, string) (*release.Release, error) { return s.r, nil }
func (s fixedReleaseStub) List(context.Context) ([]release.Release, error)       { return nil, nil }
func (s fixedReleaseStub) Location() string                                      { return "https://control-plane.test" }

// The shared builder's size refusal is typed, so a deploy can tell the bound
// from every other refusal without reading a message.
func TestBundleConfigTooLargeIsTypedAndInvalid(t *testing.T) {
	err := error(&bundle.ConfigTooLargeError{Size: bundle.MaxManifestBytes + 1})
	if !strings.Contains(err.Error(), "bundle config exceeds") {
		t.Errorf("message = %q", err)
	}
	if !errors.Is(err, release.ErrInvalid) {
		t.Error("a ConfigTooLargeError must stay a release.ErrInvalid")
	}
}
