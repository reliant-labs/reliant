package cluster

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// --- fakes ---------------------------------------------------------------

// fakeSecretGetter resolves Secret keys from an in-memory table.
// secrets[name] is the set of keys present; a name absent from the map
// reports exists=false. err (when set) is returned for every lookup.
type fakeSecretGetter struct {
	secrets map[string]map[string]struct{}
	err     error
}

func (f fakeSecretGetter) GetSecretKeys(_ context.Context, _, _, name string) (map[string]struct{}, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	keys, ok := f.secrets[name]
	if !ok {
		return nil, false, nil
	}
	return keys, true, nil
}

// fakeConfigMapGetter resolves ConfigMap keys from an in-memory table,
// mirroring fakeSecretGetter.
type fakeConfigMapGetter struct {
	configMaps map[string]map[string]struct{}
	err        error
}

func (f fakeConfigMapGetter) GetConfigMapKeys(_ context.Context, _, _, name string) (map[string]struct{}, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	keys, ok := f.configMaps[name]
	if !ok {
		return nil, false, nil
	}
	return keys, true, nil
}

// fakeImageChecker resolves image existence from an in-memory set.
// present images return (true, nil). Images absent from every set return
// (false, nil) — a real registry not-found that must BLOCK. inconclusive
// images return (false, ErrImageCheckInconclusive) — a TRANSPORT failure that
// must WARN+proceed, not block. authDenied images return (false,
// ErrImageCheckAuthDenied) — the registry refused the lookup, so existence is
// UNKNOWN and the gate must BLOCK (cannot confirm) rather than fail open. err
// (when set) is a hard checker failure returned for every lookup.
type fakeImageChecker struct {
	present      map[string]struct{}
	inconclusive map[string]struct{}
	authDenied   map[string]struct{}
	err          error
}

func (f fakeImageChecker) ImageExists(_ context.Context, ref string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if _, ok := f.inconclusive[ref]; ok {
		return false, fmt.Errorf("%w: dial tcp: connection refused", ErrImageCheckInconclusive)
	}
	if _, ok := f.authDenied[ref]; ok {
		return false, fmt.Errorf("%w: denied: requested access to the resource is denied", ErrImageCheckAuthDenied)
	}
	_, ok := f.present[ref]
	return ok, nil
}

// credAwareImageChecker is a fakeImageChecker that ALSO models cluster
// credentials: an image in authDenied is auth-denied to the LOCAL daemon, but
// once WithDockerConfigDir is called (the credentialed retry) it resolves the
// ref from credPresent / credMiss as the CLUSTER would. This lets a test prove
// the auth-denied → credentialed-retry → TRUE-verdict path without a real
// docker daemon. It satisfies CredentialedImageChecker.
type credAwareImageChecker struct {
	fakeImageChecker
	// credentialed reports whether this instance is the post-WithDockerConfigDir
	// (cluster-creds) variant. The base instance is the local-daemon view.
	credentialed bool
	// credPresent / credMiss define the CLUSTER's view once authenticated.
	credPresent map[string]struct{}
	credMiss    map[string]struct{}
	// dirSeen records the DOCKER_CONFIG dir handed to WithDockerConfigDir, so a
	// test can assert the creds were actually materialised + threaded.
	dirSeen *string
}

func (c credAwareImageChecker) WithDockerConfigDir(dir string) ImageChecker {
	if c.dirSeen != nil {
		*c.dirSeen = dir
	}
	c.credentialed = true
	return c
}

func (c credAwareImageChecker) ImageExists(ctx context.Context, ref string) (bool, error) {
	if !c.credentialed {
		return c.fakeImageChecker.ImageExists(ctx, ref)
	}
	// Credentialed (cluster) view: the auth-denied refs now resolve.
	if _, ok := c.credPresent[ref]; ok {
		return true, nil
	}
	if _, ok := c.credMiss[ref]; ok {
		return false, nil // confirmed miss from the cluster's perspective
	}
	// Still denied even with cluster creds.
	return false, fmt.Errorf("%w: denied: requested access to the resource is denied", ErrImageCheckAuthDenied)
}

// fakePullCredsResolver returns a canned docker config.json for the named
// secrets. err (when set) is a hard resolver failure. empty (when true) models
// "no usable creds" — returns (nil, nil).
type fakePullCredsResolver struct {
	config []byte
	err    error
	empty  bool
	// namesSeen records the secret names the resolver was asked for.
	namesSeen *[]string
}

