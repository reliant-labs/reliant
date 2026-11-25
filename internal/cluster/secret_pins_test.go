package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/release"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func yamlDocs(t *testing.T, stream string) []any {
	t.Helper()
	var items []any
	for _, d := range splitDocs(stream) {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err != nil {
			t.Fatal(err)
		}
		items = append(items, m)
	}
	return items
}

func annotationOf(t *testing.T, doc any, path ...string) string {
	t.Helper()
	cur := doc.(map[string]any)
	for _, p := range path {
		next, _ := cur[p].(map[string]any)
		if next == nil {
			return ""
		}
		cur = next
	}
	md, _ := cur["metadata"].(map[string]any)
	ann, _ := md["annotations"].(map[string]any)
	v, _ := ann[release.AnnotationSecretPins].(string)
	return v
}

// pinStream is an ExternalSecret pinned to version `version`, a Deployment that
// mounts the Secret it writes, one that does not, and a CronJob that reads it
// through an env var.
func pinStream(version string) string {
	return `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: db-role
  namespace: app
spec:
  target:
    name: db-role
  data:
    - secretKey: password
      remoteRef:
        key: secrets/proj/prod/RELIANT_DB_ROLE_PASSWORD
        property: value
        version: "` + version + `"
    - secretKey: floating
      remoteRef:
        key: secrets/proj/prod/UNPINNED
        property: value
---
apiVersion: apps/v1
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
          envFrom:
            - secretRef:
                name: db-role
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: unrelated
  namespace: app
spec:
  template:
    spec:
      containers:
        - name: u
          image: reg/u@sha256:1
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly
  namespace: app
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: n
              image: reg/n@sha256:1
              env:
                - name: PW
                  valueFrom:
                    secretKeyRef:
                      name: db-role
                      key: password
---
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate-abc
  namespace: app
spec:
  template:
    spec:
      containers:
        - name: m
          image: reg/m@sha256:1
          envFrom:
            - secretRef:
                name: db-role
`
}

// TestStampSecretPins: a consumer's pod template carries the pinned version, so
// moving the pin moves the template and the controller rolls it.
func TestStampSecretPins(t *testing.T) {
	items := stampSecretPins(yamlDocs(t, pinStream("1")))
	const want = "db-role/RELIANT_DB_ROLE_PASSWORD=1"
	if got := annotationOf(t, items[1], "spec", "template"); got != want {
		t.Errorf("the Deployment that mounts the Secret carries %q, want %q", got, want)
	}
	if got := annotationOf(t, items[2], "spec", "template"); got != "" {
		t.Errorf("a Deployment that does not read the Secret must not be stamped, got %q", got)
	}
	if got := annotationOf(t, items[3], "spec", "jobTemplate", "spec", "template"); got != want {
		t.Errorf("the CronJob that reads it through an env var carries %q, want %q", got, want)
	}
	// A one-shot Job's name is a hash of its spec: stamping it would re-run a
	// migration for a rotation. It reads the current value because the gate
	// holds it until the ExternalSecret has synced.
	if got := annotationOf(t, items[4], "spec", "template"); got != "" {
		t.Errorf("a one-shot Job must not be stamped (it would be renamed and re-run), got %q", got)
	}
	if strings.Contains(want, "UNPINNED") {
		t.Fatal("test bug")
	}
}

// TestStampSecretPins_BumpChangesTheTemplate is the rotation the whole feature
// serves: a pin bump from 1 to 2 changes exactly the consumer's pod template.
func TestStampSecretPins_BumpChangesTheTemplate(t *testing.T) {
	v1 := annotationOf(t, stampSecretPins(yamlDocs(t, pinStream("1")))[1], "spec", "template")
	v2 := annotationOf(t, stampSecretPins(yamlDocs(t, pinStream("2")))[1], "spec", "template")
	if v1 == v2 || v2 != "db-role/RELIANT_DB_ROLE_PASSWORD=2" {
		t.Fatalf("pin 1 -> 2 must change the pod template annotation: %q -> %q", v1, v2)
	}
	// Deterministic: a render is a pure function of its inputs.
	again := annotationOf(t, stampSecretPins(yamlDocs(t, pinStream("2")))[1], "spec", "template")
	if again != v2 {
		t.Fatalf("stamping is not deterministic: %q vs %q", again, v2)
	}
}

// TestStampSecretPins_NothingPinnedIsInert: a floating read has no version to
// move, and a stream with no ExternalSecret is returned untouched.
func TestStampSecretPins_NothingPinnedIsInert(t *testing.T) {
	floating := strings.Replace(pinStream("1"), `        version: "1"`+"\n", "", 1)
	for _, doc := range stampSecretPins(yamlDocs(t, floating)) {
		if annotationOf(t, doc, "spec", "template") != "" || annotationOf(t, doc, "spec", "jobTemplate", "spec", "template") != "" {
			t.Fatalf("a floating ExternalSecret must stamp nothing: %v", doc)
		}
	}
	plain := yamlDocs(t, gateStream)
	before, _ := yaml.Marshal(plain)
	after, _ := yaml.Marshal(stampSecretPins(plain))
	if string(before) != string(after) {
		t.Fatal("a stream with no ExternalSecret must be returned byte-for-byte")
	}
}

// TestStampSecretPins_ReachesTheExtractedStream pins the wiring: the annotation
// is in what ExtractManifests returns, the one stream render, shape, bundle and
// apply all read.
func TestStampSecretPins_ReachesTheExtractedStream(t *testing.T) {
	var docs []any
	docs = append(docs, yamlDocs(t, pinStream("3"))...)
	doc, err := json.Marshal(map[string]any{"output": map[string]any{"manifests": docs}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := ExtractManifests(doc)
	if err != nil {
		t.Fatalf("ExtractManifests: %v", err)
	}
	if !strings.Contains(stream, "forge.dev/secret-pins: db-role/RELIANT_DB_ROLE_PASSWORD=3") {
		t.Fatalf("the extracted stream must carry the pin annotation on the consumer, got:\n%s", stream)
	}
}
