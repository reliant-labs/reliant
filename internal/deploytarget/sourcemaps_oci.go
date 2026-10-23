package deploytarget

// Source maps as an OCI artifact in the platform registry.
//
// F1 (sourcemaps.go) moves every source map out of the tree a deploy publishes
// and into a sidecar directory next to it, with a manifest.json. This file is
// what happens to that sidecar next: it is packed and pushed to the registry so
// the control plane can symbolicate browser errors, or, for an environment
// whose observability is not hosted, kept on disk.
//
// # THE ARTIFACT FORMAT (the contract the control plane's symbolicator reads)
//
//	repository    <registry host>/<org>/<project>/<frontend>-sourcemaps
//	              = release.SourcemapsRepository(<project push base>, <frontend>)
//	tag           <build id>   (release.SourcemapsTag: `+` becomes `_`; the
//	              12-hex id forge derives is the tag verbatim)
//	manifest      OCI image manifest, schemaVersion 2
//	  artifactType  application/vnd.forge.sourcemaps.v1          SourcemapsArtifactType
//	  config        the OCI empty descriptor (application/vnd.oci.empty.v1+json)
//	  layers[0]     application/vnd.forge.sourcemaps.v1.tar+gzip  SourcemapsLayerMediaType
//	  annotations   org.opencontainers.image.created = 1970-01-01T00:00:00Z (pinned)
//	                dev.forge.sourcemaps.build_id / .frontend / .project
//
// The single layer is a deterministic tar.gz (sorted entries, zero timestamps
// and owners, mode 0644) of the sidecar directory:
//
//	manifest.json                    forge.dev/sourcemaps/v1 (SourcemapManifest)
//	<path>.map                       one per generated file, at the SAME
//	                                 relative path the generated file has in the
//	                                 served site
//
// manifest.json lists, for every map, `path` (the generated file, relative to
// the site root, so the base path is part of it), `map` (the map's own path in
// the layer) and `sha256`. A frame URL https://app.example/assets/index-B9o.js
// is resolved by stripping the origin and the leading slash and looking the
// result up in `files[].path`.
//
// Everything in the manifest and the tar is a pure function of the maps and the
// build identity, so re-pushing an unchanged build yields the same manifest
// digest. The tag is a convenience for lookup by build id; the digest is the
// identity, and is what a release records.
//
// # THE RELEASE RECORD
//
// A release that shipped the frontend records the artifact as
// `<frontend>.sourcemaps` (pkg/release/sourcemaps.go): Kind oci, Mode shared,
// Digests{"*": <manifest digest>}, Version = <build id>, URI = <repository>.
// Only a site built at release-cut time can be recorded that way (forge.OnHosted:
// `forge env build --release`); a Firebase or bucket frontend is built after the
// release is frozen, so its maps are filed by build id and its record is the
// state file below, not the ledger.
//
// # WHERE MAPS GO
//
//	hosted observability   pushed to the registry (RegistrySourcemaps)
//	anything else          kept in .forge/sourcemaps/<env>/<frontend>/<build id>/
//	                       and uploaded nowhere (LocalSourcemaps)
//
// Either way the outcome is written to .forge/state/sourcemaps-<env>-<frontend>.json
// (SourcemapsRecord), which is how a later step (the release cut, a human) finds
// what the last build filed.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"oras.land/oras-go/v2/content"

	"github.com/reliant-labs/forge/internal/statefile"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	// SourcemapsArtifactType is the OCI artifactType of a sourcemaps artifact.
	SourcemapsArtifactType = "application/vnd.forge.sourcemaps.v1"
	// SourcemapsLayerMediaType is its single layer: the sidecar directory.
	SourcemapsLayerMediaType = "application/vnd.forge.sourcemaps.v1.tar+gzip"

	// The manifest annotations repeat what manifest.json inside the layer says,
	// so a registry listing can tell artifacts apart without pulling one.

	// SourcemapsAnnotationBuildID is the build id the maps belong to.
	SourcemapsAnnotationBuildID = "dev.forge.sourcemaps.build_id"
	// SourcemapsAnnotationFrontend is the frontend the maps belong to.
	SourcemapsAnnotationFrontend = "dev.forge.sourcemaps.frontend"
	// SourcemapsAnnotationProject is the forge project the frontend belongs to.
	SourcemapsAnnotationProject = "dev.forge.sourcemaps.project"
)

