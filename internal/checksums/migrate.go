// One-time migration off the legacy global manifest
// (.forge/checksums.json) onto self-certifying files.
//
// Runs automatically (and loudly) on the first `forge generate` /
// `forge project upgrade` in a project that still carries the legacy manifest.
// Per legacy Tier-1 entry, in order:
//
//   - on-disk bytes match the entry's recorded hash OR any hash in its
//     render history → the file is a pristine forge render of some
//     vintage: stamp the forge:hash marker into it (comment-incapable
//     formats get a scoped .forge/hashes.json entry instead);
//   - bytes match NOTHING the manifest recorded → provenance unknown.
//     This is kalshi's exact mess (fr-9a54388f0b): a manifest committed
//     from a different work lane than the committed bytes. The entry is
//     returned as Unverified; the generate pipeline gives each such path
//     one rescue attempt (side-render the fresh template output and
//     compare bodies — a match proves pristineness and stamps the file),
//     and everything unrescued is stamped with the UnverifiedMarkerValue
//     sentinel so the stomp guard names it on every run until the user
//     resolves it (--force to regenerate, `forge project disown` to keep).
//
// Disowned (and legacy forked) entries convert to .forge/disowned.json.
// Plain Tier-2 entries are dropped — scaffold-once files are user-owned
// from birth and need no record. Legacy Tier-1 entries whose path the
// CURRENT forge no longer emits are dropped too (the file stays, now
// plainly user-owned — stamping it would feed the stale-artifact sweep
// a file no emitter will ever refresh).
//
// The legacy manifest is DELETED at the end. The migration is
// metadata-only and durable: it lands once, even if the same run later
// aborts at the stomp guard.
package checksums

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// LegacyEntry mirrors the manifest-era per-file record (structured
// shape). Only the fields the migration consults are decoded.
type LegacyEntry struct {
	Hash       string   `json:"hash"`
	History    []string `json:"history,omitempty"`
	Tier       int      `json:"tier,omitempty"`
	Disowned   bool     `json:"disowned,omitempty"`
	DisownedAt string   `json:"disowned_at,omitempty"`
	Forked     bool     `json:"forked,omitempty"`
	ForkedAt   string   `json:"forked_at,omitempty"`
}

// LegacyManifest is the decoded .forge/checksums.json.
type LegacyManifest struct {
	ForgeVersion string                 `json:"forge_version"`
	Files        map[string]LegacyEntry `json:"files"`
}

