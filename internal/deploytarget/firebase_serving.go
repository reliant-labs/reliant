package deploytarget

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// This file owns what Firebase Hosting answers for a path the deploy did
// not upload, and which Cache-Control every answer carries. Both are
// serving policy forge renders into firebase.json, and both were wrong in
// the same incident (Sentry ELECTRON-CC, 2026-10-10):
//
//   - The site declared the conventional SPA fallback, a `** ->
//     /index.html` rewrite. A tab opened before a deploy then asked for a
//     code-split chunk that deploy had removed, Firebase answered the
//     missing /assets/<name>-<hash>.js with index.html and HTTP 200, and
//     the dynamic import died on a MIME/parse error instead of a 404 the
//     app could recognise. A catch-all rewrite cannot tell a client-side
//     route from a missing file; spaFallbackRewrite can.
//   - With no `headers` block, Firebase served everything — index.html and
//     content-hashed chunks alike — `max-age=3600`. The entry document must
//     revalidate on every load, or a deploy is invisible for an hour; the
//     hashed assets can be cached forever, because a new build emits new
//     names.

// spaStaticExtensions are the extensions of files a build emits. A missing
// path ending in one of them is a missing FILE, and it is answered 404 —
// never the SPA's index.html, because HTML served as JavaScript (or CSS, or
// a source map, or a wasm module) turns a broken asset reference into a
// parse error deep inside a chunk loader, with a 200 in the network tab.
//
// The test is the LAST segment's extension, matched case-insensitively,
// and only these extensions — not "any dot". A client-side route can carry
// a dot (a route param that names a file, `/workflow/flows%2Fdeploy.yaml`),
// and refusing every dotted path would turn those deep links into 404s.
// A hashed asset always ends in one of these, so a stale chunk is a 404
// wherever it lives.
var spaStaticExtensions = []string{
	// scripts, styles, maps, modules
	"js", "mjs", "cjs", "css", "map", "wasm",
	// data and documents a build ships as files
	"json", "webmanifest", "xml", "txt", "html", "htm", "pdf",
	// images
	"ico", "png", "jpg", "jpeg", "gif", "svg", "webp", "avif", "bmp",
	// fonts
	"woff", "woff2", "ttf", "otf", "eot",
	// media
	"mp4", "webm", "mp3", "ogg", "wav",
}

// Cache-Control values forge renders for a Firebase site by default.
const (
	// cacheImmutable is for the build's content-hashed asset directory: a
	// new build emits new names, so a name never changes meaning.
	cacheImmutable = "public, max-age=31536000, immutable"
	// cacheRevalidate is for everything else — above all the entry
	// document (served for every client-side route via the SPA fallback)
	// and config.js, which change on every deploy.
	cacheRevalidate = "no-cache"
)

// spaFallbackRewrite is the firebase.json rewrite that serves the SPA's
// entry document for a client-side route and for nothing else.
//
// Firebase applies a rewrite only to a path no uploaded file matches, so
// the question it must answer is "is this missing path a route or a
// file?". It answers with an RE2 `regex` (the syntax Firebase documents for
// rewrites; Go's regexp is the same RE2 dialect, which is how the tests
// pin it): the path must lie inside the fallback document's directory, and
// its last segment must not end in a static-file extension. So `/inbox`,
// `/project/<id>/` and `/admin` (for `/admin/index.html`) get the entry
// document, while a missing `/assets/InboxPage-<hash>.js` falls through to
// Firebase's 404.
//
// Why a generated regex and not a glob: RE2 has no negative lookahead, and
// Firebase's glob negation is documented not to compose (`!{a,b}` matches
// what it should exclude). The regex is the complement of "ends in one of
// spaStaticExtensions", built from a trie by staticExtComplement. It is
// unreadable by design and tested exhaustively instead.
func spaFallbackRewrite(destination string) map[string]any {
	return map[string]any{
		"regex":       spaNavigationRegex(destination),
		"destination": destination,
	}
}

// spaNavigationRegex returns the anchored RE2 expression matching every
// route-shaped path inside destination's directory.
func spaNavigationRegex(destination string) string {
	scope := path.Dir("/" + strings.TrimPrefix(destination, "/"))
	if scope == "/" {
		scope = ""
	}
	// The last segment is either dot-free (a route, or "" for a trailing
	// slash) or ends in an extension that is not a static file's.
	last := `(?:[^/.]*|[^/]*\.` + staticExtComplement() + `)`
	body := `/(?:[^/]*/)*` + last
	if scope == "" {
		return `^` + body + `$`
	}
	// A mount answers for itself too: /admin as well as /admin/...
	return `^` + regexp.QuoteMeta(scope) + `(?:` + body + `)?$`
}

