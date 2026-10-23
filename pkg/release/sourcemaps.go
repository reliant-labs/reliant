package release

import (
	"fmt"
	"regexp"
	"strings"
)

// A frontend's browser source maps are filed in the platform registry as their
// own OCI artifact, keyed so that a reader holding only (registry base,
// frontend, service.version) can find the maps without a lookup table. This
// file is the one definition of that keying; forge writes it and the control
// plane's symbolicator reads it.
//
//	repository  <registry base>/<frontend>-sourcemaps        SourcemapsRepository
//	tag         <build id>                                    SourcemapsTag
//	release     artifacts["<frontend>.sourcemaps"]            SourcemapsArtifactName
//
// <registry base> is `<registry host>/<org>/<project>`: the push base every
// hosted artifact of the project lives under. The artifact itself (media
// types, layer layout, manifest.json) is documented where it is written,
// internal/deploytarget/sourcemaps_oci.go.

// SourcemapsArtifactSuffix ends the name of the release artifact that records a
// frontend's source maps.
//
// The release records, beside the frontend that shipped, an OCI artifact:
//
//	artifacts["web.sourcemaps"] = Artifact{
//	    Kind:    KindOCI,
//	    Mode:    ModeShared,
//	    Digests: {SharedVariant: "sha256:…"},   // the artifact's manifest digest
//	    Version: "3f9c0e1a2b4d",                // the build id, exactly as the bundle reports it
//	    URI:     "registry.example/<org>/<project>/web-sourcemaps",
//	}
//
// Unlike an image, it is NOT keyed by its repository: the key is the frontend's
// name plus this suffix, so a reader holding a frontend and a release finds the
// maps without composing a registry path. The repository is URI, whole, host
// included — never a prefix to join the name onto (a dot is not legal in a
// repository segment, so `web.sourcemaps` can never be one).
const SourcemapsArtifactSuffix = ".sourcemaps"

// SourcemapsRepositorySuffix ends the repository segment the maps live in.
// It is a dash, not a dot: the registry admits only RFC-1123 labels below the
// project, so the segment is an ordinary image-shaped name.
const SourcemapsRepositorySuffix = "-sourcemaps"

// maxRepositoryLabel is the RFC-1123 label limit every repository segment
// obeys (the control plane's registry grammar enforces it).
const maxRepositoryLabel = 63

var (
	labelPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// tagPattern is the OCI distribution tag grammar.
	tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

// SourcemapsArtifactName is the release artifact name for a frontend's maps.
func SourcemapsArtifactName(frontend string) string {
	return frontend + SourcemapsArtifactSuffix
}

// SourcemapsFrontend is the inverse of [SourcemapsArtifactName]: the frontend
// an artifact name holds the maps of, and false for any other artifact.
func SourcemapsFrontend(artifact string) (string, bool) {
	frontend, ok := strings.CutSuffix(artifact, SourcemapsArtifactSuffix)
	if !ok || frontend == "" {
		return "", false
	}
	return frontend, true
}

// SourcemapsRepository is the repository a frontend's maps are pushed to:
// `<registryBase>/<frontend>-sourcemaps`. registryBase is the project's push
// base (`<registry host>/<org>/<project>`). The frontend must be a lower-case
// RFC-1123 label short enough that the suffixed segment still is one.
func SourcemapsRepository(registryBase, frontend string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(registryBase), "/")
	if base == "" {
		return "", fmt.Errorf("source maps need a registry base (<host>/<org>/<project>) to be filed under")
	}
	segment := frontend + SourcemapsRepositorySuffix
	if !labelPattern.MatchString(frontend) || len(segment) > maxRepositoryLabel {
		return "", fmt.Errorf("frontend %q cannot name a source map repository: it must be a lower-case RFC-1123 label of at most %d characters (%q is the segment)",
			frontend, maxRepositoryLabel-len(SourcemapsRepositorySuffix), segment)
	}
	return base + "/" + segment, nil
}

// SourcemapsTag is the registry tag a build's maps are filed under: the build
// id, which is also the bundle's service.version. An id that is already a valid
// OCI tag (the 12-hex id forge derives always is) is the tag verbatim. A
// project-set version may hold `+` (semver build metadata), which a tag cannot;
// it becomes `_`, the convention Helm and Docker use for the same reason. Any
// other character a tag cannot hold is an error rather than a silent rewrite,
// because a rewrite the symbolicator did not apply would file the maps where
// nothing looks.
func SourcemapsTag(buildID string) (string, error) {
	tag := strings.ReplaceAll(buildID, "+", "_")
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("build id %q cannot be a registry tag (letters, digits, `_`, `.`, `-`; at most 128 characters; `+` is read as `_`)", buildID)
	}
	return tag, nil
}
