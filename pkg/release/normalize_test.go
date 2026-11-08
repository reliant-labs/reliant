package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// canary is a string that appears nowhere else, so finding it in a hash
// input proves a Secret value survived redaction.
const canary = "ZmFrZS1kYi1wYXNzd29yZC1DQU5BUlk="

func secretDoc() map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "db", "namespace": "shop"},
		"data":       map[string]any{"DATABASE_URL": canary},
		"stringData": map[string]any{"TOKEN": "plain-" + canary},
	}
}

func TestRedactSecretsReplacesEveryValueAndKeepsKeys(t *testing.T) {
	out, ok := RedactSecrets(secretDoc()).(map[string]any)
	if !ok {
		t.Fatalf("RedactSecrets returned %T, want a map", out)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("the canary survived redaction: %s", encoded)
	}
	for _, field := range []string{"data", "stringData"} {
		values, ok := out[field].(map[string]any)
		if !ok {
			t.Fatalf("%s is %T, want a map", field, out[field])
		}
		if len(values) != 1 {
			t.Fatalf("%s has %d keys, want 1 — the KEYS are a shape fact and must survive", field, len(values))
		}
		for key, value := range values {
			s, _ := value.(string)
			if !strings.HasPrefix(s, RedactedSecretPrefix) {
				t.Fatalf("%s[%s] = %q, want the %q marker", field, key, s, RedactedSecretPrefix)
			}
		}
	}
	if _, ok := out["data"].(map[string]any)["DATABASE_URL"]; !ok {
		t.Fatal("the data key DATABASE_URL was dropped")
	}
}

// Idempotence is what lets ObjectHashes redact defensively without changing
// the hash a caller who already redacted would get. Without it, the one safe
// entry point and the one correct hash would be different calls.
func TestRedactSecretsIsIdempotent(t *testing.T) {
	once, err := HashDocument(RedactSecrets(secretDoc()))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := HashDocument(RedactSecrets(RedactSecrets(secretDoc())))
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Fatalf("redacting twice changed the hash:\n once  %s\n twice %s", once, twice)
	}
}

func TestRedactSecretsLeavesOtherKindsAlone(t *testing.T) {
	cm := map[string]any{
		"kind": "ConfigMap",
		"data": map[string]any{"MESSAGE": "hello"},
	}
	out, _ := RedactSecrets(cm).(map[string]any)
	if got := out["data"].(map[string]any)["MESSAGE"]; got != "hello" {
		t.Fatalf("a ConfigMap value was redacted: %v", got)
	}
}

func TestNormalizeImagesRewritesOnlyTheDigest(t *testing.T) {
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	doc := map[string]any{
		"kind": "Deployment",
		"spec": map[string]any{
			"template": map[string]any{"spec": map[string]any{"containers": []any{
				map[string]any{"image": "ghcr.io/acme/api@" + digest},
			}}},
			"env": []any{map[string]any{"name": "DAEMON_IMAGE", "value": "ghcr.io/acme/api@" + digest}},
		},
	}
	out := NormalizeImages(doc, map[string]string{"api": digest})
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(encoded)
	if strings.Contains(got, digest) {
		t.Fatalf("a release-bound digest survived normalization: %s", got)
	}
	if want := "ghcr.io/acme/api@" + ArtifactPlaceholder + "api"; !strings.Contains(got, want) {
		t.Fatalf("normalized document does not carry %q:\n%s", want, got)
	}
	// The REPOSITORY is config: pushing an image somewhere else is a
	// change, and only the digest is what a promotion moves.
	if !strings.Contains(got, "ghcr.io/acme/api@") {
		t.Fatalf("normalization ate the repository, which is part of the config:\n%s", got)
	}
	// The input must be untouched — the caller still needs it to hash.
	if first := doc["spec"].(map[string]any)["template"]; first == nil {
		t.Fatal("the input document was mutated")
	}
	containers := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	if img := containers[0].(map[string]any)["image"]; img != "ghcr.io/acme/api@"+digest {
		t.Fatalf("NormalizeImages mutated its input: %v", img)
	}
}

func TestNormalizeImagesWithNoPinsIsIdentity(t *testing.T) {
	doc := map[string]any{"kind": "ConfigMap", "data": map[string]any{"a": "b"}}
	before, err := HashDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	after, err := HashDocument(NormalizeImages(doc, nil))
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("normalizing with no pins changed the hash: %s vs %s", before, after)
	}
}

// Two artifacts sharing a digest must normalize the same way every time, or
// the ConfigHash would depend on map iteration order.
func TestNormalizeImagesIsStableAcrossSharedDigests(t *testing.T) {
	const digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	pins := map[string]string{"zeta": digest, "alpha": digest, "mu": digest}
	doc := map[string]any{"image": "ghcr.io/acme/x@" + digest}
	first, err := HashDocument(NormalizeImages(doc, pins))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		again, err := HashDocument(NormalizeImages(doc, pins))
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("normalization is order-dependent: %s vs %s", first, again)
		}
	}
	if !strings.Contains(mustJSON(t, NormalizeImages(doc, pins)), ArtifactPlaceholder+"alpha") {
		t.Fatal("the lowest-sorting artifact must win a shared digest")
	}
}

