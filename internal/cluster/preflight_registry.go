package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/errcode"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// RegistryImageChecker is the live ImageChecker. It asks the image's registry
// directly — a HEAD on the manifest over the OCI distribution API — and takes
// its verdict from the HTTP status:
//
//   - 200 → present.
//   - 404 → CONFIRMED absent (ErrImageNotFound, carrying the registry's
//     answer).
//   - 401 / 403 → auth-denied (ErrImageCheckAuthDenied): the registry was
//     reached and would not say. On a private registry a missing image looks
//     exactly like this, so the preflight does not fail open on it.
//   - 429, 5xx, a network failure → inconclusive (ErrImageCheckInconclusive):
//     a statement about the registry, not the image.
//
// WHY NOT `docker manifest inspect`. Its verdict had to be recovered from its
// prose by substring: any output containing "not found" was a confirmed miss.
// On 2026-10-07 Docker Hub was answering 500s, and a lookup whose text
// contained "not found" blocked a prod release for an image that existed and
// that prod was running. A status code cannot be misread that way, and the
// lookup needs no docker daemon.
//
// Credentials come from the docker config (credential helpers included) —
// the ambient one, or DockerConfigDir's when the preflight retries with the
// CLUSTER's pull credentials.
type RegistryImageChecker struct {
	// DockerConfigDir, when set, reads credentials from <dir>/config.json
	// instead of the ambient docker config.
	DockerConfigDir string
}

// registryHTTPClient is the HTTP client under the auth layer: oras's retrying
// client, which already backs off on 429 and 5xx before answering. A package
// seam rather than a field, because production has exactly one value for it;
// a test swaps it for a client that does not spend seconds backing off from
// the failures it serves on purpose.
var registryHTTPClient = retry.DefaultClient

// WithDockerConfigDir returns a copy that authenticates with the credentials
// in dir/config.json — the cluster's pull creds. Implements
// CredentialedImageChecker.
func (c RegistryImageChecker) WithDockerConfigDir(dir string) ImageChecker {
	c.DockerConfigDir = dir
	return c
}

// ImageExists asks ref's registry whether its manifest exists.
func (c RegistryImageChecker) ImageExists(ctx context.Context, ref string) (bool, error) {
	reg, repository, tag, digest := splitImageRef(ref)
	host := registryHost(reg)
	reference := digest
	if reference == "" {
		reference = tag
	}
	if reference == "" {
		reference = "latest"
	}
	repo, err := remote.NewRepository(host + "/" + repository)
	if err != nil {
		// A ref the registry API cannot even address. The apply would fail
		// on it too, but that is the apply's report to make; here it is
		// simply not verifiable.
		return false, fmt.Errorf("%w: parse %q: %v", ErrImageCheckInconclusive, ref, err)
	}
	repo.PlainHTTP = registryRefIsInsecure(ref)
	store, err := c.credentialStore()
	if err != nil {
		return false, fmt.Errorf("%w: read docker credentials: %v", ErrImageCheckInconclusive, err)
	}
	repo.Client = &auth.Client{Client: registryHTTPClient, Cache: auth.NewCache(), Credential: credentials.Credential(store)}

	_, err = repo.Resolve(ctx, reference)
	return registryVerdict(host, err)
}

func (c RegistryImageChecker) credentialStore() (credentials.Store, error) {
	if c.DockerConfigDir != "" {
		return credentials.NewStore(filepath.Join(c.DockerConfigDir, "config.json"), credentials.StoreOptions{})
	}
	return credentials.NewStoreFromDocker(credentials.StoreOptions{})
}

// registryHost is the host that serves a registry's API. Docker Hub's name
// (docker.io) is not the API host.
func registryHost(registry string) string {
	if registry == "docker.io" {
		return "registry-1.docker.io"
	}
	return registry
}

// registryVerdict maps a manifest HEAD's outcome to the ImageChecker contract.
func registryVerdict(host string, err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errdef.ErrNotFound) {
		return false, fmt.Errorf("%w: %s answered HTTP 404 for the manifest", ErrImageNotFound, host)
	}
	var resp *errcode.ErrorResponse
	if errors.As(err, &resp) {
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return false, fmt.Errorf("%w: %s answered %v", ErrImageNotFound, host, resp)
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return false, fmt.Errorf("%w: %s answered %v", ErrImageCheckAuthDenied, host, resp)
		default:
			return false, fmt.Errorf("%w: %s answered %v", ErrImageCheckInconclusive, host, resp)
		}
	}
	return false, fmt.Errorf("%w: %v", ErrImageCheckInconclusive, err)
}
