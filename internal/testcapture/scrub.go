package testcapture

import (
	"regexp"
	"strings"
)

// The secret shapes a stored stream is scrubbed of: a port of control-plane's
// scripts/lib/scrub-secrets.sh (the filter its gate runs over a log before it
// leaves the machine), kept pattern for pattern so a record forge uploads and
// a log control-plane publishes are scrubbed alike. A heuristic, so it redacts
// generously: a token-shaped string, a `password=`/`token: ` value, a URL's
// userinfo, a bearer/basic credential, a PEM private key block. It never makes
// a stream safe to publish by itself; it is the second line behind a test
// never printing a secret.
//
// Applied in order, like the sed expressions it ports. sed -E is POSIX
// leftmost-longest and RE2 leftmost-first; no pattern here has alternatives
// whose order changes what is redacted.
var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), "[redacted]"},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), "[redacted]"},
	{regexp.MustCompile(`xox[abposr]-[A-Za-z0-9-]{10,}`), "[redacted]"},
	{regexp.MustCompile(`(A3T|AKIA|ASIA)[0-9A-Z]{16}`), "[redacted]"},
	{regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), "[redacted]"},
	{regexp.MustCompile(`(sk|rk|pk)_(live|test)_[A-Za-z0-9]{10,}`), "[redacted]"},
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`), "[redacted]"},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[redacted-jwt]"},
	{regexp.MustCompile(`(://)[^/@[:space:]"']+@`), "${1}[redacted]@"},
	{regexp.MustCompile(`(?i)((bearer|basic|token)[[:space:]]+)[A-Za-z0-9._~+/=-]{12,}`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)((password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|authorization)["']?[[:space:]]*[:=][[:space:]]*["']?)[^"'[:space:],;]{4,}`), "${1}[redacted]"},
}

var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)
)

// redactedKey replaces a whole PEM private key block, as sed's `c\` does.
const redactedKey = "[redacted private key]"

// Scrubber redacts secrets from text one line at a time. It is stateful only
// for a PEM private key, whose block spans lines: every line from BEGIN to END
// becomes one "[redacted private key]" line. A zero Scrubber is ready to use;
// use one per stream.
type Scrubber struct {
	inKey bool
}

// Line scrubs one line (without its newline). drop reports a line inside a
// private key block, which the caller removes: the block's first line already
// stands for the whole block.
func (s *Scrubber) Line(line string) (out string, drop bool) {
	if s.inKey {
		if pemEnd.MatchString(line) {
			s.inKey = false
		}
		return "", true
	}
	if loc := pemBegin.FindStringIndex(line); loc != nil {
		// A key that ends on the line it began (a one-line dump) closes
		// here; otherwise every line up to its END is dropped.
		s.inKey = !pemEnd.MatchString(line[loc[1]:])
		return redactedKey, false
	}
	for _, p := range secretPatterns {
		line = p.re.ReplaceAllString(line, p.repl)
	}
	return line, false
}

// Text scrubs s, which may hold several lines; each keeps its line ending, and
// a dropped line goes with its ending.
func (s *Scrubber) Text(text string) string {
	if text == "" {
		return text
	}
	var b strings.Builder
	for rest := text; rest != ""; {
		line, nl := rest, ""
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			line, nl = rest[:i], "\n"
			rest = rest[i+1:]
		} else {
			rest = ""
		}
		out, drop := s.Line(line)
		if drop {
			continue
		}
		b.WriteString(out)
		b.WriteString(nl)
	}
	return b.String()
}
