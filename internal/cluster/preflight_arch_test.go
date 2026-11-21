package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeImageArchChecker resolves an image's architecture(s) from an in-memory
// table. inconclusive images return ErrImageCheckInconclusive (the arch can't
// be read → WARN-don't-block). An image absent from both maps reports no
// architectures (treated as unknown → matches, never false-fails).
type fakeImageArchChecker struct {
	archs        map[string][]string
	inconclusive map[string]struct{}
}

func (f fakeImageArchChecker) ImageArchitectures(_ context.Context, ref string) ([]string, error) {
	if _, ok := f.inconclusive[ref]; ok {
		return nil, fmt.Errorf("%w: dial tcp: connection refused", ErrImageCheckInconclusive)
	}
	return f.archs[ref], nil
}

// archManifest is a Deployment whose container image is built for arm64 — the
// 2026-06-24 incident shape (an arm64 image headed for amd64 nodes).
const archManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: control-plane
  namespace: app-prod
spec:
  template:
    spec:
      containers:
        - name: control-plane
          image: ghcr.io/reliant/control-plane:v1.4.0
`

// TestPreflight_ArchMismatchBlocks: an arm64 image deploying to amd64 nodes
// BLOCKS with the actionable exec-format-error message.
func TestPreflight_ArchMismatchBlocks(t *testing.T) {
	opts := PreflightOpts{
		Manifests:  archManifest,
		Namespace:  "app-prod",
		Images:     fakeImageChecker{present: keySet("ghcr.io/reliant/control-plane:v1.4.0")},
		ImageArch:  fakeImageArchChecker{archs: map[string][]string{"ghcr.io/reliant/control-plane:v1.4.0": {"arm64"}}},
		TargetArch: "amd64",
	}
	err := Preflight(context.Background(), opts)
	if err == nil {
		t.Fatal("expected preflight to BLOCK on arch mismatch")
	}
	msg := err.Error()
	if !strings.Contains(msg, "WRONG architecture") {
		t.Errorf("error should have the wrong-arch block; got:\n%s", msg)
	}
	if !strings.Contains(msg, "arm64") || !strings.Contains(msg, "amd64") {
		t.Errorf("error should name both archs; got:\n%s", msg)
	}
	if !strings.Contains(msg, "exec-format-error") {
		t.Errorf("error should reference the exec-format-error class; got:\n%s", msg)
	}
}

// TestPreflight_ArchMatchPasses: a matching arch (incl. a multi-arch index
// that includes the target) does NOT block.
func TestPreflight_ArchMatchPasses(t *testing.T) {
	for name, archs := range map[string][]string{
		"single-amd64": {"amd64"},
		"multi-arch":   {"amd64", "arm64"},
	} {
		t.Run(name, func(t *testing.T) {
			opts := PreflightOpts{
				Manifests:  archManifest,
				Namespace:  "app-prod",
				Images:     fakeImageChecker{present: keySet("ghcr.io/reliant/control-plane:v1.4.0")},
				ImageArch:  fakeImageArchChecker{archs: map[string][]string{"ghcr.io/reliant/control-plane:v1.4.0": archs}},
				TargetArch: "amd64",
			}
			if err := Preflight(context.Background(), opts); err != nil {
				t.Fatalf("expected pass for archs %v, got: %v", archs, err)
			}
		})
	}
}

// TestPreflight_ArchGateInertWhenTargetUndeclared: with NO target arch (the
// env hasn't declared platform — incl. the local e2e path) the gate is inert
// even when the image is a "wrong" arch. WARN-don't-block.
func TestPreflight_ArchGateInertWhenTargetUndeclared(t *testing.T) {
	opts := PreflightOpts{
		Manifests: archManifest,
		Namespace: "app-prod",
		Images:    fakeImageChecker{present: keySet("ghcr.io/reliant/control-plane:v1.4.0")},
		ImageArch: fakeImageArchChecker{archs: map[string][]string{"ghcr.io/reliant/control-plane:v1.4.0": {"arm64"}}},
		// TargetArch empty → gate inert.
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("expected the arch gate to be inert with no target arch, got: %v", err)
	}
}

// TestPreflight_ArchGateWarnsWhenUnreadable: an inconclusive arch read (the
// arch is UNKNOWN) does NOT block — a mismatch can only be asserted on a known
// arch.
func TestPreflight_ArchGateWarnsWhenUnreadable(t *testing.T) {
	opts := PreflightOpts{
		Manifests:  archManifest,
		Namespace:  "app-prod",
		Images:     fakeImageChecker{present: keySet("ghcr.io/reliant/control-plane:v1.4.0")},
		ImageArch:  fakeImageArchChecker{inconclusive: keySet("ghcr.io/reliant/control-plane:v1.4.0")},
		TargetArch: "amd64",
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("expected an unreadable arch to WARN-don't-block, got: %v", err)
	}
}

// TestPreflight_ArchGateSkipsLocalRegistry: a local-registry image is skipped
// by SkipImageRef — the gate never inspects it (the e2e/local-registry path is
// a no-op).
func TestPreflight_ArchGateSkipsLocalRegistry(t *testing.T) {
	const localManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: control-plane
  namespace: dev
spec:
  template:
    spec:
      containers:
        - name: control-plane
          image: registry.localhost:5051/control-plane:dev
`
	opts := PreflightOpts{
		Manifests: localManifest,
		Namespace: "dev",
		Images:    fakeImageChecker{},
		// A "wrong" arch would block IF inspected — but SkipImageRef drops it.
		ImageArch:    fakeImageArchChecker{archs: map[string][]string{"registry.localhost:5051/control-plane:dev": {"arm64"}}},
		TargetArch:   "amd64",
		SkipImageRef: LocalImageRef,
	}
	if err := Preflight(context.Background(), opts); err != nil {
		t.Fatalf("expected the local-registry image to be skipped, got: %v", err)
	}
}

// TestParseManifestArchitectures covers both manifest shapes the live checker
// parses: a single-image manifest (.architecture) and a manifest list
// (.manifests[].platform.architecture), dropping "unknown" attestation rows.
func TestParseManifestArchitectures(t *testing.T) {
	single := []byte(`{"architecture":"amd64","os":"linux"}`)
	got, err := parseManifestArchitectures(single)
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	if len(got) != 1 || got[0] != "amd64" {
		t.Errorf("single arch = %v, want [amd64]", got)
	}

	index := []byte(`{"manifests":[
		{"platform":{"architecture":"amd64","os":"linux"}},
		{"platform":{"architecture":"arm64","os":"linux"}},
		{"platform":{"architecture":"unknown","os":"unknown"}}
	]}`)
	got, err = parseManifestArchitectures(index)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(got) != 2 || got[0] != "amd64" || got[1] != "arm64" {
		t.Errorf("index archs = %v, want [amd64 arm64] (unknown dropped)", got)
	}
}
