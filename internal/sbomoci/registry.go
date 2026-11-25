package sbomoci

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/reliant-labs/forge/internal/dockercred"
)

// RegistryOptions says how to reach a registry. The zero value is forge's
// default for every in-process registry client: the ambient docker
// credential store, read the way the docker CLI reads it (dockercred) — the
// same credential `docker push` presented for the image this SBOM describes,
// and the one the static-site push uses — over HTTPS, except plain HTTP for
// the loopback registries a local cluster publishes.
type RegistryOptions struct {
	// Credential overrides the docker credential store, e.g. with the
	// control-plane identity a bundle push presents to the platform registry.
	Credential auth.CredentialFunc
}

// registryHTTPClient is the transport under every registry client: oras-go's
// retrying default. A var so the package's tests can swap in one that fails
// a refused request at once instead of backing off.
var registryHTTPClient = retry.DefaultClient

// openRepository opens repository with SkipReferrersGC set: see the package
// doc for why every attach must never delete the previous fallback index.
func openRepository(repository string, opts RegistryOptions) (*remote.Repository, error) {
	repo, err := remote.NewRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("repository %q: %w", repository, err)
	}
	repo.PlainHTTP = loopbackRegistry(repo.Reference.Registry)
	repo.SkipReferrersGC = true
	credential := opts.Credential
	if credential == nil {
		credential, err = dockercred.Credential()
		if err != nil {
			return nil, fmt.Errorf("read docker credentials: %w", err)
		}
	}
	repo.Client = &auth.Client{Client: registryHTTPClient, Cache: auth.NewCache(), Credential: credential}
	return repo, nil
}

// loopbackRegistry is a registry a local cluster publishes on this machine,
// which speaks plain HTTP. The same rule as deploytarget's site push, so the
// SBOM goes over the transport its image went over.
func loopbackRegistry(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasPrefix(h, "127.") || h == "host.k3d.internal"
}

// parseSubject parses `<repository>@sha256:<hex>`, refusing a tag.
func parseSubject(subject string) (registry.Reference, digest.Digest, error) {
	ref, err := registry.ParseReference(strings.TrimSpace(subject))
	if err != nil {
		return registry.Reference{}, "", fmt.Errorf("subject %q: %w", subject, err)
	}
	if ref.Reference == "" {
		return registry.Reference{}, "", fmt.Errorf("subject %q names no digest: want <repository>@sha256:<hex>", subject)
	}
	d, err := ref.Digest()
	if err != nil {
		return registry.Reference{}, "", fmt.Errorf("subject %q is not by digest (want <repository>@sha256:<hex>): %w", subject, err)
	}
	if d.Algorithm() != digest.SHA256 {
		return registry.Reference{}, "", fmt.Errorf("subject %q: digest algorithm %s, want sha256", subject, d.Algorithm())
	}
	return ref, d, nil
}

// resolveSubject is the subject's full manifest descriptor (media type and
// size are part of the `subject` field), and proof it exists in the registry:
// a referrer to a manifest the registry does not hold would dangle.
func resolveSubject(ctx context.Context, repo *remote.Repository, d digest.Digest) (ocispec.Descriptor, error) {
	desc, err := repo.Resolve(ctx, d.String())
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("resolve subject %s@%s: %w", repo.Reference.Registry+"/"+repo.Reference.Repository, d, err)
	}
	if desc.Digest != d {
		return ocispec.Descriptor{}, fmt.Errorf("registry resolved subject %s to %s", d, desc.Digest)
	}
	return desc, nil
}

// keyedMutex serializes work per key, holding an entry only while someone
// holds or waits for it.
type keyedMutex struct {
	mu      sync.Mutex
	entries map[string]*keyedEntry
}

type keyedEntry struct {
	mu      sync.Mutex
	holders int
}

// subjectLocks serializes attaches to one subject within this process: the
// tag-schema fallback index is an unconditional read-modify-write.
var subjectLocks = &keyedMutex{entries: map[string]*keyedEntry{}}

func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	e := k.entries[key]
	if e == nil {
		e = &keyedEntry{}
		k.entries[key] = e
	}
	e.holders++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.holders--
		if e.holders == 0 {
			delete(k.entries, key)
		}
		k.mu.Unlock()
	}
}
