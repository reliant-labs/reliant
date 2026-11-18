package obsbackend

import (
	"regexp"
	"strings"
	"unicode"
)

// Cmd joins argv into a runnable command line, single-quoting any word the
// shell would split. It mirrors obsout.Command; obsbackend stays free of the
// output layer so the cloud backend can share it.
func Cmd(argv ...string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = quoteWord(a)
	}
	return strings.Join(words, " ")
}

var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func quoteWord(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SQLShape is a statement with every literal removed: strings and numbers
// become ?, comments and runs of whitespace collapse, keywords lower-case. It
// is what the raw-SQL usage counter logs (DESIGN §14 D2 condition 2): enough to
// see WHICH constructs callers reach for that the structured verbs lack, and
// nothing a caller typed as a value (an id, an email, a trace id).
//
// Identifiers and function names are kept: they are the signal.
func SQLShape(sql string) string {
	var b strings.Builder
	rs := []rune(sql)
	space := func() {
		if s := b.String(); s != "" && !strings.HasSuffix(s, " ") {
			b.WriteByte(' ')
		}
	}
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			space()
			i++
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			space()
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			i += 2
			for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
				i++
			}
			i += 2
			space()
		case c == '\'':
			i = skipQuoted(rs, i, '\'')
			b.WriteByte('?')
		case c == '{': // {name:Type} is a bound parameter, not a literal
			j := i
			for j < len(rs) && rs[j] != '}' {
				j++
			}
			b.WriteString(string(rs[i:min(j+1, len(rs))]))
			i = j + 1
		case unicode.IsDigit(c):
			// A token that starts with a digit is a number (1, 1.5e3, 0x1F),
			// whatever follows: none of it is structure.
			for i < len(rs) && (unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i]) || rs[i] == '.' || rs[i] == '_') {
				i++
			}
			b.WriteByte('?')
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_' || rs[j] == '.') {
				j++
			}
			w := string(rs[i:j])
			if isKeyword(w) {
				w = strings.ToLower(w)
			}
			b.WriteString(w)
			i = j
		default:
			b.WriteRune(c)
			i++
		}
	}
	return strings.TrimSpace(b.String())
}

// skipQuoted returns the index after the quote that closes the one at i,
// honouring backslash escapes and doubled quotes.
func skipQuoted(rs []rune, i int, q rune) int {
	i++
	for i < len(rs) {
		switch {
		case rs[i] == '\\':
			i += 2
		case rs[i] == q && i+1 < len(rs) && rs[i+1] == q:
			i += 2
		case rs[i] == q:
			return i + 1
		default:
			i++
		}
	}
	return i
}

var keywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`SELECT FROM WHERE GROUP BY ORDER HAVING LIMIT OFFSET AS AND OR NOT IN IS NULL JOIN LEFT RIGHT INNER
		OUTER FULL CROSS ON USING UNION ALL DISTINCT WITH CASE WHEN THEN ELSE END BETWEEN LIKE ILIKE DESC ASC ARRAY INTERVAL FINAL PREWHERE SAMPLE`) {
		keywords[k] = true
	}
}

func isKeyword(w string) bool { return keywords[strings.ToUpper(w)] }
