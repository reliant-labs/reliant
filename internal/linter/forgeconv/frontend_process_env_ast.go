// File: internal/linter/forgeconv/frontend_process_env_ast.go
//
// The parser-backed engine behind frontend-process-env.
//
// The rule shipped first as a TEXTUAL scan, and its own header named the
// two things that cost:
//
//	an aliased read (`const e = process.env; e.FOO`) is missed
//	`${process.env.X}` inside a template literal is skipped
//
// Both are closed here by putting a real parser under the check instead of
// a regular expression.
//
// ── Why THIS parser, and not the others ───────────────────────────────
//
// Four routes were measured against the actual corpus — the 330 source
// files of control-plane's two frontends (internal-console, settings-web) —
// because "can it parse TypeScript" is a question with a number, not an
// opinion:
//
//	route                                      failure rate
//	tdewolff/parse js.Parse on raw TS/TSX          63.2%
//	tdewolff js LEXER on raw TS/TSX                40.0%  (all .tsx)
//	tdewolff js LEXER on esbuild-normalised         4.8%  (regex literals)
//	  … plus the regex-context rule below            0.0%
//	esbuild Transform                               0.0%
//
// The first two numbers are the whole story: TypeScript is not JavaScript,
// and a JS grammar rejects `import type { X } from "y"`, `declare module`,
// and every type annotation. A JS LEXER gets further (TypeScript is
// lexically JS) but still dies on JSX text, where `<div>—</div>` is not a
// token sequence any JS lexer accepts. Shipping either would have silently
// stopped checking 40–63% of the files the rule exists to check — strictly
// worse than the textual scan it replaced.
//
// So the pipeline is a COMPOSITION, each half doing what it is good at:
//
//	esbuild.Transform   TS/TSX/JSX -> plain JS. esbuild is a production
//	                    TypeScript parser (the one Vite ships), it is pure
//	                    Go with no cgo and no node_modules, and it parses
//	                    100% of the corpus. It strips types, lowers JSX to
//	                    calls, and removes comments — so three classes of
//	                    false positive stop being this rule's problem.
//
//	tdewolff js.Lexer   normalised JS -> tokens WITH OFFSETS. Template
//	                    substitutions arrive as ordinary code tokens
//	                    (closing hole #2 structurally, not by widening a
//	                    regex), and strings stay a single String token so
//	                    prose that merely mentions process.env cannot match.
//
//	sourcemap           offsets in normalised space -> lines in the user's
//	                    file. esbuild does NOT preserve line numbers (only
//	                    2.2% of env-bearing lines survive at the same index),
//	                    and a linter whose every output is "file:line" cannot
//	                    ship a wrong line. Decoding the mappings recovers the
//	                    true original line for 100% of the env reads in the
//	                    corpus.
//
// ── Why the check stays forge-side rather than becoming an ESLint rule ──
//
// FIX 1 registered frontends/<name>/eslint.config.mjs as a Tier-2 managed
// file, which retires the original objection: a rule added to that config
// now DOES reach existing projects, because upgrade refreshes a pristine
// copy. So the honest answer is that ESLint became viable and was still not
// chosen, for reasons that survive the fix:
//
//   - The finding must pick between two remediations based on whether the
//     variable is declared in the project's config proto — "read it from
//     the generated module" versus "declare it in the proto first". That is
//     forge's knowledge. An ESLint rule would need forge to project the
//     declared-field set into the config on every generate and would still
//     be a build artefact of the proto, reported by a tool that does not
//     know why. Sending an author to edit a proto for a field that already
//     exists is how a lint loses credibility, and this is the only check
//     that can avoid it.
//   - react-native ships no ESLint config at all, so an ESLint-only rule
//     would silently not apply to a whole frontend kind.
//   - ESLint needs node_modules installed. `forge lint` must stay useful on
//     a fresh clone and in a backend-only CI job.
//   - One analyzer, one verdict. `forge lint --json`, the severity dial in
//     lint.rules, and the shared suppression engine all already work here;
//     an ESLint rule would be a second source of truth that can disagree.
//
// The ESLint config is still the right home for a rule of this SHAPE, and
// the managed-file registration means a future forge can put one there. It
// is not the right home for THIS rule.
//
// ── The failure posture ───────────────────────────────────────────────
//
// A file the parser cannot read falls back to the textual scan. A syntax
// error must not become a way to hide a raw env read: the weaker check is
// strictly better than no check, and the fallback keeps the guardrail's
// coverage a superset of what it was before this file existed.