// LoadLegacyManifest reads .forge/checksums.json if present. Returns
// (nil, nil) when the file doesn't exist. Both historical wire shapes
// are accepted: the structured entry form and the original flat
// path→hex-string form.
func LoadLegacyManifest(root string) (*LegacyManifest, error) {
	data, err := os.ReadFile(filepath.Join(root, LegacyChecksumFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m LegacyManifest
	if err := json.Unmarshal(data, &m); err == nil && m.Files != nil {
		legacyFlat := false
		for _, e := range m.Files {
			if e.Hash == "" {
				legacyFlat = true
				break
			}
		}
		if !legacyFlat {
			return &m, nil
		}
	}
	var flat struct {
		ForgeVersion string            `json:"forge_version"`
		Files        map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("parse %s: %w", LegacyChecksumFile, err)
	}
	m = LegacyManifest{ForgeVersion: flat.ForgeVersion, Files: make(map[string]LegacyEntry, len(flat.Files))}
	for p, h := range flat.Files {
		m.Files[p] = LegacyEntry{Hash: h, History: []string{h}}
	}
	return &m, nil
}

// MigrationOutcome reports what the legacy migration did, for the loud
// one-time announcement.
type MigrationOutcome struct {
	Stamped           []string // pristine → forge:hash marker embedded
	Fallback          []string // comment-incapable → .forge/hashes.json
	DisownedConverted []string // → .forge/disowned.json
	DroppedTier2      []string // scaffold-once: user-owned, no record kept
	DroppedUnknown    []string // path current forge doesn't emit: user-owned now
	MissingOnDisk     []string // tracked but gone: nothing to certify
	Unverified        []string // matched nothing recorded: provenance unknown
}

// Total returns the number of legacy entries processed.
func (o *MigrationOutcome) Total() int {
	return len(o.Stamped) + len(o.Fallback) + len(o.DisownedConverted) +
		len(o.DroppedTier2) + len(o.DroppedUnknown) + len(o.MissingOnDisk) + len(o.Unverified)
}

// MigrateLegacyManifest performs the one-time conversion described in
// the package comment. currentTier1 reports whether the CURRENT forge
// still emits relPath as a Tier-1 output (paths it doesn't recognize
// are dropped rather than stamped). The legacy manifest file is deleted
// on success. Returns (nil, nil) when there is no legacy manifest.
//
// The caller decides what to do with Unverified paths — the pipeline
// runs the side-render rescue then stamps survivors with
// StampUnverified; `forge project upgrade` stamps them immediately.
func MigrateLegacyManifest(root string, cs *FileChecksums, currentTier1 func(string) bool) (*MigrationOutcome, error) {
	legacy, err := LoadLegacyManifest(root)
	if err != nil || legacy == nil {
		return nil, err
	}

	out := &MigrationOutcome{}
	paths := make([]string, 0, len(legacy.Files))
	for p := range legacy.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, relPath := range paths {
		entry := legacy.Files[relPath]
		full := filepath.Join(root, filepath.FromSlash(relPath))

		switch {
		case entry.Disowned || entry.Forked:
			if _, statErr := os.Stat(full); statErr != nil {
				// Disowned but gone: deletion is the documented
				// re-adoption signal — leave no record and let the next
				// emit re-own the path.
				out.MissingOnDisk = append(out.MissingOnDisk, relPath)
				continue
			}
			if cs.Disowned == nil {
				cs.Disowned = map[string]DisownedEntry{}
			}
			at := entry.DisownedAt
			if at == "" {
				at = entry.ForkedAt
			}
			if at == "" {
				at = nowRFC3339()
			}
			// No original reason survives the legacy manifest: it recorded a
			// bare `disowned: true` flag and nothing else. Say that plainly
			// rather than pointing at a log — an earlier version of this
			// message referred the reader to `.forge/friction.jsonl`, whose
			// writer has since been deleted, so it sent them to a file that
			// no longer receives anything.
			reason := "migrated from legacy .forge/checksums.json (disowned there; the legacy manifest recorded no reason)"
			if entry.Forked {
				reason = "migrated from legacy .forge/checksums.json (legacy fork-era entry; the fork state was removed)"
			}
			cs.Disowned[relPath] = DisownedEntry{Reason: reason, DisownedAt: at}
			out.DisownedConverted = append(out.DisownedConverted, relPath)
			continue

		case entry.Tier == 2:
			out.DroppedTier2 = append(out.DroppedTier2, relPath)
			continue
		}

		// Tier-1 (or legacy tier-0, which pre-tier manifests used for
		// exclusively Tier-1 emitters).
		content, readErr := os.ReadFile(full)
		if readErr != nil {
			out.MissingOnDisk = append(out.MissingOnDisk, relPath)
			continue
		}
		if currentTier1 != nil && !currentTier1(relPath) {
			out.DroppedUnknown = append(out.DroppedUnknown, relPath)
			continue
		}

		if legacyEntryMatches(entry, content) {
			if Stampable(relPath) {
				stamped, _ := Stamp(relPath, content)
				if werr := atomicfile.WriteFile(full, stamped, 0o644); werr != nil {
					return nil, fmt.Errorf("stamp %s: %w", relPath, werr)
				}
				out.Stamped = append(out.Stamped, relPath)
			} else {
				if cs.Unstampable == nil {
					cs.Unstampable = map[string]string{}
				}
				cs.Unstampable[relPath] = BodyHash(content)
				out.Fallback = append(out.Fallback, relPath)
			}
			continue
		}

		out.Unverified = append(out.Unverified, relPath)
	}

	if err := os.Remove(filepath.Join(root, LegacyChecksumFile)); err != nil && !os.IsNotExist(err) {
		return out, err
	}
	migrateGitignoreNegation(root)
	return out, nil
}

// legacyEntryMatches reports whether content's raw sha256 equals the
// entry's recorded hash or any hash in its render history — the
// manifest era hashed raw bytes with no normalization, so the
// comparison replicates that exactly.
func legacyEntryMatches(entry LegacyEntry, content []byte) bool {
	h := Hash(content)
	if h == entry.Hash {
		return true
	}
	for _, prior := range entry.History {
		if h == prior {
			return true
		}
	}
	return false
}

// StampUnverified embeds the UnverifiedMarkerValue sentinel into
// relPath so the stomp guard keeps naming the file until its
// provenance is resolved. No-op (false) for unstampable formats and
// unreadable files.
func StampUnverified(root, relPath string) bool {
	full := filepath.Join(root, filepath.FromSlash(relPath))
	content, err := os.ReadFile(full)
	if err != nil {
		return false
	}
	stamped, ok := StampWithValue(relPath, content, UnverifiedMarkerValue)
	if !ok {
		return false
	}
	return atomicfile.WriteFile(full, stamped, 0o644) == nil
}

// RescueUnverified is the migration's provenance rescue: if a fresh
// side render was parked for relPath this run and its BODY matches the
// on-disk bytes, the file is provably a pristine render of the current
// templates — stamp it for real. Returns true when rescued. The parked
// render is cleaned up either way once consulted.
func RescueUnverified(root, relPath string) bool {
	sidePath := filepath.Join(root, RenderDir, filepath.FromSlash(relPath))
	side, err := os.ReadFile(sidePath)
	if err != nil {
		return false
	}
	defer func() { _ = CleanSideRenders(root, relPath) }()
	full := filepath.Join(root, filepath.FromSlash(relPath))
	onDisk, err := os.ReadFile(full)
	if err != nil {
		return false
	}
	if BodyHash(side) != BodyHash(onDisk) {
		return false
	}
	stamped, ok := Stamp(relPath, onDisk)
	if !ok {
		return false
	}
	return atomicfile.WriteFile(full, stamped, 0o644) == nil
}

// migrateGitignoreNegation rewrites the scaffold-era
// "!.forge/checksums.json" negation in the project .gitignore to the
// new committed state files. Best-effort: .gitignore is user-owned, so
// only the exact bookkeeping line forge originally wrote is touched.
func migrateGitignoreNegation(root string) {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := string(data)
	if !strings.Contains(content, "!"+LegacyChecksumFile) {
		return
	}
	replacement := "!" + DisownedFile + "\n!" + HashesFile
	content = strings.Replace(content, "!"+LegacyChecksumFile, replacement, 1)
	// Drop any duplicate negations a previous partial migration left.
	content = strings.ReplaceAll(content, "!"+LegacyChecksumFile+"\n", "")
	_ = atomicfile.WriteFile(path, []byte(content), 0o644)
}
