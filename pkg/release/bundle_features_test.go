package release

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// bundleFeatureKeys is the document key paths each optional feature adds.
var bundleFeatureKeys = map[string][]string{
	BundleFeatureCommitIdentity:    {"provenance.commit_subject", "provenance.commit_author"},
	BundleFeatureObjectSummaries:   {"objects"},
	BundleFeatureFrontendTelemetry: {"shape.frontend_telemetry", "shape.observability_policy.retention"},
	BundleFeatureStoreSecrets:      {"shape.store_secrets", "shape.generated_secrets", "shape.openbao", "shape.external_secrets_operators", "shape.control_plane_secrets[].delivery"},
	BundleFeatureReleaseHealth:     {"shape.release_health"},
	BundleFeatureDataAccess:        {"shape.data_access"},
}

// snapshotKeys are not features: a snapshot bundle is its own schema, gated
// on the server's snapshot capability (see bundle_features.go).
var snapshotKeys = []string{"snapshot", "snapshot_digest"}

// legacyBundleDocKeys is the key-path fixture of the decoder every control
// plane built before forge#850 runs (forge ee3bded4).
func legacyBundleDocKeys(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("testdata/bundle_doc_keys.ee3bded4.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var keys []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			keys = append(keys, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}

func underAny(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+".") || strings.HasPrefix(path, p+"[]") || strings.HasPrefix(path, p+"{}") {
			return true
		}
	}
	return false
}

// TestEveryBundleDocAdditionIsAFeature: every key path BundleDoc gained since
// the legacy decoder is owned by a declared feature, so a field added to the
// document cannot reach an older control plane unannounced — the way
// forge#850's commit_subject did.
func TestEveryBundleDocAdditionIsAFeature(t *testing.T) {
	legacy := legacyBundleDocKeys(t)
	var owned []string
	for _, f := range BundleDocFeatures() {
		keys, ok := bundleFeatureKeys[f]
		if !ok {
			t.Errorf("feature %q has no key paths in bundleFeatureKeys", f)
		}
		owned = append(owned, keys...)
	}
	for _, p := range bundleDocKeyPaths(reflect.TypeFor[BundleDoc]()) {
		if slices.Contains(legacy, p) || underAny(p, owned) || underAny(p, snapshotKeys) {
			continue
		}
		t.Errorf("BundleDoc key %q is new since the legacy decoder (forge ee3bded4) and belongs to no feature.\n\n"+
			"Every control plane that predates it refuses the whole bundle with `unknown field`. Add a "+
			"feature for it in bundle_features.go (BundleDocFeatures, BundleReader.Limit, bundleFeatureKeys).", p)
	}
	for _, p := range legacy {
		if !slices.Contains(bundleDocKeyPaths(reflect.TypeFor[BundleDoc]()), p) {
			t.Errorf("BundleDoc no longer has key %q, which the legacy fixture names; a removal is not a feature — regenerate the fixture only for a new baseline", p)
		}
	}
}

