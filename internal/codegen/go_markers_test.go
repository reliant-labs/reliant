package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestGoMarkersSorted(t *testing.T) {
	names := KnownGoMarkers()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("goMarkers must stay sorted (LookupGoMarker binary-searches it): %v", names)
	}
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			t.Fatalf("duplicate marker %s", names[i])
		}
	}
}

// The reliant defect: `//forge:exclude-contract — <reason>` is not the
// directive (the parser reads only the colon form), and nothing said so.
func TestClassifyGoMarkerComment(t *testing.T) {
	cases := []struct {
		raw  string
		want GoMarkerStatus
	}{
		// The one documented form, and the bare one.
		{"//forge:exclude-contract: strategy registry", GoMarkerOK},
		{"// forge:exclude-contract: strategy registry", GoMarkerOK},
		{"//forge:exclude-contract", GoMarkerOK},
		// Lookalike separators — read and refused, so reported.
		{"//forge:exclude-contract — strategy registry", GoMarkerMalformed},
		{"// forge:exclude-contract — strategy registry", GoMarkerMalformed},
		{"//forge:exclude-contract – strategy registry", GoMarkerMalformed},
		{"//forge:exclude-contract - strategy registry", GoMarkerMalformed},
		{"//forge:exclude-contract strategy registry", GoMarkerMalformed},
		{"// forge:not-a-component -- CLI only", GoMarkerMalformed},
		{"// forge:external-component — hand-wired in providers.go", GoMarkerMalformed},
		{"//forge:outbound-io: calls stripe", GoMarkerMalformed},
		{"//forge:audience — daemon", GoMarkerMalformed},
		{"//forge:audience daemon", GoMarkerOK},
		// Typos and markers from another vocabulary, in directive form.
		{"//forge:exclude-contrat: x", GoMarkerUnknown},
		{"//forge:entity", GoMarkerUnknown},
		// Prose: a spaced sentence that mentions or starts with a marker.
		{"// forge:exclude-contract, forge:outbound-io, … are package markers", GoMarkerNone},
		{"// forge:optional-dep marks the field as nillable", GoMarkerNone},
		{"// forge:entity", GoMarkerNone},
		{"// see the forge:exclude-contract docs", GoMarkerNone},
		{"// forge:read-only-ish prose", GoMarkerNone},
		// Markers whose own parser owns their grammar.
		{"//forge:root api command=serve migrate=apply", GoMarkerOK},
		{"//forge:lint-disable-next-line forge-go-markers: fixture", GoMarkerOK},
		{"// forge:hash=0123abcd", GoMarkerOK},
		{"//forge:no-orm: hand-written repository", GoMarkerOK},
	}
	for _, tc := range cases {
		if got := ClassifyGoMarkerComment(tc.raw).Status; got != tc.want {
			t.Errorf("ClassifyGoMarkerComment(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// Every marker whose recognizer is a pure function of one comment must agree
// with the registry: what the recognizer reads, the registry calls OK, and
// what it ignores, the registry calls Malformed. A registry that drifts from
// its recognizer is a lint that either cries wolf or keeps the silence.
func TestGoMarkerRegistryAgreesWithRecognizers(t *testing.T) {
	tails := []string{"", ": why", ":why", " — why", " - why", " why", " daemon", "—why"}
	recognizers := map[string]func(raw string) bool{
		directiveExcludeContract: func(raw string) bool { _, ok := ParseExcludeContractComment(raw); return ok },
		directiveNotAComponent:   func(raw string) bool { _, ok := ParseNotAComponentComment(raw); return ok },
		directiveExternalComponent: func(raw string) bool {
			return commentEquals(&ast.Comment{Text: raw}, directiveExternalComponent)
		},
		"forge:outbound-io":        func(raw string) bool { return commentEquals(&ast.Comment{Text: raw}, "forge:outbound-io") },
		directiveConstructorMarker: func(raw string) bool { return commentEquals(&ast.Comment{Text: raw}, directiveConstructorMarker) },
		directiveNoObserve:         func(raw string) bool { return commentEquals(&ast.Comment{Text: raw}, directiveNoObserve) },
		directiveServiceMarker: func(raw string) bool {
			return HasServiceContractMarker(&ast.CommentGroup{List: []*ast.Comment{{Text: raw}}})
		},
		"forge:optional-dep": func(raw string) bool { return HasOptionalDepMarker(raw) },
		ResolvesCallerDirective: func(raw string) bool {
			return hasLineDirective(&ast.CommentGroup{List: []*ast.Comment{{Text: raw}}}, ResolvesCallerDirective)
		},
		"forge:audience": func(raw string) bool { return audienceDirectiveRE.MatchString(raw) },
	}
	for name, reads := range recognizers {
		m, ok := LookupGoMarker(name)
		if !ok {
			t.Errorf("%s has a recognizer but no registry entry", name)
			continue
		}
		for _, prefix := range []string{"//", "// "} {
			if name == "forge:audience" && prefix == "// " {
				continue // the composition graph reads its directives unspaced
			}
			for _, tail := range tails {
				raw := prefix + name + tail
				got := ClassifyGoMarkerComment(raw)
				if reads(raw) != (got.Status == GoMarkerOK) {
					t.Errorf("%q: recognizer reads it = %v, registry says %v (%s grammar)", raw, reads(raw), got.Status, m.Form)
				}
			}
		}
	}
}

// A package whose exclude-contract is spelled with an em-dash is named, so
// the contract lint can say why the opt-out did not happen.
func TestMalformedGoMarkers_FindsTheEmDashExclude(t *testing.T) {
	dir := t.TempDir()
	src := "// Package strategy is a registry.\n//\n//forge:exclude-contract — strategy registry, one constructor per algorithm\npackage strategy\n"
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if HasExcludeContractDirective(dir) {
		t.Fatalf("the em-dash spelling must not be read as the directive (one documented form)")
	}
	bad := MalformedGoMarkers(dir, directiveExcludeContract)
	if len(bad) != 1 || bad[0].Line != 3 {
		t.Fatalf("MalformedGoMarkers = %+v, want one at line 3", bad)
	}
	// Parses as Go, so the scan is over real comments.
	if _, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, "doc.go"), nil, parser.ParseComments); err != nil {
		t.Fatal(err)
	}
}
