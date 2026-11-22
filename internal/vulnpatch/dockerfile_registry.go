package vulnpatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/reliant-labs/forge/internal/dockercred"
)

// Registry is what refreshing a base image asks of the image's registry.
// Repositories are addressed host/path (docker.io/library/alpine).
type Registry interface {
	// Digest is the manifest digest a tag names now: the index digest of
	// a multi-platform image, what `FROM image:tag@<digest>` pins.
	// ErrImageNotFound when the tag does not exist.
	Digest(ctx context.Context, repository, tag string) (string, error)
	// Tags lists the repository's tags.
	Tags(ctx context.Context, repository string) ([]string, error)
	// Platforms are the platforms (os/arch[/variant]) a tag or digest
	// names an image for: every one of a multi-platform index (index
	// true), or the one of a single image.
	Platforms(ctx context.Context, repository, reference string) (platforms []string, index bool, err error)
}

// ErrImageNotFound is a Registry's answer for a tag that does not exist.
var ErrImageNotFound = errors.New("image not found")

// LiveRegistry is the Registry over the OCI distribution API, with the
// credentials `docker pull` would present (the docker config and its
// credential helpers). It needs no docker daemon.
func LiveRegistry() Registry { return liveRegistry{} }

type liveRegistry struct{}

func (liveRegistry) open(repository string) (*remote.Repository, error) {
	host, path, _ := strings.Cut(repository, "/")
	if host == "docker.io" {
		host = "registry-1.docker.io" // Docker Hub's name is not its API host
	}
	repo, err := remote.NewRepository(host + "/" + path)
	if err != nil {
		return nil, err
	}
	repo.PlainHTTP = loopbackRegistry(host)
	credential, err := dockercred.Credential()
	if err != nil {
		return nil, fmt.Errorf("read docker credentials: %w", err)
	}
	repo.Client = &auth.Client{Client: retry.DefaultClient, Cache: auth.NewCache(), Credential: credential}
	return repo, nil
}

func (r liveRegistry) Digest(ctx context.Context, repository, tag string) (string, error) {
	repo, err := r.open(repository)
	if err != nil {
		return "", err
	}
	desc, err := repo.Resolve(ctx, tag)
	if errors.Is(err, errdef.ErrNotFound) {
		return "", fmt.Errorf("%w: %s:%s", ErrImageNotFound, repository, tag)
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s:%s: %w", repository, tag, err)
	}
	return desc.Digest.String(), nil
}

func (r liveRegistry) Tags(ctx context.Context, repository string) ([]string, error) {
	repo, err := r.open(repository)
	if err != nil {
		return nil, err
	}
	var out []string
	err = repo.Tags(ctx, "", func(tags []string) error {
		out = append(out, tags...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the tags of %s: %w", repository, err)
	}
	return out, nil
}

func (r liveRegistry) Platforms(ctx context.Context, repository, reference string) ([]string, bool, error) {
	repo, err := r.open(repository)
	if err != nil {
		return nil, false, err
	}
	desc, body, err := fetchAll(ctx, repo, reference)
	if err != nil {
		return nil, false, fmt.Errorf("read %s@%s: %w", repository, reference, err)
	}
	switch desc.MediaType {
	case ocispec.MediaTypeImageIndex, dockerManifestList:
		var idx ocispec.Index
		if err := json.Unmarshal(body, &idx); err != nil {
			return nil, true, fmt.Errorf("read the index %s@%s: %w", repository, reference, err)
		}
		var out []string
		for _, m := range idx.Manifests {
			// unknown/unknown are attestations, not images.
			if m.Platform != nil && m.Platform.OS != "unknown" {
				out = appendUnique(out, platformString(m.Platform.OS, m.Platform.Architecture, m.Platform.Variant))
			}
		}
		return out, true, nil
	}
	var man ocispec.Manifest
	if err := json.Unmarshal(body, &man); err != nil {
		return nil, false, fmt.Errorf("read the manifest %s@%s: %w", repository, reference, err)
	}
	rc, err := repo.Fetch(ctx, man.Config)
	if err != nil {
		return nil, false, fmt.Errorf("read the config of %s@%s: %w", repository, reference, err)
	}
	defer func() { _ = rc.Close() }()
	cfgBody, err := content.ReadAll(rc, man.Config)
	if err != nil {
		return nil, false, fmt.Errorf("read the config of %s@%s: %w", repository, reference, err)
	}
	var cfg ocispec.Image
	if err := json.Unmarshal(cfgBody, &cfg); err != nil {
		return nil, false, fmt.Errorf("read the config of %s@%s: %w", repository, reference, err)
	}
	return []string{platformString(cfg.OS, cfg.Architecture, cfg.Variant)}, false, nil
}

// dockerManifestList is Docker's media type for a multi-platform index.
const dockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"

func fetchAll(ctx context.Context, repo *remote.Repository, reference string) (ocispec.Descriptor, []byte, error) {
	desc, rc, err := repo.FetchReference(ctx, reference)
	if errors.Is(err, errdef.ErrNotFound) {
		return desc, nil, fmt.Errorf("%w: %s", ErrImageNotFound, reference)
	}
	if err != nil {
		return desc, nil, err
	}
	defer func() { _ = rc.Close() }()
	body, err := content.ReadAll(rc, desc)
	return desc, body, err
}

func platformString(os, arch, variant string) string {
	out := strings.ToLower(os + "/" + arch)
	if variant != "" {
		out += "/" + strings.ToLower(variant)
	}
	return out
}

// loopbackRegistry reports a registry on this machine, reached over plain
// HTTP (a local registry, k3d's, a test's).
func loopbackRegistry(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasPrefix(h, "127.") || h == "[::1]"
}
