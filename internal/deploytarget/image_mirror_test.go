package deploytarget

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

// A mirror copies the upstream index verbatim — so the digest the release pins
// IS the upstream digest — tags it by that digest, and reports the platforms
// the index declares (attestation entries excluded).
func TestMirrorImageCopiesByDigestAndTagsByDigest(t *testing.T) {
	ctx := context.Background()
	src := memory.New()
	push := func(mediaType string, body []byte) ocispec.Descriptor {
		t.Helper()
		desc := content.NewDescriptorFromBytes(mediaType, body)
		if err := src.Push(ctx, desc, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		return desc
	}
	cfg := push(ocispec.MediaTypeImageConfig, []byte(`{"architecture":"amd64","os":"linux"}`))
	layer := push(ocispec.MediaTypeImageLayer, []byte("layer"))
	manifest, _ := json.Marshal(ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest, Config: cfg, Layers: []ocispec.Descriptor{layer}})
	mdesc := push(ocispec.MediaTypeImageManifest, manifest)
	amd64, attest := mdesc, mdesc
	amd64.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}
	attest.Platform = &ocispec.Platform{OS: "unknown", Architecture: "unknown"}
	index, _ := json.Marshal(ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{amd64, attest}})
	idesc := push(ocispec.MediaTypeImageIndex, index)
	if err := src.Tag(ctx, idesc, "v1.10.3"); err != nil {
		t.Fatal(err)
	}

	dst := memory.New()
	got, err := mirrorImage(ctx, src, "v1.10.3", dst)
	if err != nil {
		t.Fatalf("mirrorImage: %v", err)
	}
	if got.Digest != idesc.Digest.String() {
		t.Errorf("mirrored digest = %s, want the upstream index digest %s", got.Digest, idesc.Digest)
	}
	if !slices.Equal(got.Platforms, []string{"linux/amd64"}) {
		t.Errorf("platforms = %v, want [linux/amd64]", got.Platforms)
	}
	tagged, err := dst.Resolve(ctx, MirrorTag(got.Digest))
	if err != nil {
		t.Fatalf("the destination does not hold the digest tag %s: %v", MirrorTag(got.Digest), err)
	}
	if tagged.Digest != idesc.Digest {
		t.Errorf("digest tag points at %s, want %s", tagged.Digest, idesc.Digest)
	}
	if ok, err := dst.Exists(ctx, layer); err != nil || !ok {
		t.Errorf("the layer was not copied (exists=%v, err=%v)", ok, err)
	}
}

func TestSplitMirrorSource(t *testing.T) {
	for _, tc := range []struct{ in, repo, ref string }{
		{"docker.io/traefik/whoami:v1.10.3", "docker.io/traefik/whoami", "v1.10.3"},
		{"docker.io/traefik/whoami", "docker.io/traefik/whoami", "latest"},
		{"ghcr.io/acme/api@sha256:abc", "ghcr.io/acme/api", "sha256:abc"},
		{"ghcr.io/acme/api:v1@sha256:abc", "ghcr.io/acme/api", "sha256:abc"},
		{"localhost:5000/api", "localhost:5000/api", "latest"},
	} {
		repo, ref := splitMirrorSource(tc.in)
		if repo != tc.repo || ref != tc.ref {
			t.Errorf("splitMirrorSource(%q) = %q, %q; want %q, %q", tc.in, repo, ref, tc.repo, tc.ref)
		}
	}
	if got := MirrorTag("sha256:abc"); got != "sha256-abc" {
		t.Errorf("MirrorTag = %q, want sha256-abc", got)
	}
}
