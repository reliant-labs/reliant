package vulnscan

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// FindingsStore is the machine ledger as a scan of a release needs it: the
// SBOM bytes a release names, and the scans of them. *ledgerfile.Store
// satisfies it.
type FindingsStore interface {
	GetSBOM(digest string) ([]byte, error)
	PutFindings(r ledgerfile.FindingsRecord) error
	ListFindings(sbomDigest string) ([]ledgerfile.FindingsRecord, error)
}

// ShipsCode reports whether an artifact is code that ships, which an SBOM
// describes and the gate covers: an image, a host binary, an npm package or
// a Go module. A git source pin (a frontend built from another repository)
// names a commit, not bytes, and a frontend's source maps are debugging data,
// so neither gets an SBOM at the cut, and neither leaves a release "not
// scanned". It is the rule the cut's SBOM step (internal/cli/build_sbom.go,
// sbomDescribes) applies, stated where the gate reads it.
func ShipsCode(name string, art release.Artifact) bool {
	if _, maps := release.SourcemapsFrontend(name); maps {
		return false
	}
	switch art.Kind {
	case release.KindOCI, release.KindFile, release.KindNPM, release.KindGoModule:
		return true
	}
	return false
}

// Record wraps a scan of one SBOM in the ledger's envelope; the scan itself
// is the body.
func Record(sbomDigest string, s *Scan) (ledgerfile.FindingsRecord, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return ledgerfile.FindingsRecord{}, err
	}
	return ledgerfile.FindingsRecord{
		SBOMDigest:     sbomDigest,
		Scanner:        s.Scanner,
		ScannerVersion: s.ScannerVersion,
		DBSchema:       s.DBSchema,
		DBBuiltAt:      s.DBBuiltAt,
		ScannedAt:      s.ScannedAt,
		Body:           body,
	}, nil
}

// ScanOf reads a stored record's body back into a Scan. A body some other
// writer stored under another schema is an error, never an empty scan.
func ScanOf(r ledgerfile.FindingsRecord) (*Scan, error) {
	var s Scan
	if err := json.Unmarshal(r.Body, &s); err != nil {
		return nil, fmt.Errorf("findings for %s (db %s): %w", r.SBOMDigest, r.DBBuiltAt.Format("2006-01-02T15:04Z"), err)
	}
	if s.Schema != ScanSchema {
		return nil, fmt.Errorf("findings for %s (db %s) are %q, not %s", r.SBOMDigest, r.DBBuiltAt.Format("2006-01-02T15:04Z"), s.Schema, ScanSchema)
	}
	if s.Findings == nil {
		s.Findings = []Finding{}
	}
	return &s, nil
}

// Rescan says when ScanRelease runs the scanner instead of reading the
// ledger.
type Rescan int

const (
	// RescanNever only reads what the ledger holds.
	RescanNever Rescan = iota
	// RescanIfNewer rescans an SBOM never scanned, or last scanned with an
	// older database than the one on this machine (`forge vuln list`: the
	// machine ledger's rescan on read).
	RescanIfNewer
	// RescanAlways scans every SBOM again (`forge vuln scan --release`).
	RescanAlways
)

// ReleaseScan is what ScanRelease learned about a release.
type ReleaseScan struct {
	// Artifacts are the release's artifacts, sorted by name.
	Artifacts []ArtifactScan
	// Notes are what the reader should know but that changed no verdict:
	// a rescan that failed while an older scan stood in for it.
	Notes []string
	// Rescanned counts the SBOMs scanned (and stored) by this call.
	Rescanned int
}

// ScanRelease gathers the latest scan of every artifact of rel, rescanning
// per policy and storing each new scan in the ledger.
//
// local is the database on this machine (DB.Status); scanner may be nil when
// there is nothing to scan with, which makes every policy RescanNever. A
// rescan is attempted only against a local database that is present and not
// stale: a stale one could only produce a stale verdict, and grype refuses it
// anyway.
func ScanRelease(ctx context.Context, store FindingsStore, scanner *Scanner, local Status, rel *release.Release, policy Rescan) (ReleaseScan, error) {
	var out ReleaseScan
	names := make([]string, 0, len(rel.Artifacts))
	for name, art := range rel.Artifacts {
		// An artifact that ships no code (a source pin, source maps) has
		// no SBOM by design (ShipsCode): it is not part of the coverage.
		if ShipsCode(name, art) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	canScan := scanner != nil && local.Present && !local.Stale(scanner.DB.now())
	for _, name := range names {
		art := rel.Artifacts[name]
		a := ArtifactScan{Artifact: name}
		if !art.SBOM.HasSBOM() {
			a.Reason = "no SBOM"
			if art.SBOM != nil && art.SBOM.Reason != "" {
				a.Reason = "no SBOM: " + art.SBOM.Reason
			}
			out.Artifacts = append(out.Artifacts, a)
			continue
		}
		a.SBOMDigest = art.SBOM.Digest
		records, err := store.ListFindings(a.SBOMDigest)
		if err != nil {
			return out, err
		}
		for _, r := range records {
			s, err := ScanOf(r)
			if err != nil {
				return out, err
			}
			a.History = append(a.History, s)
		}
		if n := len(a.History); n > 0 {
			a.Latest = a.History[n-1]
		}
		due := false
		switch policy {
		case RescanAlways:
			due = true
		case RescanIfNewer:
			due = a.Latest == nil || local.BuiltAt.After(a.Latest.DBBuiltAt)
		}
		if due && canScan {
			s, err := scanAndStore(ctx, store, scanner, a.SBOMDigest)
			if err != nil {
				if a.Latest == nil {
					a.Reason = "scan failed: " + err.Error()
				} else {
					out.Notes = append(out.Notes, fmt.Sprintf("%s: rescan failed (%v); showing the scan with the database built %s", name, err, a.Latest.DBBuiltAt.Format("2006-01-02")))
				}
			} else {
				a.History = append(a.History, s)
				a.Latest = s
				out.Rescanned++
			}
		}
		if a.Latest == nil && a.Reason == "" {
			a.Reason = "never scanned"
		}
		out.Artifacts = append(out.Artifacts, a)
	}
	return out, nil
}

func scanAndStore(ctx context.Context, store FindingsStore, scanner *Scanner, digest string) (*Scan, error) {
	data, err := store.GetSBOM(digest)
	if err != nil {
		return nil, err
	}
	s, err := scanner.ScanSBOM(ctx, data)
	if err != nil {
		return nil, err
	}
	rec, err := Record(digest, s)
	if err != nil {
		return nil, err
	}
	if err := store.PutFindings(rec); err != nil {
		return nil, fmt.Errorf("store the scan of %s: %w", digest, err)
	}
	return s, nil
}
