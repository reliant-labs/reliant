package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// nsSecretGetter resolves Secret keys keyed by "<namespace>/<name>" so a
// declared external prerequisite (which carries its OWN namespace, often NOT
// the deploy namespace) is checked in the right namespace.
type nsSecretGetter struct {
	secrets map[string]map[string]struct{} // "ns/name" -> key set
	err     error
}

func (g nsSecretGetter) GetSecretKeys(_ context.Context, _, ns, name string) (map[string]struct{}, bool, error) {
	if g.err != nil {
		return nil, false, g.err
	}
	keys, ok := g.secrets[ns+"/"+name]
	if !ok {
		return nil, false, nil
	}
	return keys, true, nil
}

// nsSecretValueGetter resolves decoded Secret values keyed by "<ns>/<name>".
type nsSecretValueGetter struct {
	values map[string]map[string][]byte // "ns/name" -> key -> value
	err    error
}

func (g nsSecretValueGetter) GetSecretValues(_ context.Context, _, ns, name string) (map[string][]byte, bool, error) {
	if g.err != nil {
		return nil, false, g.err
	}
	v, ok := g.values[ns+"/"+name]
	if !ok {
		return nil, false, nil
	}
	return v, true, nil
}

// A bundle with one cluster Deployment so hasK8sServices-style refs exist;
// the declared-prereq check is independent of manifest refs, so a minimal
// manifest stream is fine.
const prereqManifests = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/x/api:v1
`

func TestPreflight_RequiredSecretPresent(t *testing.T) {
	opts := PreflightOpts{
		Manifests: prereqManifests,
		Context:   "prod-ctx",
		Namespace: "prod",
		Secrets: nsSecretGetter{secrets: map[string]map[string]struct{}{
			"cert-manager/cloudflare-api-token": {"api-token": {}},
		}},
		RequiredSecrets: []RequiredSecret{
			{Name: "cloudflare-api-token", Namespace: "cert-manager", Keys: []string{"api-token"}},
		},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if len(res.MissingRequiredSecretKeys) != 0 {
		t.Fatalf("expected no missing required secrets, got %v", res.MissingRequiredSecretKeys)
	}
}

func TestPreflight_RequiredSecretAbsentBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests: prereqManifests,
		Context:   "prod-ctx",
		Namespace: "prod",
		// The declared Secret is in cert-manager; the getter has it nowhere.
		Secrets: nsSecretGetter{secrets: map[string]map[string]struct{}{}},
		RequiredSecrets: []RequiredSecret{
			{Name: "cloudflare-api-token", Namespace: "cert-manager", Keys: []string{"api-token"}},
		},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	missing, ok := res.MissingRequiredSecretKeys["cert-manager/cloudflare-api-token"]
	if !ok {
		t.Fatalf("expected the absent declared Secret to be reported, got %v", res.MissingRequiredSecretKeys)
	}
	// The whole-Secret marker stands in for an entirely-absent Secret.
	if len(missing) != 1 || missing[0] != wholeSecretMarker {
		t.Fatalf("expected whole-secret marker, got %v", missing)
	}
	if res.OK() {
		t.Fatal("expected result NOT OK when a declared-required Secret is absent")
	}
}

func TestPreflight_RequiredSecretMissingKeyBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests: prereqManifests,
		Context:   "prod-ctx",
		Namespace: "prod",
		Secrets: nsSecretGetter{secrets: map[string]map[string]struct{}{
			// Secret exists but lacks the declared key.
			"cert-manager/cloudflare-api-token": {"some-other-key": {}},
		}},
		RequiredSecrets: []RequiredSecret{
			{Name: "cloudflare-api-token", Namespace: "cert-manager", Keys: []string{"api-token"}},
		},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	missing := res.MissingRequiredSecretKeys["cert-manager/cloudflare-api-token"]
	if len(missing) != 1 || missing[0] != "api-token" {
		t.Fatalf("expected the missing api-token key, got %v", missing)
	}
}

func TestPreflight_RequiredSecretSkippedWithoutContext(t *testing.T) {
	// No Context => the declared-prereq check is inert (like the secretKeyRef
	// check), so a "missing" declared Secret does NOT block (local dev path).
	opts := PreflightOpts{
		Manifests:       prereqManifests,
		Namespace:       "prod",
		Secrets:         nsSecretGetter{secrets: map[string]map[string]struct{}{}},
		RequiredSecrets: []RequiredSecret{{Name: "x", Namespace: "cert-manager", Keys: []string{"k"}}},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if len(res.MissingRequiredSecretKeys) != 0 {
		t.Fatalf("expected no check without a context, got %v", res.MissingRequiredSecretKeys)
	}
}

func TestPreflight_ByteMatchGroupConsistent(t *testing.T) {
	tok := []byte("secret-token-value")
	opts := PreflightOpts{
		Manifests: prereqManifests,
		Context:   "prod-ctx",
		Namespace: "prod",
		Secrets: nsSecretGetter{secrets: map[string]map[string]struct{}{
			"a/tok":     {"api-token": {}},
			"b/tok-mir": {"api-token": {}},
		}},
		SecretValues: nsSecretValueGetter{values: map[string]map[string][]byte{
			"a/tok":     {"api-token": tok},
			"b/tok-mir": {"api-token": tok},
		}},
		RequiredSecrets: []RequiredSecret{
			{Name: "tok", Namespace: "a", Keys: []string{"api-token"}, ValueGroup: "g"},
			{Name: "tok-mir", Namespace: "b", Keys: []string{"api-token"}, ValueGroup: "g"},
		},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if len(res.ByteMatchMismatches) != 0 {
		t.Fatalf("expected no byte-match mismatch for identical values, got %v", res.ByteMatchMismatches)
	}
}

func TestPreflight_ByteMatchGroupDivergentBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests: prereqManifests,
		Context:   "prod-ctx",
		Namespace: "prod",
		Secrets: nsSecretGetter{secrets: map[string]map[string]struct{}{
			"a/tok":     {"api-token": {}},
			"b/tok-mir": {"api-token": {}},
		}},
		SecretValues: nsSecretValueGetter{values: map[string]map[string][]byte{
			"a/tok":     {"api-token": []byte("FRESH-token")},
			"b/tok-mir": {"api-token": []byte("STALE-token")}, // half-rotated
		}},
		RequiredSecrets: []RequiredSecret{
			{Name: "tok", Namespace: "a", Keys: []string{"api-token"}, ValueGroup: "g"},
			{Name: "tok-mir", Namespace: "b", Keys: []string{"api-token"}, ValueGroup: "g"},
		},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if len(res.ByteMatchMismatches) == 0 {
		t.Fatal("expected a byte-match mismatch for divergent group values")
	}
	if res.OK() {
		t.Fatal("expected result NOT OK on a byte-match divergence")
	}
	if !strings.Contains(res.ByteMatchMismatches[0], "value-group") {
		t.Fatalf("mismatch message should name the value-group, got %q", res.ByteMatchMismatches[0])
	}
}

func TestPreflight_RequiredSecretGetterErrorAborts(t *testing.T) {
	opts := PreflightOpts{
		Manifests:       prereqManifests,
		Context:         "prod-ctx",
		Namespace:       "prod",
		Secrets:         nsSecretGetter{err: errors.New("kubectl: connection refused")},
		RequiredSecrets: []RequiredSecret{{Name: "x", Namespace: "cert-manager", Keys: []string{"k"}}},
	}
	_, err := runPreflightChecks(context.Background(), opts)
	if err == nil {
		t.Fatal("expected a getter error to abort the preflight")
	}
}

// FormatPreflightReport includes the declared-prereq + byte-match blocks.
func TestFormatPreflightReport_PrereqBlocks(t *testing.T) {
	r := PreflightResult{
		MissingRequiredSecretKeys: map[string][]string{
			"cert-manager/cloudflare-api-token": {wholeSecretMarker},
		},
		ByteMatchMismatches: []string{`value-group "g": key "api-token" differs ...`},
	}
	out := FormatPreflightReport(r)
	if !strings.Contains(out, "DECLARED external Secret prerequisites missing") {
		t.Errorf("report missing the declared-prereq block:\n%s", out)
	}
	if !strings.Contains(out, "byte-match mismatches") {
		t.Errorf("report missing the byte-match block:\n%s", out)
	}
	if !strings.Contains(out, "out-of-band") {
		t.Errorf("report missing the out-of-band remediation hint:\n%s", out)
	}
}