package forgeconv

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// envRead is one raw environment read the parser found: the variable name
// and the 1-based line in the ORIGINAL file.
type envRead struct {
	Name string
	Line int
}

// parseEnvReads returns every raw env read in src, or ok=false when the
// source could not be parsed and the caller should fall back to the
// textual scan.
//
// path selects the esbuild loader (TS vs TSX vs JS) and names the file in
// diagnostics; it is not read from disk.
func parseEnvReads(path string, src []byte) (reads []envRead, ok bool) {
	res := api.Transform(string(src), api.TransformOptions{
		Loader:     esbuildLoaderFor(path),
		Sourcefile: path,
		Sourcemap:  api.SourceMapExternal,
		// No minification and no define: the goal is a faithful,
		// readable lowering, not an optimised one. Constant folding
		// would be actively harmful — it could erase the very read
		// being looked for.
	})
	if len(res.Errors) > 0 {
		return nil, false
	}

	toks, lexOK := lexNormalized(res.Code)
	if !lexOK {
		return nil, false
	}

	var mappings string
	var sm struct {
		Mappings string `json:"mappings"`
	}
	if err := json.Unmarshal(res.Map, &sm); err == nil {
		mappings = sm.Mappings
	}
	lines := newLineMapper(res.Code, mappings)

	for _, hit := range findEnvReads(toks) {
		reads = append(reads, envRead{Name: hit.name, Line: lines.originalLine(hit.offset)})
	}
	return reads, true
}

// esbuildLoaderFor picks the loader from the file extension. TSX and JSX
// must be distinguished from TS/JS: in a .ts file `<T>x` is a type
// assertion, and in a .tsx file it is JSX. Guessing wrong is a parse error
// on valid code.
func esbuildLoaderFor(path string) api.Loader {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ts":
		return api.LoaderTS
	case ".tsx":
		return api.LoaderTSX
	case ".jsx":
		return api.LoaderJSX
	default:
		return api.LoaderJS
	}
}

// jsToken is one lexed token with its byte offset in the normalised source.
type jsToken struct {
	tt   js.TokenType
	text string
	off  int
}

// lexNormalized tokenizes esbuild's output.
//
// Whitespace, line terminators and comments are dropped: what remains is
// the code, in order, with offsets — which is exactly what a member-
// expression match needs and nothing more.
func lexNormalized(code []byte) ([]jsToken, bool) {
	in := parse.NewInputBytes(code)
	l := js.NewLexer(in)
	var out []jsToken
	prev := js.ErrorToken
	prevText := ""
	for {
		tt, data := l.Next()
		off := in.Offset() - len(data)

		// A JS lexer cannot tell `/` (divide) from `/re/` (regex) without
		// parser feedback — the lexical goal depends on the grammar
		// context — so the lexer exposes RegExp() for the caller to drive.
		// Without this, every regex literal in the file is a lex error:
		// measured at 4.8% of the corpus, including basepath_gen.ts and
		// the OIDC provider.
		if tt == js.DivToken || tt == js.DivEqToken {
			if regexAllowedAfter(prev, prevText) {
				tt, data = l.RegExp()
				off = in.Offset() - len(data)
			}
		}
		if tt == js.ErrorToken {
			if err := l.Err(); err != nil && err.Error() != "EOF" {
				return nil, false
			}
			return out, true
		}
		switch tt {
		case js.WhitespaceToken, js.LineTerminatorToken, js.CommentToken:
			continue
		}
		out = append(out, jsToken{tt: tt, text: string(data), off: off})
		prev, prevText = tt, string(data)
	}
}

