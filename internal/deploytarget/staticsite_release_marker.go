package deploytarget

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The release marker of a hosted static site (forge#2231).
//
// The control plane serves a hosted static site from its origin, and forge
// cannot read that origin's state. So `forge env status` could never say
// which release a hosted site serves, and always reported UNREACHABLE (exit
// 2), even right after a deploy. The witness is the site itself: `env build`
// stamps the name of the release it builds into the site's tree, and `env
// status` fetches it over HTTP from the live site, through the same CDN users
// reach, and compares it with the release the env's snapshot binds.
//
// The marker names the release, not the artifact's digest: a file inside the
// tree cannot carry the digest of the tree that contains it. A release is
// immutable and cut once, so its name identifies the bytes as well as the
// digest does. The marker is part of the artifact, so two releases of the same
// source have different digests. That is the right answer: they are different
// releases.

// StaticSiteReleaseMarkerPath is where a hosted static site release carries
// its marker, relative to the site root. `.json` matters: the hosted origin
// answers a missing file with an extension with 404, never with index.html.
const StaticSiteReleaseMarkerPath = ".well-known/forge-release.json"

// StaticSiteReleaseMarkerSchema versions the marker document.
const StaticSiteReleaseMarkerSchema = "forge.static-site-release/v1"

// StaticSiteReleaseMarker is the document at StaticSiteReleaseMarkerPath.
type StaticSiteReleaseMarker struct {
	Schema   string `json:"schema"`
	Project  string `json:"project,omitempty"`
	Frontend string `json:"frontend"`
	Release  string `json:"release"`
}

// WriteStaticSiteReleaseMarker writes m into the assembled site tree, over
// any file the site's own build put there.
func WriteStaticSiteReleaseMarker(tree string, m StaticSiteReleaseMarker) error {
	if strings.TrimSpace(m.Release) == "" || strings.TrimSpace(m.Frontend) == "" {
		return fmt.Errorf("release marker: release and frontend are required")
	}
	m.Schema = StaticSiteReleaseMarkerSchema
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(tree, filepath.FromSlash(StaticSiteReleaseMarkerPath))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("release marker: %w", err)
	}
	if err := atomicfile.WriteFile(dst, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("release marker: %w", err)
	}
	return nil
}

// ParseStaticSiteReleaseMarker reads a marker document. Anything that is not
// one, such as an HTML page a host served in its place, is an error, never an
// empty marker.
func ParseStaticSiteReleaseMarker(data []byte) (StaticSiteReleaseMarker, error) {
	var m StaticSiteReleaseMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return StaticSiteReleaseMarker{}, fmt.Errorf("not a forge release marker: %w", err)
	}
	if m.Schema != StaticSiteReleaseMarkerSchema {
		return StaticSiteReleaseMarker{}, fmt.Errorf("not a forge release marker: schema %q, want %q", m.Schema, StaticSiteReleaseMarkerSchema)
	}
	if strings.TrimSpace(m.Release) == "" {
		return StaticSiteReleaseMarker{}, fmt.Errorf("forge release marker names no release")
	}
	return m, nil
}