func (f fakePullCredsResolver) ResolveDockerConfig(_ context.Context, _, _ string, names []string) ([]byte, error) {
	if f.namesSeen != nil {
		*f.namesSeen = names
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.empty {
		return nil, nil
	}
	if f.config != nil {
		return f.config, nil
	}
	return []byte(`{"auths":{"ghcr.io":{"auth":"dGVzdDp0ZXN0"}}}`), nil
}

func keySet(keys ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

// --- manifest fixtures ---------------------------------------------------

const deploymentManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app-prod
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:abc123
          env:
            - name: SERVICE_SECRET
              valueFrom:
                secretKeyRef:
                  name: app-secrets
                  key: service_secret
            - name: DB_URL
              valueFrom:
                secretKeyRef:
                  name: app-secrets
                  key: db_url
`

const jobWithEnvFrom = `
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  namespace: app-prod
spec:
  template:
    spec:
      containers:
        - name: migrate
          image: ghcr.io/acme/migrate:abc123
          envFrom:
            - secretRef:
                name: bulk-secrets
`

// secretVolumeManifest mounts an out-of-band kubeconfig Secret as a volume —
// exactly the shape `ClusterClient external=True` produces (forge does NOT
// mint that Secret). Also carries a projected secret source and an
// imagePullSecret, plus a configMap volume, to exercise every new shape.
const secretVolumeManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: proxy
  namespace: app-prod
spec:
  template:
    spec:
      imagePullSecrets:
        - name: registry-creds
      containers:
        - name: proxy
          image: ghcr.io/acme/proxy:abc123
          volumeMounts:
            - name: kubeconfig
              mountPath: /etc/kube
      volumes:
        - name: kubeconfig
          secret:
            secretName: external-kubeconfig
        - name: settings
          configMap:
            name: proxy-settings
        - name: combined
          projected:
            sources:
              - secret:
                  name: projected-secret
              - configMap:
                  name: projected-config
`

// configMapKeyRefManifest projects a ConfigMap key into env — a missing key
// fails identically to a missing Secret key (CreateContainerConfigError).
const configMapKeyRefManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app-prod
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:abc123
          env:
            - name: LOG_LEVEL
              valueFrom:
                configMapKeyRef:
                  name: app-config
                  key: log_level
          envFrom:
            - configMapRef:
                name: bulk-config
`

// pullSecretManifest is a minimal Deployment that pulls a PRIVATE image with an
// imagePullSecret — the shape that exercises the cluster-credentialed image
// verification path. No env/secret refs so the test isolates image verification.
const pullSecretManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app-prod
spec:
  template:
    spec:
      imagePullSecrets:
        - name: registry-creds
      containers:
        - name: api
          image: ghcr.io/acme/private-api:abc123
`

// --- CollectManifestRefs -------------------------------------------------

func TestCollectManifestRefs(t *testing.T) {
	refs := CollectManifestRefs(deploymentManifest+"\n---\n"+jobWithEnvFrom, RefPlacement{})

	// Images.
	wantImages := []string{"ghcr.io/acme/api:abc123", "ghcr.io/acme/migrate:abc123"}
	gotImages := make([]string, 0, len(refs.Images))
	for img := range refs.Images {
		gotImages = append(gotImages, img)
	}
	sort.Strings(gotImages)
	if strings.Join(gotImages, ",") != strings.Join(wantImages, ",") {
		t.Fatalf("images: got %v, want %v", gotImages, wantImages)
	}

	// secretKeyRef keys.
	appKeys := byName(refs.Secrets)["app-secrets"]
	if _, ok := appKeys["service_secret"]; !ok {
		t.Errorf("app-secrets should reference service_secret; got %v", appKeys)
	}
	if _, ok := appKeys["db_url"]; !ok {
		t.Errorf("app-secrets should reference db_url; got %v", appKeys)
	}

	// envFrom secretRef → whole-Secret marker (key "").
	bulkKeys := byName(refs.Secrets)["bulk-secrets"]
	if _, ok := bulkKeys[""]; !ok {
		t.Errorf("bulk-secrets should carry the whole-Secret marker; got %v", bulkKeys)
	}
}

// A resource the SAME manifest stream renders is not a missing dependency:
// `kubectl apply` creates it in the same pass. Demanding it exist first makes
// a first deploy unsatisfiable — the user is told to provision the very thing
// this deploy provisions. Regression test for a bundle carrying an in-cluster
// IdP whose bootstrap ConfigMap is rendered two documents above the Deployment
// that mounts it.
func TestCollectManifestRefs_SelfProvidedNotRequired(t *testing.T) {
	const stream = `apiVersion: v1
kind: ConfigMap
metadata:
  name: zitadel-steps
  namespace: control-plane-prod
data:
  steps.yaml: "FirstInstance: {}"
---
apiVersion: v1
kind: Secret
metadata:
  name: self-made
  namespace: control-plane-prod
stringData:
  token: abc
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: zitadel
  namespace: control-plane-prod
spec:
  template:
    spec:
      containers:
        - name: zitadel
          image: ghcr.io/zitadel/zitadel:v4.19.5
          env:
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: self-made
                  key: token
            - name: EXTERNAL
              valueFrom:
                secretKeyRef:
                  name: provisioned-elsewhere
                  key: password
          volumeMounts:
            - name: steps
              mountPath: /steps.yaml
      volumes:
        - name: steps
          configMap:
            name: zitadel-steps
`
	refs := CollectManifestRefs(stream, RefPlacement{})

	if _, ok := byName(refs.ConfigMaps)["zitadel-steps"]; ok {
		t.Errorf("zitadel-steps is rendered by this stream and must not be required: %v", refs.ConfigMaps)
	}
	if _, ok := byName(refs.Secrets)["self-made"]; ok {
		t.Errorf("self-made Secret is rendered by this stream and must not be required: %v", refs.Secrets)
	}
	// The genuinely external reference must STILL be required — this is the
	// check's whole value, and over-subtracting would silently gut it.
	if _, ok := byName(refs.Secrets)["provisioned-elsewhere"]; !ok {
		t.Errorf("provisioned-elsewhere is NOT in the stream and must remain required: %v", refs.Secrets)
	}
	// Images are never self-provided: an image is satisfied by a registry.
	if _, ok := refs.Images["ghcr.io/zitadel/zitadel:v4.19.5"]; !ok {
		t.Errorf("image refs must be unaffected by self-provided subtraction: %v", refs.Images)
	}
}

// A reference marked `optional: true` is not a schedule-time dependency:
// kubelet starts the container without it (an optional secretKeyRef yields no
// env var, an optional secret volume mounts empty). Demanding it made a
// deliberately-absent break-glass Secret — present for minutes, once, ever —
// block every ordinary deploy and read in `forge project audit` as a pod that
// "will FailedMount". Required refs beside it must still be collected.
func TestCollectManifestRefs_OptionalRefsNotRequired(t *testing.T) {
	const job = `apiVersion: batch/v1
kind: Job
metadata:
  name: openbao-bootstrap
spec:
  template:
    spec:
      containers:
        - name: bootstrap
          image: quay.io/openbao/openbao:2.6.2
          env:
            - name: OPENBAO_BREAK_GLASS_TOKEN
              valueFrom:
                secretKeyRef:
                  name: root-once
                  key: token
                  optional: true
            - name: OPENBAO_SERVICE_SECRET_ID
              valueFrom:
                secretKeyRef:
                  name: approles
                  key: service-secret-id
            - name: FEATURE_FLAG
              valueFrom:
                configMapKeyRef:
                  name: optional-flags
                  key: flag
                  optional: true
          envFrom:
            - secretRef:
                name: optional-bulk
                optional: true
      volumes:
        - name: extra
          secret:
            secretName: optional-volume
            optional: true
        - name: extra-cm
          configMap:
            name: optional-cm-volume
            optional: true
`
	refs := CollectManifestRefs(job, RefPlacement{})
	for _, name := range []string{"root-once", "optional-bulk", "optional-volume"} {
		if _, ok := byName(refs.Secrets)[name]; ok {
			t.Errorf("optional Secret %q must not be demanded; got %v", name, refs.Secrets)
		}
	}
	for _, name := range []string{"optional-flags", "optional-cm-volume"} {
		if _, ok := byName(refs.ConfigMaps)[name]; ok {
			t.Errorf("optional ConfigMap %q must not be demanded; got %v", name, refs.ConfigMaps)
		}
	}
	if _, ok := byName(refs.Secrets)["approles"]["service-secret-id"]; !ok {
		t.Errorf("a required secretKeyRef beside an optional one must still be demanded; got %v", refs.Secrets)
	}
	if misses := CheckSecretSupply(job, []SecretSupply{{Name: "approles"}}); len(misses) != 0 {
		t.Errorf("an optional Secret must not be an undeclared mount; got %+v", misses)
	}
}

func TestCollectManifestRefs_Empty(t *testing.T) {
	refs := CollectManifestRefs("", RefPlacement{})
	if len(refs.Secrets) != 0 || len(refs.Images) != 0 {
		t.Fatalf("empty manifests should yield no refs; got %+v", refs)
	}
}

// --- Preflight: all present → proceeds -----------------------------------

func TestPreflight_AllPresent(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{
			"app-secrets": keySet("service_secret", "db_url"),
		}},
		Images: fakeImageChecker{present: keySet("ghcr.io/acme/api:abc123")},
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("expected preflight to pass, got: %v", err)
	}
}

