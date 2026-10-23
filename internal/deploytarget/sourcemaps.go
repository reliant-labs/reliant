package deploytarget

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Source maps are build output a deploy must never serve by default.
//
// A map holds the app's original sources. Serving one to the public is a
// decision, not a side effect of turning maps on to symbolicate errors, and
// a Vite or Next build writes the maps NEXT TO the bundle and names them in a
// trailer comment, so a plain copy of the output publishes them. The static
// stage is the one place every target (Firebase, a bucket, hosted) passes
// through, so it is the one place that fixes this:
//
//  1. a build id is derived from the source and the build env, and injected
//     as the frontend's APP_VERSION — the browser's service.version;
//  2. after the output is assembled, every source map is moved out of the
//     served tree into a sidecar directory (<staging>.sourcemaps, same
//     relative paths) with a manifest, and the sourceMappingURL trailers are
//     removed from the bundles;
//  3. a guard refuses to publish a tree that still holds a map.
//
// The sidecar is what a later step pushes to the platform registry so errors
// can be symbolicated; this file only produces it.

// Sourcemap publishing modes (the frontend's `sourcemaps` setting).
const (
	// SourcemapsPublish extracts maps to the sidecar and serves none. Default.
	SourcemapsPublish = "publish"
	// SourcemapsPublic leaves maps in the served tree, for an open-source app
	// that wants them public. The explicit opt-out of the guard.
	SourcemapsPublic = "public"
	// SourcemapsOff drops maps: stripped from the tree, kept nowhere.
	SourcemapsOff = "off"
)

const (
	// SourcemapManifestSchema versions manifest.json.
	SourcemapManifestSchema = "forge.dev/sourcemaps/v1"
	// SourcemapManifestName is the manifest's file name inside the sidecar.
	SourcemapManifestName = "manifest.json"
	// SourcemapsEnvVar is handed to the frontend build: "1" asks the bundler to
	// emit maps (the scaffolded vite/next configs read it), "0" says do not.
	// Gating on it keeps maps out of builds that do not go through this stage
	// — a Docker image of the same frontend would otherwise serve them.
	SourcemapsEnvVar = "FORGE_SOURCEMAPS"

	buildIDLen = 12
)

// sourcemapsDirSuffix names the sidecar next to a staging tree.
const sourcemapsDirSuffix = ".sourcemaps"

// SourcemapsDir is where a staging tree's extracted maps and manifest live: a
// sibling of the tree, so nothing that publishes the tree can reach it.
func SourcemapsDir(stagingDir string) string {
	return filepath.Clean(stagingDir) + sourcemapsDirSuffix
}

// SourcemapManifest is manifest.json: which maps the sidecar holds and the
// build they belong to.
//
// Every path is relative to the site root, which is also the staging root, so
// a generated file's Path is its URL path with the origin and the leading
// slash removed (BasePath is already part of it; it is recorded for readers
// that want to know the mount).
type SourcemapManifest struct {
	Schema   string          `json:"schema"`
	Project  string          `json:"project"`
	Frontend string          `json:"frontend"`
	BuildID  string          `json:"build_id"`
	BasePath string          `json:"base_path"`
	Files    []SourcemapFile `json:"files"`
}

// SourcemapFile is one extracted map. Path is the generated file it maps
// (assets/index-abc.js), Map is the map's own path in the sidecar
// (assets/index-abc.js.map).
type SourcemapFile struct {
	Path   string `json:"path"`
	Map    string `json:"map"`
	SHA256 string `json:"sha256"`
}

func resolveSourcemapsMode(mode string) (string, error) {
	switch mode {
	case "":
		return SourcemapsPublish, nil
	case SourcemapsPublish, SourcemapsPublic, SourcemapsOff:
		return mode, nil
	}
	return "", fmt.Errorf("sourcemaps %q is not one of %s | %s | %s", mode, SourcemapsPublish, SourcemapsPublic, SourcemapsOff)
}

// sourceTreeSkipDirs are never part of a frontend's source identity: they are
// dependencies or build output, which change without the code changing.
var sourceTreeSkipDirs = map[string]bool{
	"node_modules": true, ".git": true, ".forge": true,
	".next": true, ".next-prod": true, "out": true, "dist": true, "build": true,
	".turbo": true, ".vite": true, "coverage": true,
}

