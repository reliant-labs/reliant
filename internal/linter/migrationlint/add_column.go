package migrationlint

import "strings"

// unsafeAddedNotNull scopes NOT NULL and its exemptions to each added column,
// not the whole ALTER TABLE. CHECK/default/generated expressions and quoted
// strings/identifiers cannot supply column constraint keywords. A DEFAULT on a
// different ADD action cannot make an unsafe column safe either.
func unsafeAddedNotNull(text string) bool {
	for _, words := range topLevelAlterActions(text) {
		add := -1
		for i := 0; i+1 < len(words); i++ {
			if words[i] == "add" && words[i+1] == "column" {
				add = i + 2
				break
			}
		}
		if add < 0 {
			continue
		}
		if add+2 < len(words) && words[add] == "if" && words[add+1] == "not" && words[add+2] == "exists" {
			add += 3
		}
		// Skip the name, which may itself be a quoted keyword.
		if add >= len(words) {
			continue
		}
		definition := strings.Join(words[add+1:], " ")
		if hasNotNull(definition) && !hasDefault(definition) && !generatedTopLevelStored(words[add+1:]) {
			return true
		}
	}
	return false
}

func generatedTopLevelStored(words []string) bool {
	for i := 0; i+3 < len(words); i++ {
		if words[i] == "generated" && words[i+1] == "always" && words[i+2] == "as" && words[i+3] == "stored" {
			return true
		}
	}
	return false
}

// topLevelAlterActions tokenizes only depth-zero words and separates ALTER
// actions at depth-zero commas. It reuses the PostgreSQL quote/comment scanner
// so commas/keywords in nested CHECKs, types and literals are never actions.
func topLevelAlterActions(src string) [][]string {
	out := [][]string{{}}
	depth := 0
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			if n := strings.IndexByte(src[i:], '\n'); n >= 0 {
				i += n
			} else {
				i = len(src)
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = blockCommentEnd(src, i)
		case c == '\'' || ((c == 'e' || c == 'E') && i+1 < len(src) && src[i+1] == '\'' && !identBefore(src, i)):
			quote := i
			if c != '\'' {
				quote++
			}
			i = quotedEnd(src, quote, '\'', c != '\'')
		case c == '"':
			end := quotedEnd(src, i, '"', false)
			if depth == 0 {
				out[len(out)-1] = append(out[len(out)-1], "\"identifier\"")
			}
			i = end
		case c == '$' && !identBefore(src, i):
			i = dollarQuoteEnd(src, i)
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case c == ',' && depth == 0:
			out = append(out, []string{})
			i++
		case isIdentStart(c):
			end := i + 1
			for end < len(src) && isIdentByte(src[end]) {
				end++
			}
			if depth == 0 {
				out[len(out)-1] = append(out[len(out)-1], strings.ToLower(src[i:end]))
			}
			i = end
		default:
			i++
		}
	}
	return out
}
