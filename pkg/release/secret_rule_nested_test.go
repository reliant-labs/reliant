package release

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRedactObjectNestedObjectPolicies(t *testing.T) {
	secret := map[string]any{"kind": "Secret", "stringData": map[string]any{"PASSWORD": "hunter2", "ALREADY": RedactedWithheld}, "data": map[string]string{"USER": "short-private"}}
	wantSecret := map[string]any{"kind": "Secret", "stringData": map[string]any{"PASSWORD": RedactedWithheld, "ALREADY": RedactedWithheld}, "data": map[string]any{"USER": RedactedWithheld}}
	config := map[string]any{"kind": "ConfigMap", "data": map[string]any{"API_TOKEN": "abc", "PORT": "8080"}, "binaryData": map[string]string{"blob": "short-private"}}
	wantConfig := map[string]any{"kind": "ConfigMap", "data": map[string]any{"API_TOKEN": RedactedWithheld, "PORT": "8080"}, "binaryData": map[string]any{"blob": RedactedWithheld}}
	for _, tc := range []struct {
		name     string
		in, want any
	}{
		{"kubernetes-list", map[string]any{"kind": "List", "items": []any{secret, config}}, map[string]any{"kind": "List", "items": []any{wantSecret, wantConfig}}},
		{"nested-list", map[string]any{"kind": "List", "items": []any{map[string]any{"kind": "List", "items": []any{secret, config}}}}, map[string]any{"kind": "List", "items": []any{map[string]any{"kind": "List", "items": []any{wantSecret, wantConfig}}}}},
		{"root-array", []any{secret, []any{config}}, []any{wantSecret, []any{wantConfig}}},
		{"extension", map[string]any{"kind": "Custom", "extension": map[string]any{"nested": secret}}, map[string]any{"kind": "Custom", "extension": map[string]any{"nested": wantSecret}}},
		{"mixed-malformed", []any{nil, 42, "public", map[string]any{"kind": "Secret", "stringData": []any{"short-private"}}, map[string]any{"kind": "ConfigMap", "data": nil}}, []any{nil, 42, "public", map[string]any{"kind": "Secret", "stringData": RedactedWithheld}, map[string]any{"kind": "ConfigMap", "data": RedactedWithheld}}},
		{"typed-map", []any{map[string]string{"kind": "Secret", "stringData": "short-private"}}, []any{map[string]any{"kind": "Secret", "stringData": RedactedWithheld}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			got := RedactObject(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("redacted = %#v, want %#v", got, tc.want)
			}
			after, _ := json.Marshal(tc.in)
			if string(before) != string(after) {
				t.Fatal("nested input mutated")
			}
			encoded, _ := json.Marshal(got)
			for _, bad := range []string{"hunter2", "short-private", "abc", "sha256:"} {
				if strings.Contains(string(encoded), bad) {
					t.Errorf("secret/fingerprint survived: %s", encoded)
				}
			}
			if again := RedactObject(got); !reflect.DeepEqual(again, got) {
				t.Fatalf("not idempotent: %#v -> %#v", got, again)
			}
		})
	}
}
