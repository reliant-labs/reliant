package cutjournal

// The registry transport: the journal is pushed beside the images it names
// and read back from there, through the same ambient docker credentials the
// image pushes used.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// maxJournalBytes bounds what Fetch reads: a journal is a few KiB of state.
const maxJournalBytes = 8 << 20

// fixedCreated pins the manifest's created annotation, so the manifest digest
// is a pure function of the journal.
const fixedCreated = "1970-01-01T00:00:00Z"

// Target is the slice of a registry repository the journal is written to
// and read from. oras's remote.Repository satisfies it, and so does its
// in-memory store.
type Target interface {
	content.Pusher
	content.Fetcher
	content.Resolver
	content.Tagger
}

// Open is the repository at `<host>/<path>` (no tag), authenticated from the
// ambient docker credential store — what `docker push` to it would present.
func Open(repository string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("cut journal repository %q: %w", repository, err)
	}
	repo.PlainHTTP = plainHTTPRegistry(repo.Reference.Registry)
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("read docker credentials: %w", err)
	}
	repo.Client = &auth.Client{Client: retry.DefaultClient, Cache: auth.NewCache(), Credential: credentials.Credential(store)}
	return repo, nil
}

// plainHTTPRegistry reports the loopback registries a local k3d publishes,
// which are reached over plain HTTP.
func plainHTTPRegistry(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasPrefix(h, "127.") || h == "host.k3d.internal"
}

// Push writes j to t under tag and returns the manifest digest. The tag is
// set last, so it never names an incomplete artifact.
func Push(ctx context.Context, t Target, tag string, j Journal) (string, error) {
	layer, err := Encode(j)
	if err != nil {
		return "", err
	}
	layerDesc := content.NewDescriptorFromBytes(LayerMediaType, layer)
	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactType,
		Config:       ocispec.DescriptorEmptyJSON,
		Layers:       []ocispec.Descriptor{layerDesc},
		Annotations: map[string]string{
			ocispec.AnnotationCreated: fixedCreated,
			"dev.forge.cut.version":   j.Version,
			"dev.forge.cut.env":       j.Env,
			"dev.forge.cut.commit":    j.Commit,
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	manifestDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	manifestDesc.ArtifactType = ArtifactType
	for _, blob := range []struct {
		desc ocispec.Descriptor
		data []byte
	}{{layerDesc, layer}, {ocispec.DescriptorEmptyJSON, ocispec.DescriptorEmptyJSON.Data}, {manifestDesc, raw}} {
		if err := t.Push(ctx, blob.desc, bytes.NewReader(blob.data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
			return "", fmt.Errorf("push cut journal %s: %w", blob.desc.MediaType, err)
		}
	}
	if err := t.Tag(ctx, manifestDesc, tag); err != nil {
		return "", fmt.Errorf("tag cut journal %s: %w", tag, err)
	}
	return manifestDesc.Digest.String(), nil
}

// Fetch reads the journal tagged tag in t, verifying every byte against its
// descriptor. ErrNotFound when the tag does not exist.
func Fetch(ctx context.Context, t Target, tag string) (Journal, string, error) {
	desc, err := t.Resolve(ctx, tag)
	if err != nil {
		if errors.Is(err, errdef.ErrNotFound) {
			return Journal{}, "", ErrNotFound
		}
		return Journal{}, "", fmt.Errorf("resolve cut journal %s: %w", tag, err)
	}
	raw, err := readVerified(ctx, t, desc)
	if err != nil {
		return Journal{}, "", fmt.Errorf("read cut journal %s: %w", tag, err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Journal{}, "", fmt.Errorf("cut journal %s: manifest: %w", tag, err)
	}
	if manifest.ArtifactType != ArtifactType || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != LayerMediaType {
		return Journal{}, "", fmt.Errorf("cut journal %s: %s is not a cut journal (artifactType %q)", tag, desc.Digest, manifest.ArtifactType)
	}
	layer, err := readVerified(ctx, t, manifest.Layers[0])
	if err != nil {
		return Journal{}, "", fmt.Errorf("read cut journal %s: %w", tag, err)
	}
	j, err := Decode(layer)
	if err != nil {
		return Journal{}, "", err
	}
	return j, desc.Digest.String(), nil
}

func readVerified(ctx context.Context, t content.Fetcher, desc ocispec.Descriptor) ([]byte, error) {
	if desc.Size > maxJournalBytes {
		return nil, fmt.Errorf("%s is %d bytes, over the %d a cut journal may be", desc.Digest, desc.Size, maxJournalBytes)
	}
	rc, err := t.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	vr := content.NewVerifyReader(rc, desc)
	raw, err := io.ReadAll(vr)
	if err != nil {
		return nil, err
	}
	if err := vr.Verify(); err != nil {
		return nil, err
	}
	return raw, nil
}

// CheckImages asks the registry about each image the journal recorded, and
// lists every one that is not as recorded: a version tag that no longer
// names the recorded digest (or is gone), or a reused image whose digest is
// gone. open returns the repository an image lives in.
func CheckImages(ctx context.Context, j Journal, open func(repository string) (content.Resolver, error)) []string {
	var out []string
	for _, img := range j.Images {
		ref, sep := img.Tag, ":"
		if ref == "" {
			ref, sep = img.Digest, "@"
		}
		name := img.Repository + sep + ref
		repo, err := open(img.Repository)
		if err != nil {
			out = append(out, fmt.Sprintf("%s could not be read: %v", name, err))
			continue
		}
		desc, err := repo.Resolve(ctx, ref)
		switch {
		case errors.Is(err, errdef.ErrNotFound):
			out = append(out, fmt.Sprintf("%s no longer exists (the journal recorded %s)", name, img.Digest))
		case err != nil:
			out = append(out, fmt.Sprintf("%s could not be read: %v", name, err))
		case desc.Digest.String() != img.Digest:
			out = append(out, fmt.Sprintf("%s names %s, the journal recorded %s", name, desc.Digest, img.Digest))
		}
	}
	return out
}
