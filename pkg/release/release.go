package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ErrInvalid is wrapped by every validation failure in this package, so a
// backend can map "the caller sent a malformed release" to one status code
// without string-matching.
var ErrInvalid = errors.New("invalid release ledger entry")

// ErrReleaseConflict is returned when a version label is re-cut with a
// DIFFERENT artifact set. A version that meant two digest sets would void
// every guarantee promotion rests on, so it is refused rather than resolved.
var ErrReleaseConflict = errors.New("release version already names a different artifact set")

// SharedVariant is the sole Digests key of a Mode [ModeShared] artifact: one
// digest serves every environment.
const SharedVariant = "*"

// digestPattern is the canonical OCI content address. A tag is a mutable
// pointer and would silently void "the bytes that passed staging are the
// bytes in prod", so nothing else is admitted where a digest is expected.
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ValidDigest reports whether d is a canonical `sha256:<64 hex>` digest.
func ValidDigest(d string) bool { return digestPattern.MatchString(d) }

// ─── Kind ────────────────────────────────────────────────────────────────────

// Kind says WHAT an artifact is, and therefore how it is addressed and
// verified. Closed: see the package doc.
type Kind string

const (
	// KindOCI is a container image, addressed by digest alone.
	KindOCI Kind = "oci"
	// KindNPM is an npm package, addressed by version + integrity.
	KindNPM Kind = "npm"
	// KindGoModule is a Go module, addressed by version + go.sum hash.
	KindGoModule Kind = "gomod"
	// KindFile is a published file, addressed by version/URI + content hash.
	KindFile Kind = "file"
	// KindGit is source built at deploy time (a forge.GitSource frontend),
	// addressed by the commit its declared ref resolved to.
	KindGit Kind = "git"
)

// Kinds is the closed set, in a stable order (for error messages and for
// the hosted schema's CHECK, which must list exactly these).
var Kinds = []Kind{KindOCI, KindNPM, KindGoModule, KindFile, KindGit}

// Valid reports whether k is one of [Kinds].
func (k Kind) Valid() bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// UnmarshalJSON refuses an empty or unknown kind.
func (k *Kind) UnmarshalJSON(data []byte) error {
	return decodeClosed(data, "artifact kind", func(s string) bool { return Kind(s).Valid() }, kindNames(), (*string)(k))
}

func kindNames() []string {
	out := make([]string, len(Kinds))
	for i, k := range Kinds {
		out[i] = string(k)
	}
	return out
}

// ─── Mode ────────────────────────────────────────────────────────────────────

// Mode says how an artifact's identity is PINNED — a separate axis from
// [Kind]. Closed.
type Mode string

const (
	// ModeShared: built once, one identity, promoted byte-identical to
	// every environment. OCI digests live under [SharedVariant].
	ModeShared Mode = "shared"
	// ModeVariant: genuinely different per-environment builds (a
	// NEXT_PUBLIC_* baked at build time). Digests is keyed by variant name.
	ModeVariant Mode = "variant"
	// ModeSource: built from a pinned commit at deploy time; the identity
	// is [Artifact.Source]. Only [KindGit] uses it.
	ModeSource Mode = "source"
)

// Modes is the closed set.
var Modes = []Mode{ModeShared, ModeVariant, ModeSource}

// Valid reports whether m is one of [Modes].
func (m Mode) Valid() bool {
	for _, known := range Modes {
		if m == known {
			return true
		}
	}
	return false
}

// UnmarshalJSON refuses an empty or unknown mode.
func (m *Mode) UnmarshalJSON(data []byte) error {
	names := make([]string, len(Modes))
	for i, mode := range Modes {
		names[i] = string(mode)
	}
	return decodeClosed(data, "artifact mode", func(s string) bool { return Mode(s).Valid() }, names, (*string)(m))
}

// decodeClosed is the one strict decoder the three closed enums share.
func decodeClosed(data []byte, what string, valid func(string) bool, names []string, out *string) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("%w: %s must be a string: %v", ErrInvalid, what, err)
	}
	if !valid(s) {
		return fmt.Errorf("%w: unknown %s %q (expected one of %s) — refusing to decode it as a default",
			ErrInvalid, what, s, strings.Join(names, ", "))
	}
	*out = s
	return nil
}

// ─── Artifact / Release ──────────────────────────────────────────────────────

// Git is a release's source provenance. Best-effort: empty on a non-git tree.
type Git struct {
	Commit string `json:"commit,omitempty"`
	Tag    string `json:"tag,omitempty"`
	// Dirty means the release was cut from a tree with uncommitted changes:
	// its bytes correspond to no reviewable commit.
	Dirty bool `json:"dirty,omitempty"`
}

