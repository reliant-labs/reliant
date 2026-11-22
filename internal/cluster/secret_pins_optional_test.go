package cluster

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updatePinGoldens = flag.Bool("update-pins", false, "rewrite testdata/secret_pins/*.golden.yaml from the current stamp")

// optionalPinStream is a pinned ExternalSecret and the shapes that read the
// Secret it writes optionally — an env var, envFrom, a volume, a projected
// source, on a Deployment, a StatefulSet and a CronJob — plus an optional
// reader of a Secret no ExternalSecret in the stream writes, and a required
// reader for comparison.
const optionalPinStream = `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: vault
  namespace: app
spec:
  target:
    name: vault
  data:
    - secretKey: vault_key
      remoteRef:
        key: orgs/acme/prod/VAULT_KEY
        property: value
        version: "4"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: optional-env
  namespace: app
spec:
  template:
    spec:
      containers:
        - name: c
          image: reg/c@sha256:1
          env:
            - name: VAULT_KEY
              valueFrom:
                secretKeyRef:
                  name: vault
                  key: vault_key
                  optional: true
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: optional-mounts
  namespace: app
spec:
  template:
    spec:
      containers:
        - name: c
          image: reg/c@sha256:1
          envFrom:
            - secretRef:
                name: vault
                optional: true
      volumes:
        - name: v
          secret:
            secretName: vault
            optional: true
        - name: p
          projected:
            sources:
              - secret:
                  name: vault
                  optional: true
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: optional-cron
  namespace: app
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: c
              image: reg/c@sha256:1
              env:
                - name: VAULT_KEY
                  valueFrom:
                    secretKeyRef:
                      name: vault
                      key: vault_key
                      optional: true
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: optional-absent
  namespace: app
spec:
  template:
    spec:
      containers:
        - name: c
          image: reg/c@sha256:1
          env:
            - name: BREAK_GLASS
              valueFrom:
                secretKeyRef:
                  name: break-glass
                  key: token
                  optional: true
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: required-env
  namespace: app
spec:
  template:
    spec:
      containers:
        - name: c
          image: reg/c@sha256:1
          env:
            - name: VAULT_KEY
              valueFrom:
                secretKeyRef:
                  name: vault
                  key: vault_key
`

// TestStampSecretPins_OptionalReaderIsPinned: an optional reference to a
// Secret a pinned ExternalSecret writes is pinned exactly like a required one
// — in every reference shape and on every pinned kind — so rotating an
// optional credential rolls the pods that read it. An optional reference to a
// Secret nothing in the stream writes contributes nothing.
func TestStampSecretPins_OptionalReaderIsPinned(t *testing.T) {
	items := stampSecretPins(yamlDocs(t, optionalPinStream))
	const want = "vault/VAULT_KEY=4"
	for i, path := range map[int][]string{
		1: {"spec", "template"},
		2: {"spec", "template"},
		3: {"spec", "jobTemplate", "spec", "template"},
		5: {"spec", "template"},
	} {
		if got := annotationOf(t, items[i], path...); got != want {
			t.Errorf("doc %d carries %q, want %q", i, got, want)
		}
	}
	if got := annotationOf(t, items[4], "spec", "template"); got != "" {
		t.Errorf("an optional reader of a Secret no ExternalSecret writes must not be stamped, got %q", got)
	}
}

// TestStampSecretPins_OptionalBumpRollsThePod is the rotation itself: moving
// the pin moves the optional reader's pod template.
func TestStampSecretPins_OptionalBumpRollsThePod(t *testing.T) {
	v4 := annotationOf(t, stampSecretPins(yamlDocs(t, optionalPinStream))[1], "spec", "template")
	bumped := bytes.Replace([]byte(optionalPinStream), []byte(`version: "4"`), []byte(`version: "5"`), 1)
	v5 := annotationOf(t, stampSecretPins(yamlDocs(t, string(bumped)))[1], "spec", "template")
	if v4 == v5 || v5 != "vault/VAULT_KEY=5" {
		t.Fatalf("pin 4 -> 5 must change the optional reader's pod template: %q -> %q", v4, v5)
	}
}

// TestStampSecretPins_OptionalGolden pins the whole stamped stream, through
// the one extraction hook render, shape, bundle and apply share, so any change
// to what an optional reader carries is a reviewed diff.
//
// Regenerate with: go test ./internal/cluster -run OptionalGolden -update-pins
func TestStampSecretPins_OptionalGolden(t *testing.T) {
	doc, err := json.Marshal(map[string]any{"output": map[string]any{"manifests": yamlDocs(t, optionalPinStream)}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := ExtractManifests(doc)
	if err != nil {
		t.Fatalf("ExtractManifests: %v", err)
	}
	golden := filepath.Join("testdata", "secret_pins", "optional.golden.yaml")
	if *updatePinGoldens {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(stream), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update-pins): %v", err)
	}
	if stream != string(want) {
		t.Errorf("stamped stream differs from %s (regenerate with -update-pins and review the diff):\n%s", golden, stream)
	}
}