// SourcemapBuild is one build's extracted maps, ready to be filed.
type SourcemapBuild struct {
	// Project and Frontend name the build in the artifact's annotations.
	Project, Frontend string
	// BuildID is the bundle's service.version and the key the maps are filed
	// under.
	BuildID string
	// Dir is the sidecar directory (SourcemapsDir of the staging tree).
	Dir string
	// Maps is how many maps the sidecar holds.
	Maps int
}

// SourcemapsRecord is the outcome of filing a build's maps. Repository and
// Digest are set for a registry push; Dir for a local copy.
type SourcemapsRecord struct {
	BuildID    string `json:"build_id"`
	Frontend   string `json:"frontend"`
	Maps       int    `json:"maps"`
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Dir        string `json:"dir,omitempty"`
	FiledAt    string `json:"filed_at"`
}

// Pushed reports whether the maps went to a registry (and so can be recorded in
// a release), rather than to a local directory.
func (r SourcemapsRecord) Pushed() bool { return r.Repository != "" && r.Digest != "" }

// SourcemapPublisher files one build's maps. The static providers call it
// after the stage and BEFORE anything goes live, so a build whose maps could
// not be filed never ships: it would be an unsymbolicatable build in
// production. nil on a provider means maps are extracted (F1) and not filed.
type SourcemapPublisher interface {
	PublishSourcemaps(ctx context.Context, b SourcemapBuild) (SourcemapsRecord, error)
}

// PackSourcemaps packs a sidecar directory into the artifact's layer.
func PackSourcemaps(dir string) ([]byte, error) {
	if _, err := os.Stat(filepath.Join(dir, SourcemapManifestName)); err != nil {
		return nil, fmt.Errorf("pack source maps %s: no %s (%w)", dir, SourcemapManifestName, err)
	}
	return packTree(dir, "source map artifact", "there are no source maps to file")
}

// sourcemapsAnnotations is the manifest's annotations for a build.
func sourcemapsAnnotations(b SourcemapBuild) map[string]string {
	return map[string]string{
		SourcemapsAnnotationBuildID:  b.BuildID,
		SourcemapsAnnotationFrontend: b.Frontend,
		SourcemapsAnnotationProject:  b.Project,
	}
}

// sourcemapsTarget is what a registry (or an in-memory store) must offer to
// take the artifact: blob pushes and a tag.
type sourcemapsTarget interface {
	content.Pusher
	content.Tagger
}

// pushSourcemaps is the transport-agnostic half, so it runs against any oras
// target. It returns the manifest digest; the tag is set after the blobs and
// manifest are in, so a tag never names an incomplete artifact.
func pushSourcemaps(ctx context.Context, target sourcemapsTarget, tag string, b SourcemapBuild, layer []byte) (string, error) {
	desc, err := pushArtifact(ctx, target, SourcemapsArtifactType, SourcemapsLayerMediaType, sourcemapsAnnotations(b), layer)
	if err != nil {
		return "", err
	}
	if err := target.Tag(ctx, desc, tag); err != nil {
		return "", fmt.Errorf("tag %s: %w", tag, err)
	}
	return desc.Digest.String(), nil
}

// PushSourcemapsArtifact pushes a packed sidecar to repository under tag and
// returns the manifest digest. Credentials come from the ambient docker
// credential store, as for the static site.
func PushSourcemapsArtifact(ctx context.Context, repository, tag string, b SourcemapBuild, layer []byte) (string, error) {
	repo, err := newRegistryRepository("source map", repository)
	if err != nil {
		return "", err
	}
	return pushSourcemaps(ctx, repo, tag, b, layer)
}

// RegistrySourcemaps files maps in the project's registry subtree.
type RegistrySourcemaps struct {
	// ProjectDir and Env say where the record is written
	// (.forge/state/sourcemaps-<env>-<frontend>.json). ProjectDir empty means ".".
	ProjectDir, Env string
	// Base resolves the project's push base (`<host>/<org>/<project>`). It is
	// called at the first publish, not at construction, so a deploy that
	// extracted no map never needs the credential that resolving it takes.
	Base func(ctx context.Context) (string, error)
	// Push stores the packed artifact. nil is the real registry
	// (PushSourcemapsArtifact); tests state the registry's answer.
	Push func(ctx context.Context, repository, tag string, b SourcemapBuild, layer []byte) (digest string, err error)
}