// --- Preflight: missing secret key → error listing it --------------------

func TestPreflight_MissingSecretKey(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{
			// db_url present, service_secret missing.
			"app-secrets": keySet("db_url"),
		}},
		Images: fakeImageChecker{present: keySet("ghcr.io/acme/api:abc123")},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("expected preflight to fail on missing secret key")
	}
	msg := err.Error()
	if !strings.Contains(msg, "app-prod/app-secrets") {
		t.Errorf("error should name the Secret app-prod/app-secrets; got:\n%s", msg)
	}
	if !strings.Contains(msg, "service_secret") {
		t.Errorf("error should list the missing key service_secret; got:\n%s", msg)
	}
	if strings.Contains(msg, "db_url") {
		t.Errorf("error should NOT list the present key db_url; got:\n%s", msg)
	}
	if !strings.Contains(msg, "--skip-preflight") {
		t.Errorf("error should mention the --skip-preflight escape hatch; got:\n%s", msg)
	}
}

// --- Preflight: secret entirely absent → whole-secret marker -------------

func TestPreflight_SecretEntirelyMissing(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		// app-secrets not in the table → exists=false.
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		Images:  fakeImageChecker{present: keySet("ghcr.io/acme/api:abc123")},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("expected preflight to fail on absent Secret")
	}
	if !strings.Contains(err.Error(), wholeSecretMarker) {
		t.Errorf("error should carry the whole-Secret marker; got:\n%s", err.Error())
	}
}

