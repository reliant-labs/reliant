package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestCompositionArtifactCanonicalizesTagAndDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	prev := externalImageDigestResolver
	t.Cleanup(func() { externalImageDigestResolver = prev })
	externalImageDigestResolver = func(context.Context, string) (string, []string, error) { return digest, []string{"linux/amd64"}, nil }
	for _, ref := range []string{"docker.io/library/registry:3.1.2@" + digest, "localhost:5000/registry:3.1.2@" + digest} {
		artifact, err := resolveCompositionArtifact(t.Context(), ref, "v1", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if artifact.Location != imageRepository(ref) {
			t.Fatalf("repository kept a mutable tag: %+v", artifact)
		}
		if _, err := release.ArtifactIdentity(artifact); err != nil {
			t.Fatalf("valid tagged digest produced invalid artifact: %v", err)
		}
	}
	if _, err := resolveCompositionArtifact(t.Context(), "docker.io/library/registry:3.1.2@sha256:"+strings.Repeat("b", 64), "v1", nil, nil); err == nil {
		t.Fatal("registry descriptor mismatch accepted")
	}
}
