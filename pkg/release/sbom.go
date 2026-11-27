package release

import "fmt"

// SBOMRef records the software bill of materials forge produced (or was
// handed) for one artifact. It is EVIDENCE about the artifact, never part of
// its identity: [Release.SameContent] ignores it, so a re-cut of the same
// digests whose SBOM a newer generator wrote is the same release.
//
// The bytes are not here. Digest names them, and they live where the
// artifact's runtime keeps them: an OCI referrer beside an image (Referrer),
// the machine ledger's content-addressed SBOM store, and the control plane's
// copy. Every reader verifies the bytes against Digest.
//
// Source and Status are an OPEN vocabulary on read: the constants below are
// what this forge writes, and a reader tolerates a value a newer forge adds
// rather than refusing the release that carries it, because a ledger line
// nobody can read fails every command that reads the ledger.
type SBOMRef struct {
	Digest    string `json:"digest,omitempty"`    // sha256 of the SBOM bytes
	Format    string `json:"format,omitempty"`    // "cyclonedx-json@1.6"
	Generator string `json:"generator,omitempty"` // "syft@v1.x.y"
	Source    string `json:"source,omitempty"`    // image | registry | binary | lockfile | ci-provided
	Referrer  string `json:"referrer,omitempty"`  // repo@sha256:… of the referrer manifest, when attached
	Status    string `json:"status,omitempty"`    // ok | unavailable | not_applicable
	Reason    string `json:"reason,omitempty"`
}

// SBOMRef.Source values: what the SBOM was generated from.
const (
	// SBOMSourceImage is an image forge built, read from the local build.
	SBOMSourceImage = "image"
	// SBOMSourceRegistry is an image read back from its registry by digest.
	SBOMSourceRegistry = "registry"
	// SBOMSourceBinary is a host binary forge built.
	SBOMSourceBinary = "binary"
	// SBOMSourceLockfile is the lockfiles of a source tree (a static site,
	// a version-only record).
	SBOMSourceLockfile = "lockfile"
	// SBOMSourceCIProvided is an SBOM an outside pipeline produced and
	// handed to `forge release record --sbom`.
	SBOMSourceCIProvided = "ci-provided"
)

// SBOMRef.Status values.
const (
	// SBOMStatusOK is an SBOM that was produced and stored: Digest is set.
	SBOMStatusOK = "ok"
	// SBOMStatusUnavailable records that no SBOM could be produced, and
	// Reason says why, so a reader can say "not scanned" with a cause.
	SBOMStatusUnavailable = "unavailable"
	// SBOMStatusNotApplicable records that the artifact is not code that
	// ships, so no SBOM describes it ([SBOMApplicable] is false), and
	// Reason says what it is instead. It names no SBOM and never counts as
	// a missing one.
	//
	// It RECORDS the rule; it is never an input to it. Coverage and the
	// gates decide applicability from the artifact by [SBOMApplicable], so
	// a ref claiming not_applicable on an image cannot take that image out
	// of a gate. A forge that predates this value reads it as an unknown
	// status it tolerates (see [SBOMRef]), and already leaves the same
	// artifacts out of coverage by the same rule.
	SBOMStatusNotApplicable = "not_applicable"
)

// SBOMFormatCycloneDXJSON is the format forge's own generator writes.
const SBOMFormatCycloneDXJSON = "cyclonedx-json@1.6"

// Why an artifact gets no SBOM: the Reason of an [SBOMStatusNotApplicable]
// ref, one per class [SBOMNotApplicable] knows.
const (
	// SBOMNotApplicableSourcemaps is a frontend's source maps
	// ([SourcemapsArtifactName]): debugging data, not code that runs.
	SBOMNotApplicableSourcemaps = "source maps are debugging data, not code that runs"
	// SBOMNotApplicableGitSource is a [KindGit] source pin: it names a
	// commit, not bytes that ship. A site built from it is its own artifact,
	// with its own SBOM (of the source's lockfile).
	SBOMNotApplicableGitSource = "a git source pin names a commit, not bytes that ship"
)

// SBOMApplicable reports whether an SBOM describes the artifact: whether it
// is code that ships (an image, a host binary, an npm package or a Go
// module). It is the one rule the cut records SBOMs by and every coverage
// count and gate judges by, so "N of N applicable" means the same thing
// wherever it is printed.
func SBOMApplicable(name string, a Artifact) bool {
	_, na := SBOMNotApplicable(name, a)
	return !na
}

// SBOMNotApplicable reports whether no SBOM describes the artifact, and why
// (one of the SBOMNotApplicable* reasons). The inverse of [SBOMApplicable].
func SBOMNotApplicable(name string, a Artifact) (reason string, ok bool) {
	if _, maps := SourcemapsFrontend(name); maps {
		return SBOMNotApplicableSourcemaps, true
	}
	switch a.Kind {
	case KindOCI, KindFile, KindNPM, KindGoModule:
		return "", false
	case KindGit:
		return SBOMNotApplicableGitSource, true
	}
	return fmt.Sprintf("an artifact of kind %q is not code that ships", a.Kind), true
}

