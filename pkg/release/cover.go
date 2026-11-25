package release

// Build variants of a release's images: today, the `cover` variant, the same
// source built for test coverage (`go build -cover`, the Dockerfile's cover
// stage). A preview runs it; a persistent env never does.
//
// A variant rides BESIDE the image it varies, never in its place. The image
// `ghcr.io/acme/shop` stays a [ModeShared] artifact — the digest staging ran
// and prod will run — and its cover build is a second artifact,
// `ghcr.io/acme/shop#cover`, of [ModeVariant] with its digest under the
// variant's key. Both shapes predate variants, so a reader that knows nothing
// of them decodes the release, pins only the shared digest
// ([Release.SharedDigests] skips a variant artifact), and can never ship the
// cover build by accident.
//
// What is refused is narrow: placing a VARIANT DIGEST on an env that must not
// run it ([Release.CheckPins]). A release that merely carries a cover variant
// promotes to prod like any other, and prod resolves the shared digest.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CoverVariant names a release's coverage-instrumented builds.
const CoverVariant = "cover"

// variantSep joins an image repository and a variant into the variant
// artifact's name. `#` appears in no image reference, so the name can never be
// mistaken for one.
const variantSep = "#"

// ErrVariantOnPersistentEnv is returned when a pin set would place a build
// variant's digest (a cover build) on an env that runs only shared digests.
var ErrVariantOnPersistentEnv = errors.New("a build variant's digest cannot be placed on a persistent env")

// VariantArtifactName is the name of repository's variant artifact:
// "ghcr.io/acme/shop#cover".
func VariantArtifactName(repository, variant string) string {
	return repository + variantSep + variant
}

// SplitVariantArtifactName splits a variant artifact's name into the image
// repository and the variant; ok is false for any other name.
func SplitVariantArtifactName(name string) (repository, variant string, ok bool) {
	i := strings.LastIndex(name, variantSep)
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

// VariantDigests is the repository → digest pin set of one variant: every
// variant artifact the release carries for it.
func (r Release) VariantDigests(variant string) map[string]string {
	out := map[string]string{}
	for name, a := range r.Artifacts {
		repo, v, ok := SplitVariantArtifactName(name)
		if !ok || v != variant || a.Kind != KindOCI || a.Mode != ModeVariant {
			continue
		}
		if d := a.Digests[variant]; d != "" {
			out[repo] = d
		}
	}
	return out
}

// PinsFor is the image pin set an env resolves from the release: the shared
// digests, and — only for an env that runs the variant (a preview runs
// [CoverVariant]) — the variant's digest in place of the shared one for each
// image that has it. variant "" is the shared set alone, what every
// persistent env resolves; it is [Release.SharedDigests].
func (r Release) PinsFor(variant string) map[string]string {
	out := r.SharedDigests()
	if variant == "" {
		return out
	}
	for repo, d := range r.VariantDigests(variant) {
		out[repo] = d
	}
	return out
}

// CheckPins refuses a pin set that would place one of the release's variant
// digests on an env that does not run that variant. runs is the variant the
// env runs ("" for a persistent env, which runs none). Only the pins are
// judged: a release that carries a variant is never refused for carrying it.
func (r Release) CheckPins(runs string, pins map[string]string) error {
	variantOf := map[string]string{}
	for name, a := range r.Artifacts {
		_, v, ok := SplitVariantArtifactName(name)
		if !ok || a.Mode != ModeVariant {
			continue
		}
		for _, d := range a.Digests {
			variantOf[d] = v
		}
	}
	var bad []string
	for repo, d := range pins {
		if v, ok := variantOf[d]; ok && v != runs {
			bad = append(bad, fmt.Sprintf("%s@%s (the %s build)", repo, d, v))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("%w: release %q: %s. A persistent env runs the release's shared digests — the bytes staging ran — and a %s build is for local, ephemeral and preview envs",
		ErrVariantOnPersistentEnv, r.Version, strings.Join(bad, ", "), CoverVariant)
}

// validateVariantArtifact checks the shape of an artifact whose name is a
// variant artifact's: an image of [ModeVariant] whose only key is its variant.
func validateVariantArtifact(name string, a Artifact) error {
	_, variant, ok := SplitVariantArtifactName(name)
	if !ok {
		return nil
	}
	if a.Kind != KindOCI || a.Mode != ModeVariant || len(a.Digests) != 1 || a.Digests[variant] == "" {
		return fmt.Errorf("%w: artifact %q names the %q build variant of an image, so it is an oci artifact of mode variant whose only digest is under %q",
			ErrInvalid, name, variant, variant)
	}
	return nil
}
