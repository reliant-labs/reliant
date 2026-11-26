package codegen

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// WorkloadSite is one workload literal (`<alias>.Workload { name = "…" … }`)
// in a project's KCL, located by file and line: where a workload is DECLARED,
// read from the source without evaluating it.
//
// It is the cheap reader `forge project shapes` lists workloads from and
// `forge project audit` locates them with. It sees literals only: a workload
// built by a comprehension or a function has no literal name and no site, and
// a field a later union overrides reads as the literal wrote it. What an env
// actually runs is its render's answer, not this one.
type WorkloadSite struct {
	// Name is the literal's `name = "<x>"`.
	Name string
	// Kind is the literal's `kind = "<k>"`, or "" when it writes none (the
	// schema default, a service) or not as a string literal.
	Kind string
	// Build is the build type the literal's `build` names — go, docker,
	// shell or remote — directly or through a top-level binding in the same
	// file; "" when it declares no build or one this cannot read.
	Build string
	// Image is the literal's `image = "<ref>"`, or "".
	Image string
	// File (project-relative, slash-separated) and Line locate the literal.
	File string
	Line int
}

var (
	// workloadOpen matches the opening of any workload literal, bound or
	// nested: `fw.Workload {`, `forge.Workload {`.
	workloadOpen = regexp.MustCompile(`\b(?:[A-Za-z_][A-Za-z0-9_]*\.)?Workload\s*\{`)
	// workloadImageField matches an `image = "<ref>"` field on a line of its own.
	workloadImageField = regexp.MustCompile(`(?m)^\s*image\s*=\s*"([^"]+)"\s*$`)
	// workloadBuildField matches `build = <alias>.<T>Build`, or a binding.
	workloadBuildField = regexp.MustCompile(`(?m)^\s*build\s*=\s*(?:(?:[A-Za-z_][A-Za-z0-9_]*\.)?(Go|Docker|Shell|Remote)Build\b|([A-Za-z_][A-Za-z0-9_]*)\s*$)`)
)

// DeclaredWorkloadSites returns every named workload literal in the .k files
// under deploy/kcl, sorted by file and line. Unreadable files are skipped: a
// partial inventory beats none. Directories whose names start with "." or
// "_", and vendored module trees, are not read.
func DeclaredWorkloadSites(projectDir string) []WorkloadSite {
	root := filepath.Join(projectDir, "deploy", "kcl")
	var out []WorkloadSite
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // partial output beats no output
		}
		if d.IsDir() {
			n := d.Name()
			if path != root && (strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".k") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil //nolint:nilerr // partial output beats no output
		}
		rel, _ := filepath.Rel(projectDir, path)
		out = append(out, workloadSitesIn(string(raw), filepath.ToSlash(rel))...)
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// workloadSitesIn is DeclaredWorkloadSites for one file's source, rel being
// the path its sites carry.
func workloadSitesIn(src, rel string) []WorkloadSite {
	src = stripKCLProseKeepLines(src)
	var out []WorkloadSite
	// Search code only: an example inside an ordinary string is not a site.
	code := []byte(src)
	for i := 0; i < len(src); i++ {
		if end := kclProseEnd(src, i); end > i {
			for j := i; j < end; j++ {
				code[j] = blankKeepNewline(code[j])
			}
			i = end - 1
		}
	}
	for _, loc := range workloadOpen.FindAllStringIndex(string(code), -1) {
		body, ok := kclBlockBody(src, loc[1])
		if !ok {
			continue
		}
		top := kclTopLevel(body)
		m := workloadNameField.FindStringSubmatch(top)
		if m == nil {
			continue
		}
		s := WorkloadSite{Name: m[1], File: rel, Line: 1 + strings.Count(src[:loc[0]], "\n")}
		if k := workloadKindField.FindStringSubmatch(top); k != nil {
			s.Kind = k[1]
		}
		if im := workloadImageField.FindStringSubmatch(top); im != nil {
			s.Image = im[1]
		}
		if b := workloadBuildField.FindStringSubmatch(top); b != nil {
			switch {
			case b[1] != "":
				s.Build = strings.ToLower(b[1])
			case b[2] != "":
				bound := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(b[2]) + `\s*=\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)?(Go|Docker|Shell|Remote)Build\b`)
				if bm := bound.FindStringSubmatch(src); bm != nil {
					s.Build = strings.ToLower(bm[1])
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// stripKCLProseKeepLines is StripKCLProse that keeps every line where it was:
// a docstring becomes the newlines it spanned, so a line number read from the
// result is the line in the file.
func stripKCLProseKeepLines(src string) string {
	b := []byte(src)
	for i := 0; i < len(src); i++ {
		end := kclProseEnd(src, i)
		if end == i {
			continue
		}
		if src[i] == '#' || (i+2 < len(src) && src[i] == src[i+1] && src[i] == src[i+2]) {
			for j := i; j < end; j++ {
				b[j] = blankKeepNewline(b[j])
			}
		}
		i = end - 1
	}
	return string(b)
}

// kclTopLevel is a literal body with everything nested inside its brackets
// blanked (newlines kept), so a field pattern matches the literal's own
// fields and never one of a nested literal's (`ports = [fw.Port { name = … }]`).
func kclTopLevel(body string) string {
	b := []byte(body)
	depth := 0
	for i := 0; i < len(b); i++ {
		if end := kclProseEnd(body, i); end > i {
			if depth > 0 || body[i] == '#' {
				for j := i; j < end; j++ {
					b[j] = blankKeepNewline(b[j])
				}
			}
			i = end - 1
			continue
		}
		c := b[i]
		switch {
		case c == '{' || c == '[' || c == '(':
			depth++
			if depth == 1 {
				continue // keep the opener: `build = fw.GoBuild {` still reads
			}
		case c == '}' || c == ']' || c == ')':
			if depth > 0 {
				depth--
			}
			if depth == 0 {
				continue
			}
		}
		if depth > 0 {
			b[i] = blankKeepNewline(c)
		}
	}
	return string(b)
}

// kclProseEnd skips a comment or a single/triple quoted string, including
// escaped quotes. Returning i means this byte is code. Raw strings use the
// same delimiter boundaries; their optional prefix remains ordinary code.
func kclProseEnd(src string, i int) int {
	if src[i] == '#' {
		if n := strings.IndexByte(src[i:], '\n'); n >= 0 {
			return i + n
		}
		return len(src)
	}
	quote := src[i]
	if quote != '\'' && quote != '"' {
		return i
	}
	width := 1
	if i+2 < len(src) && src[i+1] == quote && src[i+2] == quote {
		width = 3
	}
	delim := src[i : i+width]
	for j := i + width; j < len(src); j++ {
		if src[j] == '\\' {
			j++
			continue
		}
		if strings.HasPrefix(src[j:], delim) {
			return j + width
		}
	}
	return len(src)
}

func blankKeepNewline(c byte) byte {
	if c == '\n' {
		return c
	}
	return ' '
}
