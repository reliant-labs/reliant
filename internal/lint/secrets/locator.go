package secrets

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// SourceFile is one KCL file of the project.
type SourceFile struct {
	// Path is project-relative, slash-separated.
	Path string
	Text string
}

// Locator maps a declaration to the file:line that wrote it. A render knows
// WHAT an environment declares but not WHERE, so a finding is anchored by
// reading the project's KCL: the line that names the Secret, the path or the
// field. A declaration it cannot find anchors at the env's main.k, line 1, which
// is still a place to start from.
type Locator struct {
	files []SourceFile
}

// NewLocator builds a locator over the given KCL files.
func NewLocator(files []SourceFile) *Locator {
	fs := append([]SourceFile(nil), files...)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
	return &Locator{files: fs}
}

// A needle is one pattern a declaration's line may match.
type needle struct {
	re *regexp.Regexp
	// near, when set, must appear within the five lines above the match: it
	// tells `name = "x"` of an ExternalSecret from the same line of a
	// workload's secret reference.
	near string
}

func quoted(s string) needle {
	return needle{re: regexp.MustCompile(`"` + regexp.QuoteMeta(s) + `"`)}
}

func nameDecl(s, near string) needle {
	return needle{re: regexp.MustCompile(`\bname\s*=\s*"` + regexp.QuoteMeta(s) + `"`), near: near}
}

func field(name string) needle {
	return needle{re: regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=`)}
}

func word(w string) needle {
	return needle{re: regexp.MustCompile(`\b` + regexp.QuoteMeta(w) + `\b`)}
}

// find returns the first line matching the needles, in order. Files in the
// env's own directory are searched before the rest of the KCL (shared libs).
func (l *Locator) find(env Env, needles ...needle) (string, int) {
	if l != nil {
		dir := path.Dir(env.File)
		for _, n := range needles {
			if f, line := l.search(n, func(p string) bool { return strings.HasPrefix(p, dir+"/") }); line > 0 {
				return f, line
			}
			if f, line := l.search(n, func(string) bool { return true }); line > 0 {
				return f, line
			}
		}
	}
	return env.File, 1
}

func (l *Locator) search(n needle, accept func(string) bool) (string, int) {
	var fallbackFile string
	fallbackLine := 0
	for _, f := range l.files {
		if !accept(f.Path) {
			continue
		}
		lines := strings.Split(f.Text, "\n")
		for i, text := range lines {
			if isKCLComment(text) || !n.re.MatchString(text) {
				continue
			}
			if n.near == "" {
				return f.Path, i + 1
			}
			for j := max(0, i-5); j <= i; j++ {
				if strings.Contains(lines[j], n.near) {
					return f.Path, i + 1
				}
			}
			if fallbackLine == 0 {
				fallbackFile, fallbackLine = f.Path, i+1
			}
		}
	}
	return fallbackFile, fallbackLine
}

func isKCLComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

// adoptionRE names the declarations that opt a project into the platform
// secrets model. A project whose KCL says none of them has nothing for this
// lane to judge, so the caller does not even render it.
var adoptionRE = regexp.MustCompile(`\b(StoreSource|OpenBao|OpenBaoStore|HostedStore|GCPSecretManagerStore|ExternalSecretsOperator|external_secrets_operators)\b`)

// MentionsPlatformSecrets reports whether the project's KCL declares anything
// of the platform secrets model.
func MentionsPlatformSecrets(files []SourceFile) bool {
	for _, f := range files {
		for _, line := range strings.Split(f.Text, "\n") {
			if !isKCLComment(line) && adoptionRE.MatchString(line) {
				return true
			}
		}
	}
	return false
}
