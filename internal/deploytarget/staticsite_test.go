package deploytarget

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeStaticSiteFrontend builds a StaticSiteFrontend rooted at
// projectDir with a Next.js-style admin export under /admin plus a
// sibling Vite SPA bundled at the site root — the same recipe
// fakeFirebaseFrontend uses, deliberately, so the two providers'
// assembled layouts are comparable.
func fakeStaticSiteFrontend() StaticSiteFrontend {
	return StaticSiteFrontend{
		Name:      "admin-web",
		Path:      "admin-web",
		DevRunner: "npm",
		BuildEnv: map[string]string{
			"NEXT_PUBLIC_API_URL": "https://api.staging.example.com",
		},
		Spec: StaticSiteSpec{
			Bucket:    "gs://reliant-staging-web",
			PublicDir: "out",
			BasePath:  "/admin",
			Bundle: []BundleDirSpec{
				{Src: "../reliant-web/dist", Dest: ""},
			},
			CacheControl: []CacheRuleSpec{
				{Pattern: "_next/static/**", CacheControl: "public, max-age=31536000, immutable"},
				{Pattern: "**", CacheControl: "public, max-age=0, must-revalidate"},
			},
			CDN: &StaticSiteCDNSpec{
				URLMap:               "reliant-staging-lb",
				ExtraInvalidatePaths: []string{"/manifest.json"},
			},
			KeepReleases: 5,
		},
	}
}

// TestStaticSiteDryRunPlan asserts --dry-run prints the build command,
// the assembled layout, the release/live prefixes, the upload commands
// with their Cache-Control headers, and the invalidation paths — without
// running npm or gcloud, and without touching the staging tree.
func TestStaticSiteDryRunPlan(t *testing.T) {
	fake := &fakeRunner{}
	prov := StaticSiteProvider{ProjectDir: t.TempDir(), Runner: fake}

	out := captureStdout(t, func() {
		group := ServiceGroup{
			ProviderID:  prov.Name(),
			StaticSites: []StaticSiteFrontend{fakeStaticSiteFrontend()},
			DryRun:      true,
		}
		if err := prov.Deploy(context.Background(), group); err != nil {
			t.Fatalf("dry-run deploy: %v", err)
		}
	})

	if len(fake.calls) != 0 {
		t.Fatalf("dry-run executed %d command(s); want 0: %v", len(fake.calls), fake.calls)
	}

	want := []string{
		`static-site deploy plan for frontend "admin-web"`,
		"npm install",
		"npm run build",
		"NEXT_PUBLIC_API_URL=https://api.staging.example.com",
		"-> /admin",       // public_dir mounted under base_path
		"-> /   (bundle:", // sibling SPA at the site root
		"gs://reliant-staging-web/releases/<digest>",
		"gs://reliant-staging-web/live",
		"--cache-control=public, max-age=31536000, immutable",
		"gcloud storage rsync --recursive --delete-unmatched-destination-objects",
		"invalidate-cdn-cache reliant-staging-lb --path /admin/config.js",
		"invalidate-cdn-cache reliant-staging-lb --path /manifest.json",
		"retention:    keep 5 releases",
	}
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Errorf("dry-run output missing %q\n---\n%s", s, out)
		}
	}
}

