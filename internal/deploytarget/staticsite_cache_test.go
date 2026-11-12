package deploytarget

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// uploadSnapshotRunner records, for every `gcloud storage cp` it is asked to
// run, the Cache-Control it carries and the files under its cwd — what a
// real upload would send, at the object names it would send them under.
type uploadSnapshotRunner struct {
	*fakeRunner
	t       *testing.T
	uploads map[string]string // object path -> Cache-Control ("" = none)
}

func (r *uploadSnapshotRunner) RunInDir(ctx context.Context, dir string, env map[string]string, name string, args ...string) error {
	if name == "gcloud" && len(args) > 1 && args[0] == "storage" && args[1] == "cp" {
		header := ""
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--cache-control="); ok {
				header = v
			}
		}
		if args[len(args)-2] != "." {
			r.t.Errorf("upload source must be the cwd tree (paths kept), got %v", args)
		}
		for _, f := range listFiles(r.t, dir) {
			if prev, dup := r.uploads[f]; dup {
				r.t.Errorf("%s uploaded twice (%q, then %q)", f, prev, header)
			}
			r.uploads[f] = header
		}
	}
	return r.fakeRunner.RunInDir(ctx, dir, env, name, args...)
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// uploadTree stages files, runs the real upload against a recording runner,
// and returns each object's Cache-Control.
func uploadTree(t *testing.T, spec StaticSiteSpec, files map[string]string) map[string]string {
	t.Helper()
	staging := filepath.Join(t.TempDir(), "staging")
	writeTree(t, staging, files)
	r := &uploadSnapshotRunner{fakeRunner: &fakeRunner{}, t: t, uploads: map[string]string{}}
	if err := uploadRelease(context.Background(), r, spec, staging, "deadbeef1234"); err != nil {
		t.Fatalf("uploadRelease: %v", err)
	}
	for _, c := range r.calls {
		if !strings.HasSuffix(c, " . gs://site/releases/deadbeef1234") {
			t.Errorf("upload must target the immutable release prefix, got %q", c)
		}
	}
	if len(r.uploads) != len(files) {
		t.Errorf("uploaded %d objects, staged %d: %v", len(r.uploads), len(files), r.uploads)
	}
	return r.uploads
}

// TestStaticSiteDefaultCachePolicy pins the OnBucket default when a frontend
// declares no cache_control: the same policy OnFirebase and hosted static
// hosting serve. The entry documents and the runtime config are never
// uploaded cacheable (GCS would otherwise serve them max-age=3600, so a
// deploy or a config-only promotion stays invisible for an hour), and the
// hashed asset directory is immutable.
func TestStaticSiteDefaultCachePolicy(t *testing.T) {
	cases := []struct {
		name     string
		spec     StaticSiteSpec
		files    map[string]string
		wantImmu []string
	}{
		{
			name: "root mount",
			spec: StaticSiteSpec{Bucket: "site", AssetDir: "assets"},
			files: map[string]string{
				"index.html":           "<html>",
				"config.js":            "window.__FORGE_CONFIG__={}",
				"version.json":         "{}",
				"favicon.ico":          "x",
				"assets/app-abc123.js": "a",
				"assets/css/s-1.css":   "b",
			},
			wantImmu: []string{"assets/app-abc123.js", "assets/css/s-1.css"},
		},
		{
			name: "under base_path, next export",
			spec: StaticSiteSpec{Bucket: "site", BasePath: "/admin", AssetDir: "_next/static"},
			files: map[string]string{
				"index.html":                          "<html>spa",
				"admin/index.html":                    "<html>",
				"admin/config.js":                     "window.__FORGE_CONFIG__={}",
				"admin/_next/static/chunks/a-1.js":    "a",
				"_next/static/not-this-frontend.js":   "b",
				"admin/inbox/index.html":              "<html>",
				"admin/_next/static/css/app-2.css":    "c",
				"admin/_next/static-lookalike/app.js": "d",
			},
			wantImmu: []string{"admin/_next/static/chunks/a-1.js", "admin/_next/static/css/app-2.css"},
		},
		{
			name:  "no asset_dir",
			spec:  StaticSiteSpec{Bucket: "site"},
			files: map[string]string{"index.html": "<html>", "config.js": "c", "assets/app.js": "a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uploadTree(t, tc.spec, tc.files)
			immutable := map[string]bool{}
			for _, f := range tc.wantImmu {
				immutable[f] = true
			}
			for f := range tc.files {
				want := cacheRevalidate
				if immutable[f] {
					want = cacheImmutable
				}
				if got[f] != want {
					t.Errorf("%s uploaded with Cache-Control %q, want %q", f, got[f], want)
				}
			}
			for _, entry := range []string{"index.html", "config.js", "version.json", "admin/index.html", "admin/config.js"} {
				if h, ok := got[entry]; ok && h != "no-cache" {
					t.Errorf("%s must never be uploaded cacheable, got %q", entry, h)
				}
			}
		})
	}
}

// TestStaticSiteDeclaredCacheRulesWin pins that declared rules replace the
// default, first match wins, objects keep their site-relative names (a
// wildcard source would upload `_next/static/chunks/a.js` as `a.js`), and an
// object no rule matches is uploaded with no header.
func TestStaticSiteDeclaredCacheRulesWin(t *testing.T) {
	spec := StaticSiteSpec{
		Bucket:   "site",
		AssetDir: "assets", // ignored: declared rules replace the default
		CacheControl: []CacheRuleSpec{
			{Pattern: "_next/static/**", CacheControl: "public, max-age=31536000, immutable"},
			{Pattern: "*.html", CacheControl: "public, max-age=0, must-revalidate"},
			{Pattern: "/assets/**", CacheControl: "public, max-age=600"},
		},
	}
	got := uploadTree(t, spec, map[string]string{
		"_next/static/chunks/a.js": "a",
		"index.html":               "<html>",
		"docs/index.html":          "<html>",
		"assets/app.js":            "x",
		"config.js":                "c",
	})
	want := map[string]string{
		"_next/static/chunks/a.js": "public, max-age=31536000, immutable",
		"index.html":               "public, max-age=0, must-revalidate",
		"docs/index.html":          "", // `*` does not cross "/"
		"assets/app.js":            "public, max-age=600",
		"config.js":                "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uploads =\n%v\nwant\n%v", got, want)
	}
}

func TestCacheGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**", "index.html", true},
		{"**", "a/b/c.js", true},
		{"assets/**", "assets/x.js", true},
		{"assets/**", "assets/deep/x.js", true},
		{"assets/**", "assetsx/x.js", false},
		{"assets/**", "admin/assets/x.js", false},
		{"**/*.html", "index.html", true},
		{"**/*.html", "a/b/index.html", true},
		{"*.html", "a/index.html", false},
		{"/config.js", "config.js", true},
	}
	for _, tc := range cases {
		got, err := cacheGlobMatch(tc.pattern, tc.name)
		if err != nil {
			t.Fatalf("%q: %v", tc.pattern, err)
		}
		if got != tc.want {
			t.Errorf("cacheGlobMatch(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
	if _, err := cacheGlobMatch("[", "x"); err == nil {
		t.Error("a malformed pattern must be an error, not a silent non-match")
	}
}