// Source is the content-addressed identity of a source-built artifact.
type Source struct {
	// Repo is the repository the source is fetched from.
	Repo string `json:"repo"`
	// Ref is the tag/branch/sha as DECLARED (what a human edits).
	Ref string `json:"ref"`
	// Subdir is the path within the repo the component builds from.
	Subdir string `json:"subdir,omitempty"`
	// Commit is what Ref resolved to when the release was cut — the actual
	// content address.
	Commit string `json:"commit,omitempty"`
}

// Artifact is ONE thing a release froze. Which fields carry the identity
// depends on Kind and Mode; [Artifact.Validate] is the rule.
type Artifact struct {
	Kind Kind `json:"kind"`
	Mode Mode `json:"mode"`
	// Digests maps a variant key → canonical digest. OCI only: for
	// [ModeShared] the only key is [SharedVariant].
	Digests map[string]string `json:"digests,omitempty"`
	// Platforms is the OS/arch set the image manifest advertises.
	// Informational, and NOT part of the artifact's identity.
	Platforms []string `json:"platforms,omitempty"`
	// Source is set exactly for [ModeSource].
	Source *Source `json:"source,omitempty"`
	// Version is how a consumer ASKS for a non-OCI artifact.
	Version string `json:"version,omitempty"`
	// Integrity is how a consumer KNOWS they got the right bytes, in the
	// ecosystem's native hash format. Stored verbatim.
	Integrity string `json:"integrity,omitempty"`
	// URI locates the artifact when name+version does not (a download URL,
	// a non-default registry, or the registry an image was pushed to).
	URI string `json:"uri,omitempty"`
	// BuiltFrom is the git checkout the artifact's build command ran in, when
	// that is a checkout forge can name — a ShellBuild's cwd. Informational
	// provenance like Platforms, and NOT part of the identity: the digest
	// names the bytes, this says where they were built.
	BuiltFrom *BuildSource `json:"built_from,omitempty"`
}

// BuildSource is the checkout an artifact was built in: its repository, HEAD
// and dirty flag at build time. Like [Provenance.Dirty] it is a claim the
// building machine makes, recorded as evidence.
//
// It is distinct from [Source], which PINS a source-mode artifact (the commit
// IS its identity). A BuildSource rides beside an image's digest and explains
// it; changing it does not change what the release ships.
type BuildSource struct {
	// Repo is the canonical remote ("github.com/org/repo"); "" when the
	// checkout had none.
	Repo string `json:"repo,omitempty"`
	// Commit is HEAD, full hex.
	Commit string `json:"commit"`
	// Dirty: the checkout had uncommitted changes when the build started.
	Dirty bool `json:"dirty,omitempty"`
}

// Validate enforces the per-kind addressing rules. name is used only in the
// error.
func (a Artifact) Validate(name string) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: artifact %q: %s", ErrInvalid, name, fmt.Sprintf(format, args...))
	}
	if name == "" {
		return fmt.Errorf("%w: artifact name is required", ErrInvalid)
	}
	if !a.Kind.Valid() {
		return bad("unknown kind %q (expected one of %s)", a.Kind, strings.Join(kindNames(), ", "))
	}
	if !a.Mode.Valid() {
		return bad("unknown mode %q", a.Mode)
	}
	switch a.Kind {
	case KindOCI:
		switch a.Mode {
		case ModeShared:
			if len(a.Digests) != 1 || a.Digests[SharedVariant] == "" {
				return bad("a shared image carries exactly one digest, under %q", SharedVariant)
			}
		case ModeVariant:
			if len(a.Digests) == 0 {
				return bad("a variant image carries at least one digest")
			}
			if _, ok := a.Digests[SharedVariant]; ok {
				return bad("a variant image may not use the shared key %q", SharedVariant)
			}
		default:
			return bad("an image is mode shared or variant, not %q", a.Mode)
		}
		for variant, d := range a.Digests {
			if variant == "" {
				return bad("empty variant key")
			}
			if !ValidDigest(d) {
				return bad("digest %q for variant %q is not a canonical sha256 digest", d, variant)
			}
		}
		if a.Source != nil {
			return bad("an image carries no source pin")
		}
	case KindGit:
		if a.Mode != ModeSource {
			return bad("a git artifact is mode source, not %q", a.Mode)
		}
		if a.Source == nil || a.Source.Repo == "" || a.Source.Ref == "" {
			return bad("a git artifact needs a source repo and ref")
		}
		if len(a.Digests) != 0 {
			return bad("a git artifact carries no digest")
		}
	default: // npm, gomod, file
		if a.Mode != ModeShared {
			return bad("a %s artifact is mode shared, not %q", a.Kind, a.Mode)
		}
		if len(a.Digests) != 0 {
			return bad("a %s artifact carries no image digest", a.Kind)
		}
		if a.Source != nil {
			return bad("a %s artifact carries no source pin", a.Kind)
		}
		if a.Version == "" && a.URI == "" {
			return bad("a %s artifact must be addressable: version or uri is required", a.Kind)
		}
	}
	if a.Mode == ModeSource && a.Kind != KindGit {
		return bad("mode source is only for kind git")
	}
	if a.BuiltFrom != nil {
		if a.Kind == KindGit {
			return bad("a git artifact's commit is its source pin; it carries no built_from")
		}
		if !ValidObjectID(a.BuiltFrom.Commit) {
			return bad("built_from commit %q is not a full git object id", a.BuiltFrom.Commit)
		}
	}
	return nil
}

