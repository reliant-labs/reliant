package webruntimepeers

import "testing"

// @opentelemetry/api 1.10 ships only .d.mts / .d.cts declarations. A pin at
// either is a file Next's webpack cannot parse ("Module parse failed:
// Unexpected token" on `declare const`), so such a package is pinned by its
// directory: no entry.
func TestDeclarationEntry_SkipsNonDtsDeclarations(t *testing.T) {
	manifest := []byte(`{"name":"@opentelemetry/api","types":"./dist/index.d.cts","exports":{".":{"require":{"types":"./dist/index.d.cts","default":"./dist/index.cjs"},"default":{"types":"./dist/index.d.mts","default":"./dist/index.mjs"}}}}`)
	if got := DeclarationEntry(manifest, nil); got != "" {
		t.Fatalf("DeclarationEntry = %q, want no entry (directory pin)", got)
	}
	both := []byte(`{"types":"./dist/index.d.cts","exports":{".":{"types":"./dist/index.d.ts"}}}`)
	if got := DeclarationEntry(both, nil); got != "dist/index.d.ts" {
		t.Fatalf("DeclarationEntry = %q, want the .d.ts", got)
	}
}
