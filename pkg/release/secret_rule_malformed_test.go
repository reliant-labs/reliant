package release

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func assertRedactedCopy(t *testing.T, doc, want any) {
	t.Helper()
	before, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	got := RedactObject(doc)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("redacted = %#v, want %#v", got, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "short-private") || strings.Contains(string(encoded), "sha256:") {
		t.Errorf("secret or fingerprint survived: %s", encoded)
	}
	after, _ := json.Marshal(doc)
	if string(after) != string(before) {
		t.Fatal("input mutated")
	}
	if again := RedactObject(got); !reflect.DeepEqual(again, got) {
		t.Errorf("redaction not idempotent: %#v -> %#v", got, again)
	}
}

func TestRedactObjectSensitiveHeadersAndMalformedValues(t *testing.T) {
	names := []string{"Authorization", "authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Auth", "X-Session-ID", "PASSWORD"}
	values := []any{"short-private", map[string]any{"value": "short-private"}, []any{"short-private"}, 42, nil}
	for _, name := range names {
		if !LooksSecret(name, "short-private") {
			t.Errorf("LooksSecret missed %s", name)
		}
		for _, value := range values {
			t.Run(name, func(t *testing.T) {
				for _, field := range []string{"env", "httpHeaders", "custom"} {
					doc := map[string]any{field: []any{map[string]any{"name": name, "value": value}}}
					want := map[string]any{field: []any{map[string]any{"name": name, "value": RedactedWithheld}}}
					assertRedactedCopy(t, doc, want)
				}
				assertRedactedCopy(t, map[string]any{"kind": "ConfigMap", "data": map[string]any{name: value}}, map[string]any{"kind": "ConfigMap", "data": map[string]any{name: RedactedWithheld}})
			})
		}
	}
	for _, name := range []any{nil, 42, []any{"Authorization"}, map[string]any{"header": "Authorization"}} {
		assertRedactedCopy(t, map[string]any{"name": name, "value": "short-private"}, map[string]any{"name": name, "value": RedactedWithheld})
	}
}

func TestRedactObjectMalformedSecretArgs(t *testing.T) {
	values := []any{map[string]any{"value": "short-private"}, []any{"short-private"}, nil, 42, "short-private", "--other=short-private"}
	for _, field := range []string{"args", "command"} {
		for _, value := range values {
			assertRedactedCopy(t, map[string]any{field: []any{"--password", value, "--port=8080"}}, map[string]any{field: []any{"--password", RedactedWithheld, "--port=8080"}})
		}
		for _, value := range []any{map[string]any{"value": "short-private"}, "short-private", nil, 42} {
			assertRedactedCopy(t, map[string]any{field: value}, map[string]any{field: RedactedWithheld})
		}
	}
}

func TestRedactObjectMalformedDataFields(t *testing.T) {
	for _, kind := range []string{"Secret", "ConfigMap"} {
		for _, field := range []string{"data", "stringData", "binaryData"} {
			if kind == "ConfigMap" && field == "stringData" || kind == "Secret" && field == "binaryData" {
				continue
			}
			for _, value := range []any{"short-private", []any{"short-private"}, nil, 42} {
				assertRedactedCopy(t, map[string]any{"kind": kind, field: value}, map[string]any{"kind": kind, field: RedactedWithheld})
			}
		}
	}
}

func TestRedactObjectMalformedEnvAndHeaders(t *testing.T) {
	for _, field := range []string{"env", "httpHeaders"} {
		for _, value := range []any{map[string]any{"Authorization": "short-private"}, "short-private", nil, 42} {
			assertRedactedCopy(t, map[string]any{field: value}, map[string]any{field: RedactedWithheld})
		}
		for _, value := range []any{"short-private", nil, 42, map[string]any{"value": "short-private"}} {
			assertRedactedCopy(t, map[string]any{field: []any{value}}, map[string]any{field: []any{RedactedWithheld}})
		}
	}
	assertRedactedCopy(t, map[string]any{"value": "short-private"}, map[string]any{"value": RedactedWithheld})
}

func TestRedactObjectTypedMapsKeepSecretPolicy(t *testing.T) {
	assertRedactedCopy(t, map[string]string{"name": "Authorization", "value": "short-private"}, map[string]any{"name": "Authorization", "value": RedactedWithheld})
	assertRedactedCopy(t, map[string]any{"kind": "ConfigMap", "data": map[string]string{"Cookie": "short-private", "PORT": "8080"}}, map[string]any{"kind": "ConfigMap", "data": map[string]any{"Cookie": RedactedWithheld, "PORT": "8080"}})
	assertRedactedCopy(t, map[string]any{"kind": "Secret", "data": map[string]string{"USER": "short-private"}}, map[string]any{"kind": "Secret", "data": map[string]any{"USER": RedactedWithheld}})
}
