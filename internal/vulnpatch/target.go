package vulnpatch

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// ErrNeedsMajor is ChooseTarget's answer when every fix is in a later major
// and AllowMajor is off.
var ErrNeedsMajor = errors.New("needs a major bump")

// TargetError explains why no target was chosen.
type TargetError struct {
	// Reason is the sentence for the finding's Reason.
	Reason string
	// Major is the lowest fix in a later major, when that is the reason.
	Major string
	err   error
}

func (e *TargetError) Error() string { return e.Reason }
func (e *TargetError) Unwrap() error { return e.err }

// ChooseTarget picks the version to bump installed to: the lowest of fixed
// at or above installed, in installed's major unless allowMajor. A
// prerelease fix is chosen only for a prerelease install. The answer is
// spelled the way fixed spells it.
//
// It refuses (a *TargetError) when nothing in fixed is a version, when no
// fix is at or above installed, and — ErrNeedsMajor — when the only fixes
// are in a later major and allowMajor is off.
func ChooseTarget(installed string, fixed []string, allowMajor bool) (string, error) {
	cur := canonical(installed)
	if cur == "" {
		return "", &TargetError{Reason: fmt.Sprintf("installed version %q is not a semantic version", installed)}
	}
	type cand struct{ raw, v string }
	var cands []cand
	for _, f := range fixed {
		v := canonical(f)
		if v == "" {
			continue
		}
		if semver.Prerelease(v) != "" && semver.Prerelease(cur) == "" {
			continue
		}
		if semver.Compare(v, cur) >= 0 {
			cands = append(cands, cand{raw: strings.TrimSpace(f), v: v})
		}
	}
	if len(cands) == 0 {
		return "", &TargetError{Reason: fmt.Sprintf("no fixed version at or above %s (fixed in: %s)", installed, orNone(fixed))}
	}
	sort.Slice(cands, func(i, j int) bool { return semver.Compare(cands[i].v, cands[j].v) < 0 })
	for _, c := range cands {
		if semver.Major(c.v) == semver.Major(cur) {
			return c.raw, nil
		}
	}
	if allowMajor {
		return cands[0].raw, nil
	}
	return "", &TargetError{
		Reason: fmt.Sprintf("needs a major bump: the lowest fix is %s, a new major from %s (--allow-major takes it)", cands[0].raw, installed),
		Major:  cands[0].raw,
		err:    ErrNeedsMajor,
	}
}

// canonical is v as golang.org/x/mod/semver reads it ("v1.2.3"), or "" when
// it is no version. A leading "v" is optional, and a Go "+incompatible"
// suffix is ignored for ordering.
func canonical(v string) string {
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
	return semver.Canonical(v)
}

// compareVersions orders two versions; an unparseable one sorts first.
func compareVersions(a, b string) int {
	return semver.Compare(canonical(a), canonical(b))
}

// sameMajor reports whether a and b share a major version.
func sameMajor(a, b string) bool {
	ca, cb := canonical(a), canonical(b)
	return ca != "" && cb != "" && semver.Major(ca) == semver.Major(cb)
}

// goVersion spells v the way `go get` takes it: with a leading "v".
func goVersion(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// npmVersion spells v the way npm, pnpm and yarn take it: without one.
func npmVersion(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

func orNone(vs []string) string {
	if len(vs) == 0 {
		return "none"
	}
	return strings.Join(vs, ", ")
}