// staticExtComplement returns an RE2 fragment matching exactly the strings
// over [^/.] that are NOT (case-insensitively) one of spaStaticExtensions —
// the complement of a finite set, which RE2 cannot express with a negation
// operator and therefore needs spelled out.
//
// It walks a trie of the extensions. From a node, a string is outside the
// set when it ends here and the node is not a whole extension, when its
// next character leaves the trie (anything may follow), or when it follows
// a child and the rest is outside the set from that child.
func staticExtComplement() string {
	root := &extTrie{children: map[byte]*extTrie{}}
	for _, ext := range spaStaticExtensions {
		root.insert(strings.ToLower(ext))
	}
	return root.complement()
}

type extTrie struct {
	terminal bool
	children map[byte]*extTrie
}

func (t *extTrie) insert(word string) {
	n := t
	for i := 0; i < len(word); i++ {
		c := word[i]
		next, ok := n.children[c]
		if !ok {
			next = &extTrie{children: map[byte]*extTrie{}}
			n.children[c] = next
		}
		n = next
	}
	n.terminal = true
}

func (t *extTrie) complement() string {
	if len(t.children) == 0 {
		// The end of an extension: anything longer is outside the set.
		return `[^/.]+`
	}
	keys := make([]byte, 0, len(t.children))
	for c := range t.children {
		keys = append(keys, c)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	// A character that leaves the trie: anything but a separator, a dot,
	// or one of the children (in either case).
	var excluded strings.Builder
	excluded.WriteString(`/.`)
	for _, c := range keys {
		excluded.WriteString(caseVariants(c))
	}
	alts := []string{`[^` + excluded.String() + `][^/.]*`}
	for _, c := range keys {
		alts = append(alts, charClass(c)+t.children[c].complement())
	}
	group := `(?:` + strings.Join(alts, `|`) + `)`
	if !t.terminal {
		// Ending here is outside the set too: a proper prefix of an
		// extension ("w" of "woff") is not a static extension.
		group += `?`
	}
	return group
}

// caseVariants is c in both cases when it is a letter, for use inside a
// character class.
func caseVariants(c byte) string {
	lower, upper := strings.ToLower(string(c)), strings.ToUpper(string(c))
	if lower == upper {
		return regexp.QuoteMeta(lower)
	}
	return lower + upper
}

// charClass matches c case-insensitively.
func charClass(c byte) string {
	v := caseVariants(c)
	if len(v) == 1 {
		return v
	}
	return `[` + v + `]`
}

// firebaseHeaders is the firebase.json `headers` block for a site.
//
// Firebase applies EVERY header rule whose source matches the request path
// and, for a header set twice, keeps the LAST match. So the catch-all goes
// first and the asset directory after it.
//
// With no declared rules forge renders the working default: the build's
// content-hashed asset directory is immutable for a year and everything
// else revalidates. The match is on the REQUEST path, which is what makes
// the catch-all cover the entry document served for every client-side
// route — a rule on "/index.html" would never see /inbox.
//
// Declared rules (Frontend.cache_control) replace the default entirely.
// They are first-match-wins, as on every runtime, so they are rendered in
// reverse to land under Firebase's last-match-wins.
func firebaseHeaders(rules []CacheRuleSpec, assetPrefix string) []map[string]any {
	if len(rules) > 0 {
		out := make([]map[string]any, 0, len(rules))
		for i := len(rules) - 1; i >= 0; i-- {
			out = append(out, cacheControlHeader("/"+strings.TrimPrefix(rules[i].Pattern, "/"), rules[i].CacheControl))
		}
		return out
	}
	out := []map[string]any{cacheControlHeader("**", cacheRevalidate)}
	if assetPrefix != "" {
		out = append(out, cacheControlHeader("/"+assetPrefix+"/**", cacheImmutable))
	}
	return out
}

func cacheControlHeader(source, value string) map[string]any {
	return map[string]any{
		"source":  source,
		"headers": []map[string]string{{"key": "Cache-Control", "value": value}},
	}
}
