package cluster

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// An `optional: true` reference has two modes: an ABSENT resource demands
// nothing, and a PRESENT one is still read by the pod, so a key it lacks is
// reported (as a warning: kubelet starts the pod with the variable unset) and
// its version pin rolls the pod (secret_pins_test.go).

// optionalStream reads every reference shape optionally, beside one required
// reference, from a single Deployment.
const optionalStream = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
spec:
  template:
    spec:
      containers:
        - name: api
          image: reg/api@sha256:1
          env:
            - name: REQUIRED
              valueFrom:
                secretKeyRef:
                  name: shared
                  key: required_key
            - name: OPTIONAL_IN_SHARED
              valueFrom:
                secretKeyRef:
                  name: shared
                  key: integration_key
                  optional: true
            - name: OPTIONAL_ALONE
              valueFrom:
                secretKeyRef:
                  name: integration
                  key: token
                  optional: true
            - name: FLAG
              valueFrom:
                configMapKeyRef:
                  name: flags
                  key: flag
                  optional: true
          envFrom:
            - secretRef:
                name: bulk
                optional: true
      volumes:
        - name: v
          secret:
            secretName: mounted
            optional: true
`

// countingSecrets is fakeSecretGetter that records each lookup and can fail
// one Secret's.
type countingSecrets struct {
	fakeSecretGetter
	failOn string
	mu     sync.Mutex
	asked  map[string]int
}

func (c *countingSecrets) GetSecretKeys(ctx context.Context, kctx, ns, name string) (map[string]struct{}, bool, error) {
	c.mu.Lock()
	if c.asked == nil {
		c.asked = map[string]int{}
	}
	c.asked[name]++
	c.mu.Unlock()
	if name == c.failOn {
		return nil, false, errors.New("forbidden")
	}
	return c.fakeSecretGetter.GetSecretKeys(ctx, kctx, ns, name)
}

func TestCollectManifestRefs_OptionalRefsRecordedApart(t *testing.T) {
	refs := CollectManifestRefs(optionalStream, RefPlacement{})
	if got := byName(refs.Secrets); len(got) != 1 || len(got["shared"]) != 1 {
		t.Fatalf("only the required reference is demand; Secrets = %v", refs.Secrets)
	}
	opt := byName(refs.OptionalSecrets)
	for name, key := range map[string]string{"shared": "integration_key", "integration": "token", "bulk": "", "mounted": ""} {
		if _, ok := opt[name][key]; !ok {
			t.Errorf("optional Secret %q key %q must be recorded in OptionalSecrets; got %v", name, key, refs.OptionalSecrets)
		}
	}
	if _, ok := byName(refs.OptionalConfigMaps)["flags"]["flag"]; !ok || len(refs.ConfigMaps) != 0 {
		t.Errorf("an optional ConfigMap is OptionalConfigMaps, not demand; ConfigMaps=%v Optional=%v", refs.ConfigMaps, refs.OptionalConfigMaps)
	}
}

// A Secret the stream renders satisfies optional references to it too.
func TestCollectManifestRefs_SelfProvidedDropsOptional(t *testing.T) {
	stream := optionalStream + "---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: integration\n  namespace: app\n"
	if _, ok := byName(CollectManifestRefs(stream, RefPlacement{}).OptionalSecrets)["integration"]; ok {
		t.Error("a Secret the stream itself renders must not be looked up for its optional readers")
	}
}

func optionalPreflight(secrets SecretGetter, configMaps ConfigMapGetter) PreflightOpts {
	return PreflightOpts{
		Manifests: optionalStream, Context: "gke_prod", Namespace: "app",
		Secrets: secrets, ConfigMaps: configMaps,
	}
}

// Absent: an optional reference whose Secret or ConfigMap does not exist is
// no finding of any kind, and a whole-resource optional reference (envFrom,
// volume) costs no lookup at all — present or absent, it has nothing to check.
func TestPreflight_OptionalAbsentIsNoFinding(t *testing.T) {
	secrets := &countingSecrets{fakeSecretGetter: fakeSecretGetter{secrets: map[string]map[string]struct{}{
		"shared": keySet("required_key", "integration_key"),
	}}}
	res, err := runPreflightChecks(context.Background(), optionalPreflight(secrets, fakeConfigMapGetter{}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() || len(res.OptionalKeyWarnings) != 0 {
		t.Fatalf("absent optional resources must report nothing; OK=%v missing=%v warnings=%v", res.OK(), res.MissingSecretKeys, res.OptionalKeyWarnings)
	}
	if secrets.asked["bulk"]+secrets.asked["mounted"] != 0 {
		t.Errorf("a whole-Secret optional reference must cost no lookup; asked %v", secrets.asked)
	}
	if secrets.asked["shared"] != 1 || secrets.asked["integration"] != 1 {
		t.Errorf("each keyed Secret is read exactly once; asked %v", secrets.asked)
	}
}

// Present: a Secret or ConfigMap that exists but lacks the key an optional
// reference reads is a warning — the deploy still proceeds — and a present
// key is nothing.
func TestPreflight_OptionalPresentIsKeyChecked(t *testing.T) {
	secrets := fakeSecretGetter{secrets: map[string]map[string]struct{}{
		"shared":      keySet("required_key"), // integration_key absent by design
		"integration": keySet("token"),
		"bulk":        keySet("x"),
	}}
	res, err := runPreflightChecks(context.Background(), optionalPreflight(secrets, fakeConfigMapGetter{configMaps: map[string]map[string]struct{}{
		"flags": keySet("other"),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("a missing optional key must not block: %s", FormatPreflightReport(res))
	}
	want := []string{
		`ConfigMap app/flags: optional key "flag" is absent`,
		`Secret app/shared: optional key "integration_key" is absent`,
	}
	if len(res.OptionalKeyWarnings) != len(want) {
		t.Fatalf("warnings = %q, want %d", res.OptionalKeyWarnings, len(want))
	}
	for i, w := range want {
		if !strings.HasPrefix(res.OptionalKeyWarnings[i], w) {
			t.Errorf("warning[%d] = %q, want prefix %q", i, res.OptionalKeyWarnings[i], w)
		}
	}
}

// A required reference keeps blocking beside an optional one to the same
// Secret, and a key both read is demand, never a warning.
func TestPreflight_OptionalBesideRequiredStillBlocks(t *testing.T) {
	res, err := runPreflightChecks(context.Background(), optionalPreflight(fakeSecretGetter{secrets: map[string]map[string]struct{}{
		"shared": keySet("unrelated"),
	}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.MissingSecretKeys["app/shared"]; len(got) != 1 || got[0] != "required_key" {
		t.Fatalf("the required key must block alone; MissingSecretKeys = %v", res.MissingSecretKeys)
	}
	if len(res.OptionalKeyWarnings) != 1 || !strings.Contains(res.OptionalKeyWarnings[0], "integration_key") {
		t.Errorf("the optional key beside it is a warning; got %q", res.OptionalKeyWarnings)
	}

	res, err = runPreflightChecks(context.Background(), optionalPreflight(fakeSecretGetter{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.MissingSecretKeys["app/shared"]; len(got) != 1 || got[0] != wholeSecretMarker {
		t.Errorf("an absent Secret a required reference reads still blocks; got %v", res.MissingSecretKeys)
	}
}

// An optional-only Secret that cannot be read is a warning, not an aborted
// preflight: what the read would have found does not block either.
func TestPreflight_OptionalReadFailureWarns(t *testing.T) {
	secrets := &countingSecrets{
		fakeSecretGetter: fakeSecretGetter{secrets: map[string]map[string]struct{}{"shared": keySet("required_key", "integration_key")}},
		failOn:           "integration",
	}
	res, err := runPreflightChecks(context.Background(), optionalPreflight(secrets, nil))
	if err != nil {
		t.Fatalf("an unreadable optional Secret must not abort the preflight: %v", err)
	}
	if !res.OK() || len(res.OptionalKeyWarnings) != 1 || !strings.Contains(res.OptionalKeyWarnings[0], "Secret app/integration: could not be read") {
		t.Errorf("want one could-not-read warning; OK=%v warnings=%q", res.OK(), res.OptionalKeyWarnings)
	}

	secrets.failOn = "shared"
	if _, err := runPreflightChecks(context.Background(), optionalPreflight(secrets, nil)); err == nil {
		t.Error("an unreadable Secret a required reference reads must still abort")
	}
}

// A Secret this deploy provisions is not looked up for its optional readers
// either.
func TestPreflight_OptionalProvisionedByDeployIsSkipped(t *testing.T) {
	secrets := &countingSecrets{fakeSecretGetter: fakeSecretGetter{secrets: map[string]map[string]struct{}{"shared": keySet("required_key", "integration_key")}}}
	opts := optionalPreflight(secrets, nil)
	opts.ProvisionedByDeploy = []ProvisionedSecret{{Name: "integration"}}
	if _, err := runPreflightChecks(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if secrets.asked["integration"] != 0 {
		t.Errorf("a Secret this deploy provisions must not be read; asked %v", secrets.asked)
	}
}
