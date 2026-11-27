package vulnpatch

import (
	"regexp"
	"strings"
)

// Version constraints, to tell whether a project's declared range already
// admits a fix (so the lockfile alone moves) or must itself be raised. Each
// check answers (admits, known): known is false when the constraint is not
// one these parsers read, and the caller then takes the conservative path.

// pySpecifierClause is one PEP 440 clause: "~=1.2", "==1.2.*", "!=1.3".
var pySpecifierClause = regexp.MustCompile(`^(~=|===|==|!=|<=|>=|<|>)\s*(\S+)$`)

// admitsPEP440 checks a PEP 440 specifier set (comma-separated clauses, as
// in requirements files, PEP 621 dependencies and .in files). An empty set
// admits everything.
func admitsPEP440(spec, version string) (admits, known bool) {
	v, ok := parsePyVersion(version)
	if !ok {
		return false, false
	}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true, true
	}
	for _, clause := range strings.Split(spec, ",") {
		ok, known := admitsClause(strings.TrimSpace(clause), v, version)
		if !known {
			return false, false
		}
		if !ok {
			return false, true
		}
	}
	return true, true
}

func admitsClause(clause string, v pyVersion, raw string) (admits, known bool) {
	m := pySpecifierClause.FindStringSubmatch(clause)
	if m == nil {
		return false, false
	}
	op, want := m[1], m[2]
	if op == "===" {
		return strings.EqualFold(want, strings.TrimSpace(raw)), true
	}
	if op == "==" || op == "!=" {
		if prefix, ok := strings.CutSuffix(want, ".*"); ok {
			p, ok := parsePyVersion(prefix)
			if !ok {
				return false, false
			}
			match := p.epoch == v.epoch
			for i := range p.release {
				match = match && v.seg(i) == p.release[i]
			}
			return match == (op == "=="), true
		}
	}
	w, ok := parsePyVersion(want)
	if !ok {
		return false, false
	}
	if w.local == "" {
		v.local = "" // "==1.0" admits "1.0+local"
	}
	c := v.compare(w)
	switch op {
	case "==":
		return c == 0, true
	case "!=":
		return c != 0, true
	case "<=":
		return c <= 0, true
	case ">=":
		return c >= 0, true
	case "<":
		return c < 0, true
	case ">":
		return c > 0, true
	case "~=":
		// ~=1.4.5 is >=1.4.5, ==1.4.*.
		if len(w.release) < 2 || c < 0 {
			return false, len(w.release) >= 2
		}
		for i := 0; i < len(w.release)-1; i++ {
			if v.seg(i) != w.release[i] {
				return false, true
			}
		}
		return v.epoch == w.epoch, true
	}
	return false, false
}

// admitsPoetry checks a poetry version constraint: "^1.2", "~1.2.3",
// "1.2.*", "*", a bare "1.2.3" (exact), PEP 440 clauses, joined by "," or
// spaces (and) and "||" (or).
func admitsPoetry(constraint, version string) (admits, known bool) {
	v, ok := parsePyVersion(version)
	if !ok {
		return false, false
	}
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return true, true
	}
	for _, alt := range strings.Split(constraint, "||") {
		all := true
		for _, clause := range poetryClauses(alt) {
			ok, known := admitsPoetryClause(clause, v, version)
			if !known {
				return false, false
			}
			all = all && ok
		}
		if all {
			return true, true
		}
	}
	return false, true
}

// poetryClauses splits an and-ed constraint on commas and on spaces that
// separate clauses (">=1.2 <2" but not ">= 1.2").
func poetryClauses(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		fields := strings.Fields(part)
		for i := 0; i < len(fields); i++ {
			f := fields[i]
			if strings.Trim(f, "<>=!~^") == "" && i+1 < len(fields) {
				f += fields[i+1] // an operator standing alone: ">= 1.2"
				i++
			}
			out = append(out, f)
		}
	}
	return out
}

func admitsPoetryClause(clause string, v pyVersion, raw string) (admits, known bool) {
	switch {
	case clause == "*":
		return true, true
	case strings.HasPrefix(clause, "^"), strings.HasPrefix(clause, "~") && !strings.HasPrefix(clause, "~="):
		w, ok := parsePyVersion(clause[1:])
		if !ok || w.epoch != v.epoch {
			return false, ok
		}
		if v.compare(w) < 0 {
			return false, true
		}
		// The first segment that may not move: ^ keeps every segment up to
		// the first non-zero one, ~ keeps major.minor (major alone for "~1").
		fixed := 1
		if clause[0] == '^' {
			fixed = len(w.release)
			for i, n := range w.release {
				if n != 0 {
					fixed = i + 1
					break
				}
			}
		} else if len(w.release) >= 2 {
			fixed = 2
		}
		for i := 0; i < fixed; i++ {
			if v.seg(i) != w.seg(i) {
				return false, true
			}
		}
		return true, true
	}
	if clause != "" && !strings.ContainsAny(clause[:1], "<>=!~") {
		clause = "==" + clause // a bare version is exact
	}
	return admitsClause(clause, v, raw)
}

// pep508 is a PEP 508 requirement's name, extras and the rest (a version
// specifier, parenthesized or not, or "@ url"), markers cut off first.
var pep508 = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[([^\]]*)\])?\s*(.*)$`)

// pyRequirement is one parsed requirement line or PEP 508 string.
type pyRequirement struct {
	name    string // normalized
	extras  []string
	spec    string // the PEP 440 specifier set, parentheses removed
	url     bool   // "name @ url", a git or path dependency
	markers bool   // has "; markers"
}

func parsePEP508(s string) (pyRequirement, bool) {
	var r pyRequirement
	if i := strings.IndexByte(s, ';'); i >= 0 {
		r.markers = strings.TrimSpace(s[i+1:]) != ""
		s = s[:i]
	}
	m := pep508.FindStringSubmatch(s)
	if m == nil {
		return r, false
	}
	r.name = pyName(m[1])
	for _, e := range strings.Split(m[2], ",") {
		if e = strings.TrimSpace(e); e != "" {
			r.extras = append(r.extras, e)
		}
	}
	rest := strings.TrimSpace(m[3])
	if strings.HasPrefix(rest, "@") {
		r.url = true
		return r, true
	}
	rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(rest, "("), ")"))
	r.spec = rest
	return r, true
}

// pin is the version an "==X" (or "===X") specifier pins, if it is one.
func (r pyRequirement) pin() (string, bool) {
	spec := strings.TrimSpace(r.spec)
	if strings.Contains(spec, ",") {
		return "", false
	}
	m := pySpecifierClause.FindStringSubmatch(spec)
	if len(m) < 3 || (m[1] != "==" && m[1] != "===") || strings.HasSuffix(m[2], ".*") {
		return "", false
	}
	return m[2], true
}
