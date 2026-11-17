package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestBuildState_DigestRoundTrip proves Step 1: a BuildState carrying a
// captured Digest + Platforms persists and reads back intact, so a build push
// that records the digest hands it to a subsequent deploy.
func TestBuildState_DigestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := BuildState{
		Image:     "control-plane",
		Tag:       "v1.4.0",
		Pushed:    true,
		PushedAt:  nowRFC3339(),
		Digest:    "sha256:" + strings.Repeat("c", 64),
		Platforms: []string{"linux/amd64"},
	}
	if err := WriteBuildState(dir, "prod", want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadBuildState(dir, "prod")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil {
		t.Fatal("read returned nil")
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("round-trip mismatch:\n  want %+v\n  got  %+v", want, *got)
	}
}

// stubImagetools replaces the registry-read seam for one test, restoring it on
// cleanup. The fake dispatches on the --format string: the digest format
// ({{.Manifest.Digest}}) returns digest, the platform-range format returns
// platformsOut. A non-nil queryErr makes EVERY registry read fail — modeling a
// ref that isn't in the registry, or a missing/broken buildx.
func stubImagetools(t *testing.T, digest, platformsOut string, queryErr error) {
	t.Helper()
	prev := imagetoolsInspect
	t.Cleanup(func() { imagetoolsInspect = prev })
	imagetoolsInspect = func(_ context.Context, _ string, format string) ([]byte, error) {
		if queryErr != nil {
			return nil, queryErr
		}
		if strings.Contains(format, "Manifest.Manifests") {
			return []byte(platformsOut), nil
		}
		return []byte(digest), nil
	}
}

// stubBuildxAvailable forces the buildx-availability probe for one test.
func stubBuildxAvailable(t *testing.T, available bool) {
	t.Helper()
	prev := buildxAvailable
	t.Cleanup(func() { buildxAvailable = prev })
	buildxAvailable = func(context.Context) bool { return available }
}

// TestImageRepoDigest_RegistryAuthoritative proves the happy path: the digest
// (and registry-advertised platforms) come straight from the registry read,
// with no local-daemon involvement.
func TestImageRepoDigest_RegistryAuthoritative(t *testing.T) {
	want := "sha256:" + strings.Repeat("a", 64)
	stubImagetools(t, want+"\n", "linux/amd64\nlinux/arm64\n", nil)
	stubBuildxAvailable(t, true)

	dig, plats, err := imageRepoDigest(context.Background(), "ghcr.io/reliant/app:v1")
	if err != nil {
		t.Fatalf("imageRepoDigest: %v", err)
	}
	if dig != want {
		t.Errorf("digest = %q, want registry digest %q", dig, want)
	}
	if !reflect.DeepEqual(plats, []string{"linux/amd64", "linux/arm64"}) {
		t.Errorf("platforms = %v, want [linux/amd64 linux/arm64]", plats)
	}
}

// TestImageRepoDigest_NoStaleLocalFallback is the regression guard for the
// imagetools-unavailable-fallback friction. When the registry query FAILS
// (here: ref not in registry) the function must NOT silently substitute a
// stale local-daemon digest. The old code fell back to `docker inspect`'s
// RepoDigests and could return a digest from a prior local pull; the fix
// removed that path entirely. Correct behavior is ("", nil, err) so the caller
// records no digest and deploy falls back to the mutable tag.
func TestImageRepoDigest_NoStaleLocalFallback(t *testing.T) {
	stubImagetools(t, "", "", errors.New("ERROR: ghcr.io/reliant/app:v1: not found"))
	stubBuildxAvailable(t, true) // buildx IS installed; the REF isn't in the registry.

	dig, plats, err := imageRepoDigest(context.Background(), "ghcr.io/reliant/app:v1")
	if err == nil {
		t.Fatalf("expected error when registry query fails, got digest %q", dig)
	}
	if dig != "" || plats != nil {
		t.Fatalf("on registry-query failure want empty digest/platforms (no stale local read), got %q / %v", dig, plats)
	}
}

// TestImageRepoDigest_BuildxUnavailableActionable proves that when buildx is
// not installed, the error names the missing tool (operator-fixable) instead of
// masking the gap with a local-cache read.
func TestImageRepoDigest_BuildxUnavailableActionable(t *testing.T) {
	stubImagetools(t, "", "", errors.New("docker: 'buildx' is not a docker command"))
	stubBuildxAvailable(t, false)

	_, _, err := imageRepoDigest(context.Background(), "ghcr.io/reliant/app:v1")
	if err == nil {
		t.Fatal("expected error when buildx unavailable")
	}
	if !strings.Contains(err.Error(), "buildx") {
		t.Errorf("error %q should name buildx so the operator can install it", err)
	}
}

// TestImageRepoDigest_NonDigestOutputRejected guards against a registry read
// that succeeds but returns something that isn't a sha256 digest (an empty or
// garbled manifest field). The function must reject it rather than record a
// bogus value as the pinned digest.
func TestImageRepoDigest_NonDigestOutputRejected(t *testing.T) {
	stubImagetools(t, "not-a-digest\n", "linux/amd64\n", nil)
	stubBuildxAvailable(t, true)

	dig, _, err := imageRepoDigest(context.Background(), "ghcr.io/reliant/app:v1")
	if err == nil {
		t.Fatalf("expected error for non-sha256 output, got digest %q", dig)
	}
	if dig != "" {
		t.Errorf("digest = %q on error, want empty", dig)
	}
}
