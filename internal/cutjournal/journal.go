// Package cutjournal is the record a release cut leaves in the registry so a
// cut that is interrupted after its pushes can be finished instead of burned.
//
// # Why it exists
//
// `forge env build <env> --release <v>` pushes every image the env declares
// under the tag <v>, then records the release in the env's ledger. The two
// writes are not atomic. A machine that dies between them (control-plane
// v1.7.94-rc.1, 2026-11-04: every image pushed, the host rebooted before the
// ledger write) leaves immutable tags the ledger never saw, and the local
// build state that held their digests died with the checkout. Every retry of
// <v> then fails at its first push, so the version was burned and the set had
// to be re-identified by hand as the next version, from the same commits.
//
// The registry still holds everything the cut needs except the PROOF that the
// tagged bytes came from the commit being cut. The journal is that proof. The
// cut writes it right after its last push and before its ledger record:
// version, env, project, the forge that built it, the commit and tree it
// built from, every image digest it pushed under <v>, and the exact build
// state the cut harvests. A retry of <v> from the same clean commit, with the
// same forge, finds it, checks each image tag still names the digest the
// journal recorded, restores the build state, and records the release over
// the images already pushed: nothing is rebuilt. Anything else (another
// commit, a dirty tree, another forge, a tag that moved) is refused, exactly
// as before.
//
// # Where it lives
//
// One OCI artifact, tagged <v>.forge-cut (Tag), in the first push destination
// the env declares (sorted), which the writer and the reader resolve the same
// way from the same render. The image tags themselves are left alone: the
// journal names them, it never moves one.
//
// What it cannot do: resume a cut interrupted DURING its pushes. Some images
// are then tagged and some are not, and no journal was written; that cut is
// refused as it always was.
package cutjournal

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	// Schema names the journal document's version.
	Schema = "forge.dev/cut-journal/v1"
	// ArtifactType is the OCI artifactType of a journal manifest.
	ArtifactType = "application/vnd.forge.cut-journal.v1"
	// LayerMediaType is its single layer: the journal document.
	LayerMediaType = "application/vnd.forge.cut-journal.v1+json"
	// TagSuffix is appended to the release version to make the journal's tag.
	TagSuffix = ".forge-cut"
)

// ErrNotFound is returned by Fetch when no journal is tagged for the version.
var ErrNotFound = errors.New("no cut journal")

// Journal is one cut's record of what it pushed and from where.
type Journal struct {
	Schema string `json:"schema"`
	// Project, Env and Version identify the cut.
	Project string `json:"project"`
	Env     string `json:"env"`
	Version string `json:"version"`
	// ForgeVersion is the forge that built and would have recorded it. The
	// build state below is that forge's format, so only it resumes the cut.
	ForgeVersion string `json:"forge_version"`
	// Commit, Tree and Dirty are the project checkout the cut built.
	Commit string `json:"commit"`
	Tree   string `json:"tree,omitempty"`
	Dirty  bool   `json:"dirty"`
	// Images are the images the cut pushed under the release version.
	Images []Image `json:"images"`
	// State is the build state the cut harvests, by file name under the
	// project's state directory, byte for byte.
	State []File `json:"state"`
	// Outputs are build outputs the cut reads besides images (the metrics
	// schema), by path relative to the build output directory.
	Outputs []File `json:"outputs,omitempty"`
}

// Image is one image the cut records: <Repository>@<Digest>. Tag is set when
// the cut pushed it under the release version (<Repository>:<Tag> named
// Digest then), and empty for an image the cut reused under another tag.
type Image struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest"`
}

// Ref is the image's full reference: `<repository>[:<tag>]@<digest>`.
func (img Image) Ref() string {
	if img.Tag == "" {
		return img.Repository + "@" + img.Digest
	}
	return img.Repository + ":" + img.Tag + "@" + img.Digest
}

// PushedUnder reports whether the cut pushed any image under version — the
// only case in which a retry would collide with its tags.
func (j Journal) PushedUnder(version string) bool {
	for _, img := range j.Images {
		if img.Tag == version {
			return true
		}
	}
	return false
}

// File is a file's name and exact content.
type File struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// tagRE is the OCI distribution tag grammar.
var tagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// Tag is the journal tag for a release version. It refuses a version whose
// journal tag the registry would not accept (a `+`, or too long), so a cut
// learns that before it builds, not after.
func Tag(version string) (string, error) {
	tag := version + TagSuffix
	if version == "" || !tagRE.MatchString(tag) {
		return "", fmt.Errorf("release version %q cannot carry a cut journal: %q is not a valid OCI tag", version, tag)
	}
	return tag, nil
}

