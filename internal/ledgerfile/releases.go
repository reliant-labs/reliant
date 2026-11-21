package ledgerfile

import (
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/release"
)

// releasesFile is the single append-only log of every release this project
// has cut.
//
// ONE FILE, NOT ONE PER VERSION. The retired backend used
// .forge/releases/<version>.json, which needed a lossy version→filename
// mapping (the real file ".forge/releases/v1_0_0.json" does not name its own
// version, so every reader had to read the version from INSIDE the file
// anyway). An append-only log needs no such mapping, is read in one open,
// and matches how every other record in this store is held.
const releasesFile = "releases.jsonl"

// Releases returns every release in the order it was cut, OLDEST FIRST.
//
// NOT SORTED NEWEST-FIRST HERE, deliberately. "Newest" is a semver-then-
// timestamp judgement that internal/cli already implements once
// (sortReleasesNewestFirst), and both backends' List funnel through it. A
// second comparator in this package could disagree with that one about which
// release is latest, which is the kind of split that shows up as two forge
// commands naming different "latest" releases for one project.
func (s *Store) Releases() ([]release.Release, error) {
	var out []release.Release
	err := s.withLock(func() error {
		var err error
		out, err = s.releasesLocked()
		return err
	})
	return out, err
}

func (s *Store) releasesLocked() ([]release.Release, error) {
	all, err := decodeAll(s.path(releasesFile), release.Release.Validate)
	if err != nil {
		return nil, err
	}
	// A re-cut appends, so the newest line for a version wins. Collapse by
	// version, keeping the last occurrence.
	byVersion := map[string]release.Release{}
	order := make([]string, 0, len(all))
	for _, r := range all {
		if _, seen := byVersion[r.Version]; !seen {
			order = append(order, r.Version)
		}
		byVersion[r.Version] = r
	}
	out := make([]release.Release, 0, len(order))
	for _, v := range order {
		out = append(out, byVersion[v])
	}
	return out, nil
}

// Release returns one release by version, or (nil, nil) when it was never
// cut — the caller decides whether that is an error.
func (s *Store) Release(version string) (*release.Release, error) {
	all, err := s.Releases()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Version == version {
			return &all[i], nil
		}
	}
	return nil, nil
}

// ErrReleaseNotFound is RecordReleaseMigrations naming a version never cut.
var ErrReleaseNotFound = errors.New("release not found")

// RecordReleaseMigrations records m as the migration manifest of a release
// cut without one — the backfill for releases cut before the cut recorded
// it. recorded is false when the release already holds an equal manifest; a
// DIFFERENT one is release.ErrReleaseConflict, because a manifest, once
// recorded, is what every later deploy judged.
//
// It APPENDS the release again with the manifest set: the log is
// append-only, and the newest line for a version is the one releasesLocked
// keeps. The manifest is evidence, not identity (Release.SameContent ignores
// it), so this does not make the release a different one.
func (s *Store) RecordReleaseMigrations(version string, m release.MigrationManifest) (recorded bool, err error) {
	if err := m.Validate(); err != nil {
		return false, err
	}
	m.Normalize()
	err = s.withLock(func() error {
		all, err := s.releasesLocked()
		if err != nil {
			return err
		}
		for _, existing := range all {
			if existing.Version != version {
				continue
			}
			if existing.Migrations != nil {
				if existing.Migrations.Equal(m) {
					return nil
				}
				return fmt.Errorf("release %q: %w: it already records a different migration manifest", version, release.ErrReleaseConflict)
			}
			existing.Migrations = &m
			recorded = true
			return appendRecord(s.path(releasesFile), existing)
		}
		return fmt.Errorf("release %q: %w", version, ErrReleaseNotFound)
	})
	if err != nil {
		return false, err
	}
	return recorded, nil
}

// ErrArtifactNotFound is RecordReleaseSBOM naming an artifact the release
// does not have.
var ErrArtifactNotFound = errors.New("artifact not found in release")

// RecordReleaseSBOM records ref as the SBOM of one artifact of a release
// already cut — the backfill for a release cut before forge recorded SBOMs,
// or one whose SBOM could not be produced at the cut. recorded is false when
// the artifact already names an SBOM: the first one recorded stands, because
// it is what every scan since was computed against (release.BackfillSBOMs
// has the rule). Not a conflict either way: an SBOM is evidence, not
// identity.
//
// Like RecordReleaseMigrations it APPENDS the release again, and the newest
// line for a version is the one releasesLocked keeps.
func (s *Store) RecordReleaseSBOM(version, artifact string, ref release.SBOMRef) (recorded bool, err error) {
	err = s.withLock(func() error {
		all, err := s.releasesLocked()
		if err != nil {
			return err
		}
		for _, existing := range all {
			if existing.Version != version {
				continue
			}
			a, ok := existing.Artifacts[artifact]
			if !ok {
				return fmt.Errorf("release %q artifact %q: %w", version, artifact, ErrArtifactNotFound)
			}
			a.SBOM = &ref
			requested := existing
			requested.Artifacts = map[string]release.Artifact{artifact: a}
			filled, changed := release.BackfillSBOMs(existing, requested)
			if !changed {
				return nil
			}
			if err := filled.Validate(); err != nil {
				return err
			}
			recorded = true
			return appendRecord(s.path(releasesFile), filled)
		}
		return fmt.Errorf("release %q: %w", version, ErrReleaseNotFound)
	})
	if err != nil {
		return false, err
	}
	return recorded, nil
}

// CutRelease records r. created is false when an identical release already
// held the version (an idempotent retry); a DIFFERENT artifact set under the
// same version is release.ErrReleaseConflict.
//
// A RELEASE IS IMMUTABLE, and release.CheckRecut is the one implementation
// of that rule both backends call — here under the lock, and on the hosted
// side under a unique index plus an append-only trigger. One version label
// meaning two digest sets would void every guarantee promotion rests on.
func (s *Store) CutRelease(r release.Release) (created bool, err error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	err = s.withLock(func() error {
		all, err := s.releasesLocked()
		if err != nil {
			return err
		}
		for _, existing := range all {
			if existing.Version != r.Version {
				continue
			}
			// Same content is a no-op retry: nothing is appended, so
			// the log does not grow on every CI re-run.
			if err := release.CheckRecut(existing, r); err != nil {
				return err
			}
			// ...unless the retry knows an SBOM the record lacks. SBOMs
			// are evidence, not identity (SameContent ignores them), so
			// the record is appended again with them filled in — never
			// replaced — exactly as RecordReleaseMigrations backfills a
			// manifest.
			if filled, changed := release.BackfillSBOMs(existing, r); changed {
				return appendRecord(s.path(releasesFile), filled)
			}
			return nil
		}
		created = true
		return appendRecord(s.path(releasesFile), r)
	})
	if err != nil {
		return false, err
	}
	return created, nil
}
