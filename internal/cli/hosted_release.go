package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedBackendDigestResolver resolves a tag-pinned image to its registry
// digest. A var so tests state the registry's answer instead of needing one.
var hostedBackendDigestResolver = func(ctx context.Context, ref string) (string, []string, error) {
	return externalImageDigestResolver(ctx, ref)
}

// harvestHostedBackendArtifacts records the image every HOSTED workload that
// forge does not build declares — so a release cut covers the workloads
// a hosted deploy ships, and `forge env deploy` can pin them from the binding.
//
// WHY THIS IS SEPARATE FROM THE BUILD-STATE HARVEST. Such a workload names an
// image forge did not build (CI pushed it, or it is a third-party image), so
// there is no .forge/state record to project. The declaration is
// the source of truth for WHICH image; the registry is the source of truth for
// WHICH BYTES:
//
//   - a digest-pinned image (`repo@sha256:…`) is recorded as declared;
//   - a tag-pinned image is resolved to its digest NOW, at cut time. That is
//     the one moment the tag is allowed to mean something: once recorded, the
//     release pins the bytes and a later re-push of the tag changes nothing.
//
// An artifact the build-state harvest already recorded (forge built and
// pushed it) wins: it is the digest of the bytes this very pipeline produced.
// Artifacts are keyed by hostedArtifactKey, the same rule the hosted provider
// pins by, so the cut and the deploy cannot disagree.
func harvestHostedBackendArtifacts(ctx context.Context, envName string, entities *KCLEntities, out map[string]release.Artifact) error {
	if entities == nil {
		return nil
	}
	var errs []string
	for _, svc := range entities.WorkloadsOn(RuntimeHosted) {
		name := hostedArtifactKey(entities, svc)
		if _, have := out[name]; have || svc.Image != "" {
			// forge built and pushed it (the build state's digest wins), or
			// forge builds it and this cut simply did not — the release
			// coverage check names that, with the build command.
			continue
		}
		image := svc.Spec.Image
		if _, have := out[name]; have {
			// forge built and pushed it: the build state's digest wins.
			continue
		}
		repo := deploytarget.HostedImageRepository(image)
		digest := deploytarget.HostedImageDigest(image)
		var platforms []string
		if digest == "" {
			d, p, err := hostedBackendDigestResolver(ctx, image)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: resolve %s to a digest: %v", svc.Name, image, err))
				continue
			}
			digest, platforms = d, p
		}
		out[name] = release.Artifact{
			Kind:      release.KindOCI,
			Mode:      release.ModeShared,
			Digests:   map[string]string{release.SharedVariant: digest},
			URI:       strings.TrimSuffix(repo, "/"+deploytarget.HostedArtifactName(image)),
			Platforms: platforms,
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("hosted backend images could not be recorded:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