// --- Preflight: missing image → error ------------------------------------

func TestPreflight_MissingImage(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{
			"app-secrets": keySet("service_secret", "db_url"),
		}},
		// image set empty → not present.
		Images: fakeImageChecker{present: map[string]struct{}{}},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("expected preflight to fail on missing image")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Images not found") {
		t.Errorf("error should have an Images not found block; got:\n%s", msg)
	}
	if !strings.Contains(msg, "ghcr.io/acme/api:abc123") {
		t.Errorf("error should list the missing image; got:\n%s", msg)
	}
}

// --- Preflight: groups EVERYTHING missing in one report ------------------

func TestPreflight_GroupsAllMissing(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets:   fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		Images:    fakeImageChecker{present: map[string]struct{}{}},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	msg := err.Error()
	// Both the Secret block AND the image block must appear — the whole
	// point is one report listing everything.
	if !strings.Contains(msg, "app-secrets") || !strings.Contains(msg, "Images not found") {
		t.Errorf("report should group BOTH missing secrets and images; got:\n%s", msg)
	}
}

// --- Preflight: SkipImageRef skips local images --------------------------

func TestPreflight_SkipsLocalImages(t *testing.T) {
	const localManifest = `
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: registry.localhost:5000/api:dev
`
	opts := PreflightOpts{
		Manifests: localManifest,
		// No secrets referenced; image is local and should be skipped even
		// though the checker reports it absent.
		Images:       fakeImageChecker{present: map[string]struct{}{}},
		SkipImageRef: LocalImageRef,
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("local image should be skipped, got: %v", err)
	}
}

// --- Preflight: no secret context → secret check skipped -----------------

func TestPreflight_NoContextSkipsSecretCheck(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		// Context empty → secret check skipped entirely, even with a getter.
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		Images:  fakeImageChecker{present: keySet("ghcr.io/acme/api:abc123")},
		// app-secrets is provided out-of-band (this test exercises the live
		// secret-check skip, not the render-time back-prop gate); declare the
		// supply so the back-prop gate doesn't flag the fixture's secretKeyRef.
		SecretSupply: []SecretSupply{{Name: "app-secrets", Kind: SupplyGenerated}},
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("missing secret context should skip the secret check, got: %v", err)
	}
}

// --- Preflight: nothing to check → no-op ----------------------------------

func TestPreflight_NothingToCheck(t *testing.T) {
	const bare = `
kind: ConfigMap
metadata:
  name: settings
data:
  log_level: info
`
	opts := PreflightOpts{
		Manifests: bare,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets:   fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		Images:    fakeImageChecker{present: map[string]struct{}{}},
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("no refs to check should no-op, got: %v", err)
	}
}

