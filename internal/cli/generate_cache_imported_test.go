package cli

// The generate cache's coverage of imported proto modules (package.json
// forge.imported_hooks). Such a module is a vendored copy of another repo's
// contract: outside the project's buf workspace and, in the case that
// motivated it, inside a wholly gitignored directory — which the cache's
// catch-all for ignored files collapses away. An edit there changes which
// hooks generate writes (and whether each is a query or a mutation), so it
// must turn the next run into a full one.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const importedHooksManifest = `{"forge": {"imported_hooks": [{"proto_root": "../../proto-vendor/other", "gen_dir": "other", "services": ["other.v1.FooService"]}]}}` + "\n"

// withIgnoredImportedModule points the fixture's web frontend at an imported
// module under proto-vendor/, which .gitignore ignores whole.
func withIgnoredImportedModule(t *testing.T, f *cacheFixture) {
	t.Helper()
	ignore, err := os.ReadFile(filepath.Join(f.root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, ".gitignore", string(ignore)+"proto-vendor/\n")
	f.write(t, "frontends/web/package.json", importedHooksManifest)
	f.write(t, "proto-vendor/other/buf.yaml", "version: v2\n")
	f.write(t, "proto-vendor/other/other/v1/foo.proto", "syntax = \"proto3\";\npackage other.v1;\n")
	f.write(t, "proto-vendor/other/README.md", "vendored by buf export\n")
}

// TestGenerateCacheImportedProtoModuleInvalidates is the regression test for
// forge#2707's review: each change to an ignored imported module, alone,
// must invalidate the record and be named in the reason.
func TestGenerateCacheImportedProtoModuleInvalidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *cacheFixture)
		reason string
	}{
		{"ignored imported proto edited", func(t *testing.T, f *cacheFixture) {
			f.write(t, "proto-vendor/other/other/v1/foo.proto",
				"syntax = \"proto3\";\npackage other.v1;\nservice FooService { rpc ListFoos(Req) returns (Res); }\n")
		}, "proto-vendor/other/other/v1/foo.proto"},
		{"ignored imported proto added", func(t *testing.T, f *cacheFixture) {
			f.write(t, "proto-vendor/other/other/v1/bar.proto", "syntax = \"proto3\";\npackage other.v1;\n")
		}, "proto-vendor/other/other/v1/bar.proto"},
		{"ignored imported proto deleted", func(t *testing.T, f *cacheFixture) {
			mustRemove(t, filepath.Join(f.root, "proto-vendor", "other", "other", "v1", "foo.proto"))
		}, "proto-vendor/other/other/v1/foo.proto"},
		{"ignored imported buf.yaml edited", func(t *testing.T, f *cacheFixture) {
			f.write(t, "proto-vendor/other/buf.yaml", "version: v2\nmodules:\n  - path: other\n")
		}, "proto-vendor/other/buf.yaml"},
		{"ignored imported buf.lock added", func(t *testing.T, f *cacheFixture) {
			f.write(t, "proto-vendor/other/buf.lock", "version: v2\n")
		}, "proto-vendor/other/buf.lock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // each row owns its fixture; nothing global is touched
			f := newCacheFixture(t)
			withIgnoredImportedModule(t, f)
			m := f.record(t)
			if d := diffGenerateFingerprint(m, f.fingerprint(t, m)); d != "" {
				t.Fatalf("an untouched tree must match its record, got: %s", d)
			}
			tc.mutate(t, f)
			d := diffGenerateFingerprint(m, f.fingerprint(t, m))
			if d == "" {
				t.Fatalf("FALSE SKIP: %s did not invalidate the record", tc.name)
			}
			if !strings.Contains(d, tc.reason) {
				t.Errorf("the miss reason should name %q, got: %s", tc.reason, d)
			}
		})
	}
}

// TestGenerateCacheImportedModuleNonInputStillMatches pins the other side:
// a file buf never reads, in the same ignored module, is not a change.
func TestGenerateCacheImportedModuleNonInputStillMatches(t *testing.T) {
	f := newCacheFixture(t)
	withIgnoredImportedModule(t, f)
	m := f.record(t)
	f.write(t, "proto-vendor/other/README.md", "re-exported\n")
	if d := diffGenerateFingerprint(m, f.fingerprint(t, m)); d != "" {
		t.Fatalf("a non-buf file of an imported module must not invalidate the record, got: %s", d)
	}
}
