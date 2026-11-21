package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// CacheDirEnv relocates the SBOM cache.
const CacheDirEnv = "FORGE_SBOM_CACHE"

// Cache holds the SBOMs this machine generated, keyed by everything that
// decides the document's bytes ([CacheKey]): which syft ran, what it read,
// and the subject name and version the document records:
//
//	<dir>/<generator>/<subject>[@<platform>]-<name hash>.cdx.json   the SBOM bytes
//	<dir>/<generator>/<subject>[@<platform>]-<name hash>.json       {source, sbom_digest, name, version}
//
// The subject is the digest of the described bytes (an image digest, or the
// hash of a binary or lockfile), so an entry is valid for as long as those
// bytes exist, across projects, worktrees and releases: the same image under
// the same name, cut into two releases or re-cut, runs syft once. The name is
// part of the key because the document carries it (--source-name): two
// artifacts with identical bytes under different names are two documents,
// and serving one for the other would name the wrong artifact. A newer syft
// is a new generator directory, so upgrading forge regenerates rather than
// reusing an older tool's view.
//
// It is a cache, never a record: the machine ledger holds every SBOM a
// release names, and deleting this directory costs syft runs, nothing else.
// Every read re-hashes the bytes against the digest beside them, so a
// damaged entry is a miss, never served. A nil *Cache caches nothing.
type Cache struct {
	Dir string
}

// DefaultCacheDir is $FORGE_SBOM_CACHE, else <UserCacheDir>/forge/sbom —
// beside forge's other machine caches (tools, git sources, KCL).
func DefaultCacheDir() (string, error) {
	if d := strings.TrimSpace(os.Getenv(CacheDirEnv)); d != "" {
		return d, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate the SBOM cache: %w (set %s to choose one)", err, CacheDirEnv)
	}
	return filepath.Join(base, "forge", "sbom"), nil
}

// CacheKey is every input that decides an SBOM's bytes: the generator
// (`syft@v1.57.2`), the digest of what it read, the platform of a
// multi-platform image, and the source name and version the document
// records.
type CacheKey struct {
	Generator, Subject, Platform string
	SourceName, SourceVersion    string
}

type cacheMeta struct {
	Source        string `json:"source"`
	SBOMDigest    string `json:"sbom_digest"`
	Format        string `json:"format"`
	SourceName    string `json:"source_name"`
	SourceVersion string `json:"source_version,omitempty"`
}

// Get returns the cached SBOM for key and the source it was read from.
func (c *Cache) Get(key CacheKey) (data []byte, source string, ok bool) {
	stem, ok := c.stem(key)
	if !ok {
		return nil, "", false
	}
	raw, err := os.ReadFile(stem + ".json") //nolint:gosec // stem is under the cache dir, each segment flattened
	if err != nil {
		return nil, "", false
	}
	var meta cacheMeta
	// The name and version are checked as well as hashed into the file
	// name, so an entry is served only for the exact subject it describes.
	if json.Unmarshal(raw, &meta) != nil || meta.Source == "" || meta.Format != Format ||
		meta.SourceName != key.SourceName || meta.SourceVersion != key.SourceVersion {
		return nil, "", false
	}
	data, err = os.ReadFile(stem + ".cdx.json") //nolint:gosec // as above
	if err != nil || Digest(data) != meta.SBOMDigest || checkDocument(data) != nil {
		return nil, "", false
	}
	return data, meta.Source, true
}

// Put stores an SBOM. The bytes land before the metadata that vouches for
// them, each by rename, so a concurrent Get sees a whole entry or a miss.
func (c *Cache) Put(key CacheKey, source string, data []byte) error {
	stem, ok := c.stem(key)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(stem), 0o700); err != nil {
		return err
	}
	meta, err := json.Marshal(cacheMeta{Source: source, SBOMDigest: Digest(data), Format: Format,
		SourceName: key.SourceName, SourceVersion: key.SourceVersion})
	if err != nil {
		return err
	}
	if err := writeAtomic(stem+".cdx.json", data); err != nil {
		return err
	}
	return writeAtomic(stem+".json", meta)
}

func (c *Cache) stem(key CacheKey) (string, bool) {
	if c == nil || c.Dir == "" || key.Generator == "" || !release.ValidDigest(key.Subject) {
		return "", false
	}
	name := strings.Replace(key.Subject, ":", "-", 1)
	if key.Platform != "" {
		name += "@" + safeSegment(key.Platform)
	}
	// The name and version are hashed, not spelled, into the file name: a
	// repository path is longer than a path segment may be on some
	// filesystems, and the meta file carries them in full.
	sum := sha256.Sum256([]byte(key.SourceName + "\x00" + key.SourceVersion))
	name += "-" + hex.EncodeToString(sum[:8])
	return filepath.Join(c.Dir, safeSegment(key.Generator), name), true
}

// safeSegment flattens a value into one path segment.
func safeSegment(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == '@':
			return r
		}
		return '_'
	}, s)
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