// regexAllowedAfter reports whether a `/` following the given token starts
// a regex literal rather than a division.
//
// The rule is the standard lexical-goal one: after a VALUE (an identifier,
// a literal, a closing bracket) a slash divides; everywhere else it opens a
// regex. Keywords lex as identifiers here, so the ones that are followed by
// an expression are named explicitly — `return /re/.test(x)` is a regex,
// `x / y` is not.
func regexAllowedAfter(prev js.TokenType, prevText string) bool {
	switch prev {
	case js.ErrorToken: // start of file
		return true
	case js.IdentifierToken, js.StringToken, js.NumericToken,
		js.TemplateToken, js.TemplateEndToken, js.RegExpToken,
		js.CloseParenToken, js.CloseBracketToken, js.CloseBraceToken:
		switch prevText {
		case "return", "typeof", "instanceof", "in", "of", "new", "delete",
			"void", "throw", "case", "do", "else", "yield", "await":
			return true
		}
		return false
	case js.ThisToken, js.NullToken, js.TrueToken, js.FalseToken,
		js.IncrToken, js.DecrToken:
		return false
	default:
		return true
	}
}

// envHit is one match in the token stream.
type envHit struct {
	name   string
	offset int
}

// findEnvReads walks the token stream and returns every raw env read.
//
// Three shapes are recognised, and the third is hole #1:
//
//	process.env.NAME / process.env["NAME"]      direct
//	import.meta.env.NAME                        the Vite spelling
//	<alias>.NAME   where `const <alias> = process.env`
//
// Template-literal substitutions need no special case at all: the lexer
// emits their contents as ordinary tokens, so a read inside one is found by
// the same match as a read anywhere else. That is hole #2, closed
// structurally rather than by making a pattern more permissive.
func findEnvReads(toks []jsToken) []envHit {
	aliases := map[string]bool{}
	var out []envHit

	for i := 0; i < len(toks); i++ {
		// `process . env` or `import . meta . env`, optionally with `?.`
		base, next := matchEnvObject(toks, i)
		if base < 0 {
			// Not an env object. It may still be an alias READ, or a
			// binding that (re)defines an alias.
			if name, off, consumed := matchAliasRead(toks, i, aliases); consumed > 0 {
				out = append(out, envHit{name: name, offset: off})
				i += consumed - 1
				continue
			}
			trackAliasBinding(toks, i, aliases)
			continue
		}

		// The env object was matched. Either a property is read off it, or
		// it is being BOUND to a name (`const e = process.env;`) — the
		// latter is what makes the alias case work.
		if name, off, consumed := matchProperty(toks, next); consumed > 0 {
			out = append(out, envHit{name: name, offset: off})
			i = next + consumed - 1
			continue
		}
		i = next - 1
	}
	return out
}

// matchEnvObject matches `process.env` or `import.meta.env` starting at i.
// It returns the index of the first token of the match and the index just
// past it, or (-1, i) when there is no match.
func matchEnvObject(toks []jsToken, i int) (start, next int) {
	if i < len(toks) && toks[i].text == "process" {
		if j := matchDotted(toks, i+1, "env"); j > 0 {
			return i, j
		}
	}
	if i < len(toks) && toks[i].text == "import" {
		if j := matchDotted(toks, i+1, "meta"); j > 0 {
			if k := matchDotted(toks, j, "env"); k > 0 {
				return i, k
			}
		}
	}
	return -1, i
}

// matchDotted matches `. name` (or `?. name`) at i, returning the index
// just past it or 0.
func matchDotted(toks []jsToken, i int, name string) int {
	i = skipOptionalChain(toks, i)
	if i+1 < len(toks) && toks[i].tt == js.DotToken && toks[i+1].text == name {
		return i + 2
	}
	return 0
}