// computeBuildID is the build's identity: the version the bundle reports as
// service.version and the key its source maps are filed under.
//
// A version the project already set wins and is used verbatim, so the id the
// browser reports and the id the maps are filed under cannot differ.
// Otherwise it is sha256(source tree ‖ sorted build env)[:12]. Both inputs
// are known before the build runs, and the env is part of it because two
// environments can build one tree with different inlined values and so
// different bytes.
func computeBuildID(frontendDir, versionVar string, env map[string]string) (string, error) {
	if v := env[versionVar]; v != "" {
		return v, nil
	}
	h := sha256.New()
	_, _ = io.WriteString(h, "forge.build-id.v1\n")
	tree, err := sourceTreeDigest(frontendDir)
	if err != nil {
		return "", err
	}
	_, _ = io.WriteString(h, tree+"\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		if k != versionVar {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "%d:%s=%d:%s\n", len(k), k, len(env[k]), env[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:buildIDLen], nil
}

// sourceTreeDigest hashes the regular files under dir, skipping dependency and
// output directories, in sorted path order.
func sourceTreeDigest(dir string) (string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && sourceTreeSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest frontend source %s: %w", dir, err)
	}
	sort.Strings(rels)
	h := sha256.New()
	for _, rel := range rels {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("digest frontend source %s: %w", rel, err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return "", fmt.Errorf("digest frontend source %s: %w", rel, err)
		}
		_, _ = fmt.Fprintf(h, "%d:%s:%d\n", len(rel), rel, info.Size())
		_, cerr := io.Copy(h, f)
		_ = f.Close()
		if cerr != nil {
			return "", fmt.Errorf("digest frontend source %s: %w", rel, cerr)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractSourcemaps moves every source map out of the staged tree into the
// sidecar and strips the sourceMappingURL trailers. It returns how many maps
// it extracted. SourcemapsPublic is a no-op; SourcemapsOff extracts and then
// discards the sidecar, so the tree ends up identical to publish.
func extractSourcemaps(plan StagePlan) (int, error) {
	if plan.Sourcemaps == SourcemapsPublic {
		return 0, nil
	}
	sidecar := SourcemapsDir(plan.StagingDir)
	if err := os.MkdirAll(sidecar, 0o755); err != nil {
		return 0, fmt.Errorf("create sourcemap sidecar: %w", err)
	}

	var files []string
	err := filepath.WalkDir(plan.StagingDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(plan.StagingDir, path)
		if rerr != nil {
			return rerr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("scan staging tree: %w", err)
	}
	sort.Strings(files)

	// External maps first, so an inline map never overwrites a file one.
	for _, rel := range files {
		if !looksLikeMapName(rel) {
			continue
		}
		src := filepath.Join(plan.StagingDir, filepath.FromSlash(rel))
		if !isSourceMap(src) {
			continue
		}
		if err := moveFile(src, filepath.Join(sidecar, filepath.FromSlash(rel))); err != nil {
			return 0, fmt.Errorf("move source map %s: %w", rel, err)
		}
	}
	for _, rel := range files {
		if !hasTrailerExt(rel) {
			continue
		}
		if err := stripTrailer(plan.StagingDir, sidecar, rel); err != nil {
			return 0, err
		}
	}

	manifest, err := buildSourcemapManifest(plan, sidecar)
	if err != nil {
		return 0, err
	}
	n := len(manifest.Files)
	if plan.Sourcemaps == SourcemapsOff {
		return n, os.RemoveAll(sidecar)
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := atomicfile.WriteFile(filepath.Join(sidecar, SourcemapManifestName), append(b, '\n'), 0o644); err != nil {
		return 0, fmt.Errorf("write sourcemap manifest: %w", err)
	}
	return n, nil
}

func buildSourcemapManifest(plan StagePlan, sidecar string) (SourcemapManifest, error) {
	m := SourcemapManifest{
		Schema:   SourcemapManifestSchema,
		Project:  plan.Project,
		Frontend: plan.Name,
		BuildID:  plan.BuildID,
		BasePath: plan.BasePath,
		Files:    []SourcemapFile{},
	}
	err := filepath.WalkDir(sidecar, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(sidecar, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == SourcemapManifestName {
			return nil
		}
		sum, herr := fileSHA256(path)
		if herr != nil {
			return herr
		}
		m.Files = append(m.Files, SourcemapFile{Path: strings.TrimSuffix(rel, ".map"), Map: rel, SHA256: sum})
		return nil
	})
	if err != nil {
		return m, fmt.Errorf("index source maps: %w", err)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, nil
}

// guardNoSourceMaps fails when the tree about to be published still holds a
// source map, unless the frontend opted in to public maps. Extraction removes
// every map, so this only fires for one added after it — a carried-over asset,
// a bundle dir copied late — which is exactly what it is for.
func guardNoSourceMaps(stagingDir, mode string) error {
	if mode == SourcemapsPublic {
		return nil
	}
	var found []string
	err := filepath.WalkDir(stagingDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !looksLikeMapName(d.Name()) || !isSourceMap(path) {
			return err
		}
		rel, _ := filepath.Rel(stagingDir, path)
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan for source maps: %w", err)
	}
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)
	shown := found
	if len(shown) > 5 {
		shown = append(shown[:5:5], fmt.Sprintf("… and %d more", len(found)-5))
	}
	return fmt.Errorf("refusing to publish %d source map(s): %s.\n"+
		"Source maps contain the app's original sources and are never served by default; the stage step "+
		"moves them to %s. One in the served tree means something added it after that. "+
		"A frontend that means to serve its maps declares telemetry = forge.BrowserTelemetry {sourcemaps = %q}",
		len(found), strings.Join(shown, ", "), filepath.Base(SourcemapsDir(stagingDir)), SourcemapsPublic)
}

// looksLikeMapName reports whether a path could be a source map by name. The
// content test (isSourceMap) decides; this only avoids opening every file.
func looksLikeMapName(path string) bool {
	return strings.HasSuffix(path, ".map")
}

// isSourceMap reports whether the file is a source map document: JSON with a
// version and either mappings or sections. A `.map` extension alone proves
// nothing — a game level or a geo file can carry it.
func isSourceMap(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		Version  int             `json:"version"`
		Mappings *string         `json:"mappings"`
		Sections json.RawMessage `json:"sections"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return false
	}
	return doc.Version > 0 && (doc.Mappings != nil || len(doc.Sections) > 0)
}

func hasTrailerExt(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".js", ".mjs", ".cjs", ".css":
		return true
	}
	return false
}

// stripTrailer removes a bundle's trailing sourceMappingURL comment. An inline
// (data:) map is decoded into the sidecar first, so it is extracted rather
// than lost. The trailer is the last line, so no mapped position shifts.
func stripTrailer(stagingDir, sidecar, rel string) error {
	path := filepath.Join(stagingDir, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	body, ref, ok := splitSourceMapTrailer(string(b))
	if !ok {
		return nil
	}
	if strings.HasPrefix(ref, "data:") {
		if err := extractInlineMap(sidecar, rel, ref); err != nil {
			return err
		}
	}
	if err := atomicfile.WriteFile(path, []byte(body), info.Mode().Perm()); err != nil {
		return fmt.Errorf("strip source map trailer from %s: %w", rel, err)
	}
	return nil
}

// extractInlineMap writes a bundle's inline (data:) map into the sidecar as if
// it had been an external one. A map that cannot be decoded is dropped with
// its trailer: it is unusable, and it must not be served.
func extractInlineMap(sidecar, rel, ref string) error {
	mapJSON, err := decodeDataURL(ref)
	if err != nil {
		return nil
	}
	dst := filepath.Join(sidecar, filepath.FromSlash(rel+".map"))
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(dst, mapJSON, 0o644); err != nil {
		return fmt.Errorf("write inline source map for %s: %w", rel, err)
	}
	return nil
}

// splitSourceMapTrailer separates a trailing `//# sourceMappingURL=…` (or the
// CSS `/*# … */` form) from the code before it. The comment must be the last
// non-blank line and stand alone on it; anything else is left untouched.
func splitSourceMapTrailer(s string) (body, ref string, ok bool) {
	const key = "sourceMappingURL="
	idx := strings.LastIndex(s, key)
	if idx < 0 {
		return s, "", false
	}
	lineStart := strings.LastIndexByte(s[:idx], '\n') + 1
	switch strings.TrimSpace(s[lineStart:idx]) {
	case "//#", "//@", "/*#", "/*@":
	default:
		return s, "", false
	}
	rest := s[idx+len(key):]
	line := rest
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		line = rest[:nl]
		if strings.TrimSpace(rest[nl:]) != "" {
			return s, "", false
		}
	}
	ref = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "*/"))
	if ref == "" {
		return s, "", false
	}
	return s[:lineStart], ref, true
}

func decodeDataURL(ref string) ([]byte, error) {
	comma := strings.IndexByte(ref, ',')
	if comma < 0 {
		return nil, fmt.Errorf("malformed data URL")
	}
	payload := ref[comma+1:]
	if strings.Contains(ref[:comma], ";base64") {
		return base64.StdEncoding.DecodeString(payload)
	}
	s, err := url.PathUnescape(payload)
	return []byte(s), err
}

func moveFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Remove(src)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
