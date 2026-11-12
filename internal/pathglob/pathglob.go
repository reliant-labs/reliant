// Package pathglob matches project-relative, slash-separated paths against the
// globs a forge.yaml list holds (`scaffold.exclude`).
//
// The syntax is path.Match's, per segment, plus `**`:
//
//   - a pattern is anchored at the project root: `docs/*.md` matches
//     docs/a.md, not web/docs/a.md;
//   - `*`, `?` and `[...]` match within one segment;
//   - a `**` segment matches zero or more whole segments: `frontends/**`
//     matches everything under frontends/, `**/*.gen.ts` every .gen.ts file;
//   - a trailing `/` names a directory and everything under it:
//     `frontends/web/` is `frontends/web/**`;
//   - a pattern with no meta character also matches everything under it when
//     it names a directory: `frontends/web` is `frontends/web/**` too.
//
// Validate rejects what could never match a project-relative path, so a typo is
// a load error instead of a pattern that silently excludes nothing.
package pathglob

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Normalize returns pattern in its canonical form: no leading "./", and a
// trailing "/" turned into "/**".
func Normalize(pattern string) string {
	p := strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	if strings.HasSuffix(p, "/") {
		p = strings.TrimRight(p, "/") + "/**"
	}
	return p
}

// Validate reports why pattern is not a usable project-relative glob, or nil.
func Validate(pattern string) error {
	p := Normalize(pattern)
	switch {
	case p == "":
		return errors.New("empty pattern")
	case strings.Contains(pattern, `\`):
		return errors.New("use forward slashes: patterns are project-relative paths, not OS paths")
	case strings.HasPrefix(p, "/"):
		return errors.New("patterns are relative to the project root; drop the leading /")
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "":
			return errors.New("empty path segment (//)")
		case seg == "." || seg == "..":
			return fmt.Errorf("%q segment: patterns name paths inside the project", seg)
		case seg == "**":
			continue
		case strings.Contains(seg, "**"):
			return fmt.Errorf("%q: ** must be a whole segment (dir/**/x, not dir/a**)", seg)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("%q: %v", seg, err)
		}
	}
	return nil
}

// Match reports whether rel, a project-relative slash path, matches pattern.
// An invalid pattern matches nothing.
func Match(pattern, rel string) bool {
	p := Normalize(pattern)
	if Validate(p) != nil {
		return false
	}
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" {
		return false
	}
	pSegs := strings.Split(p, "/")
	rSegs := strings.Split(rel, "/")
	if matchSegs(pSegs, rSegs) {
		return true
	}
	// A literal pattern naming a directory covers its subtree.
	if !hasMeta(p) && strings.HasPrefix(rel, p+"/") {
		return true
	}
	return false
}

// StaticPrefix is the leading run of literal segments of pattern: the
// directory every path it can match lives under ("" when the first segment
// is already a wildcard).
func StaticPrefix(pattern string) string {
	var out []string
	for _, seg := range strings.Split(Normalize(pattern), "/") {
		if hasMeta(seg) {
			break
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

func hasMeta(s string) bool { return strings.ContainsAny(s, "*?[") }

func matchSegs(p, r []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			// Collapse consecutive ** segments.
			for len(p) > 0 && p[0] == "**" {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(r); i++ {
				if matchSegs(p, r[i:]) {
					return true
				}
			}
			return false
		}
		if len(r) == 0 {
			return false
		}
		if ok, _ := path.Match(p[0], r[0]); !ok {
			return false
		}
		p, r = p[1:], r[1:]
	}
	return len(r) == 0
}
