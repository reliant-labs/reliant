// Copyright (c) 2025 Reliant Labs
//
// Package suggest provides skill matching/suggestion logic that operates on
// pre-loaded []config.StoredSkill slices. It is filesystem-free and safe for
// server-side code (API server, worker) to import.
//
// The tokenizer and scoring logic here mirrors the original catalog matcher
// but is decoupled from the daemon-only catalog package.
package suggest

import (
	"sort"
	"strings"
	"unicode"

	"github.com/reliant-labs/reliant/internal/config"
)

// Suggested is a skill matched against a user message with a relevance score.
type Suggested struct {
	Skill config.StoredSkill
	Score float64
}

// tokenSet holds normalized tokens for a skill.
type tokenSet struct {
	skill      config.StoredSkill
	tokens     map[string]struct{}
	nameTokens map[string]struct{}
}

// stopwords are common English words filtered out during tokenization.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "is": {}, "are": {}, "was": {}, "were": {},
	"in": {}, "on": {}, "at": {}, "to": {}, "for": {}, "of": {}, "with": {},
	"and": {}, "or": {}, "but": {}, "not": {}, "this": {}, "that": {}, "it": {},
	"as": {}, "be": {}, "by": {}, "from": {}, "has": {}, "have": {}, "had": {},
	"do": {}, "does": {}, "did": {}, "will": {}, "would": {}, "could": {},
	"should": {}, "may": {}, "might": {}, "can": {}, "if": {}, "then": {},
	"else": {}, "when": {}, "where": {}, "how": {}, "what": {}, "which": {},
	"who": {}, "whom": {}, "why": {}, "all": {}, "each": {}, "every": {},
	"any": {}, "some": {}, "no": {}, "only": {}, "own": {}, "so": {},
	"than": {}, "too": {}, "very": {}, "just": {}, "about": {}, "also": {},
	"into": {}, "out": {}, "up": {}, "down": {}, "over": {}, "after": {},
	"before": {}, "between": {}, "under": {}, "again": {}, "further": {},
	"once": {}, "here": {}, "there": {}, "both": {}, "other": {}, "more": {},
	"most": {}, "such": {}, "like": {}, "use": {}, "used": {}, "using": {},
	"you": {}, "your": {}, "we": {}, "our": {}, "they": {}, "their": {},
	"its": {}, "his": {}, "her": {}, "them": {}, "these": {}, "those": {},
	"been": {}, "being": {}, "same": {}, "get": {}, "got": {}, "make": {},
	"made": {},
}

// programmingStopwords are common programming keywords that are too generic to be useful.
var programmingStopwords = map[string]struct{}{
	"func": {}, "var": {}, "const": {}, "return": {}, "import": {},
	"package": {}, "type": {}, "struct": {}, "interface": {}, "string": {},
	"int": {}, "bool": {}, "nil": {}, "true": {}, "false": {},
	"new": {}, "delete": {}, "break": {}, "continue": {}, "switch": {},
	"case": {}, "default": {}, "else": {}, "for": {}, "range": {},
	"map": {}, "chan": {}, "select": {}, "defer": {}, "go": {},
	"class": {}, "public": {}, "private": {}, "protected": {}, "static": {},
	"void": {}, "abstract": {}, "final": {}, "extends": {}, "implements": {},
	"let": {}, "def": {}, "self": {}, "print": {}, "println": {},
	"main": {}, "args": {}, "err": {}, "error": {},
}

// tokenize splits text into a set of normalized, filtered tokens.
func tokenize(text string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, word := range splitWords(text) {
		w := strings.ToLower(word)
		if len(w) < 3 {
			continue
		}
		if _, ok := stopwords[w]; ok {
			continue
		}
		if _, ok := programmingStopwords[w]; ok {
			continue
		}
		tokens[w] = struct{}{}
	}
	return tokens
}

// splitWords splits text on whitespace and common punctuation, returning individual words.
func splitWords(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		return true
	})
}

// tokenizeName extracts tokens specifically from a skill name, also splitting
// on hyphens so "error-handling" contributes "error" and "handling" tokens.
func tokenizeName(name string) map[string]struct{} {
	tokens := make(map[string]struct{})

	lower := strings.ToLower(name)
	if len(lower) >= 3 {
		tokens[lower] = struct{}{}
	}

	for _, part := range strings.Split(lower, "-") {
		part = strings.TrimSpace(part)
		if len(part) < 3 {
			continue
		}
		if _, ok := stopwords[part]; ok {
			continue
		}
		if _, ok := programmingStopwords[part]; ok {
			continue
		}
		tokens[part] = struct{}{}
	}

	return tokens
}

// buildTokenSet builds a tokenSet for a single skill from name + description + body.
func buildTokenSet(skill config.StoredSkill) tokenSet {
	nameTokens := tokenizeName(skill.Name)
	allText := skill.Name + " " + skill.Description + " " + skill.Body
	return tokenSet{
		skill:      skill,
		tokens:     tokenize(allText),
		nameTokens: nameTokens,
	}
}

// Suggest finds skills whose tokens match the user message. Returns skills
// sorted by relevance score (higher = better), limited to maxResults. Skills
// that opt out of model invocation are skipped.
func Suggest(skills []config.StoredSkill, userMessage string, maxResults int) []Suggested {
	userTokens := tokenize(userMessage)
	if len(userTokens) == 0 {
		return nil
	}

	var matches []Suggested
	for _, skill := range skills {
		if skill.DisableModelInvocation {
			continue
		}
		if skill.UserInvocable != nil && !*skill.UserInvocable {
			continue
		}

		ts := buildTokenSet(skill)

		nameMatches := 0
		otherMatches := 0
		for token := range userTokens {
			if _, ok := ts.nameTokens[token]; ok {
				nameMatches++
			} else if _, ok := ts.tokens[token]; ok {
				otherMatches++
			}
		}

		// Minimum threshold: at least 2 matched tokens, or 1 name token match.
		if nameMatches == 0 && otherMatches < 2 {
			continue
		}

		score := float64(nameMatches*3+otherMatches) / float64(len(userTokens))
		matches = append(matches, Suggested{
			Skill: skill,
			Score: score,
		})
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Score > matches[j].Score
	})

	if maxResults > 0 && len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	return matches
}
