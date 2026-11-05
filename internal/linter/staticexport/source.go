package staticexport

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// sourceFile is one JS/TS source of the frontend, read once.
type sourceFile struct {
	// rel is the path relative to the frontend dir, slash-separated.
	rel string
	// code is the source with every comment blanked out — same length,
	// newlines kept — so offsets and line numbers still point at the real
	// file while a commented-out `output: "export"` or `'use server'`
	// matches nothing.
	code string
	// rawHeader is the start of the file WITH its comments, where forge's
	// templates write the banner that says a file came from a scaffold.
	rawHeader string
}

// rawHeaderBytes is how much of a file's head is kept with comments intact.
const rawHeaderBytes = 4096

// newSourceFile builds a sourceFile from a file's contents.
func newSourceFile(rel, raw string) *sourceFile {
	head := raw
	if len(head) > rawHeaderBytes {
		head = head[:rawHeaderBytes]
	}
	return &sourceFile{rel: rel, code: stripComments(raw), rawHeader: head}
}

// lineAt is the 1-based line of byte offset off.
func (s *sourceFile) lineAt(off int) int {
	if off < 0 {
		return 1
	}
	if off > len(s.code) {
		off = len(s.code)
	}
	return strings.Count(s.code[:off], "\n") + 1
}

// sourceExts are the extensions whose contents ship in a Next.js bundle.
var sourceExts = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".mts": true, ".cts": true, ".mdx": true,
}

// skippedDirs never ship, wherever they appear.
var skippedDirs = map[string]bool{"node_modules": true, "__tests__": true, "__mocks__": true}

// skippedRootDirs never ship when they sit at the frontend's root: build
// output, static assets, and test suites. Deeper down the same names are
// ordinary route segments (`src/app/build/page.tsx`), so they are scanned.
var skippedRootDirs = map[string]bool{
	"out": true, "dist": true, "build": true, "coverage": true, "public": true,
	"e2e": true, "test": true, "tests": true,
}

// collectSources reads every shipping source file under dir.
func collectSources(dir string) ([]*sourceFile, error) {
	var out []*sourceFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is simply not scanned
		}
		name := d.Name()
		if d.IsDir() {
			if p == dir {
				return nil
			}
			// .next, .next-prod, .turbo, .git … are build state, not source.
			if skippedDirs[name] || strings.HasPrefix(name, ".") ||
				(filepath.Dir(p) == dir && skippedRootDirs[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExts[filepath.Ext(name)] || isTestOrTypeFile(name) {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil //nolint:nilerr // unreadable file: nothing to judge
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, newSourceFile(filepath.ToSlash(rel), string(raw)))
		return nil
	})
	return out, err
}

// isTestOrTypeFile reports a file that never ships: a test, a story, a
// declaration file, or a tooling config the bundler does not import.
func isTestOrTypeFile(name string) bool {
	for _, marker := range []string{".test.", ".spec.", ".stories.", ".d."} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	for _, cfg := range []string{"vitest.config.", "playwright.config.", "eslint.config.", "prettier.config.", "postcss.config.", "stylelint.config.", "tailwind.config.", "jest.config."} {
		if strings.HasPrefix(name, cfg) {
			return true
		}
	}
	return false
}

// stripComments blanks every // and /* */ comment in JS/TS source, replacing
// each comment byte with a space and keeping newlines, so the result has the
// same length and line structure as the input. String and template literals
// are respected: a `//` inside "http://…" is not a comment.
//
// It is a scanner, not a parser. A regex literal containing a quote can
// confuse it for the rest of that line, which costs at most a missed or extra
// match on one line — acceptable for a lint whose rules all stay silent when
// unsure.
func stripComments(src string) string {
	b := []byte(src)
	const (
		code = iota
		lineComment
		blockComment
		singleQuote
		doubleQuote
		template
	)
	state := code
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch state {
		case code:
			switch {
			case c == '/' && i+1 < len(b) && b[i+1] == '/':
				state = lineComment
				b[i], b[i+1] = ' ', ' '
				i++
			case c == '/' && i+1 < len(b) && b[i+1] == '*':
				state = blockComment
				b[i], b[i+1] = ' ', ' '
				i++
			case c == '\'':
				state = singleQuote
			case c == '"':
				state = doubleQuote
			case c == '`':
				state = template
			}
		case lineComment:
			if c == '\n' {
				state = code
			} else {
				b[i] = ' '
			}
		case blockComment:
			if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				b[i], b[i+1] = ' ', ' '
				i++
				state = code
			} else if c != '\n' {
				b[i] = ' '
			}
		case singleQuote, doubleQuote:
			quote := byte('\'')
			if state == doubleQuote {
				quote = '"'
			}
			switch c {
			case '\\':
				i++
			case quote, '\n':
				state = code
			}
		case template:
			switch c {
			case '\\':
				i++
			case '`':
				state = code
			}
		}
	}
	return string(b)
}