// TestStaticSiteAssemblesSameTreeAsFirebase is the anti-drift test for
// the extraction. Both providers now project onto the shared StageInput,
// and this asserts that a frontend described identically to each one
// produces a byte-identical assembled tree.
//
// It exists because the two providers' build-and-assemble halves were
// one copy split in two, and the failure mode of a silent divergence is
// nasty: a site that deploys fine but serves subtly different content
// depending on which target it happens to use.
func TestStaticSiteAssemblesSameTreeAsFirebase(t *testing.T) {
	// Both fixtures declare the sibling bundle as "../reliant-web/dist",
	// which resolves against the project root — so the sibling lives
	// NEXT TO projectDir, not inside it.
	root := t.TempDir()
	projectDir := filepath.Join(root, "app")

	// A frontend build output plus a sibling pre-built SPA.
	writeTree(t, filepath.Join(projectDir, "admin-web", "out"), map[string]string{
		"index.html":              "<html>admin</html>",
		"_next/static/app.abc.js": "console.log(1)",
		"config.js":               "window.__FORGE_CONFIG__={env:'dev'}", // the dev copy that must be overwritten
	})
	writeTree(t, filepath.Join(root, "reliant-web", "dist"), map[string]string{
		"index.html": "<html>spa</html>",
	})

	const runtimeConfig = "window.__FORGE_CONFIG__={env:'prod'}"

	fbStaging := filepath.Join(t.TempDir(), "fb")
	fbProv := FirebaseProvider{ProjectDir: projectDir, Runner: &fakeRunner{}, StagingRoot: fbStaging}
	fbFE := fakeFirebaseFrontend(projectDir, fbStaging)
	fbFE.RuntimeConfigJS = runtimeConfig
	fbPlan, err := fbProv.buildPlan(fbFE)
	if err != nil {
		t.Fatalf("firebase plan: %v", err)
	}
	if err := assembleStaging(fbPlan.Stage); err != nil {
		t.Fatalf("firebase assemble: %v", err)
	}

	ssProv := StaticSiteProvider{ProjectDir: projectDir, Runner: &fakeRunner{}}
	ssFE := fakeStaticSiteFrontend()
	ssFE.RuntimeConfigJS = runtimeConfig
	ssPlan, err := ssProv.buildPlan(ssFE)
	if err != nil {
		t.Fatalf("static-site plan: %v", err)
	}
	// The staging dir comes off the RESOLVED PLAN — the same value
	// production uses — rather than from a test-only override knob, so
	// this exercises the real path rather than a shape only tests can
	// produce.
	ssStaging := ssPlan.Stage.StagingDir
	t.Cleanup(func() { _ = os.RemoveAll(ssStaging) })
	if err := assembleStaging(ssPlan.Stage); err != nil {
		t.Fatalf("static-site assemble: %v", err)
	}

	fbFiles := readTree(t, fbStaging)
	ssFiles := readTree(t, ssStaging)

	if len(fbFiles) != len(ssFiles) {
		t.Fatalf("assembled trees differ in file count: firebase=%v static-site=%v", keysOf(fbFiles), keysOf(ssFiles))
	}
	for rel, want := range fbFiles {
		got, ok := ssFiles[rel]
		if !ok {
			t.Errorf("static-site tree missing %q", rel)
			continue
		}
		if got != want {
			t.Errorf("content differs at %q:\n firebase:    %q\n static-site: %q", rel, want, got)
		}
	}

	// The runtime config document must have OVERWRITTEN the dev copy that
	// travelled inside the build output — in both trees. This is the
	// promotion-correctness property, and getting it backwards ships dev
	// config to prod.
	if got := ssFiles["admin/config.js"]; got != runtimeConfig {
		t.Errorf("runtime config not written last: admin/config.js = %q, want %q", got, runtimeConfig)
	}
}

