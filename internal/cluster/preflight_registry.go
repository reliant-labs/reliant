package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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

// RegistryImageArchChecker is the live ImageArchChecker. Like
// RegistryImageChecker it asks the image's registry directly over the OCI
// distribution API, and it reads every manifest shape a registry serves:
//
//   - an OCI index or a Docker manifest list: `.manifests[].platform`, one
//     entry per platform. "unknown" attestation entries (buildx provenance and
//     SBOMs) are dropped — they are not runnable.
//   - a single image manifest, OCI or Docker schema 2: the architecture is not
//     in the manifest at all but in its CONFIG blob, which is fetched and read.
//
// WHY NOT `docker manifest inspect`. Its client rejected a plain Docker schema
// 2 manifest — the format of an ordinary single-arch image, our own GAR images
// included — as "unsupported manifest format", and its error dumped the
// manifest's bytes as a Go struct, so the arch gate was skipped for exactly the
// images it exists to check (forge report 939b541b). It also needed a docker
// daemon for a question the registry answers.
//
// Anything the registry does not answer conclusively is
// ErrImageCheckInconclusive, so the gate warns rather than blocks: a mismatch
// is only ever asserted on a known architecture.
type RegistryImageArchChecker struct {
	// DockerConfigDir, when set, reads credentials from <dir>/config.json
	// instead of the ambient docker config.
	DockerConfigDir string
}

// WithDockerConfigDir returns a copy that authenticates with the credentials
// in dir/config.json. Implements CredentialedImageArchChecker.
func (c RegistryImageArchChecker) WithDockerConfigDir(dir string) ImageArchChecker {
	c.DockerConfigDir = dir
	return c
}

// Manifest media types the arch gate reads.
const (
	mediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaTypeOCIManifest        = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIIndex           = "application/vnd.oci.image.index.v1+json"
)

// maxManifestBytes bounds what is read of a manifest or a config blob: both
// are small JSON documents, and an unbounded read of a hostile answer is not.
const maxManifestBytes = 4 << 20

// ImageArchitectures returns the distinct architectures ref advertises.
func (c RegistryImageArchChecker) ImageArchitectures(ctx context.Context, ref string) ([]string, error) {
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
		return nil, fmt.Errorf("%w: parse %q: %v", ErrImageCheckInconclusive, ref, err)
	}
	repo.PlainHTTP = registryRefIsInsecure(ref)
	store, err := RegistryImageChecker(c).credentialStore()
	if err != nil {
		return nil, fmt.Errorf("%w: read docker credentials: %v", ErrImageCheckInconclusive, err)
	}
	repo.Client = &auth.Client{Client: registryHTTPClient, Cache: auth.NewCache(), Credential: credentials.Credential(store)}

	desc, rc, err := repo.FetchReference(ctx, reference)
	if err != nil {
		_, verr := registryVerdict(host, err)
		return nil, archInconclusive(verr)
	}
	body, err := readBounded(rc)
	if err != nil {
		return nil, fmt.Errorf("%w: read the manifest of %q: %v", ErrImageCheckInconclusive, ref, err)
	}
	return archsFromManifest(desc.MediaType, body, func(config ocispec.Descriptor) ([]byte, error) {
		rc, err := repo.Blobs().Fetch(ctx, config)
		if err != nil {
			_, verr := registryVerdict(host, err)
			return nil, archInconclusive(verr)
		}
		return readBounded(rc)
	})
}

// archsFromManifest reads the architectures out of one manifest document,
// fetching its config blob when the manifest is a single image's. mediaType
// is the registry's Content-Type; a registry that omits it is answered from
// the document's own mediaType field.
func archsFromManifest(mediaType string, body []byte, fetchConfig func(ocispec.Descriptor) ([]byte, error)) ([]string, error) {
	var doc struct {
		MediaType string             `json:"mediaType"`
		Config    ocispec.Descriptor `json:"config"`
		Manifests []json.RawMessage  `json:"manifests"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: the manifest (%s) is not JSON: %v", ErrImageCheckInconclusive, emptyMediaType(mediaType), err)
	}
	if mediaType == "" || mediaType == "application/json" {
		mediaType = doc.MediaType
	}
	switch {
	case mediaType == mediaTypeOCIIndex || mediaType == mediaTypeDockerManifestList || (mediaType == "" && len(doc.Manifests) > 0):
		return parseManifestArchitectures(body)
	case mediaType == mediaTypeOCIManifest || mediaType == mediaTypeDockerManifest || (mediaType == "" && doc.Config.Digest != ""):
		if doc.Config.Digest == "" {
			return nil, fmt.Errorf("%w: the image manifest (%s) names no config blob", ErrImageCheckInconclusive, emptyMediaType(mediaType))
		}
		raw, err := fetchConfig(doc.Config)
		if err != nil {
			return nil, err
		}
		var config struct {
			Architecture string `json:"architecture"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			return nil, fmt.Errorf("%w: the image config %s is not JSON: %v", ErrImageCheckInconclusive, doc.Config.Digest, err)
		}
		if a := strings.TrimSpace(config.Architecture); a != "" && a != "unknown" {
			return []string{a}, nil
		}
		return nil, fmt.Errorf("%w: the image config %s states no architecture", ErrImageCheckInconclusive, doc.Config.Digest)
	}
	return nil, fmt.Errorf("%w: unsupported manifest media type %s", ErrImageCheckInconclusive, emptyMediaType(mediaType))
}

// archInconclusive keeps a registry verdict inside the arch gate's contract:
// whatever the registry said (absent, denied, unreachable), the architecture
// is unknown, so the gate warns. Existence is the existence check's to judge.
func archInconclusive(err error) error {
	if err == nil || errors.Is(err, ErrImageCheckInconclusive) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrImageCheckInconclusive, err)
}

// readBounded reads at most maxManifestBytes and fails on anything longer: a
// truncated document can still parse (a JSON prefix, a manifest list cut short)
// into an incomplete architecture set, which the gate would take as conclusive.
func readBounded(rc io.ReadCloser) ([]byte, error) {
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxManifestBytes {
		return nil, fmt.Errorf("%w: the document exceeds %d bytes", ErrImageCheckInconclusive, maxManifestBytes)
	}
	return body, nil
}

func emptyMediaType(mt string) string {
	if mt == "" {
		return "(no media type)"
	}
	return mt
}