// --- Preflight: getter error aborts (not treated as missing) -------------

func TestPreflight_SecretGetterErrorAborts(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets:   fakeSecretGetter{err: errors.New("kubectl: connection refused")},
		Images:    fakeImageChecker{present: keySet("ghcr.io/acme/api:abc123")},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("expected the getter error to abort the preflight")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("getter error should propagate; got: %v", err)
	}
	// A lookup failure must NOT be reported as a missing-key verdict.
	if strings.Contains(err.Error(), "missing keys") {
		t.Errorf("lookup error must not masquerade as a missing-key report; got: %v", err)
	}
}

// --- CollectManifestRefs: new shapes -------------------------------------

func TestCollectManifestRefs_SecretVolumesAndPullSecrets(t *testing.T) {
	refs := CollectManifestRefs(secretVolumeManifest, RefPlacement{})

	// volumes[].secret.secretName — the ClusterClient external=True shape.
	if _, ok := byName(refs.Secrets)["external-kubeconfig"][""]; !ok {
		t.Errorf("secret volume should record external-kubeconfig (existence-only); got %v", refs.Secrets)
	}
	// volumes[].projected.sources[].secret.name
	if _, ok := byName(refs.Secrets)["projected-secret"][""]; !ok {
		t.Errorf("projected secret source should be recorded; got %v", refs.Secrets)
	}
	// imagePullSecrets[].name — recorded in Secrets (existence)…
	if _, ok := byName(refs.Secrets)["registry-creds"][""]; !ok {
		t.Errorf("imagePullSecret should be recorded in Secrets; got %v", refs.Secrets)
	}
	// …AND in ImagePullSecrets (the registry-credential source).
	if _, ok := pullSecretNames(refs)["registry-creds"]; !ok {
		t.Errorf("imagePullSecret should be recorded in ImagePullSecrets; got %v", refs.ImagePullSecrets)
	}
	// A non-pull-secret reference must NOT leak into ImagePullSecrets.
	if _, ok := pullSecretNames(refs)["external-kubeconfig"]; ok {
		t.Errorf("a secret-volume name must not be recorded as an imagePullSecret; got %v", refs.ImagePullSecrets)
	}
	// volumes[].configMap.name
	if _, ok := byName(refs.ConfigMaps)["proxy-settings"][""]; !ok {
		t.Errorf("configMap volume should be recorded; got %v", refs.ConfigMaps)
	}
	// volumes[].projected.sources[].configMap.name
	if _, ok := byName(refs.ConfigMaps)["projected-config"][""]; !ok {
		t.Errorf("projected configMap source should be recorded; got %v", refs.ConfigMaps)
	}
}

func TestCollectManifestRefs_ConfigMapKeyRefs(t *testing.T) {
	refs := CollectManifestRefs(configMapKeyRefManifest, RefPlacement{})

	// configMapKeyRef → keyed reference.
	if _, ok := byName(refs.ConfigMaps)["app-config"]["log_level"]; !ok {
		t.Errorf("configMapKeyRef should record app-config/log_level; got %v", refs.ConfigMaps)
	}
	// envFrom configMapRef → whole-ConfigMap marker.
	if _, ok := byName(refs.ConfigMaps)["bulk-config"][""]; !ok {
		t.Errorf("envFrom configMapRef should record the whole-ConfigMap marker; got %v", refs.ConfigMaps)
	}
}

