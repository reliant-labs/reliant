package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const (
	testJWT      = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	testToken    = "Zt9qK2vXw8LmP4rB7nYc3HdF"
	testHex64    = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	testImageRef = "ghcr.io/acme/api@sha256:" + testHex64
)

// Deliberately invalid, synthetic fixture: exercises the PEM delimiter detector
// without embedding DER/base64 key material or a usable credential in source.
var testPEM = strings.Join([]string{
	strings.Repeat("-", 5) + "BEGIN " + "PRIVATE KEY" + strings.Repeat("-", 5),
	"not-a-key: synthetic detector fixture",
	strings.Repeat("-", 5) + "END " + "PRIVATE KEY" + strings.Repeat("-", 5),
}, "\n")

func TestLooksSecret(t *testing.T) {
	for _, tc := range []struct {
		why         string
		name, value string
		want        bool
	}{
		// By name, whatever the value.
		{"password name", "DB_PASSWORD", "hunter2", true},
		{"lower-case secret key name", "stripe_secret_key", "x", true},
		{"token name", "API_TOKEN", "abc", true},
		{"dsn name", "DATABASE_DSN", "host=db", true},
		{"credential name", "GOOGLE_APPLICATION_CREDENTIALS", "/etc/gcp/sa.json", true},
		{"key in a camelCase name", "signingKey", "v", true},
		{"passwd name", "SMTP_PASSWD", "p", true},
		{"a name with nothing to hide", "DB_PASSWORD", "", false},
		{"already redacted", "DB_PASSWORD", RedactedWithheld, false},
		{"the withheld marker", "API_TOKEN", RedactedWithheld, false},

		// By shape, under an innocent name.
		{"URL userinfo", "DATABASE_URL", "postgres://app:s3cr3t@db:5432/shop", true},
		{"a JWT", "UPSTREAM_AUTH", testJWT, true},
		{"a PEM private key", "TLS", testPEM, true},
		{"a bearer credential", "UPSTREAM", "Bearer abcdef0123456789", true},
		{"OTel exporter headers", "OTEL_EXPORTER_OTLP_HEADERS", "authorization=Basic dXNlcjpwYXNzd29yZA==", true},
		{"an Anthropic key", "LLM", "sk-ant-api03-abcdefghijklmnop", true},
		{"a GitHub token", "GH", "ghp_abcdefghijklmnopqrstuvwxyz0123", true},
		{"a credential key=value in a value", "OPTS", "user=app password=s3cr3t", true},

		// By entropy.
		{"a generated token", "UPSTREAM", testToken, true},
		{"a bare 64-hex string", "CHECKSUM_SALT_VALUE", testHex64, true},
		{"no name at all, generated token", "", testToken, true},

		// Shown.
		{"a log level", "LOG_LEVEL", "info", false},
		{"a port", "PORT", "8080", false},
		{"a word", "ENVIRONMENT", "production", false},
		{"a boolean", "FEATURE_X", "true", false},
		{"an endpoint", "OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector.observability.svc:4317", false},
		{"a URL without userinfo", "DATABASE_URL", "postgres://db.shop.svc:5432/shop?sslmode=disable", false},
		{"an image pinned by digest", "WORKER_IMAGE", testImageRef, false},
		{"a git commit id", "GIT_COMMIT", "0123456789abcdef0123456789abcdef01234567", false},
		{"a UUID", "TENANT_ID", "123e4567-e89b-12d3-a456-426614174000", false},
		{"a CamelCase name with a year", "APP_NAME", "MyApplicationName2024", false},
		{"a kebab-case name", "SERVICE", "my-service-name-here-v2", false},
		{"a path", "DATA_DIR", "/var/lib/forge/data", false},
		{"a short value", "REGION", "us-central1", false},
	} {
		if got := LooksSecret(tc.name, tc.value); got != tc.want {
			t.Errorf("%s: LooksSecret(%q, %q) = %v, want %v", tc.why, tc.name, tc.value, got, tc.want)
		}
	}
}