// Location is the repository the journal of a cut is written to and read
// from: the first of the env's push destinations, sorted. "" when there is none.
func Location(destinations []string) string {
	sorted := make([]string, 0, len(destinations))
	for _, d := range destinations {
		if d = strings.TrimSpace(d); d != "" {
			sorted = append(sorted, d)
		}
	}
	if len(sorted) == 0 {
		return ""
	}
	sort.Strings(sorted)
	return sorted[0]
}

// Encode validates j and returns its canonical JSON.
func Encode(j Journal) ([]byte, error) {
	j.Schema = Schema
	sort.Slice(j.Images, func(a, b int) bool { return j.Images[a].Repository < j.Images[b].Repository })
	sort.Slice(j.State, func(a, b int) bool { return j.State[a].Name < j.State[b].Name })
	sort.Slice(j.Outputs, func(a, b int) bool { return j.Outputs[a].Name < j.Outputs[b].Name })
	if err := j.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(j)
}

// Decode parses and validates a journal document.
func Decode(raw []byte) (Journal, error) {
	var j Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return Journal{}, fmt.Errorf("cut journal: %w", err)
	}
	if j.Schema != Schema {
		return Journal{}, fmt.Errorf("cut journal: schema %q, this forge reads %q", j.Schema, Schema)
	}
	if err := j.validate(); err != nil {
		return Journal{}, err
	}
	return j, nil
}

// validate holds the document to what a resume may restore: state files are
// bare names in the state directory, outputs stay inside the output directory.
// A journal is read back from a registry, so nothing in it may name a path
// outside the two directories it is restored into.
func (j Journal) validate() error {
	if j.Version == "" || j.Env == "" || j.Commit == "" {
		return errors.New("cut journal: version, env and commit are required")
	}
	if len(j.Images) == 0 {
		return errors.New("cut journal: records no image")
	}
	for _, img := range j.Images {
		if img.Repository == "" || !strings.HasPrefix(img.Digest, "sha256:") {
			return fmt.Errorf("cut journal: incomplete image %+v", img)
		}
	}
	for _, f := range j.State {
		if !StateFileName(f.Name) {
			return fmt.Errorf("cut journal: state file %q is not a build-state file name", f.Name)
		}
	}
	for _, f := range j.Outputs {
		clean := path.Clean(f.Name)
		if f.Name == "" || clean != f.Name || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("cut journal: output %q is not a path inside the build output directory", f.Name)
		}
	}
	return nil
}

// StateFileName reports whether name is a build-state file a cut harvests:
// a bare `build-*.json` or `sourcemaps-*.json`.
func StateFileName(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) || name != path.Base(name) || !strings.HasSuffix(name, ".json") {
		return false
	}
	return strings.HasPrefix(name, "build-") || strings.HasPrefix(name, "sourcemaps-")
}

// Expect is the cut a journal must describe to be resumed by it.
type Expect struct {
	Project, Env, Version, ForgeVersion string
	Commit, Tree                        string
	Dirty                               bool
}

// Mismatches lists every way j does not describe the cut want would make:
// empty means the journal's images are this cut's images, as far as the
// journal can say. The registry's side (does each tag still name its digest)
// is CheckImages.
func (j Journal) Mismatches(want Expect) []string {
	var out []string
	if j.Version != want.Version {
		out = append(out, fmt.Sprintf("it records release %s, not %s", j.Version, want.Version))
	}
	if j.Env != want.Env {
		out = append(out, fmt.Sprintf("it was cut for env %s, not %s", j.Env, want.Env))
	}
	if want.Project != "" && j.Project != want.Project {
		out = append(out, fmt.Sprintf("it was cut by project %s, not %s", j.Project, want.Project))
	}
	if j.Dirty {
		out = append(out, "it was built from a dirty checkout, so no commit names its bytes")
	}
	if want.Dirty {
		out = append(out, "this checkout is dirty, so it cannot prove it is the source the images were built from")
	}
	if want.Commit == "" || j.Commit != want.Commit {
		out = append(out, fmt.Sprintf("it was built from commit %s and this checkout is at %s", short(j.Commit), short(want.Commit)))
	} else if j.Tree != "" && want.Tree != "" && j.Tree != want.Tree {
		out = append(out, fmt.Sprintf("it was built from tree %s and this checkout's tree is %s", short(j.Tree), short(want.Tree)))
	}
	if j.ForgeVersion != want.ForgeVersion {
		out = append(out, fmt.Sprintf("it was built by forge %s and this is forge %s", j.ForgeVersion, want.ForgeVersion))
	}
	return out
}

func short(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
