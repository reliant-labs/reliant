package templates

import (
	"strings"
	"testing"
)

// Every browser frontend is scaffolded with a public/_headers that refuses
// framing by another origin. Hosted static sites send no anti-framing header
// of their own, so without it a signed-in app could be framed and clickjacked
// by any page, a sibling tenant's included (forge bug 87e9b689). The file is
// the project's to edit; the scaffold only makes the safe policy the default.
func TestBrowserFrontendsScaffoldAntiFramingHeaders(t *testing.T) {
	for _, kind := range []string{"nextjs", "vite-spa"} {
		files, err := ListFrontendTree(kind)
		if err != nil {
			t.Fatalf("ListFrontendTree(%q): %v", kind, err)
		}
		var found *FrontendTreeFile
		for i := range files {
			if files[i].Rel == "public/_headers" {
				found = &files[i]
			}
		}
		if found == nil {
			t.Errorf("%s: the scaffolded tree has no public/_headers", kind)
			continue
		}
		body, err := FrontendTemplates().Get(found.Path)
		if err != nil {
			t.Fatalf("%s: read %s: %v", kind, found.Path, err)
		}
		// Parsed the way the hosting edge reads it: a column-0 pattern, then
		// indented "Name: value" lines.
		var rule string
		got := map[string]string{}
		for _, line := range strings.Split(string(body), "\n") {
			switch {
			case strings.TrimSpace(line) == "", strings.HasPrefix(strings.TrimSpace(line), "#"):
			case !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t"):
				rule = strings.TrimSpace(line)
			case rule == "/*":
				name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
				if ok {
					got[strings.TrimSpace(name)] = strings.TrimSpace(value)
				}
			}
		}
		if got["Content-Security-Policy"] != "frame-ancestors 'self'" {
			t.Errorf("%s: /* Content-Security-Policy = %q, want frame-ancestors 'self'", kind, got["Content-Security-Policy"])
		}
		if got["X-Frame-Options"] != "SAMEORIGIN" {
			t.Errorf("%s: /* X-Frame-Options = %q, want SAMEORIGIN", kind, got["X-Frame-Options"])
		}
	}
}
