package obsout

import (
	"regexp"
	"strings"
)

// Page is the bound every list document carries. Embed it in the document next
// to the list.
type Page struct {
	// Truncated is true when the list is not everything the query matched.
	Truncated bool `json:"truncated"`
	// Omitted counts items a cap cut from what the backend returned (not
	// counting a further page the backend holds). Absent when nothing was cut.
	Omitted int `json:"omitted,omitempty"`
	// NextPageToken resumes the list; pass it back as --page-token. Absent when
	// there is no further page.
	NextPageToken string `json:"next_page_token,omitempty"`
}

// Cap keeps at most max items and reports how many it dropped. max <= 0 keeps
// everything.
func Cap[T any](items []T, max int) (kept []T, omitted int) {
	if max <= 0 || len(items) <= max {
		return items, 0
	}
	return items[:max], len(items) - max
}

// Bound caps items and fills the page: truncated when anything was cut or the
// backend has a next page. A backend that paged for the verb passes its token.
func Bound[T any](items []T, max int, nextToken string) ([]T, Page) {
	kept, omitted := Cap(items, max)
	return kept, Page{Truncated: omitted > 0 || nextToken != "", Omitted: omitted, NextPageToken: nextToken}
}

// MaxSuggested is the most next commands a document offers.
const MaxSuggested = 3

// Suggest returns the first MaxSuggested distinct non-empty commands, in the
// order given: the caller ranks them. Each must be a complete, runnable
// command (use [Command]); a suggestion is a concrete call, never advice.
func Suggest(cmds ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range cmds {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) == MaxSuggested {
			break
		}
	}
	return out
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Command joins argv into one shell-runnable command line, single-quoting any
// word that needs it.
func Command(argv ...string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = quote(a)
	}
	return strings.Join(words, " ")
}

func quote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