// PublishSourcemaps implements SourcemapPublisher.
func (r RegistrySourcemaps) PublishSourcemaps(ctx context.Context, b SourcemapBuild) (SourcemapsRecord, error) {
	tag, err := release.SourcemapsTag(b.BuildID)
	if err != nil {
		return SourcemapsRecord{}, err
	}
	base, err := r.Base(ctx)
	if err != nil {
		return SourcemapsRecord{}, fmt.Errorf("resolve the registry to file source maps in: %w", err)
	}
	repository, err := release.SourcemapsRepository(base, b.Frontend)
	if err != nil {
		return SourcemapsRecord{}, err
	}
	layer, err := PackSourcemaps(b.Dir)
	if err != nil {
		return SourcemapsRecord{}, err
	}
	push := r.Push
	if push == nil {
		push = PushSourcemapsArtifact
	}
	digest, err := push(ctx, repository, tag, b, layer)
	if err != nil {
		return SourcemapsRecord{}, fmt.Errorf("push source maps to %s:%s: %w", repository, tag, err)
	}
	rec := SourcemapsRecord{BuildID: b.BuildID, Frontend: b.Frontend, Maps: b.Maps, Repository: repository, Tag: tag, Digest: digest}
	return rec, WriteSourcemapsState(r.ProjectDir, r.Env, rec)
}

// LocalSourcemaps keeps maps on disk for an environment whose observability is
// not hosted: nothing is uploaded.
type LocalSourcemaps struct {
	// ProjectDir is the project root the .forge directory lives under, and Env
	// the deploy env the maps are kept for.
	ProjectDir, Env string
}

// LocalSourcemapsDir is where a build's maps are kept:
// <project>/.forge/sourcemaps/<env>/<frontend>/<build id>.
func LocalSourcemapsDir(projectDir, env, frontend, buildID string) string {
	if projectDir == "" {
		projectDir = "."
	}
	if env == "" {
		env = "default"
	}
	return filepath.Join(projectDir, ".forge", "sourcemaps",
		statefile.SafeSegment(env), statefile.SafeSegment(frontend), statefile.SafeSegment(buildID))
}

// PublishSourcemaps implements SourcemapPublisher.
func (l LocalSourcemaps) PublishSourcemaps(_ context.Context, b SourcemapBuild) (SourcemapsRecord, error) {
	dst := LocalSourcemapsDir(l.ProjectDir, l.Env, b.Frontend, b.BuildID)
	// A rebuild of the same id replaces the directory, so it never holds a map
	// the new build does not.
	if err := os.RemoveAll(dst); err != nil {
		return SourcemapsRecord{}, fmt.Errorf("replace %s: %w", dst, err)
	}
	if err := copyDir(b.Dir, dst); err != nil {
		return SourcemapsRecord{}, fmt.Errorf("keep source maps in %s: %w", dst, err)
	}
	rec := SourcemapsRecord{BuildID: b.BuildID, Frontend: b.Frontend, Maps: b.Maps, Dir: dst}
	return rec, WriteSourcemapsState(l.ProjectDir, l.Env, rec)
}

// FileStageSourcemaps files the maps a stage run extracted. It is the one call
// every static path makes, after the stage and BEFORE anything goes live. A nil
// publisher, or a stage that extracted nothing, is a no-op.
func FileStageSourcemaps(ctx context.Context, pub SourcemapPublisher, kind, project, frontend string, res StageResult) error {
	if res.SourcemapsDir == "" || pub == nil {
		return nil
	}
	if res.BuildID == "" {
		fmt.Printf("  [%s] %s: %d source map(s) extracted, but the frontend reports no build id (it has no APP_VERSION variable), so they cannot be keyed and are not filed: %s\n",
			kind, frontend, res.Sourcemaps, res.SourcemapsDir)
		return nil
	}
	rec, err := pub.PublishSourcemaps(ctx, SourcemapBuild{
		Project: project, Frontend: frontend,
		BuildID: res.BuildID, Dir: res.SourcemapsDir, Maps: res.Sourcemaps,
	})
	if err != nil {
		return fmt.Errorf("%s %s: file source maps: %w.\n"+
			"  Nothing was published: a build whose maps are not filed cannot be symbolicated, so it does not ship.\n"+
			"  To ship without maps, declare telemetry = forge.BrowserTelemetry {sourcemaps = %q} on the frontend", kind, frontend, err, SourcemapsOff)
	}
	ReportSourcemapsRecord(kind, frontend, rec)
	return nil
}

