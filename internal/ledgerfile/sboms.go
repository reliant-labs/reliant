package ledgerfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The SBOM and findings store: the bytes a release's SBOMRef names, and what
// each vulnerability scan of them found.
//
//	<ledger>/<project-id>/
//	  sboms/sha256-<hex>.cdx.json      the SBOM bytes, content-addressed
//	  findings/sha256-<hex>/<db-built-at>_<scanner>@<scanner-version>.json
//	                                   one scan of that SBOM per scanner, scanner
//	                                   version and scanner DB build
//
// A findings file is keyed by everything that decides its result: the SBOM,
// the scanner and its version, and the DB build. Two scanners, or two
// versions of one, reading the same DB build are two pieces of evidence and
// get two files. The same scanner, version and DB build re-scanning the same
// SBOM is a deterministic re-evaluation of one question, and replaces its
// own file.
//
// The SBOM is ALWAYS written here, even when it is also attached to the
// image as an OCI referrer, so a scan never needs the network for bytes.
//
// Neither directory is append-only jsonl, and neither needs the ledger lock:
// every file is written whole to a temp file beside it and renamed into
// place, so a reader sees the old file or the new one, never a torn one. An
// SBOM's name is its digest, so two writers of one name write the same bytes;
// and every read re-hashes the bytes against the name, so a file that was
// damaged on disk is reported, never served.
const (
	sbomsDir    = "sboms"
	findingsDir = "findings"
	sbomSuffix  = ".cdx.json"
	// findingsTimeLayout is the DB-build part of a findings file's name:
	// UTC, fixed width, and free of the ':' a file name cannot carry on
	// every OS. Readers order by the records' own times, not by names.
	findingsTimeLayout = "20060102T150405Z"
	findingsSuffix     = ".json"
)

// ErrSBOMNotFound is GetSBOM naming a digest this ledger holds no SBOM for.
var ErrSBOMNotFound = errors.New("sbom not found in the machine ledger")

// ErrSBOMCorrupt is an SBOM file whose bytes no longer hash to its name.
var ErrSBOMCorrupt = errors.New("sbom in the machine ledger does not match its digest")

// sbomFileStem is the store's name for an SBOM digest: "sha256:<hex>"
// becomes "sha256-<hex>", the form the OCI tag fallback uses too.
func sbomFileStem(digest string) (string, error) {
	if !release.ValidDigest(digest) {
		return "", fmt.Errorf("%w: sbom digest %q is not a canonical sha256 digest", release.ErrInvalid, digest)
	}
	return strings.Replace(digest, ":", "-", 1), nil
}

func sbomDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// PutSBOM stores an SBOM's bytes under their sha256 digest and returns it —
// the value an artifact's release.SBOMRef.Digest records. Storing bytes the
// store already holds is a no-op; a stored file that no longer matches its
// digest is replaced with these bytes, which do.
func (s *Store) PutSBOM(data []byte) (digest string, err error) {
	if len(data) == 0 {
		return "", fmt.Errorf("%w: sbom is empty", release.ErrInvalid)
	}
	if !json.Valid(data) {
		return "", fmt.Errorf("%w: sbom is not JSON", release.ErrInvalid)
	}
	digest = sbomDigest(data)
	stem, err := sbomFileStem(digest)
	if err != nil {
		return "", err
	}
	path := s.path(sbomsDir, stem+sbomSuffix)
	if existing, err := os.ReadFile(path); err == nil && sbomDigest(existing) == digest { //nolint:gosec // path comes from Store.path
		return digest, nil
	}
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	return digest, nil
}

// GetSBOM returns the SBOM stored under digest, verified: ErrSBOMNotFound
// when there is none, ErrSBOMCorrupt when its bytes no longer hash to it.
func (s *Store) GetSBOM(digest string) ([]byte, error) {
	stem, err := sbomFileStem(digest)
	if err != nil {
		return nil, err
	}
	path := s.path(sbomsDir, stem+sbomSuffix)
	data, err := os.ReadFile(path) //nolint:gosec // path comes from Store.path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", digest, ErrSBOMNotFound)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if got := sbomDigest(data); got != digest {
		return nil, fmt.Errorf("%s holds %s: %w", path, got, ErrSBOMCorrupt)
	}
	return data, nil
}

// FindingsRecord is one vulnerability scan of one SBOM by one scanner version
// against one build of that scanner's database.
//
// It is an ENVELOPE: what the scan found is Body, whose schema belongs to the
// command that writes and reads it (`forge vuln`), not to this store. The
// envelope holds only what the store files by and what says whether a
// result is current: a newer DB build means a rescan is due.
type FindingsRecord struct {
	// SBOMDigest is the SBOM that was scanned.
	SBOMDigest string `json:"sbom_digest"`
	// Scanner and ScannerVersion name the tool ("grype", "v0.122.1"). Both
	// are part of the record's key: another scanner, or another version of
	// this one, is another record.
	Scanner        string `json:"scanner"`
	ScannerVersion string `json:"scanner_version,omitempty"`
	// DBSchema is the scanner database's schema version, and DBBuiltAt
	// when that database was built. DBBuiltAt is part of the record's key
	// and its primary order (ListFindings).
	DBSchema  string    `json:"db_schema,omitempty"`
	DBBuiltAt time.Time `json:"db_built_at"`
	// ScannedAt is when the scan ran.
	ScannedAt time.Time `json:"scanned_at"`
	// Body is the scan result, opaque here.
	Body json.RawMessage `json:"body"`
}

