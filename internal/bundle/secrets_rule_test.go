package bundle

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// legacyCheckSecretDocument is checkSecretDocument as it stood before it
// called the shared secret rule in pkg/release, kept verbatim (its own field
// list and marker test included) as the oracle the current check is held to.
func legacyCheckSecretDocument(where string, body any) error {
	doc, ok := body.(map[string]any)
	if !ok || doc["kind"] != "Secret" {
		return nil
	}
	name, _ := doc["metadata"].(map[string]any)["name"].(string)
	for _, field := range []string{"data", "stringData"} {
		values, ok := doc[field].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range values {
			s, isString := value.(string)
			if isString && strings.HasPrefix(s, release.RedactedSecretPrefix) {
				continue
			}
			return fmt.Errorf("%w: %s: Secret %q key %s.%s is not redacted "+
				"(a bundle is pushed, cached and kept forever, so the value would leak permanently)",
				ErrSecretValue, where, name, field, key)
		}
	}
	return fmt.Errorf("%w: %s: Secret %q is in the manifest layer; Secrets are synced by forge, never reconciled from a bundle",
		ErrSecretValue, where, name)
}

// TestCheckSecretDocument_RefusesWhatItAlwaysRefused proves that moving the
// F-13 predicate onto the shared rule changed nothing: over every input class
// (non-Secrets, redacted and unredacted Secrets, non-string and non-map
// values, and plain values the viewer would withhold), the check refuses
// exactly what the old one refused, with the same message. A case with
// several unredacted keys compares the refusal class only: the old check
// named whichever key map iteration reached first, the new one names the
// first in sorted order.
func TestCheckSecretDocument_RefusesWhatItAlwaysRefused(t *testing.T) {
	marker := release.RedactedSecretPrefix + "sha256:0123"
	secret := func(fields map[string]any) map[string]any {
		doc := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "db"}}
		for k, v := range fields {
			doc[k] = v
		}
		return doc
	}
	for _, tc := range []struct {
		name     string
		body     any
		manyBad  bool
		wantFail bool
	}{
		{"not a map", "kind: Secret", false, false},
		{"nil", nil, false, false},
		{"a ConfigMap with a password", map[string]any{"kind": "ConfigMap", "metadata": map[string]any{"name": "c"},
			"data": map[string]any{"DB_PASSWORD": "hunter2"}}, false, false},
		{"a Deployment whose env looks secret", map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "api"},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
				"name": "api", "env": []any{map[string]any{"name": "API_TOKEN", "value": "Zt9qK2vXw8LmP4rB7nYc3HdF"}},
			}}}}}}, false, false},
		{"a kindless map with data", map[string]any{"data": map[string]any{"A": "plain"}}, false, false},
		{"a redacted Secret", secret(map[string]any{"data": map[string]any{"A": marker}, "stringData": map[string]any{"B": marker}}), false, true},
		{"a Secret redacted by the viewer's marker", secret(map[string]any{"data": map[string]any{"A": release.RedactedWithheld}}), false, true},
		{"an empty Secret", secret(nil), false, true},
		{"a plain data value", secret(map[string]any{"data": map[string]any{"A": "aHVudGVyMg=="}}), false, true},
		{"a plain stringData value", secret(map[string]any{"data": map[string]any{"A": marker}, "stringData": map[string]any{"B": "hunter2"}}), false, true},
		{"a non-string value", secret(map[string]any{"data": map[string]any{"A": 42}}), false, true},
		{"a nil value", secret(map[string]any{"stringData": map[string]any{"A": nil}}), false, true},
		{"a value that only contains the marker", secret(map[string]any{"data": map[string]any{"A": "x" + marker}}), false, true},
		{"data that is not a map", secret(map[string]any{"data": []any{"hunter2"}}), false, true},
		{"several unredacted keys", secret(map[string]any{"data": map[string]any{"A": "1", "B": "2"}, "stringData": map[string]any{"C": "3"}}), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkSecretDocument("manifests/x.yaml", tc.body)
			want := legacyCheckSecretDocument("manifests/x.yaml", tc.body)
			if (got != nil) != tc.wantFail || (want != nil) != tc.wantFail {
				t.Fatalf("refused: new %v, old %v; want refusal %v", got, want, tc.wantFail)
			}
			if got == nil {
				return
			}
			if !errors.Is(got, ErrSecretValue) {
				t.Fatalf("refusal %v is not ErrSecretValue", got)
			}
			if tc.manyBad {
				if !strings.Contains(got.Error(), "is not redacted") || !strings.Contains(want.Error(), "is not redacted") {
					t.Fatalf("refusal class differs:\n new %v\n old %v", got, want)
				}
				return
			}
			if got.Error() != want.Error() {
				t.Fatalf("refusal differs:\n new %v\n old %v", got, want)
			}
		})
	}
}