func TestRedactedDigest(t *testing.T) {
	sum := sha256.Sum256([]byte("hunter2"))
	want := RedactedSecretPrefix + "sha256:" + hex.EncodeToString(sum[:])[:12]
	got := RedactedDigest("hunter2")
	if got != want {
		t.Fatalf("RedactedDigest = %q, want %q", got, want)
	}
	if again := RedactedDigest(got); again != got {
		t.Fatalf("RedactedDigest is not idempotent: %q -> %q", got, again)
	}
	if RedactedDigest("hunter2") == RedactedDigest("hunter3") {
		t.Fatal("explicit digest utility did not distinguish values")
	}
	if !IsRedacted(got) || !IsRedacted(RedactedWithheld) {
		t.Fatal("IsRedacted must recognize every redaction this package writes")
	}
	for _, v := range []any{"hunter2", 42, nil, "sha256:" + testHex64} {
		if IsRedacted(v) {
			t.Errorf("IsRedacted(%v) = true", v)
		}
	}
}

func TestSecretObjectValues(t *testing.T) {
	doc := map[string]any{
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "db"},
		"stringData": map[string]any{"B": "2", "A": "1"},
		"data":       map[string]any{"Z": "z"},
	}
	got := SecretObjectValues(doc)
	want := []SecretObjectValue{
		{Field: "data", Key: "Z", Value: "z"},
		{Field: "stringData", Key: "A", Value: "1"},
		{Field: "stringData", Key: "B", Value: "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretObjectValues = %+v, want %+v", got, want)
	}
	for _, notSecret := range []any{
		map[string]any{"kind": "ConfigMap", "data": map[string]any{"A": "1"}},
		map[string]any{"data": map[string]any{"A": "1"}},
		"Secret",
		nil,
	} {
		if IsSecretObject(notSecret) || SecretObjectValues(notSecret) != nil {
			t.Errorf("%v was treated as a Secret", notSecret)
		}
	}
	if vs := SecretObjectValues(map[string]any{"kind": "Secret", "data": []any{"x"}}); vs != nil {
		t.Errorf("a non-map data field yielded values: %+v", vs)
	}
}

// container is a container spec carrying every kind of env entry the viewer
// must tell apart.
func container(name string) map[string]any {
	return map[string]any{
		"name":  name,
		"image": testImageRef,
		"env": []any{
			map[string]any{"name": "LOG_LEVEL", "value": "info"},
			map[string]any{"name": "DB_PASSWORD", "value": "hunter2"},
			map[string]any{"name": "DATABASE_URL", "value": "postgres://app:s3cr3t@db:5432/shop"},
			map[string]any{"name": "UPSTREAM", "value": testToken},
			map[string]any{"name": "STRIPE_KEY", "valueFrom": map[string]any{
				"secretKeyRef": map[string]any{"name": "stripe", "key": "SECRET_KEY"},
			}},
		},
		"envFrom": []any{
			map[string]any{"secretRef": map[string]any{"name": "app-secrets"}},
			map[string]any{"configMapRef": map[string]any{"name": "app-config"}},
		},
		"args": []any{"serve", "--port=8080", "--db-password=hunter2", "--api-token", "abc", "--verbose", testPEM},
		"readinessProbe": map[string]any{"httpGet": map[string]any{
			"path":        "/healthz",
			"httpHeaders": []any{map[string]any{"name": "Authorization", "value": "Bearer abcdef0123456789"}},
		}},
	}
}

func podSpec() map[string]any {
	return map[string]any{
		"containers":     []any{container("api")},
		"initContainers": []any{container("migrate")},
		"volumes": []any{map[string]any{
			"name":   "tls",
			"secret": map[string]any{"secretName": "api-tls"},
		}},
	}
}

func workloads() map[string]map[string]any {
	template := func() map[string]any { return map[string]any{"spec": podSpec()} }
	return map[string]map[string]any{
		"Deployment": {
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "api"},
			"spec":     map[string]any{"replicas": 2, "template": template()},
		},
		"StatefulSet": {
			"apiVersion": "apps/v1", "kind": "StatefulSet",
			"metadata": map[string]any{"name": "api"},
			"spec":     map[string]any{"serviceName": "api", "template": template()},
		},
		"CronJob": {
			"apiVersion": "batch/v1", "kind": "CronJob",
			"metadata": map[string]any{"name": "nightly"},
			"spec": map[string]any{"schedule": "0 3 * * *", "jobTemplate": map[string]any{
				"spec": map[string]any{"template": template()},
			}},
		},
	}
}

