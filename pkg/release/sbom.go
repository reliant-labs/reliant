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
	Status    string `json:"status,omitempty"`    // ok | unavailable
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
)

// SBOMFormatCycloneDXJSON is the format forge's own generator writes.
const SBOMFormatCycloneDXJSON = "cyclonedx-json@1.6"

// HasSBOM reports whether the ref names stored SBOM bytes. An
// [SBOMStatusUnavailable] ref, which records only that there is none, does
// not.
func (s *SBOMRef) HasSBOM() bool { return s != nil && s.Digest != "" }

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
// existing is not modified; the result has its own Artifacts map.
func BackfillSBOMs(existing, requested Release) (Release, bool) {
	out := existing
	changed := false
	for name, a := range existing.Artifacts {
		req, ok := requested.Artifacts[name]
		if !ok || req.SBOM == nil || a.SBOM.HasSBOM() {
			continue
		}
		if a.SBOM != nil && !req.SBOM.HasSBOM() {
			// Both say "none"; the first reason recorded stands.
			continue
		}
		if !changed {
			out.Artifacts = make(map[string]Artifact, len(existing.Artifacts))
			for n, v := range existing.Artifacts {
				out.Artifacts[n] = v
			}
			changed = true
		}
		ref := *req.SBOM
		a.SBOM = &ref
		out.Artifacts[name] = a
	}
	return out, changed
}

// WithoutSBOMs returns r with every artifact's SBOM cleared, for a document
// an older decoder reads strictly (a snapshot release's CutRelease document).
// r is not modified.
func (r Release) WithoutSBOMs() Release {
	if len(r.Artifacts) == 0 {
		return r
	}
	out := r
	out.Artifacts = make(map[string]Artifact, len(r.Artifacts))
	for n, a := range r.Artifacts {
		a.SBOM = nil
		out.Artifacts[n] = a
	}
	return out
}