// collectRefs must NOT invent references the manifest doesn't make: a bare
// ConfigMap definition (not a reference) and a Service have nothing to
// collect.
func TestCollectManifestRefs_IgnoresNonReferences(t *testing.T) {
	const noRefs = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: settings
data:
  log_level: info
---
apiVersion: v1
kind: Service
metadata:
  name: api
spec:
  ports:
    - port: 80
`
	refs := CollectManifestRefs(noRefs, RefPlacement{})
	if len(refs.Secrets) != 0 {
		t.Errorf("no Secret references expected; got %v", refs.Secrets)
	}
	if len(refs.ConfigMaps) != 0 {
		t.Errorf("no ConfigMap references expected; got %v", refs.ConfigMaps)
	}
	if len(refs.Images) != 0 {
		t.Errorf("no images expected; got %v", refs.Images)
	}
}

// --- Preflight: missing secret-volume Secret → blocked -------------------

func TestPreflight_MissingSecretVolumeBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests: secretVolumeManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		// None of the referenced Secrets exist → every one is a whole-Secret
		// miss; the external-kubeconfig is the load-bearing one.
		Secrets:    fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		ConfigMaps: fakeConfigMapGetter{configMaps: map[string]map[string]struct{}{}},
		Images:     fakeImageChecker{present: keySet("ghcr.io/acme/proxy:abc123")},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("expected preflight to block on a missing secret-volume Secret")
	}
	msg := err.Error()
	if !strings.Contains(msg, "app-prod/external-kubeconfig") {
		t.Errorf("error should name the missing secret-volume Secret; got:\n%s", msg)
	}
	if !strings.Contains(msg, wholeSecretMarker) {
		t.Errorf("a missing whole-Secret should carry the whole-Secret marker; got:\n%s", msg)
	}
}

// --- Preflight: missing configMapKeyRef → blocked ------------------------

func TestPreflight_MissingConfigMapKeyBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests: configMapKeyRefManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets:   fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		ConfigMaps: fakeConfigMapGetter{configMaps: map[string]map[string]struct{}{
			// app-config exists but is missing log_level; bulk-config absent.
			"app-config": keySet("other_key"),
		}},
		Images: fakeImageChecker{present: keySet("ghcr.io/acme/api:abc123")},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("expected preflight to block on a missing ConfigMap key")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ConfigMap app-prod/app-config missing keys") || !strings.Contains(msg, "log_level") {
		t.Errorf("error should name the missing ConfigMap key; got:\n%s", msg)
	}
	if !strings.Contains(msg, "app-prod/bulk-config") {
		t.Errorf("error should flag the entirely-absent bulk-config; got:\n%s", msg)
	}
	if !strings.Contains(msg, "missing ConfigMap key(s)") {
		t.Errorf("remediation footer should mention ConfigMap keys; got:\n%s", msg)
	}
}

// ConfigMap check is skipped when no getter/context is configured (local
// dev), mirroring the Secret check.
func TestPreflight_NoContextSkipsConfigMapCheck(t *testing.T) {
	opts := PreflightOpts{
		Manifests:  configMapKeyRefManifest,
		ConfigMaps: fakeConfigMapGetter{configMaps: map[string]map[string]struct{}{}},
		Images:     fakeImageChecker{present: keySet("ghcr.io/acme/api:abc123")},
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("missing context should skip the ConfigMap check, got: %v", err)
	}
}

// --- Preflight: image-check inconclusive (auth) → WARN + proceed ----------

func TestPreflight_InconclusiveImageWarnsAndProceeds(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{
			"app-secrets": keySet("service_secret", "db_url"),
		}},
		// The image check can't reach a verdict (auth) — must NOT block.
		Images: fakeImageChecker{inconclusive: keySet("ghcr.io/acme/api:abc123")},
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("an inconclusive image check must warn+proceed, not block; got: %v", err)
	}
}

// The inconclusive verdict is surfaced as a structured ImageWarning, distinct
// from a confirmed MissingImage.
func TestRunPreflightChecks_InconclusiveIsWarningNotMiss(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Images:    fakeImageChecker{inconclusive: keySet("ghcr.io/acme/api:abc123")},
		// Supply the fixture's secret so the render-time back-prop gate
		// (which runs on this no-context path) doesn't flag it — this test
		// asserts only the image-inconclusive behavior.
		SecretSupply: []SecretSupply{{Name: "app-secrets", Kind: SupplyGenerated}},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("inconclusive image must not abort: %v", err)
	}
	if len(res.MissingImages) != 0 {
		t.Errorf("inconclusive image must not be a confirmed miss; got MissingImages=%v", res.MissingImages)
	}
	if len(res.ImageWarnings) != 1 || !strings.Contains(res.ImageWarnings[0], "couldn't verify") {
		t.Errorf("expected one couldn't-verify warning; got %v", res.ImageWarnings)
	}
	if !res.OK() {
		t.Errorf("a run with only inconclusive images should be OK() (proceed)")
	}
}

// --- Preflight: image auth-denied → BLOCKED (cannot confirm, no fail-open) -

// A private-registry auth denial leaves the image's presence UNKNOWN; the gate
// must BLOCK rather than silently pass (which would ImagePullBackOff in prod).
func TestPreflight_AuthDeniedImageBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{
			"app-secrets": keySet("service_secret", "db_url"),
		}},
		Images: fakeImageChecker{authDenied: keySet("ghcr.io/acme/api:abc123")},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("an auth-denied image lookup must BLOCK the deploy (cannot confirm), not fail open")
	}
	msg := err.Error()
	if !strings.Contains(msg, "could not be confirmed") {
		t.Errorf("report should name the unverifiable-image block; got:\n%s", msg)
	}
	// Message must name BOTH possible causes + the override.
	for _, want := range []string{
		"never pushed",
		"pull credentials",
		"--skip-preflight",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("report must mention %q so the operator can act; got:\n%s", want, msg)
		}
	}
}

// The auth-denied verdict is a BLOCKING UnverifiableImage, NOT a non-blocking
// ImageWarning — that distinction is the whole fix.
func TestRunPreflightChecks_AuthDeniedIsBlockNotWarning(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Images:    fakeImageChecker{authDenied: keySet("ghcr.io/acme/api:abc123")},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("auth-denied image must not abort the preflight: %v", err)
	}
	if len(res.ImageWarnings) != 0 {
		t.Errorf("auth-denied must NOT be a non-blocking warning; got ImageWarnings=%v", res.ImageWarnings)
	}
	if len(res.UnverifiableImages) != 1 {
		t.Fatalf("expected one UnverifiableImage (blocking); got %v", res.UnverifiableImages)
	}
	if res.OK() {
		t.Error("a run with an auth-denied (unverifiable) image must NOT be OK() — it blocks")
	}
}

// --- FIX 1: cluster-credentialed image verification ----------------------

// The core fix: the LOCAL daemon is auth-denied for a private image (no creds),
// but the bundle declares an imagePullSecret. The preflight resolves the
// cluster's pull creds and RETRIES the lookup from the cluster's perspective —
// the image IS present, so the verdict is TRUE and the deploy is NOT blocked.
// Before the fix this auth-denied lookup was a false UnverifiableImage block.
func TestRunPreflightChecks_AuthDeniedButClusterCredsConfirmPresent(t *testing.T) {
	const ref = "ghcr.io/acme/private-api:abc123"
	var seenDir string
	var seenNames []string
	opts := PreflightOpts{
		Manifests: pullSecretManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Images: credAwareImageChecker{
			fakeImageChecker: fakeImageChecker{authDenied: keySet(ref)}, // local daemon: denied
			credPresent:      keySet(ref),                               // cluster: present
			dirSeen:          &seenDir,
		},
		PullCreds: fakePullCredsResolver{namesSeen: &seenNames},
	}

	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("credentialed retry must not abort the preflight: %v", err)
	}
	if len(res.UnverifiableImages) != 0 {
		t.Errorf("cluster creds confirmed the image present — it must NOT be an UnverifiableImage block; got %v", res.UnverifiableImages)
	}
	if len(res.MissingImages) != 0 {
		t.Errorf("image is present from the cluster's view — not a miss; got %v", res.MissingImages)
	}
	if !res.OK() {
		t.Error("a present (cluster-credentialed) image must make the run OK() — no false block")
	}
	if seenDir == "" {
		t.Error("the credentialed checker must be handed a DOCKER_CONFIG dir (creds materialised)")
	}
	if len(seenNames) != 1 || seenNames[0] != "registry-creds" {
		t.Errorf("resolver should be asked for the bundle's imagePullSecret; got %v", seenNames)
	}
}

// A confirmed miss from the cluster's perspective (creds resolve, but the image
// genuinely isn't there) must BLOCK as a MissingImage — the credentialed verdict
// is authoritative both ways.
func TestRunPreflightChecks_AuthDeniedClusterCredsConfirmMiss(t *testing.T) {
	const ref = "ghcr.io/acme/private-api:abc123"
	opts := PreflightOpts{
		Manifests: pullSecretManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Images: credAwareImageChecker{
			fakeImageChecker: fakeImageChecker{authDenied: keySet(ref)},
			credMiss:         keySet(ref), // cluster: confirmed absent
		},
		PullCreds: fakePullCredsResolver{},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected abort: %v", err)
	}
	if len(res.MissingImages) != 1 || res.MissingImages[0] != ref {
		t.Errorf("a cluster-confirmed miss must be a blocking MissingImage; got missing=%v unverifiable=%v", res.MissingImages, res.UnverifiableImages)
	}
}

// When the cluster creds ALSO can't confirm the image (still denied even with
// the pull secret), the conservative block is kept — the gate must not pass an
// image it cannot confirm. No regression of the auth-denied semantics.
func TestRunPreflightChecks_AuthDeniedClusterCredsAlsoDenied_KeepsBlock(t *testing.T) {
	const ref = "ghcr.io/acme/private-api:abc123"
	opts := PreflightOpts{
		Manifests: pullSecretManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Images: credAwareImageChecker{
			fakeImageChecker: fakeImageChecker{authDenied: keySet(ref)},
			// neither credPresent nor credMiss → credentialed lookup stays denied
		},
		PullCreds: fakePullCredsResolver{},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected abort: %v", err)
	}
	if len(res.UnverifiableImages) != 1 {
		t.Errorf("still-denied-with-cluster-creds must keep the Unverifiable block; got unverifiable=%v missing=%v", res.UnverifiableImages, res.MissingImages)
	}
}

// No imagePullSecret in the bundle → no creds to resolve → today's behaviour:
// an auth-denied lookup BLOCKS as UnverifiableImage. The fix must not regress
// the credentials-absent path.
func TestRunPreflightChecks_AuthDeniedNoPullSecret_KeepsBlock(t *testing.T) {
	const ref = "ghcr.io/acme/api:abc123"
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Images: credAwareImageChecker{
			fakeImageChecker: fakeImageChecker{authDenied: keySet(ref)},
			credPresent:      keySet(ref), // would resolve IF asked — but it must NOT be
		},
		PullCreds: fakePullCredsResolver{},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected abort: %v", err)
	}
	if len(res.UnverifiableImages) != 1 {
		t.Errorf("no imagePullSecret → no credentialed retry → auth-denied must still block; got unverifiable=%v missing=%v", res.UnverifiableImages, res.MissingImages)
	}
}

// dockerConfigAuthsFromSecret must decode a real kubectl-shaped
// dockerconfigjson Secret (base64 .data[".dockerconfigjson"]) into its auths.
func TestKubectlPullCredsResolver_DecodesDockerConfigSecret(t *testing.T) {
	const dockerCfg = `{"auths":{"ghcr.io":{"auth":"dXNlcjpwYXNz"}}}`
	b64 := base64.StdEncoding.EncodeToString([]byte(dockerCfg))
	secretJSON := []byte(`{"data":{".dockerconfigjson":"` + b64 + `"}}`)

	auths := dockerConfigAuthsFromSecret(secretJSON)
	if _, ok := auths["ghcr.io"]; !ok {
		t.Errorf("expected ghcr.io auth entry decoded from the secret; got %v", auths)
	}
}

// A Secret that isn't a dockerconfigjson (no .dockerconfigjson key) contributes
// nothing — best-effort, never an error.
func TestKubectlPullCredsResolver_NonCredSecretYieldsNothing(t *testing.T) {
	secretJSON := []byte(`{"data":{"tls.crt":"abc"}}`)
	if auths := dockerConfigAuthsFromSecret(secretJSON); len(auths) != 0 {
		t.Errorf("a non-dockerconfigjson secret must yield no auths; got %v", auths)
	}
}

// --- Preflight: image transport error → WARN + proceed (not block) --------

// A genuine transport failure (connection refused) says nothing about the
// image — the cluster may still pull it — so it must WARN+proceed, unchanged.
func TestPreflight_TransportErrorWarnsAndProceeds(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{
			"app-secrets": keySet("service_secret", "db_url"),
		}},
		Images: fakeImageChecker{inconclusive: keySet("ghcr.io/acme/api:abc123")},
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("a transport-class inconclusive check must warn+proceed, not block; got: %v", err)
	}
}

// --- Preflight: image confirmed-404 → blocked ----------------------------

func TestPreflight_Confirmed404ImageBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests: deploymentManifest,
		Context:   "gke_prod",
		Namespace: "app-prod",
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{
			"app-secrets": keySet("service_secret", "db_url"),
		}},
		// Not present, not inconclusive → confirmed miss → block.
		Images: fakeImageChecker{present: map[string]struct{}{}},
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("a confirmed-absent image must block the deploy")
	}
	if !strings.Contains(err.Error(), "Images not found") {
		t.Errorf("error should carry the Images not found block; got:\n%s", err.Error())
	}
}

func TestLocalImageRef(t *testing.T) {
	cases := map[string]bool{
		"registry.localhost:5000/api:dev": true,
		"localhost:5050/api:dev":          true,
		"127.0.0.1:5000/api:dev":          true,
		"ghcr.io/acme/api:abc123":         false,
		"us-docker.pkg.dev/p/r/api:v1":    false,
	}
	for ref, want := range cases {
		if got := LocalImageRef(ref); got != want {
			t.Errorf("LocalImageRef(%q) = %v, want %v", ref, got, want)
		}
	}
}