func templateSpec(t *testing.T, kind string, doc any) map[string]any {
	t.Helper()
	spec := doc.(map[string]any)["spec"].(map[string]any)
	if kind == "CronJob" {
		spec = spec["jobTemplate"].(map[string]any)["spec"].(map[string]any)
	}
	return spec["template"].(map[string]any)["spec"].(map[string]any)
}

func envValue(t *testing.T, c map[string]any, name string) any {
	t.Helper()
	for _, e := range c["env"].([]any) {
		if m := e.(map[string]any); m["name"] == name {
			return m["value"]
		}
	}
	t.Fatalf("env %s missing", name)
	return nil
}

func TestRedactObject_WorkloadEnv(t *testing.T) {
	for kind, doc := range workloads() {
		t.Run(kind, func(t *testing.T) {
			before, _ := json.Marshal(doc)
			out := RedactObject(doc)
			if after, _ := json.Marshal(doc); string(after) != string(before) {
				t.Fatal("RedactObject mutated its input")
			}
			encoded, _ := json.Marshal(out)
			for _, leaked := range []string{"hunter2", "s3cr3t", testToken, "abcdef0123456789", "not-a-key: synthetic detector fixture"} {
				if strings.Contains(string(encoded), leaked) {
					t.Errorf("%q survived redaction", leaked)
				}
			}
			spec := templateSpec(t, kind, out)
			for _, list := range []string{"containers", "initContainers"} {
				c := spec[list].([]any)[0].(map[string]any)
				if got := envValue(t, c, "LOG_LEVEL"); got != "info" {
					t.Errorf("%s: a plain value was withheld: %v", list, got)
				}
				if got := envValue(t, c, "DB_PASSWORD"); got != RedactedWithheld {
					t.Errorf("%s: DB_PASSWORD = %v, want its digest", list, got)
				}
				if got := envValue(t, c, "DATABASE_URL"); got != RedactedWithheld {
					t.Errorf("%s: a DSN with a password was shown: %v", list, got)
				}
				if got := envValue(t, c, "UPSTREAM"); got != RedactedWithheld {
					t.Errorf("%s: a generated token was shown: %v", list, got)
				}
				if c["image"] != testImageRef {
					t.Errorf("%s: the image reference was withheld: %v", list, c["image"])
				}
				ref := c["env"].([]any)[4].(map[string]any)["valueFrom"].(map[string]any)["secretKeyRef"]
				if !reflect.DeepEqual(ref, map[string]any{"name": "stripe", "key": "SECRET_KEY"}) {
					t.Errorf("%s: a secretKeyRef did not stay a reference: %v", list, ref)
				}
				wantEnvFrom := container("x")["envFrom"]
				if !reflect.DeepEqual(c["envFrom"], wantEnvFrom) {
					t.Errorf("%s: envFrom refs changed: %v", list, c["envFrom"])
				}
				wantArgs := []any{"serve", "--port=8080", "--db-password=" + RedactedWithheld,
					"--api-token", RedactedWithheld, "--verbose", RedactedWithheld}
				if !reflect.DeepEqual(c["args"], wantArgs) {
					t.Errorf("%s: args = %v, want %v", list, c["args"], wantArgs)
				}
				header := c["readinessProbe"].(map[string]any)["httpGet"].(map[string]any)["httpHeaders"].([]any)[0].(map[string]any)
				if header["value"] != RedactedWithheld {
					t.Errorf("%s: a probe's Authorization header was shown: %v", list, header["value"])
				}
			}
			vol := spec["volumes"].([]any)[0].(map[string]any)["secret"].(map[string]any)
			if vol["secretName"] != "api-tls" {
				t.Errorf("a volume's secretName did not stay a reference: %v", vol)
			}
			if again := RedactObject(out); !reflect.DeepEqual(again, out) {
				t.Error("redacting twice differs from redacting once")
			}
		})
	}
}

