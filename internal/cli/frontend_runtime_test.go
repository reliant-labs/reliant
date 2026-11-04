package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// A frontend binds a runtime exactly as a workload does (ADR 0002 §6). These
// tests render real KCL declarations, one per runtime, through the production
// decoder, and drive the production consumers from them: the deploy dispatch,
// the hosted publish, the build filter and the destination vote. A break at
// any link reads downstream as "this frontend does not ship" and would skip
// it SILENTLY, which is why each runtime is pinned end to end.

// renderFrontendEnv renders one env whose bundle declares `frontends` (a KCL
// list body) and returns the decoded entities. bundleExtra is spliced into
// the Bundle (control_plane, workloads, ...).
func renderFrontendEnv(t *testing.T, frontends, bundleExtra string) *KCLEntities {
	t.Helper()
	kclplugin.Register()
	root := t.TempDir()
	envDir := filepath.Join(root, "deploy", "kcl", "staging")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := "[package]\nname = \"t\"\nedition = \"v0.11.4\"\n\n[dependencies]\n"
	if err := os.WriteFile(filepath.Join(root, "deploy", "kcl", "kcl.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	main := "import forge\nimport forge.workloads as fw\n\noutput = forge.render(forge.Bundle {\n    project = \"acme\"\n" +
		bundleExtra + "\n    frontends = " + frontends + "\n})\n"
	if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := renderKCLRaw(context.Background(), root, "staging")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, main)
	}
	e, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	return e
}

// Every runtime decodes into its variant, with the static build facts on the
// frontend — and nothing else. A bucket frontend's placement is the runtime's;
// its public_dir / base_path / bundle / cache rules are the frontend's.
func TestFrontendRuntime_DecodeEveryRuntime(t *testing.T) {
	e := renderFrontendEnv(t, `[
        forge.Frontend {name = "dev", path = "dev", port = 3100, runtime = forge.OnHost {}}
        forge.Frontend {name = "site", path = "site", type = "vite", public_dir = "dist", image = "ghcr.io/acme/site", runtime = forge.OnHosted {}}
        forge.Frontend {
            name = "admin-web"
            path = "admin-web"
            env_vars = [forge.EnvVar {name = "NEXT_PUBLIC_API_URL", value = "https://api.staging.example.com"}]
            public_dir = "out"
            base_path = "/admin"
            bundle = [forge.BundleDir {src = "../reliant-web/dist"}]
            cache_control = [
                forge.CacheRule {pattern = "_next/static/**", cache_control = "public, max-age=31536000, immutable"}
                forge.CacheRule {pattern = "**", cache_control = "public, max-age=0, must-revalidate"}
            ]
            runtime = forge.OnBucket {
                bucket = "gs://reliant-staging-web"
                cdn = forge.StaticSiteCDN {url_map = "reliant-staging-lb", extra_invalidate_paths = ["/manifest.json"]}
                keep_releases = 0
            }
        }
        forge.Frontend {
            name = "fb"
            path = "fb"
            base_path = "/app"
            type = "vite"
            cache_control = [forge.CacheRule {pattern = "assets/**", cache_control = "public, max-age=600"}]
            runtime = forge.OnFirebase {project = "p", site = "s", spa_fallback = "/app/index.html", keep_asset_releases = 4}
        }
        forge.Frontend {name = "spa", path = "spa", type = "vite", runtime = forge.BuildOnly {}}
    ]`, `    control_plane = forge.ControlPlane {}`)

	by := map[string]FrontendEntity{}
	for _, f := range e.Frontends {
		by[f.Name] = f
	}
	for name, want := range map[string]string{
		"dev": FrontendRuntimeHost, "site": FrontendRuntimeHosted, "admin-web": FrontendRuntimeBucket,
		"fb": FrontendRuntimeFirebase, "spa": FrontendRuntimeBuildOnly,
	} {
		if got := by[name].Runtime.Type; got != want {
			t.Errorf("%s: runtime.type = %q, want %q", name, got, want)
		}
	}

	b := by["admin-web"]
	if b.Runtime.Bucket == nil || b.Runtime.Bucket.Bucket != "gs://reliant-staging-web" {
		t.Fatalf("bucket runtime = %+v", b.Runtime.Bucket)
	}
	// 0 is "retain everything": it must survive, not read back as 10.
	if b.Runtime.Bucket.KeepReleases != 0 {
		t.Errorf("keep_releases = %d, want 0", b.Runtime.Bucket.KeepReleases)
	}
	if b.Runtime.Bucket.CDN == nil || b.Runtime.Bucket.CDN.Invalidate != "entrypoints" {
		t.Errorf("cdn = %+v, want the entrypoints default", b.Runtime.Bucket.CDN)
	}
	if b.PublicDir != "out" || b.BasePath != "/admin" || len(b.Bundle) != 1 {
		t.Errorf("static facts = public_dir %q base_path %q bundle %+v", b.PublicDir, b.BasePath, b.Bundle)
	}
	if len(b.CacheControl) != 2 || b.CacheControl[0].Pattern != "_next/static/**" {
		t.Errorf("cache rules lost their declaration order: %+v", b.CacheControl)
	}
	// Defaulted public_dir is resolved by the render, by type.
	if by["spa"].PublicDir != "dist" || by["fb"].PublicDir != "dist" || by["admin-web"].PublicDir != "out" {
		t.Errorf("defaulted public_dir: spa %q (want dist), fb %q (want dist)", by["spa"].PublicDir, by["fb"].PublicDir)
	}
	// Defaulted asset_dir likewise: the build's content-hashed directory.
	if by["fb"].AssetDir != "assets" || by["admin-web"].AssetDir != "_next/static" {
		t.Errorf("defaulted asset_dir: fb %q (want assets), admin-web %q (want _next/static)", by["fb"].AssetDir, by["admin-web"].AssetDir)
	}
	if fb := by["fb"].Runtime.Firebase; fb == nil || fb.Project != "p" || fb.Site != "s" ||
		fb.SPAFallback != "/app/index.html" || fb.KeepAssetReleases != 4 || len(fb.Rewrites) != 0 {
		t.Errorf("firebase runtime = %+v", fb)
	}
	if rules := by["fb"].CacheControl; len(rules) != 1 || rules[0].Pattern != "assets/**" {
		t.Errorf("firebase cache rules = %+v, want the declared rule (Firebase renders them as headers)", rules)
	}
}

// The provider mappings carry the frontend's own build facts plus the
// runtime's placement, intact.
func TestFrontendRuntime_ProviderMappings(t *testing.T) {
	bucket := FrontendEntity{
		Name: "admin-web", Path: "admin-web", Type: "nextjs", PublicDir: "out", AssetDir: "_next/static", BasePath: "/admin",
		Bundle:       []BundleDir{{Src: "../reliant-web/dist"}},
		CacheControl: []CacheRule{{Pattern: "**", CacheControl: "no-cache"}},
		EnvVars:      []KCLEnvVar{{Name: "NEXT_PUBLIC_API_URL", Value: "https://api"}},
		Runtime: FrontendRuntime{Type: FrontendRuntimeBucket, Bucket: &BucketRuntime{
			Bucket: "gs://b", KeepReleases: 5, CDN: &StaticSiteCDN{URLMap: "lb"},
		}},
	}
	ss := frontendToStaticSite(bucket)
	want := deploytarget.StaticSiteSpec{
		Bucket: "gs://b", PublicDir: "out", AssetDir: "_next/static", BasePath: "/admin", KeepReleases: 5,
		Bundle:       []deploytarget.BundleDirSpec{{Src: "../reliant-web/dist"}},
		CacheControl: []deploytarget.CacheRuleSpec{{Pattern: "**", CacheControl: "no-cache"}},
		CDN:          &deploytarget.StaticSiteCDNSpec{URLMap: "lb"},
	}
	if !reflect.DeepEqual(ss.Spec, want) {
		t.Errorf("frontendToStaticSite spec =\n%+v\nwant\n%+v", ss.Spec, want)
	}
	if ss.BuildEnv["NEXT_PUBLIC_API_URL"] != "https://api" {
		t.Errorf("build env = %+v", ss.BuildEnv)
	}

	fb := FrontendEntity{
		Name: "fb", Path: "fb", PublicDir: "dist", AssetDir: "assets", BasePath: "/app", Bundle: []BundleDir{{Src: "x"}},
		CacheControl: []CacheRule{{Pattern: "assets/**", CacheControl: "public, max-age=600"}},
		Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase, Firebase: &FirebaseRuntime{
			Project: "p", Site: "s", Target: "t", SPAFallback: "/app/index.html", KeepAssetReleases: 4,
		}},
	}
	got := frontendToFirebase(fb).Spec
	if got.Project != "p" || got.Site != "s" || got.Target != "t" || got.PublicDir != "dist" || got.BasePath != "/app" || len(got.Bundle) != 1 {
		t.Errorf("frontendToFirebase spec = %+v", got)
	}
	if got.SPAFallback != "/app/index.html" || got.AssetDir != "assets" || got.KeepAssetReleases != 4 ||
		!reflect.DeepEqual(got.CacheControl, []deploytarget.CacheRuleSpec{{Pattern: "assets/**", CacheControl: "public, max-age=600"}}) {
		t.Errorf("frontendToFirebase dropped serving policy: %+v", got)
	}

	if bo := frontendToBuildOnly(FrontendEntity{Name: "spa", Path: "spa", PublicDir: "dist"}); bo.PublicDir != "dist" {
		t.Errorf("frontendToBuildOnly PublicDir = %q, want the frontend's", bo.PublicDir)
	}
}

// The dispatch table, as every consumer reads it. Keyed on runtime.type, and
// nothing else: no field of the runtime changes which path a frontend takes.
func TestFrontendRuntime_DispatchTable(t *testing.T) {
	type row struct {
		ships, hosted, appliesLocally, prodBuild bool
		destination                              string
	}
	for typ, want := range map[string]row{
		FrontendRuntimeHost:      {destination: ""},
		FrontendRuntimeHosted:    {hosted: true, destination: destinationHosted},
		FrontendRuntimeBucket:    {ships: true, appliesLocally: true, prodBuild: true, destination: destinationStatic},
		FrontendRuntimeFirebase:  {ships: true, appliesLocally: true, prodBuild: true, destination: destinationStatic},
		FrontendRuntimeBuildOnly: {appliesLocally: true, prodBuild: true, destination: ""},
	} {
		t.Run(typ, func(t *testing.T) {
			f := FrontendEntity{Name: "web", Runtime: FrontendRuntime{Type: typ}}
			e := &KCLEntities{Frontends: []FrontendEntity{f}}
			if got := hasShippableFrontend(e); got != want.ships {
				t.Errorf("hasShippableFrontend = %v, want %v", got, want.ships)
			}
			if got := frontendIsHosted(f); got != want.hosted {
				t.Errorf("frontendIsHosted = %v, want %v", got, want.hosted)
			}
			if got := e.HasHosted(); got != want.hosted {
				t.Errorf("HasHosted = %v, want %v", got, want.hosted)
			}
			if got := envAppliesLocally(e); got != want.appliesLocally {
				t.Errorf("envAppliesLocally = %v, want %v", got, want.appliesLocally)
			}
			kinds := destinationKindSet(e)
			if want.destination == "" && len(kinds) != 0 {
				t.Errorf("destinations = %v, want none", kinds)
			} else if want.destination != "" && !kinds[want.destination] {
				t.Errorf("destinations = %v, want %s", kinds, want.destination)
			}
			kept := filterFrontendsForBuild([]config.FrontendConfig{{Name: "web"}}, e)
			if got := len(kept) == 1; got != want.prodBuild {
				t.Errorf("filterFrontendsForBuild kept = %v, want %v", got, want.prodBuild)
			}
		})
	}
}

// A hosted frontend publishes the SAME StaticSite CR spec it did before the
// runtime split, for the equivalent declaration. The pre-split declaration
// was `deploy = forge.StaticSite {public_dir = "dist", base_path = "/app"}`
// with no bucket, and its spec was {basePath, keepReleases: 10,
// runtimeConfig} — the literal below is that published JSON, byte for byte.
func TestFrontendRuntime_HostedPublishesTheSameStaticSiteSpec(t *testing.T) {
	e := renderFrontendEnv(t, `[forge.Frontend {
        name = "web"
        image = "ghcr.io/acme/web"
        path = "frontends/web"
        type = "vite"
        public_dir = "dist"
        base_path = "/app"
        runtime_config = {API_URL = forge.WorkloadURL {workload = "api"}, APP_NAME = "acme"}
        runtime = forge.OnHosted {}
    }]`, `    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api:v1", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}]`)

	group, err := buildHostedGroup("staging", e)
	if err != nil || group == nil {
		t.Fatalf("buildHostedGroup: %v", err)
	}
	var spec *v1alpha1.StaticSiteSpec
	for _, s := range group.Services {
		if s.Name == "web" && s.Hosted != nil && s.Hosted.Tier == deploytarget.HostedTierStatic {
			spec = s.Hosted.Static
		}
	}
	if spec == nil {
		t.Fatalf("the hosted frontend was not published as a StaticSite: %+v", group.Services)
	}
	got, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	// What a hosted frontend gains over what it declares: the platform's
	// browser-telemetry ingest, on its own origin, and its content-hashed
	// asset directory (the vite convention, "assets"), which the platform
	// caches immutably and keeps serveable across deploys.
	const before = `{"basePath":"/app","assetDir":"assets","keepReleases":10,"runtimeConfig":{"API_URL":{"workloadURL":{"name":"api"}},"APP_NAME":{"value":"acme"},"OTEL_ENDPOINT":{"value":"/_otel"}}}`
	if string(got) != before {
		t.Errorf("hosted StaticSite spec changed:\n got %s\nwant %s", got, before)
	}
	if _, err := deploytarget.PreflightHosted(*group); err != nil {
		t.Errorf("the hosted frontend is not admissible: %v", err)
	}
}

// Hosted is a runtime, not the absence of a bucket: a bucket frontend in an
// env that ALSO has a control plane and hosted workloads stays the author's.
func TestFrontendRuntime_BucketBesideHostedWorkloadsIsNotHosted(t *testing.T) {
	e := renderFrontendEnv(t, `[forge.Frontend {name = "web", path = "web", type = "vite", runtime = forge.OnBucket {bucket = "acme-web"}}]`,
		`    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api:v1", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}]`)
	group, err := buildHostedGroup("staging", e)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range group.Services {
		if s.Name == "web" {
			t.Fatalf("the bucket frontend was published to the control plane: %+v", s)
		}
	}
	if !hasShippableFrontend(e) || destinationOf(e) != destinationMixed {
		t.Errorf("bucket frontend beside hosted workloads: shippable=%v destination=%q, want true/mixed", hasShippableFrontend(e), destinationOf(e))
	}
}

// A render from a forge that knows a runtime this binary does not is refused
// by name, not decoded as "ships nowhere".
func TestFrontendRuntime_UnknownTypeIsRefused(t *testing.T) {
	_, err := parseKCLEntities([]byte(`{"frontends":[{"name":"web","path":"web","runtime":{"type":"vercel"}}]}`))
	if err == nil || !strings.Contains(err.Error(), "vercel") {
		t.Fatalf("err = %v, want a refusal naming the unknown runtime", err)
	}
}