func (r FindingsRecord) validate() error {
	if _, err := sbomFileStem(r.SBOMDigest); err != nil {
		return err
	}
	if strings.TrimSpace(r.Scanner) == "" {
		return fmt.Errorf("%w: findings record names no scanner", release.ErrInvalid)
	}
	if r.DBBuiltAt.IsZero() {
		return fmt.Errorf("%w: findings record has no db_built_at", release.ErrInvalid)
	}
	if len(r.Body) == 0 || !json.Valid(r.Body) {
		return fmt.Errorf("%w: findings record body is not JSON", release.ErrInvalid)
	}
	return nil
}

// findingsFileName is a record's file name inside its SBOM's directory:
// "<db-built-at>_<scanner>@<scanner-version>.json" ("<db-built-at>_<scanner>.json"
// when the version is unknown). It is the record's whole key below the SBOM,
// so two records that differ in scanner, version or DB build never share a
// file.
func findingsFileName(r FindingsRecord) string {
	name := r.DBBuiltAt.UTC().Format(findingsTimeLayout) + "_" + findingsNamePart(r.Scanner)
	if r.ScannerVersion != "" {
		name += "@" + findingsNamePart(r.ScannerVersion)
	}
	return name + findingsSuffix
}

// findingsNamePart makes a scanner name or version safe in a file name.
// Flattening alone could map two values to one name ("a/b" and "a_b"), and
// that collision would overwrite evidence, so a value the flattening changed
// also carries a short hash of what it really was. A value already safe —
// every real one ("grype", "v0.122.1") — is used as it is.
func findingsNamePart(v string) string {
	safe := safeSegment(v)
	if safe == v {
		return safe
	}
	sum := sha256.Sum256([]byte(v))
	return safe + "-" + hex.EncodeToString(sum[:4])
}

func (s *Store) findingsPath(r FindingsRecord) (string, error) {
	stem, err := sbomFileStem(r.SBOMDigest)
	if err != nil {
		return "", err
	}
	// Joined after Store.path, which would flatten the '@': every part of
	// the name is already flattened by findingsNamePart.
	return filepath.Join(s.path(findingsDir, stem), findingsFileName(r)), nil
}

// PutFindings stores one scan, filed under its SBOM, scanner, scanner version
// and DB build. A scan by ANOTHER scanner or version never touches this one's
// file. A second scan by the SAME scanner and version against the same DB
// build replaces the first: that is a deterministic re-evaluation of the
// same question, not new evidence.
func (s *Store) PutFindings(r FindingsRecord) error {
	if err := r.validate(); err != nil {
		return err
	}
	path, err := s.findingsPath(r)
	if err != nil {
		return err
	}
	data, err := canonicalJSON(r)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// ListFindings returns every stored scan of an SBOM, ordered by DB build,
// then by when the scan ran, OLDEST first (and by file name between records
// equal in both, so the order is deterministic). An SBOM never scanned has
// none, and that is not an error.
func (s *Store) ListFindings(sbomDigest string) ([]FindingsRecord, error) {
	stem, err := sbomFileStem(sbomDigest)
	if err != nil {
		return nil, err
	}
	dir := s.path(findingsDir, stem)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), findingsSuffix) || strings.HasPrefix(e.Name(), ".") {
			continue // a temp file mid-rename, or something that is not ours
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]FindingsRecord, 0, len(names))
	for _, name := range names {
		r, err := readFindings(filepath.Join(dir, name), sbomDigest)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	// Stable over the name order above, which breaks the remaining ties.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].DBBuiltAt.Equal(out[j].DBBuiltAt) {
			return out[i].DBBuiltAt.Before(out[j].DBBuiltAt)
		}
		return out[i].ScannedAt.Before(out[j].ScannedAt)
	})
	return out, nil
}

// LatestFindings returns the last record in ListFindings' order: the scan
// against the newest DB build this ledger has scanned the SBOM with, and of
// several scans of that build (by different scanners or versions) the one
// that ran last. (nil, nil) when it was never scanned.
func (s *Store) LatestFindings(sbomDigest string) (*FindingsRecord, error) {
	all, err := s.ListFindings(sbomDigest)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	return &all[len(all)-1], nil
}

// readFindings decodes one findings file and checks it is filed where it
// says it belongs, so a file copied into the wrong SBOM's directory, or
// under another scanner's or DB build's name, is an error rather than
// evidence it is not.
func readFindings(path, sbomDigest string) (FindingsRecord, error) {
	var r FindingsRecord
	data, err := os.ReadFile(path) //nolint:gosec // path is built from Store.path and a directory listing of it
	if err != nil {
		return r, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	if err := r.validate(); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	if r.SBOMDigest != sbomDigest || filepath.Base(path) != findingsFileName(r) {
		return r, fmt.Errorf("%w: %s holds findings for %s by %s@%s against the DB built %s, not the file's own name",
			release.ErrInvalid, path, r.SBOMDigest, r.Scanner, r.ScannerVersion, r.DBBuiltAt.UTC().Format(time.RFC3339))
	}
	return r, nil
}
