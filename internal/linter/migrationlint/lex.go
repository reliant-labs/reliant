package migrationlint

import (
	"strings"

	"github.com/reliant-labs/forge/internal/sqllex"
)

// The statement lexer lives in internal/sqllex, which `forge db analyze`
// shares; these names keep the rules reading as they did.

type lexedStatement = sqllex.Statement

func lexStatements(src string) []lexedStatement { return sqllex.Split(src) }

var (
	blockCommentEnd = sqllex.BlockCommentEnd
	quotedEnd       = sqllex.QuotedEnd
	dollarQuoteEnd  = sqllex.DollarQuoteEnd
	isIdentStart    = sqllex.IsIdentStart
	isIdentByte     = sqllex.IsIdentByte
	identBefore     = sqllex.IdentBefore
)

// migrationStatements gives the text rules the lexer’s real statement
// boundaries, with comments replaced by whitespace but literal bytes intact.
func migrationStatements(content string) []statement {
	spans := lexStatements(content)
	result := make([]statement, 0, len(spans))
	for _, s := range spans {
		result = append(result, statement{Text: blankMigrationComments(content[s.Start:s.End]), Line: s.Line})
	}
	return result
}

func blankMigrationComments(text string) string {
	var out strings.Builder
	start := 0
	for i := 0; i < len(text); i++ {
		end, comment := migrationQuotedOrCommentEnd(text, i)
		if end <= i {
			continue
		}
		if comment {
			out.WriteString(text[start:i])
			out.WriteByte(' ')
			out.WriteString(strings.Repeat("\n", strings.Count(text[i:end], "\n")))
			start = end
		}
		i = end - 1
	}
	if start == 0 {
		return text
	}
	out.WriteString(text[start:])
	return out.String()
}

// migrationQuotedOrCommentEnd reuses the statement lexer’s quote and nested
// comment handling. It returns i for an ordinary byte.
func migrationQuotedOrCommentEnd(text string, i int) (int, bool) {
	c := text[i]
	switch {
	case strings.HasPrefix(text[i:], "--"):
		if n := strings.IndexByte(text[i:], '\n'); n >= 0 {
			return i + n, true
		}
		return len(text), true
	case strings.HasPrefix(text[i:], "/*"):
		return blockCommentEnd(text, i), true
	case c == '\'' || c == '"':
		return quotedEnd(text, i, c, false), false
	case (c == 'E' || c == 'e') && i+1 < len(text) && text[i+1] == '\'' && !identBefore(text, i):
		return quotedEnd(text, i+1, '\'', true), false
	case c == '$' && !identBefore(text, i):
		return dollarQuoteEnd(text, i), false
	default:
		return i, false
	}
}