// SharedDigest returns the digest of a shared OCI artifact. Every other
// artifact answers ("", false) — that is the guard that keeps packages and
// source pins out of a pod spec without every caller remembering to check.
func (a Artifact) SharedDigest() (string, bool) {
	if a.Kind != KindOCI || a.Mode != ModeShared {
		return "", false
	}
	d, ok := a.Digests[SharedVariant]
	return d, ok && d != ""
}

// sameIdentity compares the fields that name the bytes. Platforms and
// BuiltFrom are informational and deliberately excluded: re-stating a release
// with a different platform list, or the same digests built in another
// checkout, has not claimed different bytes.
func (a Artifact) sameIdentity(b Artifact) bool {
	if a.Kind != b.Kind || a.Mode != b.Mode || a.Version != b.Version ||
		a.Integrity != b.Integrity || a.URI != b.URI {
		return false
	}
	if len(a.Digests) != len(b.Digests) {
		return false
	}
	for k, v := range a.Digests {
		if b.Digests[k] != v {
			return false
		}
	}
	switch {
	case a.Source == nil && b.Source == nil:
		return true
	case a.Source == nil || b.Source == nil:
		return false
	default:
		return *a.Source == *b.Source
	}
}

// Release is an immutable, content-addressed set of artifacts under a
// version label.
type Release struct {
	// Version is the label ("v1.4.0"). Releases are ADDRESSED by it, in
	// every backend.
	Version string `json:"release"`
	// Project is the forge project (forge.yaml `name`) the version belongs
	// to. A version label is unique within a project, not across an org:
	// two projects may both cut v1.0.0. "" is a release recorded before
	// releases carried a project.
	Project string `json:"project,omitempty"`
	// Git is the legacy provenance view, kept filled from Provenance (see
	// SetProvenance) so readers that predate Provenance keep working.
	Git Git `json:"git"`
	// Provenance is where the release's images were built from. Nil on a
	// release recorded before it existed.
	Provenance *Provenance `json:"provenance,omitempty"`
	// Commits is the source history this release adds over the release it
	// was cut after ([CaptureCommitRange]). Nil when none was recorded: a
	// non-git tree, a forge that predates it, or a capture that failed. Like
	// Provenance it is evidence about the release and not part of its
	// identity — [Release.SameContent] ignores it.
	Commits *CommitRange `json:"commits,omitempty"`
	// CreatedAt is when the release was cut.
	CreatedAt time.Time `json:"created_at"`
	// CreatedBy is who cut it, when the backend knows (a hosted ledger
	// does; a file does not).
	CreatedBy string `json:"created_by,omitempty"`
	// Artifacts maps the bare artifact name (an image name, a package name,
	// a frontend name) → its identity.
	Artifacts map[string]Artifact `json:"artifacts"`
	// Catalog and SnapshotIntents freeze complete composition intent. Absent on
	// historical releases, which remain readable but are not snapshot candidates.
	Catalog         map[ArtifactID]SnapshotArtifact `json:"catalog,omitempty"`
	SnapshotIntents map[string]string               `json:"snapshot_intents,omitempty"`
	// Run is the pipeline attempt that cut the release, or the zero Run
	// when a human cut it. It is NOT part of the release's identity:
	// [Release.SameContent] ignores it, so a CI retry that re-cuts the
	// same bytes under a new run id is still an idempotent re-cut.
	Run Run `json:"run,omitempty"`
}

// SetProvenance records where the release was built from, and keeps the
// legacy Git view in step with it. A release is shared — promoted between
// envs, imported, shown to every member — so it never carries the build
// machine's checkout path ([Provenance.ForHosted]).
func (r *Release) SetProvenance(p Provenance) {
	p = p.ForHosted()
	r.Provenance = &p
	r.Git = p.Git()
}

