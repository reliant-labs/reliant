package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/hostedimage"
	"github.com/reliant-labs/forge/internal/sbomoci"
	"github.com/reliant-labs/forge/pkg/release"
)

// The SBOM beside the image (design doc §1.3): every OCI artifact a cut
// records — an image, a hosted static site — gets its freshly generated SBOM
// attached in its own registry as an OCI 1.1 referrer (internal/sbomoci),
// and the release records the referrer's address in SBOMRef.Referrer.
//
// It is a post-generate hook, so it runs after the bytes are in the machine
// ledger: a failed attach warns and leaves Referrer empty, the ledger copy
// stays, and the cut goes on. `vuln.attach_sbom: false` in forge.yaml turns
// it off.

func init() {
	sbomPostGenerate = append(sbomPostGenerate, attachSBOMReferrer)
}

// sbomReferrerAttach attaches an SBOM as a referrer. A var so a test points
// it at an in-process registry; nil attaches nothing, the unit tests'
// hermetic default (main_test.go).
var sbomReferrerAttach = sbomoci.Attach

// attachSBOMReferrer is the hook. An artifact it does not attach to (not OCI,
// a variant image, attachment off) keeps its ref unchanged.
func attachSBOMReferrer(ctx context.Context, in sbomGenerated) (release.SBOMRef, error) {
	ref := *in.Artifact.SBOM
	digest, ok := in.Artifact.SharedDigest()
	if !ok || ref.Referrer != "" || sbomReferrerAttach == nil {
		return ref, nil
	}
	if !config.ReadVulnConfig(in.ProjectDir).AttachSBOMEnabled() {
		return ref, nil
	}
	repository := sbomSubjectRepository(in.Name, in.Artifact, in.Entities)
	if repository == "" {
		ref.Reason = "referrer not attached: the release records no registry repository for " + in.Name
		return ref, nil
	}
	subject := repository + "@" + digest
	if !sbomSubjectPushedByProject(in, repository) {
		// A third-party image the project pins (postgres, a vendor image):
		// not ours to write beside, and not worth a warning on every cut.
		ref.Reason = "referrer not attached: " + subject + " is not pushed by this project"
		return ref, nil
	}
	host, _, _ := strings.Cut(repository, "/")
	credential, err := sbomRegistryCredential(in.ProjectDir, in.Env, in.Entities, host)
	if err != nil {
		return ref, fmt.Errorf("not attached beside the image (the machine ledger keeps it): %w", err)
	}
	referrer, err := sbomReferrerAttach(ctx, subject, in.Bytes, sbomoci.AttachOptions{
		Registry:  sbomoci.RegistryOptions{Credential: credential},
		Generator: ref.Generator,
		Release:   in.Version,
		Created:   in.CutAt,
	})
	if reason, skipped := sbomoci.SkipReason(err); skipped {
		ref.Reason = reason
		return ref, nil
	}
	if err != nil {
		return ref, fmt.Errorf("not attached beside the image (the machine ledger keeps it; `vuln.attach_sbom: false` in forge.yaml stops trying): %w", err)
	}
	ref.Referrer = referrer
	return ref, nil
}

// sbomSubjectRepository is the registry repository an OCI artifact was
// pushed to — the subject's repository, never assumed to be the hook's Name,
// because callers key artifacts differently:
//
//   - `forge release record` keys by WORKLOAD and keeps the pushed reference
//     in URI (`ghcr.io/acme/api:v1`): the repository is URI, its tag or
//     digest stripped.
//   - The cut keys every OCI artifact by its repository and records no URI
//     for a built image or a hosted static site (the key IS the address:
//     harvestReleaseArtifacts). A hosted backend's URI is its repository's
//     NAMESPACE (harvestHostedBackendArtifacts): a strict path prefix of the
//     key when the key is under the push base, and, in an env with no push
//     base, the namespace of a key that is the bare artifact name.
//
// URI is part of an artifact's identity, so the cut cannot start recording a
// different one. The two namespace shapes are therefore recognised here: a
// URI that is a path prefix of a host-bearing key defers to the key, and a
// URI that with a bare key spells the repository a hosted workload of e
// declares (deploytarget.HostedImageRepository) is joined with it.
//
// "" when nothing names a registry repository.
func sbomSubjectRepository(name string, art release.Artifact, e *KCLEntities) string {
	uri := strings.TrimSuffix(hostedimage.Repository(strings.TrimSpace(art.URI)), "/")
	keyIsRepository := hostedimage.HasRegistryHost(name)
	switch {
	case uri == "" || !hostedimage.HasRegistryHost(uri):
		if keyIsRepository {
			return name
		}
		return ""
	case keyIsRepository && strings.HasPrefix(name, uri+"/"):
		return name
	case !keyIsRepository && hostedNamespaceOf(e, name, uri):
		return uri + "/" + name
	}
	return uri
}

// hostedNamespaceOf reports whether uri is the namespace of the hosted
// workload whose release key is the bare name: the cut recorded that
// artifact as (name, namespace).
func hostedNamespaceOf(e *KCLEntities, name, uri string) bool {
	for _, w := range e.WorkloadsOn(RuntimeHosted) {
		if hostedArtifactKey(e, w) == name && deploytarget.HostedImageRepository(w.Spec.Image) == uri+"/"+name {
			return true
		}
	}
	return false
}

// sbomSubjectPushedByProject reports whether this project pushed the
// artifact to repository (its sbomSubjectRepository), so writing a referrer
// beside it is ours to do:
//
//   - this cut's build state recorded it (built, or carried from an earlier
//     release of the same project, and pushed);
//   - it lives under the env's platform push base, `<registry>/<org>/<project>`
//     — hosted backends (CI-pushed ones and mirrors included) and hosted static
//     sites, the one subtree the platform admits for this project;
//   - it is the repository a workload this env BUILDS resolves to (its declared
//     image, which CI may have pushed): a workload forge does not build names a
//     third-party image, which is never ours.
//
// Everything else — a pinned postgres, a vendor image — is not.
func sbomSubjectPushedByProject(in sbomGenerated, repository string) bool {
	if in.Built {
		return true
	}
	e := in.Entities
	if e == nil {
		return false
	}
	if base := platformPushBase(e); base != "" && (repository == base || strings.HasPrefix(repository, base+"/")) {
		return true
	}
	for _, w := range e.Workloads {
		if w.Image == "" {
			continue
		}
		if imageRepository(w.Spec.Image) == repository || (w.Runtime.Type == RuntimeHosted && hostedArtifactKey(e, w) == repository) {
			return true
		}
	}
	return false
}
