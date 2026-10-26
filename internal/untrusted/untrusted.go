// Copyright (c) 2025 Reliant Labs

// Package untrusted renders text that an outside party can write (an error
// message, an issue title, a webhook label) so it can be put in front of a
// model as quoted data and not read as part of the prompt.
//
// The text of an app's browser errors is forgeable by anyone who can load one
// of its pages, so anything derived from telemetry is attacker input. The
// rules here are structural, not a list of bad phrases: no control or
// invisible characters, one line, bounded length, and — for JSON — angle
// brackets, ampersands and quotes escaped, so the value cannot close the
// delimiter it is wrapped in or open a tag of its own.
//
//forge:exclude-contract: pure string functions with no collaborator or second implementation
package untrusted

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultMaxString bounds each string inside JSON.
	DefaultMaxString = 400
	// maxListItems bounds a list rendered by JSON.
	maxListItems = 50
	// maxDepth bounds how deep JSON follows nested maps and lists.
	maxDepth = 6
	// maxKeyLen bounds a map key.
	maxKeyLen = 64
	// ellipsis ends a string that was cut.
	ellipsis = "…"
)

// Text returns s as one plain line of at most max runes (0 or less means
// DefaultMaxString): invalid UTF-8 replaced, control, format (zero-width,
// bidirectional override, tag) and private-use characters dropped, any run of
// whitespace collapsed to one space, backticks turned into apostrophes so the
// text cannot open or close a code fence, and the result cut with an ellipsis
// when it is longer.
func Text(s string, max int) string {
	if max <= 0 {
		max = DefaultMaxString
	}
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	b.Grow(min(len(s), max*4))
	space := false
	runes := 0
	for _, r := range s {
		switch {
		case r == '`':
			r = '\''
		case unicode.IsSpace(r), r == '\u2028', r == '\u2029':
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r):
			continue
		}
		if space {
			b.WriteByte(' ')
			runes++
			space = false
		}
		if runes >= max {
			return truncate(b.String(), max)
		}
		b.WriteRune(r)
		runes++
	}
	return b.String()
}

// Line is Text for a context that shows the string as it is, with no encoder
// between it and the reader (an issue title, a chat line): angle brackets are
// replaced by single guillemets, so the text cannot spell a tag. JSON does not
// need this, because it escapes them and keeps the data exact.
func Line(s string, max int) string {
	return Text(strings.NewReplacer("<", "\u2039", ">", "\u203a").Replace(s), max)
}

// truncate cuts s so that, with the ellipsis, it is at most max runes.
func truncate(s string, max int) string {
	if max <= 1 {
		return ellipsis
	}
	if utf8.RuneCountInString(s) > max-1 {
		r := []rune(s)
		s = string(r[:max-1])
	}
	return strings.TrimRight(s, " ") + ellipsis
}

// JSON renders v as compact JSON in which every string is passed through Text
// (maxString runes each, 0 or less means DefaultMaxString), map keys are
// sorted and shortened, lists and nesting are bounded, and the encoder
// escapes <, > and &. v is what encoding/json accepts: strings, numbers,
// bools, nil, []any and map[string]any. Anything else is rendered as its
// string form.
func JSON(v any, maxString int) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(clean(v, maxString, 0)); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func clean(v any, maxString, depth int) any {
	switch x := v.(type) {
	case nil, bool, int, int64, uint64, float64:
		return x
	case string:
		return Text(x, maxString)
	case []any:
		if depth >= maxDepth {
			return ellipsis
		}
		n := min(len(x), maxListItems)
		out := make([]any, 0, n+1)
		for _, e := range x[:n] {
			out = append(out, clean(e, maxString, depth+1))
		}
		if len(x) > n {
			out = append(out, ellipsis)
		}
		return out
	case []string:
		items := make([]any, len(x))
		for i, e := range x {
			items[i] = e
		}
		return clean(items, maxString, depth)
	case map[string]any:
		if depth >= maxDepth {
			return ellipsis
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[Text(k, maxKeyLen)] = clean(x[k], maxString, depth+1)
		}
		return out
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ellipsis
		}
		var generic any
		if json.Unmarshal(b, &generic) != nil {
			return ellipsis
		}
		if _, same := generic.(string); same {
			return Text(string(b), maxString)
		}
		return clean(generic, maxString, depth+1)
	}
}
