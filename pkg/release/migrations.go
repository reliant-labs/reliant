package release

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The schema history a release carries, recorded when it is cut.
//
// WHY A RELEASE RECORDS ITS MIGRATIONS. A migrator applies only versions
// ABOVE the one its database has reached (pkg/migratekit). So a release that
// carries a migration numbered at or below what an environment's database has
// already applied can never run it there: the migrator refuses at boot, the
// new pods crash-loop and the old ones keep serving. That is the right last
// line of defence, and the worst place to find out — after a 20-minute build,
// in production, with the file usually already merged.
//
// Reliant's v1.7.54 met it exactly so (2026-10-24). v1.7.52 had shipped
// migration 20261024035148; main then merged #1399's 20261024024221, a LOWER
// version, past a merge gate nothing ran. Every release cut from main after
// that was unapplyable on prod, and nothing said so until prod's migrator did.
//
// The question "has this environment's schema already passed one of this
// release's migrations?" is answerable without a database: the environment's
// promotion history names the releases it applied, and each release names the
// migrations it carried. So the cut records them here, and the cut and every
// deploy ask [PassedMigrations] before anything ships. The ledger is the only
// source both have — a control plane, a console, a teammate's checkout of
// another branch — and git history is not: a hosted reader has no repository,
// and the release's sibling checkouts are not even the project's.
//
// EVIDENCE, NOT IDENTITY. Like [Release.Commits], a manifest describes the
// release and does not define it: [Release.SameContent] ignores it, so a
// re-cut that records one where an earlier cut did not is still the same
// release, and a release cut before manifests existed can have one recorded
// afterwards (the backfill) without becoming a different release.

const (
	// MaxMigrationSets bounds how many migration directories one release
	// records: the project's own plus each sibling checkout it builds from.
	MaxMigrationSets = 16
	// MaxMigrationFiles bounds one set. Far above any real project (the
	// largest measured had 205); a bound so a hosted column has one too.
	MaxMigrationFiles = 10000
	// MaxMigrationFileBytes bounds one recorded file name.
	MaxMigrationFileBytes = 255
	// MaxMigrationSourceBytes bounds one set's source identity.
	MaxMigrationSourceBytes = 512
)

// MigrationManifest is every migration directory a release's artifacts embed,
// each as the files it held at the cut.
type MigrationManifest struct {
	// Sets is one entry per migrations directory, sorted by Source.
	Sets []MigrationSet `json:"sets"`
}

// MigrationSet is one migrations directory as a release carried it.
type MigrationSet struct {
	// Source identifies the directory across every machine and every
	// release: the canonical repository and the directory's path within it,
	// e.g. "github.com/reliant-labs/reliant/db/migrations". Two releases'
	// sets are compared only when their Sources are equal, because a
	// migration's version is meaningful only within its own directory —
	// two projects' databases have unrelated schema histories.
	Source string `json:"source"`
	// Commit is the commit the files were read at: the release's own commit
	// for the project's directory, the sibling checkout's for a sibling's.
	// Empty when the bytes were no commit's (a dirty checkout). A claim, for
	// the reader; the check never consults it.
	Commit string `json:"commit,omitempty"`
	// Files are the forward migrations' base names
	// ("20261024035148_hosted_http_action_tickets.up.sql"), sorted by
	// version then name. A name, not only a version, because what a database
	// recorded is one specific file: two branches can claim one version.
	Files []string `json:"files"`
}

// MigrationVersion is the numeric version a migration file name carries — the
// digits before the first "_" — and whether it has one. It is the rule
// golang-migrate parses by, restated here because pkg/ may not import forge's
// internal version package.
func MigrationVersion(file string) (uint64, bool) {
	prefix, _, ok := strings.Cut(file, "_")
	if !ok || prefix == "" {
		return 0, false
	}
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(prefix, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// NewMigrationSet builds a set from a directory listing: only forward
// migrations with a parseable version, sorted. A listing is whatever the
// caller read (a directory, a git tree); the filter is the one place that
// decides what counts.
func NewMigrationSet(source, commit string, names []string) MigrationSet {
	files := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		if !strings.HasSuffix(n, ".up.sql") || seen[n] {
			continue
		}
		if _, ok := MigrationVersion(n); !ok {
			continue
		}
		seen[n] = true
		files = append(files, n)
	}
	sortMigrationFiles(files)
	return MigrationSet{Source: source, Commit: commit, Files: files}
}

func sortMigrationFiles(files []string) {
	sort.Slice(files, func(i, j int) bool {
		vi, _ := MigrationVersion(files[i])
		vj, _ := MigrationVersion(files[j])
		if vi != vj {
			return vi < vj
		}
		return files[i] < files[j]
	})
}

// Normalize sorts the sets by Source and each set's files by version, so two
// manifests describing the same history compare equal.
func (m *MigrationManifest) Normalize() {
	for i := range m.Sets {
		sortMigrationFiles(m.Sets[i].Files)
	}
	sort.Slice(m.Sets, func(i, j int) bool { return m.Sets[i].Source < m.Sets[j].Source })
}

// Equal reports whether two manifests record the same files under the same
// sources. Commit is a claim about where the files were read, not part of
// what was recorded, and is ignored.
func (m MigrationManifest) Equal(other MigrationManifest) bool {
	a, b := m.byFiles(), other.byFiles()
	if len(a) != len(b) {
		return false
	}
	for src, files := range a {
		o, ok := b[src]
		if !ok || len(o) != len(files) {
			return false
		}
		for f := range files {
			if !o[f] {
				return false
			}
		}
	}
	return true
}

func (m MigrationManifest) byFiles() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(m.Sets))
	for _, s := range m.Sets {
		files := make(map[string]bool, len(s.Files))
		for _, f := range s.Files {
			files[f] = true
		}
		out[s.Source] = files
	}
	return out
}