func TestRedactObject_SecretKeepsKeyNamesOnly(t *testing.T) {
	doc := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata":   map[string]any{"name": "db", "labels": map[string]any{"app": "api"}},
		"type":       "Opaque",
		"data":       map[string]any{"PASSWORD": "aHVudGVyMg==", "ALREADY": RedactedSecretPrefix + "sha256:" + testHex64},
		"stringData": map[string]any{"USER": "app"},
	}
	out := RedactObject(doc).(map[string]any)
	want := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata":   map[string]any{"name": "db", "labels": map[string]any{"app": "api"}},
		"type":       "Opaque",
		"data":       map[string]any{"PASSWORD": RedactedWithheld, "ALREADY": RedactedWithheld},
		"stringData": map[string]any{"USER": RedactedWithheld},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("RedactObject(Secret) = %v, want %v", out, want)
	}
	if doc["data"].(map[string]any)["PASSWORD"] != "aHVudGVyMg==" {
		t.Fatal("RedactObject mutated the Secret it was given")
	}
}

func TestRedactObject_ConfigMap(t *testing.T) {
	doc := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "app-config"},
		"data": map[string]any{
			"LOG_LEVEL":    "info",
			"API_TOKEN":    "abc",
			"config.yaml":  "listen: :8080\n",
			"upstream.url": "https://user:pw@upstream.example.com",
		},
		"binaryData": map[string]any{"keystore.p12": "MIIKAgIBAzCCCcgGCSqGSIb3"},
	}
	out := RedactObject(doc).(map[string]any)
	data := out["data"].(map[string]any)
	if data["LOG_LEVEL"] != "info" || data["config.yaml"] != "listen: :8080\n" {
		t.Errorf("plain ConfigMap data was withheld: %v", data)
	}
	if data["API_TOKEN"] != RedactedWithheld {
		t.Errorf("a credential-named ConfigMap key was shown: %v", data["API_TOKEN"])
	}
	if data["upstream.url"] != RedactedWithheld {
		t.Errorf("a URL with userinfo was shown: %v", data["upstream.url"])
	}
	if got := out["binaryData"].(map[string]any)["keystore.p12"]; got != RedactedWithheld {
		t.Errorf("binaryData was shown: %v", got)
	}
}

// A forge Workload record carries its env as {name, value} pairs too; the rule
// is the pair, not the Kubernetes path.
func TestRedactObject_ForgeWorkloadAndExternalSecret(t *testing.T) {
	wl := map[string]any{
		"apiVersion": "forge.dev/v1alpha1", "kind": "Workload",
		"metadata": map[string]any{"name": "api"},
		"spec": map[string]any{"env": []any{
			map[string]any{"name": "SESSION_SECRET", "value": "s3cr3t"},
			map[string]any{"name": "PORT", "value": "8080"},
			map[string]any{"name": "DB", "managedSecret": map[string]any{"name": "db", "key": "PASSWORD"}},
		}},
	}
	env := RedactObject(wl).(map[string]any)["spec"].(map[string]any)["env"].([]any)
	if env[0].(map[string]any)["value"] != RedactedWithheld || env[1].(map[string]any)["value"] != "8080" {
		t.Errorf("Workload env = %v", env)
	}
	if !reflect.DeepEqual(env[2], wl["spec"].(map[string]any)["env"].([]any)[2]) {
		t.Errorf("a managed secret reference changed: %v", env[2])
	}

	es := map[string]any{
		"apiVersion": "external-secrets.io/v1", "kind": "ExternalSecret",
		"metadata": map[string]any{"name": "db"},
		"spec": map[string]any{
			"secretStoreRef": map[string]any{"name": "platform", "kind": "SecretStore"},
			"target":         map[string]any{"name": "db"},
			"data":           []any{map[string]any{"secretKey": "PASSWORD", "remoteRef": map[string]any{"key": "prod/db/PASSWORD"}}},
		},
	}
	if out := RedactObject(es); !reflect.DeepEqual(out, any(es)) {
		t.Errorf("an ExternalSecret (references only) changed:\n got %v\nwant %v", out, es)
	}
}