// TestStagingDigestIsContentAddressed pins the two properties the
// release archive depends on: identical content hashes identically
// (so a redeploy of unchanged content is a no-op rather than a new
// archive), and any content change produces a different digest (so a
// release prefix can never be silently overwritten with different bytes).
func TestStagingDigestIsContentAddressed(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	files := map[string]string{"index.html": "<html>hi</html>", "assets/app.js": "x=1"}
	writeTree(t, a, files)
	writeTree(t, b, files)

	da, err := stagingDigest(a)
	if err != nil {
		t.Fatalf("digest a: %v", err)
	}
	db, err := stagingDigest(b)
	if err != nil {
		t.Fatalf("digest b: %v", err)
	}
	if da != db {
		t.Errorf("identical trees produced different digests: %s vs %s", da, db)
	}

	// One byte of content changes the digest.
	if err := os.WriteFile(filepath.Join(b, "index.html"), []byte("<html>ho</html>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	dc, err := stagingDigest(b)
	if err != nil {
		t.Fatalf("digest c: %v", err)
	}
	if dc == da {
		t.Errorf("changed content produced the same digest %s", dc)
	}
}

// TestReleasesToPrune_NeverDeletesLiveOrPrevious is the load-bearing
// retention test. Retention must never be able to delete the live release
// or its predecessor, whose assets pages loaded moments ago still fetch.
func TestReleasesToPrune_NeverDeletesLiveOrPrevious(t *testing.T) {
	all := []string{"aaa", "bbb", "ccc", "ddd", "eee"}

	t.Run("live and previous are exempt even at the minimum count", func(t *testing.T) {
		got := releasesToPrune(all, 2, "eee", "ddd")
		for _, d := range got {
			if d == "eee" || d == "ddd" {
				t.Fatalf("pruned a protected release %q (live=eee previous=ddd); got %v", d, got)
			}
		}
	})

	t.Run("keep=0 retains everything", func(t *testing.T) {
		if got := releasesToPrune(all, 0, "eee", "ddd"); len(got) != 0 {
			t.Errorf("keep=0 must prune nothing, got %v", got)
		}
	})

	t.Run("nothing to prune when the bucket is under budget", func(t *testing.T) {
		if got := releasesToPrune([]string{"aaa", "bbb"}, 10, "bbb", "aaa"); len(got) != 0 {
			t.Errorf("under budget must prune nothing, got %v", got)
		}
	})

	t.Run("prunes down to the budget once over it", func(t *testing.T) {
		got := releasesToPrune(all, 3, "eee", "ddd")
		// 5 total, 2 exempt, budget 3 => room for 1 more => 2 pruned.
		if len(got) != 2 {
			t.Errorf("expected 2 pruned, got %v", got)
		}
	})

	t.Run("a first-ever deploy has no previous and still protects live", func(t *testing.T) {
		got := releasesToPrune([]string{"aaa"}, 2, "aaa", "")
		if len(got) != 0 {
			t.Errorf("must not prune the only (live) release, got %v", got)
		}
	})
}

// TestEffectiveKeepReleasesFloor pins that a spec reaching the provider
// with keep_releases = 1 is floored at 2 rather than honoured. The KCL
// check rejects 1 at render, so this is the belt-and-braces arm: a spec
// constructed in Go (a test, a future caller) must not be able to
// configure away the predecessor either.
func TestEffectiveKeepReleasesFloor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared int
		want     int
	}{
		{"zero means retain everything", 0, 0},
		{"one is floored to the minimum", 1, 2},
		{"two is honoured", 2, 2},
		{"larger values pass through", 10, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := StaticSiteSpec{KeepReleases: tc.declared}.effectiveKeepReleases()
			if got != tc.want {
				t.Errorf("keep_releases=%d resolved to %d, want %d", tc.declared, got, tc.want)
			}
		})
	}
}