func TestHashDocumentIsKeyOrderIndependentAndValueSensitive(t *testing.T) {
	a, err := HashDocument(map[string]any{"b": 1, "a": 2, "c": []any{"x", "y"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashDocument(map[string]any{"c": []any{"x", "y"}, "a": 2, "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("hash depends on key order: %s vs %s", a, b)
	}
	c, err := HashDocument(map[string]any{"b": 1, "a": 3, "c": []any{"x", "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("hash did not move when a value changed")
	}
	if !ValidDigest(a) {
		t.Fatalf("hash %q is not a canonical digest", a)
	}
}

// yaml.v3 decodes a mapping with a non-string key into map[any]any, which
// encoding/json refuses outright. Stringifying the key keeps one odd document
// from failing a whole render's hash.
func TestHashDocumentHandlesNonStringKeys(t *testing.T) {
	got, err := HashDocument(map[any]any{1: "one", "two": 2})
	if err != nil {
		t.Fatalf("HashDocument refused a map[any]any: %v", err)
	}
	want, err := HashDocument(map[string]any{"1": "one", "two": 2})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("a stringified key hashed differently: %s vs %s", got, want)
	}
}

func TestObjectHashes(t *testing.T) {
	const digest = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	pins := map[string]string{"api": digest}

	deployment := func(d string) map[string]any {
		return map[string]any{
			"kind": "Deployment",
			"spec": map[string]any{"image": "ghcr.io/acme/api@" + d},
		}
	}

	hash, configHash, err := ObjectHashes(deployment(digest), pins)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidDigest(hash) || !ValidDigest(configHash) {
		t.Fatalf("ObjectHashes returned non-canonical digests %q / %q", hash, configHash)
	}
	if hash == configHash {
		t.Fatal("hash and configHash are equal for a release-bound object; the normalization did nothing")
	}

	// THE PROPERTY THE TWO DIGESTS EXIST FOR: a new release moves Hash and
	// leaves ConfigHash alone, so "promote" and "the KCL moved" are one
	// comparison apart rather than a YAML diff apart.
	const next = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	hash2, configHash2, err := ObjectHashes(deployment(next), map[string]string{"api": next})
	if err != nil {
		t.Fatal(err)
	}
	if hash == hash2 {
		t.Fatal("Hash did not move when the pinned image changed")
	}
	if configHash != configHash2 {
		t.Fatalf("ConfigHash moved on an images-only change: %s vs %s", configHash, configHash2)
	}
}

// ObjectHashes is the one safe entry point: it redacts, so a caller cannot
// hash a value by forgetting to.
func TestObjectHashesRedactsBeforeHashing(t *testing.T) {
	hash, configHash, err := ObjectHashes(secretDoc(), nil)
	if err != nil {
		t.Fatal(err)
	}
	valueHash := sha256.Sum256([]byte(canary))
	plain, err := HashDocument(secretDoc())
	if err != nil {
		t.Fatal(err)
	}
	if hash == plain {
		t.Fatal("ObjectHashes hashed the raw Secret; the redaction did not run")
	}
	if hash != configHash {
		t.Fatalf("a Secret binds no release image, so its two hashes must agree: %s vs %s", hash, configHash)
	}
	// The hash of the VALUE is what the marker carries, and it is what
	// keeps "a secret changed" visible without the secret.
	want, err := HashDocument(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "db", "namespace": "shop"},
		"data":       map[string]any{"DATABASE_URL": RedactedSecretPrefix + "sha256:" + hex.EncodeToString(valueHash[:])},
		"stringData": map[string]any{"TOKEN": RedactedSecretPrefix + "sha256:" + hashHex("plain-"+canary)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hash != want {
		t.Fatalf("redacted Secret hash %s, want %s", hash, want)
	}
}

// ObjectHashes must not mutate what it was handed. A caller that packaged the
// document after hashing it would otherwise ship a normalized placeholder
// where a real image reference belongs — an unrunnable manifest.
func TestObjectHashesDoesNotMutateItsInput(t *testing.T) {
	const digest = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	doc := map[string]any{"kind": "Deployment", "spec": map[string]any{"image": "r/i@" + digest}}
	if _, _, err := ObjectHashes(doc, map[string]string{"i": digest}); err != nil {
		t.Fatal(err)
	}
	if got := doc["spec"].(map[string]any)["image"]; got != "r/i@"+digest {
		t.Fatalf("ObjectHashes rewrote its input: %v", got)
	}
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
