package ledgerfile

import (
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
			return release.CheckRecut(existing, r)
		}
		created = true
		return appendRecord(s.path(releasesFile), r)
	})
	if err != nil {
		return false, err
	}
	return created, nil
}
