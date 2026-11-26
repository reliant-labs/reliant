package config

import (
	"errors"
	"fmt"
	"strings"
)

// Command placeholders are `{token}`. A brace after `$` is shell (`${VAR}`)
// and never a placeholder, and a token the command field does not define is
// left as written, so ordinary shell text passes through. The one exception
// is a token containing `/` in a pin's bump: it can only mean a repository,
// so a repository the pin does not name is an error rather than a literal.

// CandidatePlaceholder is replaced in a consumer's command by the producer's
// candidate sha.
const CandidatePlaceholder = "{candidate}"

// replacePlaceholders calls fn for each `{token}` not preceded by `$`. fn
// returns the replacement and whether to replace at all. On any error it
// returns no command, so a caller can never run a partly substituted one.
func replacePlaceholders(s string, fn func(tok string) (string, bool, error)) (string, error) {
	var b strings.Builder
	var errs []error
	last := 0
	for _, m := range placeholderRe.FindAllStringSubmatchIndex(s, -1) {
		start, end := m[0], m[1]
		if start > 0 && s[start-1] == '$' {
			continue
		}
		repl, ok, err := fn(s[m[2]:m[3]])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(repl)
		last = end
	}
	if len(errs) > 0 {
		return "", errors.Join(errs...)
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

// BumpCommand returns the pin's bump command with each repository
// placeholder (`{owner/name}`, or `{name}` when unambiguous) replaced by the
// sha shas maps it to. shas is keyed by owner/name, case-insensitively, and
// must cover every repository the command names.
func (p Pin) BumpCommand(shas map[string]string) (string, error) {
	if p.Bump == "" {
		return "", fmt.Errorf("pin %s has no bump command", p.Repo)
	}
	if shas == nil {
		shas = map[string]string{}
	}
	return p.expand(shas)
}

// expand resolves the bump's placeholders. With shas nil it only checks
// them, leaving the command as written.
func (p Pin) expand(shas map[string]string) (string, error) {
	repos := append([]string{p.Repo}, p.LockstepWith...)
	byName := map[string][]string{}
	for _, r := range repos {
		if _, name, ok := strings.Cut(r, "/"); ok {
			byName[strings.ToLower(name)] = append(byName[strings.ToLower(name)], r)
		}
	}
	lowerShas := map[string]string{}
	for k, sha := range shas {
		lowerShas[strings.ToLower(k)] = sha
	}
	return replacePlaceholders(p.Bump, func(tok string) (string, bool, error) {
		var repo string
		if strings.Contains(tok, "/") {
			for _, r := range repos {
				if strings.EqualFold(r, tok) {
					repo = r
				}
			}
			if repo == "" {
				return "", false, fmt.Errorf("placeholder {%s} names no repository this pin moves; bump can use %s", tok, braceList(repos))
			}
		} else {
			switch rs := byName[strings.ToLower(tok)]; len(rs) {
			case 0:
				return "", false, nil // shell text, not a placeholder
			case 1:
				repo = rs[0]
			default:
				return "", false, fmt.Errorf("placeholder {%s} is ambiguous between %s; write the owner/name form", tok, strings.Join(rs, " and "))
			}
		}
		if shas == nil {
			return "", false, nil
		}
		sha, ok := lowerShas[strings.ToLower(repo)]
		if !ok {
			return "", false, fmt.Errorf("no sha given for %s", repo)
		}
		return sha, true, nil
	})
}

// CommandFor returns the consumer's command with {candidate} replaced by
// the producer candidate's sha.
func (c Consumer) CommandFor(candidate string) string {
	out, _ := replacePlaceholders(c.Command, func(tok string) (string, bool, error) {
		return candidate, "{"+tok+"}" == CandidatePlaceholder, nil
	})
	return out
}

func braceList(repos []string) string {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = "{" + r + "}"
	}
	return strings.Join(out, ", ")
}