// ReportSourcemapsRecord prints the one line that says where a build's maps went.
func ReportSourcemapsRecord(kind, name string, rec SourcemapsRecord) {
	if rec.Pushed() {
		fmt.Printf("  [%s] %s: %d source map(s) filed as %s:%s (%s)\n", kind, name, rec.Maps, rec.Repository, rec.Tag, rec.Digest)
		return
	}
	fmt.Printf("  [%s] %s: %d source map(s) kept in %s (observability is not hosted: nothing uploaded)\n", kind, name, rec.Maps, rec.Dir)
}

// ─── State ───────────────────────────────────────────────────────────────────

func sourcemapsStatePath(projectDir, env, frontend string) string {
	if env == "" {
		env = "default"
	}
	return statefile.Path(projectDirOr(projectDir), "sourcemaps-"+statefile.SafeSegment(env)+"-"+statefile.SafeSegment(frontend)+".json")
}

func projectDirOr(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// WriteSourcemapsState records what the last build of a frontend filed.
func WriteSourcemapsState(projectDir, env string, rec SourcemapsRecord) error {
	if rec.FiledAt == "" {
		rec.FiledAt = time.Now().UTC().Format(time.RFC3339)
	}
	return statefile.Write(sourcemapsStatePath(projectDir, env, rec.Frontend), "source map state", rec)
}

// ReadSourcemapsState returns what the last build of frontend filed, or
// (nil, nil) when nothing was.
func ReadSourcemapsState(projectDir, env, frontend string) (*SourcemapsRecord, error) {
	return statefile.Read[SourcemapsRecord](sourcemapsStatePath(projectDir, env, frontend), "source map state")
}

// ClearSourcemapsState forgets the last record, so a record that exists after a
// build is that build's.
func ClearSourcemapsState(projectDir, env, frontend string) error {
	err := os.Remove(sourcemapsStatePath(projectDir, env, frontend))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ─── Maps from a build forge did not run ─────────────────────────────────────

// DistSourcemaps describes a built output directory forge did not build (the
// Electron renderer, built by CI) whose maps are to be extracted.
type DistSourcemaps struct {
	// Dir is the built output, with its bundles and their maps.
	Dir string
	// Project, Frontend and BuildID identify the build, as for a staged one.
	Project, Frontend, BuildID string
	// BasePath is where the output is mounted in the served site ("" for the
	// root); it becomes the leading part of every path in the manifest.
	BasePath string
}

// ExtractDistSourcemaps does for a built directory what the stage does for a
// staged one: moves every map out of a COPY of it into a sidecar, with a
// manifest. The original is not touched. cleanup removes the copy and the
// sidecar.
func ExtractDistSourcemaps(in DistSourcemaps) (b SourcemapBuild, cleanup func(), err error) {
	info, err := os.Stat(in.Dir)
	if err != nil {
		return SourcemapBuild{}, nil, fmt.Errorf("built output %s: %w", in.Dir, err)
	}
	if !info.IsDir() {
		return SourcemapBuild{}, nil, fmt.Errorf("built output %s is not a directory", in.Dir)
	}
	tmp, err := os.MkdirTemp("", "forge-sourcemaps-*")
	if err != nil {
		return SourcemapBuild{}, nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	staging := filepath.Join(tmp, "site")
	dst := staging
	if rel := basePathToDestRel(in.BasePath); rel != "." {
		dst = filepath.Join(staging, rel)
	}
	if err = copyDir(in.Dir, dst); err != nil {
		return SourcemapBuild{}, nil, fmt.Errorf("copy %s: %w", in.Dir, err)
	}
	plan := StagePlan{
		Name: in.Frontend, StagingDir: staging, Project: in.Project,
		BuildID: in.BuildID, BasePath: in.BasePath, Sourcemaps: SourcemapsPublish,
	}
	n, err := extractSourcemaps(plan)
	if err != nil {
		return SourcemapBuild{}, nil, err
	}
	return SourcemapBuild{
		Project: in.Project, Frontend: in.Frontend, BuildID: in.BuildID,
		Dir: SourcemapsDir(staging), Maps: n,
	}, cleanup, nil
}
