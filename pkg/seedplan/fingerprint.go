package seedplan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprint identifies the INPUTS a seed plan is derived from, so a
// consumer that cached the plan's OUTPUT can tell whether the cache is still
// answering the same question.
//
// # Why this exists
//
// The plan itself can only be built against a live postgres: BuildPlan takes
// []schemadef.Table, and the only way to get those is to apply db/migrations
// to a shadow database and introspect it. That is fine for `forge generate`
// and for a Go test, and impossible for the two places that consume the plan's
// output without a database — a browser running the mock transport, and
// `npm test` running vitest with no server.
//
// Those consumers hold a COPY of the plan's output, written at generate time.
// A copy is a cache, and a cache with no way to detect invalidation is a trap:
// edit a migration, skip `forge generate`, and the fixtures keep serving rows
// that describe a schema that no longer exists. Nothing fails — the data is
// merely wrong, which is the expensive kind of wrong.
//
// A fingerprint closes that hole without needing a database. The inputs are
// ordinary files on disk, so anything that can read the project directory can
// recompute it — including a vitest process. Recompute, compare to the value
// recorded when the output was generated, and a mismatch is a loud failure
// naming the stale artifact instead of silently wrong data.
//
// # What it covers, and why exactly this set
//
// The two files that decide every cell of the plan:
//
//   - db/migrations/*.up.sql — the schema. Column types, CHECK vocabularies,
//     regex CHECKs, length and range bounds, UNIQUE, NOT NULL, and every
//     foreign key are read off the applied result of these.
//   - db/seeds/vocab.yaml — the domain vocabulary overlay, the one input that
//     changes a value without changing the schema.
//
// Down migrations are deliberately EXCLUDED: schemadef applies only *.up.sql,
// so a down file cannot move a single seeded value, and hashing it would
// invalidate correct fixtures for an edit that provably cannot affect them. A
// fingerprint that reports change where none can occur trains its reader to
// ignore it.
//
// db/seeds/custom/ is likewise excluded — those are the user's own INSERT
// statements applied after the plan, not inputs to it.
//
// The salt and row counts (Config, from forge.yaml) are NOT hashed here.
// They belong to the caller's own identity rather than to the schema, and
// FingerprintWithConfig folds them in for a caller that needs the stronger
// property.
type Fingerprint struct {
	// Hex is the digest, or "" when there was nothing to hash (a project
	// with no migrations yet).
	Hex string
	// Files is how many input files contributed, so a caller can tell
	// "nothing changed" from "nothing was read". A guard asserting on a
	// fingerprint built from zero files proves nothing.
	Files int
}

// Empty reports whether the fingerprint covers no input files at all.
//
// This is a distinct state from "some digest", and consumers must treat it as
// such: a project with no migrations has no schema for fixtures to go stale
// against, and a guard that failed on it would fail every project from `forge
// project new` until its first migration.
func (f Fingerprint) Empty() bool { return f.Files == 0 }

// fingerprintVersion prefixes the digest so the ALGORITHM's own identity is
// part of it. Without it, changing what gets hashed here would silently make
// every recorded fingerprint mismatch, and the resulting failure would blame
// the user's migrations for forge's change.
const fingerprintVersion = "forge-seed-fp-v1"

// FingerprintInputs computes the fingerprint of the seed plan's inputs for the
// project whose migrations live at migDir.
//
// The digest covers each input file's PATH and CONTENT, in sorted path order,
// with explicit length framing between fields. Framing matters: without it,
// concatenation is ambiguous and two different file sets can hash equal.
//
// Missing inputs are not errors. A project with no db/seeds/vocab.yaml is the
// scaffolded default, and a project with no migrations yet is a project that
// has not reached this problem — both yield a well-defined result rather than
// a failure the caller must special-case. A file that EXISTS but cannot be
// read IS an error: silently treating an unreadable input as absent would
// produce a fingerprint that matches when the underlying schema might not.
func FingerprintInputs(migDir string) (Fingerprint, error) {
	paths, err := fingerprintInputPaths(migDir)
	if err != nil {
		return Fingerprint{}, err
	}
	if len(paths) == 0 {
		return Fingerprint{}, nil
	}

	h := sha256.New()
	writeFramed(h, fingerprintVersion)
	for _, p := range paths {
		body, err := os.ReadFile(p) //nolint:gosec // paths come from fingerprintInputPaths, rooted at migDir
		if err != nil {
			return Fingerprint{}, fmt.Errorf("read seed fingerprint input %s: %w", p, err)
		}
		// The BASE name, not the full path: an absolute path would make
		// the digest depend on where the project is checked out, so the
		// same tree would fingerprint differently in CI and on a laptop.
		writeFramed(h, filepath.Base(p))
		// Normalize line endings so a checkout under a CRLF-translating
		// git config does not read as a schema change.
		writeFramedBytes(h, normalizeNewlines(body))
	}
	return Fingerprint{Hex: hex.EncodeToString(h.Sum(nil)), Files: len(paths)}, nil
}