// Validate checks the release and every artifact in it.
func (r Release) Validate() error {
	if strings.TrimSpace(r.Version) == "" {
		return fmt.Errorf("%w: release version is required", ErrInvalid)
	}
	if r.Provenance != nil {
		if err := r.Provenance.Validate(); err != nil {
			return fmt.Errorf("release %q: %w", r.Version, err)
		}
	}
	if r.Commits != nil {
		if err := r.Commits.Validate(); err != nil {
			return fmt.Errorf("release %q: %w", r.Version, err)
		}
		if r.Commits.BaseRelease == r.Version {
			return fmt.Errorf("%w: release %q names itself as the base of its commit range", ErrInvalid, r.Version)
		}
	}
	if err := r.Run.Validate(); err != nil {
		return fmt.Errorf("release %q: %w", r.Version, err)
	}
	if len(r.SnapshotIntents) > 0 || len(r.Catalog) > 0 {
		if len(r.SnapshotIntents) == 0 || len(r.Catalog) == 0 {
			return fmt.Errorf("%w: snapshot intents and catalog must both be complete", ErrInvalid)
		}
		for env, d := range r.SnapshotIntents {
			if env == "" || !ValidDigest(d) {
				return fmt.Errorf("%w: invalid snapshot intent", ErrInvalid)
			}
		}
		for id, a := range r.Catalog {
			actual, err := ArtifactIdentity(a)
			if err != nil {
				return err
			}
			if actual != id {
				return fmt.Errorf("%w: catalog identity mismatch", ErrInvalid)
			}
		}
	}
	if len(r.Artifacts) == 0 && len(r.Catalog) == 0 {
		return fmt.Errorf("%w: release %q names no artifacts — a release that names nothing cannot be promoted", ErrInvalid, r.Version)
	}
	for _, name := range r.ArtifactNames() {
		if err := r.Artifacts[name].Validate(name); err != nil {
			return fmt.Errorf("release %q: %w", r.Version, err)
		}
	}
	return nil
}

// SameContent reports whether two releases name the same bytes: the same
// artifact set with the same identities. Provenance and timestamps are not
// compared — a re-cut from CI's retry carries a fresh CreatedAt and is still
// the same release.
func (r Release) SameContent(other Release) bool {
	// Snapshot intent includes binding/spec/policy changes, even with identical
	// repository pins. Freeze full catalog attestations for snapshot recuts.
	a, _ := semanticDigest("forge.release-intent/v1", struct {
		Catalog map[ArtifactID]SnapshotArtifact
		Intents map[string]string
	}{canonicalSnapshot(Snapshot{Artifacts: r.Catalog}).Artifacts, r.SnapshotIntents})
	b, _ := semanticDigest("forge.release-intent/v1", struct {
		Catalog map[ArtifactID]SnapshotArtifact
		Intents map[string]string
	}{canonicalSnapshot(Snapshot{Artifacts: other.Catalog}).Artifacts, other.SnapshotIntents})
	if a != b {
		return false
	}
	if len(r.Artifacts) != len(other.Artifacts) {
		return false
	}
	for name, a := range r.Artifacts {
		b, ok := other.Artifacts[name]
		if !ok || !a.sameIdentity(b) {
			return false
		}
	}
	return true
}

// ArtifactNames returns the artifact names, sorted.
func (r Release) ArtifactNames() []string {
	names := make([]string, 0, len(r.Artifacts))
	for name := range r.Artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SharedDigests is the image-name → digest pin set a promotion freezes:
// every shared OCI artifact, and nothing else.
func (r Release) SharedDigests() map[string]string {
	out := map[string]string{}
	for name, a := range r.Artifacts {
		if d, ok := a.SharedDigest(); ok {
			out[name] = d
		}
	}
	return out
}

// Sources is the frontend-name → source pin set a promotion freezes.
func (r Release) Sources() map[string]Source {
	out := map[string]Source{}
	for name, a := range r.Artifacts {
		if a.Mode == ModeSource && a.Source != nil {
			out[name] = *a.Source
		}
	}
	return out
}

// CheckRecut decides what re-cutting `existing` as `requested` means:
// nil when the two name the same bytes (an idempotent retry), and
// [ErrReleaseConflict] when they do not. Both backends call this, so "a
// version label names exactly one artifact set" is one rule.
func CheckRecut(existing, requested Release) error {
	if existing.SameContent(requested) {
		return nil
	}
	return fmt.Errorf("release %q: %w", requested.Version, ErrReleaseConflict)
}