func fullBundleDoc() BundleDoc {
	api := obj("Deployment", "api", "a")
	return BundleDoc{
		Schema: BundleSchema, Project: "cp", Env: "prod", Release: "v1",
		ConfigDigest: digest("c"),
		Pins:         BundlePins{Images: map[string]string{"api": digest("a")}},
		Provenance: Provenance{
			Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40), ForgeVersion: "v0.1.46",
			CommitSubject: "fix(boot): shared state converges in admin-server only", CommitAuthor: "Reliant",
		},
		Shape: Shape{
			Kind: EnvSelfManaged, Objects: []ShapeObject{api},
			ObservabilityPolicy: &ShapeObservabilityPolicy{Enabled: true, Retention: &ShapeRetention{ReplayDays: 7, ErrorsDays: 30, ProductEventsDays: 90}},
			FrontendTelemetry: []ShapeFrontendTelemetry{{
				Frontend: "web", Sourcemaps: SourcemapsOff, ErrorsIgnore: []string{"AbortError"},
				Funnels: []ShapeFunnel{{ID: "onboarding", Steps: []string{"a", "b"}, Window: "1h", By: "session"}},
			}},
			StoreSecrets:             []ShapeStoreSecret{{Cluster: "ctx", Namespace: "app", Store: StoreHosted, Names: []string{"STRIPE_KEY"}}},
			GeneratedSecrets:         []ShapeGeneratedSecret{{Store: StoreHosted, Kind: "random", Names: []string{"STRIPE_KEY"}}},
			OpenBao:                  &ShapeOpenBao{Name: "openbao", Namespace: "openbao", Cluster: "ctx", Image: "openbao:2"},
			ExternalSecretsOperators: []ShapeExternalSecretsOperator{{Cluster: "ctx", Namespace: "external-secrets", Version: "2.13.0"}},
			ControlPlaneSecrets:      []ShapeControlPlaneSecret{{Name: "reliant-otel-auth", Namespace: "app", Cluster: "ctx", ConnectedCluster: "c", Kind: ControlPlaneSecretTelemetryToken, Delivery: ControlPlaneSecretDeliveryStore}},
			DataAccess:               &ShapeDataAccess{DBQuery: DBQueryDeny},
			ReleaseHealth:            &ShapeReleaseHealth{WindowSeconds: 1800, MinRequests: 100, MaxErrorRateIncrease: 0.02, MaxP95Ratio: 2, NewIssueMinEvents: 5, FailOn: []string{HealthErrorRate}},
		},
		Objects:   []ObjectSummary{{Key: api.Key(), Summary: "2 replicas · containers api"}},
		CreatedAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
	}
}

