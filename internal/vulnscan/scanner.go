package vulnscan

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Scanner runs grype against the machine's shared database.
type Scanner struct {
	DB *DB
}

// Scan scans one grype source: "sbom:<path>" (the normal case: a stored
// CycloneDX SBOM), "dir:<path>", or "registry:<ref>@<digest>". It never
// refreshes the database — call DB.Ensure first when a refresh is allowed —
// and it refuses a database that is missing (ErrNoDB) or stale (ErrStale)
// rather than letting grype fail on it, so the caller can say which.
//
// Scans share the database lock, so they run concurrently with each other
// and never against a half-imported database.
func (s *Scanner) Scan(ctx context.Context, source string) (*Scan, error) {
	unlock, err := s.DB.lock(ctx, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	st, err := s.DB.statusLocked(ctx)
	if err != nil {
		return nil, err
	}
	if err := usable(st, s.DB.now(), nil); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	version, err := s.DB.grype(ctx, &out, source, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", source, err)
	}
	scan, err := ParseGrype(out.Bytes(), s.DB.now())
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", source, err)
	}
	// The resolver's version is the pin; prefer it to what the binary
	// printed only when the output named none.
	if scan.ScannerVersion == "" {
		scan.ScannerVersion = normalizeVersion(version)
	}
	return scan, nil
}

// ScanSBOM scans SBOM bytes (CycloneDX or SPDX JSON; grype detects which).
func (s *Scanner) ScanSBOM(ctx context.Context, sbom []byte) (*Scan, error) {
	dir, err := os.MkdirTemp("", "forge-vuln-sbom-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "sbom.json")
	if err := atomicfile.WriteFile(path, sbom, 0o600); err != nil {
		return nil, err
	}
	return s.Scan(ctx, "sbom:"+path)
}
