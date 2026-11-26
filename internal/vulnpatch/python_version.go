package vulnpatch

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Python versions are PEP 440, not semver: "2.0rc1", "1.0.post2",
// "1!2.0", "2023.10". pyVersion parses and orders them the way pip and
// packaging do, so the Python patcher's target choice, "already fixed" test
// and specifier checks agree with the installer.

// pep440 is packaging's VERSION_PATTERN, anchored, case-insensitive.
var pep440 = regexp.MustCompile(`(?i)^v?` +
	`(?:([0-9]+)!)?` + // epoch
	`([0-9]+(?:\.[0-9]+)*)` + // release
	`(?:[-_.]?(a|b|c|rc|alpha|beta|pre|preview)[-_.]?([0-9]+)?)?` + // pre
	`(?:-([0-9]+)|[-_.]?(post|rev|r)[-_.]?([0-9]+)?)?` + // post
	`(?:[-_.]?(dev)[-_.]?([0-9]+)?)?` + // dev
	`(?:\+([a-z0-9]+(?:[-_.][a-z0-9]+)*))?$`) // local

// pyVersion is one parsed PEP 440 version.
type pyVersion struct {
	epoch   uint64
	release []uint64
	pre     int // 0 none; 1 a, 2 b, 3 rc
	preN    uint64
	post    bool
	postN   uint64
	dev     bool
	devN    uint64
	local   string
}

func parsePyVersion(s string) (pyVersion, bool) {
	m := pep440.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return pyVersion{}, false
	}
	var v pyVersion
	num := func(s string) uint64 {
		n, _ := strconv.ParseUint(s, 10, 64) // "" is 0, as PEP 440 says
		return n
	}
	v.epoch = num(m[1])
	for _, part := range strings.Split(m[2], ".") {
		v.release = append(v.release, num(part))
	}
	switch strings.ToLower(m[3]) {
	case "":
	case "a", "alpha":
		v.pre = 1
	case "b", "beta":
		v.pre = 2
	default: // c, rc, pre, preview
		v.pre = 3
	}
	v.preN = num(m[4])
	if m[5] != "" || m[6] != "" {
		v.post, v.postN = true, num(m[5]+m[7])
	}
	if m[8] != "" {
		v.dev, v.devN = true, num(m[9])
	}
	v.local = strings.ToLower(m[10])
	return v, true
}

// prerelease reports a pre- or dev release, which pip installs only when
// asked to.
func (v pyVersion) prerelease() bool { return v.pre > 0 || v.dev }

// major is the epoch and the first release segment.
func (v pyVersion) major() [2]uint64 { return [2]uint64{v.epoch, v.release[0]} }

// seg is release segment i, zero past the end ("1.2" == "1.2.0").
func (v pyVersion) seg(i int) uint64 {
	if i < len(v.release) {
		return v.release[i]
	}
	return 0
}

// compare orders a and b as packaging does: epoch, release (zero-padded),
// then a dev release of the final before its pre-releases, pre-releases
// before the final, the final before its post-releases, and a dev release
// of each before it. Local labels break ties only.
func (a pyVersion) compare(b pyVersion) int {
	if c := cmpU(a.epoch, b.epoch); c != 0 {
		return c
	}
	for i := 0; i < len(a.release) || i < len(b.release); i++ {
		if c := cmpU(a.seg(i), b.seg(i)); c != 0 {
			return c
		}
	}
	// pre: a bare dev release sorts first, no pre-release last.
	preKey := func(v pyVersion) (int, uint64) {
		switch {
		case v.pre > 0:
			return v.pre, v.preN
		case v.dev && !v.post:
			return -1, 0
		}
		return 4, 0
	}
	ap, apn := preKey(a)
	bp, bpn := preKey(b)
	if c := cmpInt(ap, bp); c != 0 {
		return c
	}
	if c := cmpU(apn, bpn); c != 0 {
		return c
	}
	if c := cmpBoolU(a.post, a.postN, b.post, b.postN); c != 0 {
		return c
	}
	// dev: no dev release sorts after any.
	if a.dev != b.dev {
		if a.dev {
			return -1
		}
		return 1
	}
	if c := cmpU(a.devN, b.devN); c != 0 {
		return c
	}
	return strings.Compare(a.local, b.local)
}

func cmpU(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt(a, b int) int { return cmpU(uint64(a+1), uint64(b+1)) }

// cmpBoolU orders an absent value before any present one.
func cmpBoolU(ah bool, an uint64, bh bool, bn uint64) int {
	if ah != bh {
		if ah {
			return 1
		}
		return -1
	}
	return cmpU(an, bn)
}

// comparePyVersions orders two version strings; an unparseable one sorts
// first.
func comparePyVersions(a, b string) int {
	va, okA := parsePyVersion(a)
	vb, okB := parsePyVersion(b)
	switch {
	case !okA && !okB:
		return strings.Compare(a, b)
	case !okA:
		return -1
	case !okB:
		return 1
	}
	return va.compare(vb)
}

// pyEqual reports whether a and b are the same version ("2.31" is
// "2.31.0").
func pyEqual(a, b string) bool {
	va, okA := parsePyVersion(a)
	vb, okB := parsePyVersion(b)
	if !okA || !okB {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return va.compare(vb) == 0
}

// choosePythonTarget is ChooseTarget for PEP 440 versions: the lowest of
// fixed at or above installed, in installed's major (epoch and first
// release segment) unless allowMajor; a pre-release fix only for a
// pre-release install. It answers with the same errors.
func choosePythonTarget(installed string, fixed []string, allowMajor bool) (string, error) {
	cur, ok := parsePyVersion(installed)
	if !ok {
		return "", &TargetError{Reason: fmt.Sprintf("installed version %q is not a PEP 440 version", installed)}
	}
	type cand struct {
		raw string
		v   pyVersion
	}
	var cands []cand
	for _, f := range fixed {
		v, ok := parsePyVersion(f)
		if !ok || v.prerelease() && !cur.prerelease() {
			continue
		}
		if v.compare(cur) >= 0 {
			cands = append(cands, cand{raw: strings.TrimSpace(f), v: v})
		}
	}
	if len(cands) == 0 {
		return "", &TargetError{Reason: fmt.Sprintf("no fixed version at or above %s (fixed in: %s)", installed, orNone(fixed))}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].v.compare(cands[j].v) < 0 })
	for _, c := range cands {
		if c.v.major() == cur.major() {
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

// pyName is a distribution name normalized as PEP 503 says: lowercase,
// each run of "-", "_" and "." one "-". "PyYAML" and "pyyaml", "zope.interface"
// and "zope-interface" are one package.
func pyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	sep := false
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' {
			sep = true
			continue
		}
		if sep && b.Len() > 0 {
			b.WriteByte('-')
		}
		sep = false
		b.WriteRune(r)
	}
	return b.String()
}

// pythonEcosystem reports the Python ecosystems, whose versions are PEP 440.
func pythonEcosystem(e Ecosystem) bool {
	return e == EcosystemPip || e == EcosystemPoetry || e == EcosystemUV
}

// compareIn orders two versions of ecosystem e: PEP 440 for Python, semver
// for the rest.
func compareIn(e Ecosystem, a, b string) int {
	if pythonEcosystem(e) {
		return comparePyVersions(a, b)
	}
	return compareVersions(a, b)
}