// NotApplicableSBOMRef is the ref recorded on an artifact no SBOM describes,
// and false for one an SBOM does.
func NotApplicableSBOMRef(name string, a Artifact) (SBOMRef, bool) {
	reason, ok := SBOMNotApplicable(name, a)
	if !ok {
		return SBOMRef{}, false
	}
	return SBOMRef{Status: SBOMStatusNotApplicable, Reason: reason}, true
}

// HasSBOM reports whether the ref names stored SBOM bytes. An
// [SBOMStatusUnavailable] or [SBOMStatusNotApplicable] ref, which records
// only that there is none, does not.
func (s *SBOMRef) HasSBOM() bool { return s != nil && s.Digest != "" }

// NotApplicable reports whether the ref records that no SBOM applies.
func (s *SBOMRef) NotApplicable() bool { return s != nil && s.Status == SBOMStatusNotApplicable }

// validate checks the invariants every value of the vocabulary shares. It
// is deliberately silent on Source and Status values it does not know (see
// the type doc).
func (s *SBOMRef) validate() error {
	if s == nil {
		return nil
	}
	if s.Digest != "" && !ValidDigest(s.Digest) {
		return fmt.Errorf("sbom digest %q is not a canonical sha256 digest", s.Digest)
	}
	if s.Status == SBOMStatusOK && s.Digest == "" {
		return fmt.Errorf("sbom status %q needs a digest", SBOMStatusOK)
	}
	if s.Referrer != "" && s.Digest == "" {
		return fmt.Errorf("sbom referrer %q needs a digest", s.Referrer)
	}
	if s.Status == SBOMStatusNotApplicable && s.Digest != "" {
		return fmt.Errorf("sbom status %q names no SBOM, but carries digest %s", SBOMStatusNotApplicable, s.Digest)
	}
	return nil
}

// BackfillSBOMs returns existing with each artifact's SBOM taken from
// requested where existing records none, and whether anything was filled.
// It is the half of a re-cut that is not identity: when
// [Release.SameContent] holds, the newer cut may still know an SBOM the
// record lacks (a release cut before forge recorded SBOMs, or one whose
// generation failed then).
//
// It NEVER replaces an SBOM the record already names: the first one recorded
// is what every scan and gate verdict since was computed against. A ref that
// only says [SBOMStatusUnavailable] names no SBOM, so a later ref that names
// one fills it.
//
// The same rule holds within a recorded SBOM: when requested names the SAME
// SBOM (equal Digest) and knows where it was attached beside its artifact
// (Referrer) while the record does not, the Referrer is filled in. A
// recorded Referrer is never replaced, and a Referrer for another SBOM is
// never taken.
//
// The release's own [Release.SourceSBOM] follows the same rule.
//
// existing is not modified; the result has its own Artifacts map.
func BackfillSBOMs(existing, requested Release) (Release, bool) {
	out := existing
	changed := false
	if ref, ok := backfillRef(existing.SourceSBOM, requested.SourceSBOM); ok {
		out.SourceSBOM = ref
		changed = true
	}
	artifactsCopied := false
	for name, a := range existing.Artifacts {
		req, ok := requested.Artifacts[name]
		if !ok {
			continue
		}
		ref, fill := backfillRef(a.SBOM, req.SBOM)
		if !fill {
			continue
		}
		if !artifactsCopied {
			out.Artifacts = make(map[string]Artifact, len(existing.Artifacts))
			for n, v := range existing.Artifacts {
				out.Artifacts[n] = v
			}
			artifactsCopied = true
		}
		changed = true
		a.SBOM = ref
		out.Artifacts[name] = a
	}
	return out, changed
}

// backfillRef is the one fill-never-replace rule: a copy of requested when
// the record should take it, and whether it should.
func backfillRef(existing, requested *SBOMRef) (*SBOMRef, bool) {
	if requested == nil {
		return nil, false
	}
	if existing.HasSBOM() {
		if existing.Referrer == "" && requested.Referrer != "" && requested.Digest == existing.Digest {
			ref := *existing
			ref.Referrer = requested.Referrer
			return &ref, true
		}
		return nil, false
	}
	if existing != nil && !requested.HasSBOM() {
		// Both say "none"; the first reason recorded stands.
		return nil, false
	}
	ref := *requested
	return &ref, true
}

// WithoutSBOMs returns r with every SBOM cleared, each artifact's and the
// release's SourceSBOM, for a document an older decoder reads strictly (a
// snapshot release's CutRelease document). r is not modified.
func (r Release) WithoutSBOMs() Release {
	out := r
	out.SourceSBOM = nil
	if len(r.Artifacts) == 0 {
		return out
	}
	out.Artifacts = make(map[string]Artifact, len(r.Artifacts))
	for n, a := range r.Artifacts {
		a.SBOM = nil
		out.Artifacts[n] = a
	}
	return out
}