// TestAnOldReaderGetsADocumentItsDecoderAccepts: a reader that names no
// feature — a control plane that predates GetCapabilities, or the
// capability — gets a document every key of which the legacy strict decoder
// knows. The unlimited document is the control: it must stray, or the
// fixture proves nothing.
func TestAnOldReaderGetsADocumentItsDecoderAccepts(t *testing.T) {
	legacy := legacyBundleDocKeys(t)
	full := fullBundleDoc()

	raw, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	strays, err := unknownKeyPaths(raw, legacy)
	if err != nil {
		t.Fatal(err)
	}
	strayRoots := map[string]bool{}
	for _, p := range strays {
		strayRoots[p] = true
	}
	for _, want := range []string{"objects", "provenance.commit_author", "provenance.commit_subject", "shape.frontend_telemetry", "shape.observability_policy.retention", "shape.store_secrets", "shape.generated_secrets", "shape.openbao", "shape.external_secrets_operators", "shape.control_plane_secrets[].delivery", "shape.release_health"} {
		if !strayRoots[want] {
			t.Fatalf("the full document does not stray at %q (strays %v): the fixture or the doc helper no longer exercises it", want, strays)
		}
	}

	limited := (&BundleReader{}).Limit(full)
	raw, err = json.Marshal(limited)
	if err != nil {
		t.Fatal(err)
	}
	if strays, err := unknownKeyPaths(raw, legacy); err != nil || len(strays) != 0 {
		t.Fatalf("a document for a reader with no features carries keys the legacy decoder refuses: %v (err %v).\n\n"+
			"That is `RecordBundle: bundle document: json: unknown field` on every control plane built before forge#850.", strays, err)
	}
	var top struct {
		Objects    *json.RawMessage           `json:"objects"`
		Provenance map[string]json.RawMessage `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	_, subject := top.Provenance["commit_subject"]
	_, author := top.Provenance["commit_author"]
	if top.Objects != nil || subject || author {
		t.Errorf("the limited document still carries a forge#850 key, even empty (objects=%v subject=%v author=%v); an empty key is still an unknown key",
			top.Objects != nil, subject, author)
	}
	// What it keeps is still a valid document for THIS decoder.
	if _, err := DecodeBundleDoc(raw); err != nil {
		t.Fatalf("the limited document no longer decodes: %v", err)
	}
	wantShape := full.Shape
	wantShape.FrontendTelemetry = nil
	wantShape.StoreSecrets, wantShape.GeneratedSecrets, wantShape.OpenBao, wantShape.ExternalSecretsOperators = nil, nil, nil, nil
	wantShape.ReleaseHealth = nil
	wantShape.DataAccess = nil
	wantShape.ControlPlaneSecrets = []ShapeControlPlaneSecret{full.Shape.ControlPlaneSecrets[0]}
	wantShape.ControlPlaneSecrets[0].Delivery = ""
	pol := *wantShape.ObservabilityPolicy
	pol.Retention = nil
	wantShape.ObservabilityPolicy = &pol
	if limited.Provenance.Commit != full.Provenance.Commit || !reflect.DeepEqual(limited.Shape, wantShape) {
		t.Fatalf("Limit removed more than the optional features:\n got %+v\nwant %+v", limited.Shape, wantShape)
	}
	if full.Shape.FrontendTelemetry == nil || full.Shape.ObservabilityPolicy.Retention == nil {
		t.Fatal("Limit modified the document it was given")
	}
}

// TestANewReaderGetsTheFullDocument: a reader that names every feature, and
// the nil reader (this forge's own machine ledger), get the document whole.
func TestANewReaderGetsTheFullDocument(t *testing.T) {
	full := fullBundleDoc()
	for name, r := range map[string]*BundleReader{
		"every feature": {Features: BundleDocFeatures()},
		"nil reader":    nil,
	} {
		if got := r.Limit(full); !reflect.DeepEqual(got, full) {
			t.Errorf("%s: Limit changed the document:\n%+v\n%+v", name, got, full)
		}
	}
	partial := (&BundleReader{Features: []string{BundleFeatureObjectSummaries}}).Limit(full)
	if len(partial.Objects) != 1 || partial.Provenance.CommitSubject != "" || partial.Provenance.CommitAuthor != "" {
		t.Errorf("a reader of object summaries only: %+v", partial)
	}
	telemetry := (&BundleReader{Features: []string{BundleFeatureFrontendTelemetry}}).Limit(full)
	if len(telemetry.Shape.FrontendTelemetry) != 1 || telemetry.Shape.ObservabilityPolicy.Retention == nil || telemetry.Objects != nil || telemetry.Shape.StoreSecrets != nil {
		t.Errorf("a reader of frontend telemetry only: %+v", telemetry)
	}
	stores := (&BundleReader{Features: []string{BundleFeatureStoreSecrets}}).Limit(full)
	if len(stores.Shape.StoreSecrets) != 1 || stores.Shape.OpenBao == nil || len(stores.Shape.ExternalSecretsOperators) != 1 || stores.Shape.FrontendTelemetry != nil || stores.Shape.DataAccess != nil || stores.Shape.ControlPlaneSecrets[0].Delivery != ControlPlaneSecretDeliveryStore {
		t.Errorf("a reader of store secrets only: %+v", stores)
	}
	health := (&BundleReader{Features: []string{BundleFeatureReleaseHealth}}).Limit(full)
	if health.Shape.ReleaseHealth == nil || health.Shape.StoreSecrets != nil || health.Shape.FrontendTelemetry != nil {
		t.Errorf("a reader of release health only: %+v", health)
	}
	access := (&BundleReader{Features: []string{BundleFeatureDataAccess}}).Limit(full)
	if access.Shape.DataAccess == nil || access.Shape.DataAccess.DBQuery != DBQueryDeny || access.Shape.StoreSecrets != nil || access.Shape.FrontendTelemetry != nil || access.Shape.ReleaseHealth != nil {
		t.Errorf("a reader of data access only: %+v", access)
	}
	if full.Shape.ControlPlaneSecrets[0].Delivery != ControlPlaneSecretDeliveryStore {
		t.Fatal("Limit modified the control-plane secrets of the document it was given")
	}
}