// TestInvalidationPaths pins the rate-limit-conscious default.
//
// The default must stay O(a few paths) regardless of site size: Cloud
// CDN meters invalidations per project, so a policy that scaled with the
// number of files would exhaust the quota for a team iterating on a
// preview environment — and the failure would arrive as a deploy error
// dozens of pushes after the habit formed.
func TestInvalidationPaths(t *testing.T) {
	t.Run("no cdn declared invalidates nothing", func(t *testing.T) {
		if got := invalidationPaths(StaticSiteSpec{BasePath: "/admin"}); len(got) != 0 {
			t.Errorf("no CDN must invalidate nothing, got %v", got)
		}
	})

	t.Run("none invalidates nothing", func(t *testing.T) {
		spec := StaticSiteSpec{CDN: &StaticSiteCDNSpec{URLMap: "lb", Invalidate: InvalidateNone}}
		if got := invalidationPaths(spec); len(got) != 0 {
			t.Errorf("invalidate=none must invalidate nothing, got %v", got)
		}
	})

	t.Run("all is a single blanket purge", func(t *testing.T) {
		spec := StaticSiteSpec{CDN: &StaticSiteCDNSpec{URLMap: "lb", Invalidate: InvalidateAll}}
		got := invalidationPaths(spec)
		if len(got) != 1 || got[0] != "/*" {
			t.Errorf("invalidate=all should be exactly [/*], got %v", got)
		}
	})

	t.Run("default purges only the non-content-addressed documents", func(t *testing.T) {
		spec := StaticSiteSpec{
			BasePath: "/admin",
			CDN:      &StaticSiteCDNSpec{URLMap: "lb", ExtraInvalidatePaths: []string{"/manifest.json"}},
		}
		got := invalidationPaths(spec)

		for _, want := range []string{"/", "/index.html", "/admin/", "/admin/index.html", "/admin/config.js", "/manifest.json"} {
			if !containsStr(got, want) {
				t.Errorf("default policy should invalidate %q; got %v", want, got)
			}
		}
		// The crucial negative: a content-hashed asset path must NEVER
		// appear, and neither must a blanket purge. Both would defeat the
		// entire point of the default.
		for _, unwanted := range []string{"/*", "/admin/*", "/_next/static/**"} {
			if containsStr(got, unwanted) {
				t.Errorf("default policy must not invalidate %q; got %v", unwanted, got)
			}
		}
		// Bounded regardless of how large the site is.
		if len(got) > 8 {
			t.Errorf("default policy should stay small (got %d paths): %v", len(got), got)
		}
	})

	t.Run("root-mounted site emits no duplicate root paths", func(t *testing.T) {
		spec := StaticSiteSpec{CDN: &StaticSiteCDNSpec{URLMap: "lb"}}
		got := invalidationPaths(spec)
		seen := map[string]int{}
		for _, p := range got {
			seen[p]++
			if seen[p] > 1 {
				t.Errorf("duplicate invalidation path %q in %v", p, got)
			}
		}
		if !containsStr(got, "/config.js") {
			t.Errorf("root-mounted site should invalidate /config.js, got %v", got)
		}
	})
}

// TestStaticSiteUploadAppliesCacheRulesInOrder pins that the most
// specific cache rule is applied FIRST and the catch-all pass cannot
// overwrite the header it set. Getting this backwards would tag
// immutable hashed assets with must-revalidate, quietly destroying the
// caching that keeps invalidation cheap.
func TestStaticSiteUploadAppliesCacheRulesInOrder(t *testing.T) {
	prov := StaticSiteProvider{}
	spec := fakeStaticSiteFrontend().Spec
	cmds := prov.uploadCmds(spec, "/staging", "deadbeef1234")

	if len(cmds) != 3 {
		t.Fatalf("expected 2 rule uploads + 1 catch-all, got %d: %v", len(cmds), cmds)
	}
	first := strings.Join(cmds[0], " ")
	if !strings.Contains(first, "_next/static/**") || !strings.Contains(first, "immutable") {
		t.Errorf("most specific rule must run first, got %q", first)
	}
	last := strings.Join(cmds[len(cmds)-1], " ")
	if !strings.Contains(last, "--no-clobber") {
		t.Errorf("catch-all pass must be --no-clobber so earlier headers survive, got %q", last)
	}
	for _, c := range cmds {
		if !strings.Contains(strings.Join(c, " "), "gs://reliant-staging-web/releases/deadbeef1234") {
			t.Errorf("upload must target the immutable release prefix, got %v", c)
		}
	}
}

// TestStaticSiteBucketNormalization confirms a bare bucket name and a
// gs:// URL resolve to the same prefixes — the schema accepts both.
func TestStaticSiteBucketNormalization(t *testing.T) {
	bare := StaticSiteSpec{Bucket: "my-site"}
	sch := StaticSiteSpec{Bucket: "gs://my-site"}
	if bare.liveURI() != sch.liveURI() {
		t.Errorf("live URIs differ: %q vs %q", bare.liveURI(), sch.liveURI())
	}
	if got := bare.releaseURI("abc"); got != "gs://my-site/releases/abc" {
		t.Errorf("releaseURI = %q", got)
	}
}

// TestStaticSiteRegisteredInDefaultRegistry guards the registration trap:
// a provider absent from the registry is not an error anywhere, the
// frontend simply never deploys.
func TestStaticSiteRegisteredInDefaultRegistry(t *testing.T) {
	if p := NewRegistry().Lookup("static-site"); p == nil {
		t.Fatal("static-site provider not registered in NewRegistry")
	}
}

// --- helpers ---

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("read tree %s: %v", root, err)
	}
	return out
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
