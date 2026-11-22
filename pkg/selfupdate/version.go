package selfupdate

import (
	"strings"

	"golang.org/x/mod/semver"
)

// canonicalVersion returns v as semver with a leading "v", or "" when it is
// not semver.
func canonicalVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// sameVersion reports whether a and b name the same release.
func sameVersion(a, b string) bool {
	ca, cb := canonicalVersion(a), canonicalVersion(b)
	if ca != "" && cb != "" {
		return semver.Compare(ca, cb) == 0
	}
	return strings.TrimPrefix(strings.TrimSpace(a), "v") == strings.TrimPrefix(strings.TrimSpace(b), "v")
}

// newer reports whether rel is newer than cur. Semver decides when both
// versions are semver and differ; otherwise the commit times do, when both
// are known. known is false when neither can order them (a "dev" build with
// no commit time): such a build is never upgraded.
func newer(cur Build, rel Release) (isNewer, known bool) {
	cv, rv := canonicalVersion(cur.Version), canonicalVersion(rel.Version)
	if cv != "" && rv != "" {
		if c := semver.Compare(rv, cv); c != 0 {
			return c > 0, true
		}
		if !cur.CommitTime.IsZero() && !rel.CommitTime.IsZero() {
			return rel.CommitTime.After(cur.CommitTime), true
		}
		return false, true
	}
	if !cur.CommitTime.IsZero() && !rel.CommitTime.IsZero() {
		return rel.CommitTime.After(cur.CommitTime), true
	}
	return false, false
}
