package naming

import (
	"sort"
	"strings"
	"unicode"
)

// ToSnakeCaseWords is ToSnakeCase for labels a person reads — graph node
// names in logs, errors, the readiness report and the start plan — where the
// split must follow the WORDS of a Go identifier, not just its case changes:
//
//	GitHubAccessLoop → github_access_loop   (ToSnakeCase: git_hub_access_loop)
//	OAuthClient      → oauth_client         (o_auth_client)
//	APIURL           → api_url              (apiurl)
//	LLMMCPBridge     → llm_mcp_bridge       (llmmcp_bridge)
//	UserIDs          → user_ids             (user_i_ds)
//
// Three rules on top of ToSnakeCase's:
//
//   - a word written with inner capitals (CompoundWords: GitHub, OAuth,
//     OpenAI, …) is one word, with any digits that follow it (OAuth2);
//   - a run of capitals that is a sequence of known initialisms
//     (GoInitialisms, plus SnakeInitialisms) splits into them;
//   - a known initialism's plural (IDs, URLs, APIs) is one word.
//
// Every other identifier gets exactly ToSnakeCase's answer, so the only
// names that change are the ones these rules name.
//
// It is deliberately NOT ToSnakeCase itself. ToSnakeCase names wire and
// schema things — proto fields, columns, entity fields — whose spelling is a
// compatibility surface; a label is not.
func ToSnakeCaseWords(s string) string {
	words := splitWords(s)
	for i, w := range words {
		words[i] = strings.ToLower(w)
	}
	return strings.Join(words, "_")
}

// CompoundWords are words conventionally written with an inner capital. Each
// is matched case-sensitively, as a whole word.
var CompoundWords = []string{
	"GitHub", "GitLab", "OAuth", "OpenAI", "OpenAPI", "OpenID", "OpenBao",
	"GraphQL", "PostgreSQL", "MySQL", "WebSocket", "WebAuthn",
	"JavaScript", "TypeScript", "YouTube", "LinkedIn", "PayPal",
}

// SnakeInitialisms are initialisms the word split knows beyond
// GoInitialisms. They are kept apart because GoInitialisms also drives
// ToPascalCase's capitalisation of generated Go identifiers, which these
// must not change.
var SnakeInitialisms = []string{
	"NATS", "OIDC", "SAML", "SSO", "MFA", "TOTP", "AWS", "GCP", "GCS", "GKE",
	"CLI", "SDK", "ORM", "CRD", "KV", "PR", "CI", "CD", "IDP", "JWKS",
}

// snakeInitialisms is every known initialism, longest first, so a greedy
// split prefers UUID over UID.
var snakeInitialisms = func() []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{GoInitialisms, SnakeInitialisms} {
		for _, w := range list {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}()

// splitWords splits s into the words ToSnakeCaseWords joins.
func splitWords(s string) []string {
	runes := []rune(s)
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	isSep := func(r rune) bool { return r == '_' || r == '-' || r == ' ' }
	// boundary reports whether a word may end before runes[j].
	boundary := func(j int) bool {
		return j >= len(runes) || unicode.IsUpper(runes[j]) || unicode.IsDigit(runes[j]) || isSep(runes[j])
	}
	for i := 0; i < len(runes); {
		r := runes[i]
		if isSep(r) {
			flush()
			i++
			continue
		}
		// A compound word, where a word may start: at the beginning, after
		// a lower-case letter, a digit or a separator — or after a run of
		// capitals that is itself an initialism (APIOAuth).
		if unicode.IsUpper(r) && (len(cur) == 0 || !unicode.IsUpper(runes[i-1]) || isInitialismRun(string(cur))) {
			if w, n := matchCompound(runes, i, boundary); n > 0 {
				flush()
				words = append(words, w)
				i += n
				continue
			}
		}
		if unicode.IsUpper(r) && (i == 0 || !unicode.IsUpper(runes[i-1])) {
			// A run of capitals, from its first: runes[i:end].
			end := i
			for end < len(runes) && unicode.IsUpper(runes[end]) {
				end++
			}
			run := string(runes[i:end])
			switch {
			case end < len(runes) && runes[end] == 's' && boundary(end+1):
				// IDs, URLs: the plural belongs to the run.
				if parts, ok := splitInitialisms(run); ok {
					flush()
					parts[len(parts)-1] += "s"
					words = append(words, parts...)
					i = end + 1
					continue
				}
			case end < len(runes) && unicode.IsLower(runes[end]):
				// HTTPStatus: the last capital starts the next word.
				run = string(runes[i : end-1])
				end--
			}
			if len(run) > 1 {
				if parts, ok := splitInitialisms(run); ok && len(parts) > 1 {
					flush()
					words = append(words, parts...)
					i = end
					continue
				}
			}
		}
		// ToSnakeCase's rule: a capital starts a word after a lower-case
		// letter, or after a capital when a lower-case letter follows it.
		if unicode.IsUpper(r) && i > 0 && len(cur) > 0 {
			prev := runes[i-1]
			if unicode.IsLower(prev) || (unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
				flush()
			}
		}
		cur = append(cur, r)
		i++
	}
	flush()
	return words
}

// matchCompound returns the compound word starting at runes[i] that ends at a
// word boundary, with any digits that follow it, and its length in runes.
func matchCompound(runes []rune, i int, boundary func(int) bool) (string, int) {
	for _, w := range CompoundWords {
		wr := []rune(w)
		if i+len(wr) > len(runes) || string(runes[i:i+len(wr)]) != w {
			continue
		}
		end := i + len(wr)
		if !boundary(end) {
			continue
		}
		for end < len(runes) && unicode.IsDigit(runes[end]) {
			end++
		}
		return string(runes[i:end]), end - i
	}
	return "", 0
}

// splitInitialisms splits an all-capitals run into known initialisms, or
// reports false when it is not wholly made of them (the run is then one
// word, as ToSnakeCase has it).
func splitInitialisms(run string) ([]string, bool) {
	if run == "" {
		return nil, true
	}
	for _, w := range snakeInitialisms {
		if strings.HasPrefix(run, w) {
			if rest, ok := splitInitialisms(run[len(w):]); ok {
				return append([]string{w}, rest...), true
			}
		}
	}
	return nil, false
}

// isInitialismRun reports whether run is a sequence of known initialisms.
func isInitialismRun(run string) bool {
	_, ok := splitInitialisms(run)
	return ok
}