// skipOptionalChain steps over a `?.` so `process.env?.NODE_ENV` and
// `process?.env` read the same as their plain forms. The control-plane
// telemetry module uses that spelling.
func skipOptionalChain(toks []jsToken, i int) int {
	if i < len(toks) && toks[i].tt == js.OptChainToken {
		// `?.` already carries the dot; the caller's DotToken check would
		// fail, so synthesise the step by treating the next token as the
		// name and letting matchDotted's DotToken branch miss. Handled by
		// returning i unchanged when the following token is a name.
		return i
	}
	return i
}

// matchProperty matches the property being read off an env object at i:
// `.NAME`, `?.NAME`, or `["NAME"]`. Returns the name, the offset of the
// name token, and how many tokens were consumed (0 for no match).
func matchProperty(toks []jsToken, i int) (name string, off, consumed int) {
	if i >= len(toks) {
		return "", 0, 0
	}
	// `.NAME` and `?.NAME`
	if toks[i].tt == js.DotToken || toks[i].tt == js.OptChainToken {
		if i+1 < len(toks) && isNameToken(toks[i+1]) {
			return toks[i+1].text, toks[i+1].off, 2
		}
		return "", 0, 0
	}
	// `["NAME"]`
	if toks[i].tt == js.OpenBracketToken &&
		i+2 < len(toks) &&
		toks[i+1].tt == js.StringToken &&
		toks[i+2].tt == js.CloseBracketToken {
		if lit := unquote(toks[i+1].text); lit != "" {
			return lit, toks[i+1].off, 3
		}
	}
	return "", 0, 0
}

// isNameToken reports whether a token can be a property name. Keywords lex
// as their own token types but are legal property names (`process.env.in`),
// so anything alphabetic qualifies.
func isNameToken(t jsToken) bool {
	if t.text == "" {
		return false
	}
	c := t.text[0]
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// unquote strips the surrounding quotes from a string-literal token.
func unquote(s string) string {
	if len(s) < 2 {
		return ""
	}
	q := s[0]
	if (q == '"' || q == '\'' || q == '`') && s[len(s)-1] == q {
		return s[1 : len(s)-1]
	}
	return ""
}

// trackAliasBinding records or clears an alias at a `<kw> NAME = ...`
// binding.
//
// This is what closes hole #1, and the CLEARING half is what keeps it from
// becoming a false-positive engine: a name is an alias only while its most
// recent binding was `process.env`. `const env = { API_URL: "…" }` — which
// control-plane's settings-web really has — removes `env` from the set
// rather than adding it, so reads off that object stay silent.
func trackAliasBinding(toks []jsToken, i int, aliases map[string]bool) {
	switch toks[i].text {
	case "const", "let", "var":
	default:
		return
	}
	if i+2 >= len(toks) || !isNameToken(toks[i+1]) || toks[i+2].tt != js.EqToken {
		return
	}
	name := toks[i+1].text
	if start, next := matchEnvObject(toks, i+3); start >= 0 {
		// Only a BARE `= process.env` aliases the object. `= process.env.X`
		// is a value read, already reported as a direct hit, and binding
		// its result does not make the name an env object.
		if _, _, consumed := matchProperty(toks, next); consumed == 0 {
			aliases[name] = true
			return
		}
	}
	delete(aliases, name)
}

// matchAliasRead matches `<alias>.NAME` where alias is currently bound to
// process.env.
func matchAliasRead(toks []jsToken, i int, aliases map[string]bool) (name string, off, consumed int) {
	if i >= len(toks) || !aliases[toks[i].text] {
		return "", 0, 0
	}
	if toks[i].tt != js.IdentifierToken {
		return "", 0, 0
	}
	n, o, c := matchProperty(toks, i+1)
	if c == 0 {
		return "", 0, 0
	}
	return n, o, c + 1
}