// Validate checks the bounds and that every recorded name is a forward
// migration with a version. Duplicate VERSIONS within a set are allowed: two
// files claiming one version is a defect the lint reports, and the manifest
// must still be able to describe a release that carries it.
func (m MigrationManifest) Validate() error {
	if len(m.Sets) > MaxMigrationSets {
		return fmt.Errorf("%w: migration manifest records %d directories, over the cap of %d", ErrInvalid, len(m.Sets), MaxMigrationSets)
	}
	sources := make(map[string]bool, len(m.Sets))
	for _, s := range m.Sets {
		if strings.TrimSpace(s.Source) == "" {
			return fmt.Errorf("%w: migration manifest has a directory with no source", ErrInvalid)
		}
		if len(s.Source) > MaxMigrationSourceBytes || !utf8.ValidString(s.Source) {
			return fmt.Errorf("%w: migration source %q is not valid UTF-8 of at most %d bytes", ErrInvalid, s.Source, MaxMigrationSourceBytes)
		}
		if sources[s.Source] {
			return fmt.Errorf("%w: migration manifest records %s twice", ErrInvalid, s.Source)
		}
		sources[s.Source] = true
		if s.Commit != "" && !ValidObjectID(s.Commit) {
			return fmt.Errorf("%w: migration source %s: commit %q is not a full git object id", ErrInvalid, s.Source, s.Commit)
		}
		if len(s.Files) > MaxMigrationFiles {
			return fmt.Errorf("%w: migration source %s records %d files, over the cap of %d", ErrInvalid, s.Source, len(s.Files), MaxMigrationFiles)
		}
		files := make(map[string]bool, len(s.Files))
		for _, f := range s.Files {
			if len(f) > MaxMigrationFileBytes || strings.ContainsAny(f, "/\\") || !strings.HasSuffix(f, ".up.sql") || !utf8.ValidString(f) {
				return fmt.Errorf("%w: migration source %s: %q is not a migration file name", ErrInvalid, s.Source, f)
			}
			if _, ok := MigrationVersion(f); !ok {
				return fmt.Errorf("%w: migration source %s: %q has no numeric version", ErrInvalid, s.Source, f)
			}
			if files[f] {
				return fmt.Errorf("%w: migration source %s lists %s twice", ErrInvalid, s.Source, f)
			}
			files[f] = true
		}
	}
	return nil
}

// AppliedManifest is one release an environment applied, with the migrations
// it carried: the input [PassedMigrations] judges a target against.
type AppliedManifest struct {
	// Release is the version label, for the refusal's wording.
	Release string
	// Manifest is what that release recorded.
	Manifest MigrationManifest
}

// PassedMigration is one migration a release carries that an environment's
// schema has already gone past without applying.
type PassedMigration struct {
	// Source is the migrations directory (MigrationSet.Source).
	Source string `json:"source"`
	// File and Version are the migration that can never apply.
	File    string `json:"file"`
	Version uint64 `json:"version"`
	// Head is the highest version the environment's applied releases
	// carried from Source; HeadFile names it and HeadRelease the first
	// release in the applied list (newest first, as callers pass it) that
	// carried it.
	Head        uint64 `json:"head"`
	HeadFile    string `json:"head_file"`
	HeadRelease string `json:"head_release"`
}

// PassedMigrations returns every migration in target that an environment
// which applied the releases in applied can never run: per source, a file
// NONE of those releases carried, at a version at or below the highest one
// any of them did.
//
// THE FILE, NOT THE VERSION, decides "already applied" — the rule
// pkg/migratekit's own check uses, for the same reason: two branches can
// claim one version, and a database recorded one specific file.
//
// A source no applied release carried is a directory the environment has
// never migrated from this ledger's point of view, and nothing in it is
// judged. applied should list only releases whose deploy did not fail: a
// release whose rollout was refused applied nothing, and counting its files
// as applied would wave through the very file that refused it. Order matters
// only to HeadRelease; the result is sorted by source then version.
func PassedMigrations(target MigrationManifest, applied []AppliedManifest) []PassedMigration {
	type head struct {
		version uint64
		file    string
		release string
	}
	heads := map[string]head{}
	have := map[string]map[string]bool{}
	for _, a := range applied {
		for _, s := range a.Manifest.Sets {
			if have[s.Source] == nil {
				have[s.Source] = map[string]bool{}
			}
			for _, f := range s.Files {
				have[s.Source][f] = true
				v, ok := MigrationVersion(f)
				if !ok {
					continue
				}
				h, seen := heads[s.Source]
				if !seen || v > h.version || (v == h.version && f > h.file) {
					heads[s.Source] = head{version: v, file: f, release: a.Release}
				}
			}
		}
	}
	var out []PassedMigration
	for _, s := range target.Sets {
		h, ok := heads[s.Source]
		if !ok {
			continue
		}
		for _, f := range s.Files {
			v, ok := MigrationVersion(f)
			if !ok || v > h.version || have[s.Source][f] {
				continue
			}
			out = append(out, PassedMigration{
				Source: s.Source, File: f, Version: v,
				Head: h.version, HeadFile: h.file, HeadRelease: h.release,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].File < out[j].File
	})
	return out
}