// FingerprintWithConfig is FingerprintInputs with the seed Config folded in,
// for a consumer whose cached output would also change if the salt or the row
// counts moved.
//
// The frontend mock fixtures are exactly such a consumer: they are the first N
// rows of the planned dataset, so a changed salt reshuffles every value and a
// changed row count changes how many exist — neither of which touches a
// migration. Hashing the schema alone would call that cache fresh when it is
// not.
func FingerprintWithConfig(migDir string, cfg Config) (Fingerprint, error) {
	base, err := FingerprintInputs(migDir)
	if err != nil || base.Empty() {
		return base, err
	}

	h := sha256.New()
	writeFramed(h, base.Hex)
	writeFramed(h, fmt.Sprintf("salt=%d", cfg.EffectiveSalt()))
	writeFramed(h, fmt.Sprintf("rows=%d", cfg.Rows))
	// Per-table overrides in sorted order — a map's iteration order is
	// random, and a digest that depends on it would flap between runs.
	tables := make([]string, 0, len(cfg.RowsPerTable))
	for t := range cfg.RowsPerTable {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		writeFramed(h, fmt.Sprintf("rows[%s]=%d", t, cfg.RowsPerTable[t]))
	}
	return Fingerprint{Hex: hex.EncodeToString(h.Sum(nil)), Files: base.Files}, nil
}

// fingerprintInputPaths returns the absolute paths of every file the seed plan
// derives from, sorted, so the digest does not depend on directory order.
func fingerprintInputPaths(migDir string) ([]string, error) {
	entries, err := os.ReadDir(migDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No migrations directory: a project that has not authored
			// a schema yet. Not an error — see FingerprintInputs.
			return nil, nil
		}
		return nil, fmt.Errorf("read migrations dir %s: %w", migDir, err)
	}

	var paths []string
	for _, e := range entries {
		// Only *.up.sql — the exact set schemadef applies. See the
		// Fingerprint doc on why down migrations are excluded.
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			paths = append(paths, filepath.Join(migDir, e.Name()))
		}
	}
	// A vocab overlay with no migrations describes nothing, so it joins the
	// set only when there is a schema for it to describe — otherwise a
	// project whose only seed-shaped file is the fully-commented scaffold
	// would report a non-empty fingerprint over zero schema.
	if len(paths) > 0 {
		vocab := VocabPath(migDir)
		if _, err := os.Stat(vocab); err == nil {
			paths = append(paths, vocab)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat seed vocab %s: %w", vocab, err)
		}
	}

	sort.Strings(paths)
	return paths, nil
}

// writeFramed adds one length-prefixed string to the digest.
func writeFramed(h interface{ Write([]byte) (int, error) }, s string) {
	writeFramedBytes(h, []byte(s))
}

// writeFramedBytes adds one length-prefixed byte field to the digest, so the
// boundary between two fields is unambiguous.
func writeFramedBytes(h interface{ Write([]byte) (int, error) }, b []byte) {
	// hash.Hash never returns an error; the signature is inherited from
	// io.Writer.
	_, _ = fmt.Fprintf(h, "%d:", len(b))
	_, _ = h.Write(b)
}

// normalizeNewlines collapses CRLF to LF so the same file fingerprints
// identically regardless of the checkout's line-ending translation.
func normalizeNewlines(b []byte) []byte {
	return []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
}
