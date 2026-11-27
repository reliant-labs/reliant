package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/topology"
)

// stubFrontendURLStatus states the control plane's answer to the status read
// a build resolves forge.FrontendURL from.
func stubFrontendURLStatus(t *testing.T, st deploytarget.HostedEnvStatus, err error) *int {
	t.Helper()
	calls := 0
	prev := frontendURLStatusReader
	frontendURLStatusReader = func(context.Context, string, *KCLEntities) (deploytarget.HostedEnvStatus, error) {
		calls++
		return st, err
	}
	t.Cleanup(func() { frontendURLStatusReader = prev })
	return &calls
}

func siteURLFrontend() FrontendEntity {
	return FrontendEntity{Name: "web", EnvVars: []KCLEnvVar{
		{Name: "SITE_URL", FrontendURL: &KCLFrontendURL{Frontend: "web"}},
		{Name: "VITE_FLAG", Value: "on"},
	}}
}

// A live custom domain is the site's origin; until one is live, the
// platform's hostname is. A declared base_path is the mount on either.
func TestHostedFrontendURL(t *testing.T) {
	static := string(deploytarget.HostedTierStatic)
	for _, tc := range []struct {
		name string
		ws   []deploytarget.HostedWorkloadStatus
		base string
		want string
	}{
		{"platform hostname", []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: static, URL: "https://web-acme.sites.example.dev/"}}, "", "https://web-acme.sites.example.dev"},
		{"live domain wins", []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: static, URL: "https://web-acme.sites.example.dev",
			Domains: []topology.CustomDomain{{Domain: "pending.example.com", State: "pending_dns"}, {Domain: "www.example.com", State: "live"}}}}, "", "https://www.example.com"},
		{"pending domain is not the origin", []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: static, URL: "https://web-acme.sites.example.dev",
			Domains: []topology.CustomDomain{{Domain: "www.example.com", State: "issuing"}}}}, "", "https://web-acme.sites.example.dev"},
		{"base path appended", []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: static, URL: "https://web-acme.sites.example.dev"}}, "/docs/", "https://web-acme.sites.example.dev/docs"},
		{"base path not doubled", []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: static, URL: "https://web-acme.sites.example.dev/docs"}}, "/docs", "https://web-acme.sites.example.dev/docs"},
		{"no hostname yet", []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: static}}, "", ""},
		{"a workload of the same name is not the site", []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: string(deploytarget.HostedTierWorkload), URL: "https://api.example.dev"}}, "", ""},
		{"absent", nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, warnings := hostedFrontendURL(tc.ws, "web", tc.base); got != tc.want || len(warnings) != 0 {
				t.Errorf("hostedFrontendURL = %q %v, want %q and no warning", got, warnings, tc.want)
			}
		})
	}
}

// A platform URL is joined with base_path segment by segment, through
// url.URL, and its query and fragment are dropped with a warning: neither
// belongs in a canonical origin.
func TestHostedFrontendURLPlatformURLShapes(t *testing.T) {
	static := string(deploytarget.HostedTierStatic)
	for _, tc := range []struct {
		name, url, base, want string
		warn                  bool
	}{
		{"path, base appended", "https://acme.sites.example.dev/sites/web/", "/docs", "https://acme.sites.example.dev/sites/web/docs", false},
		{"path already ends with the mount", "https://acme.sites.example.dev/sites/docs", "docs/", "https://acme.sites.example.dev/sites/docs", false},
		{"a suffix that is not a segment is appended", "https://acme.sites.example.dev/mydocs", "/docs", "https://acme.sites.example.dev/mydocs/docs", false},
		{"multi-segment base", "https://acme.sites.example.dev/a/b", "/a/b", "https://acme.sites.example.dev/a/b", false},
		{"escaped segment kept escaped", "https://acme.sites.example.dev/caf%C3%A9", "", "https://acme.sites.example.dev/caf%C3%A9", false},
		{"query dropped", "https://acme.sites.example.dev/?ref=x", "", "https://acme.sites.example.dev", true},
		{"fragment dropped", "https://acme.sites.example.dev/web#top", "/docs", "https://acme.sites.example.dev/web/docs", true},
		{"empty query dropped", "https://acme.sites.example.dev/?", "", "https://acme.sites.example.dev", true},
		{"userinfo refused", "https://user:pw@acme.sites.example.dev/", "", "", true},
		{"port kept", "https://acme.sites.example.dev:8443/web", "", "https://acme.sites.example.dev:8443/web", false},
		{"dev platform localhost", "http://localhost:3000/", "", "http://localhost:3000", false},
		{"IPv4 literal", "http://127.0.0.1:8080", "", "http://127.0.0.1:8080", false},
		{"IPv6 literal", "https://[2001:db8::1]:443/", "", "https://[2001:db8::1]:443", false},
		{"non-numeric port", "https://example.com:bad/", "", "", true},
		{"empty port", "https://example.com:/", "", "", true},
		{"port zero", "https://example.com:0/", "", "", true},
		{"port above 65535", "https://example.com:65536/", "", "", true},
		{"signed port", "https://example.com:+443/", "", "", true},
		{"hostname in brackets", "https://[example.com]/", "", "", true},
		{"IPv4 in brackets", "https://[127.0.0.1]/", "", "", true},
		{"unclosed bracket", "https://[2001:db8::1/", "", "", true},
		{"junk after bracket", "https://[2001:db8::1]x/", "", "", true},
		{"IPv6 zone", "https://[fe80::1%25eth0]/", "", "", true},
		{"bad label", "https://-bad.example.dev/", "", "", true},
		{"underscore", "https://bad_host.example.dev/", "", "", true},
		{"not http(s)", "ftp://acme.sites.example.dev/", "", "", true},
		{"relative", "/web", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings := hostedFrontendURL([]deploytarget.HostedWorkloadStatus{{Name: "web", Tier: static, URL: tc.url}}, "web", tc.base)
			if got != tc.want || (len(warnings) > 0) != tc.warn {
				t.Errorf("hostedFrontendURL(%q, base %q) = %q, warnings %v; want %q, warning %v", tc.url, tc.base, got, warnings, tc.want, tc.warn)
			}
		})
	}
}

// A custom domain must be a bare hostname. A malformed one is skipped with a
// warning, for the next live domain or the platform URL: a value like
// "evil.example/x" or "https://www.example.com" must never become the
// canonical origin as written.
func TestHostedFrontendURLSkipsMalformedDomains(t *testing.T) {
	const platform = "https://web-acme.sites.example.dev"
	for _, bad := range []string{
		"https://www.example.com", "www.example.com/path", "www.example.com?x=1", "www.example.com#top",
		"user@www.example.com", "www.example.com:8443", "localhost", "-bad.example.com", "bad-.example.com",
		"www..example.com", "www.exa mple.com", "www.example.com.", strings.Repeat("a", 64) + ".example.com",
	} {
		t.Run(bad, func(t *testing.T) {
			ws := []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: string(deploytarget.HostedTierStatic), URL: platform,
				Domains: []topology.CustomDomain{{Domain: bad, State: "live"}}}}
			got, warnings := hostedFrontendURL(ws, "web", "")
			if got != platform || len(warnings) != 1 || !strings.Contains(warnings[0], "not a bare hostname") {
				t.Errorf("domain %q: got %q, warnings %v; want the platform URL and one warning", bad, got, warnings)
			}
		})
	}
	// The next live domain that is well-formed wins over the platform URL.
	ws := []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: string(deploytarget.HostedTierStatic), URL: platform,
		Domains: []topology.CustomDomain{{Domain: "https://bad.example.com", State: "live"}, {Domain: "WWW.Example.com", State: "live"}}}}
	if got, warnings := hostedFrontendURL(ws, "web", "/docs"); got != "https://www.example.com/docs" || len(warnings) != 1 {
		t.Errorf("got %q, warnings %v; want the second domain, lower-cased, with the base path", got, warnings)
	}
}

// resolveBuildFrontendURLs prints the resolver's warnings into the build.
func TestResolveBuildFrontendURLsPrintsWarnings(t *testing.T) {
	stubFrontendURLStatus(t, deploytarget.HostedEnvStatus{Workloads: []deploytarget.HostedWorkloadStatus{{
		Name: "web", Tier: string(deploytarget.HostedTierStatic), URL: "https://web-acme.sites.example.dev",
		Domains: []topology.CustomDomain{{Domain: "www.example.com/x", State: "live"}},
	}}}, nil)
	var out bytes.Buffer
	got := resolveBuildFrontendURLs(context.Background(), &out, "prod", nil, siteURLFrontend())
	if got.EnvVars[0].Value != "https://web-acme.sites.example.dev" || !strings.Contains(out.String(), `custom domain "www.example.com/x" is not a bare hostname`) {
		t.Errorf("value %q, output:\n%s", got.EnvVars[0].Value, out.String())
	}
}

// The build resolves a kept FrontendURL from the env's live status, once per
// frontend, and leaves every other var as the render wrote it.
func TestResolveBuildFrontendURLs(t *testing.T) {
	calls := stubFrontendURLStatus(t, deploytarget.HostedEnvStatus{Workloads: []deploytarget.HostedWorkloadStatus{
		{Name: "web", Tier: string(deploytarget.HostedTierStatic), URL: "https://web-acme.sites.example.dev"},
	}}, nil)
	f := siteURLFrontend()
	var out bytes.Buffer
	got := resolveBuildFrontendURLs(context.Background(), &out, "prod", &KCLEntities{Frontends: []FrontendEntity{f}}, f)
	if got.EnvVars[0].Value != "https://web-acme.sites.example.dev" || got.EnvVars[1].Value != "on" {
		t.Errorf("resolved env vars = %+v", got.EnvVars)
	}
	if f.EnvVars[0].Value != "" {
		t.Error("resolution wrote through to the rendered entity's env vars")
	}
	if *calls != 1 {
		t.Errorf("status read %d times, want 1", *calls)
	}
	if env := frontendBuildEnv(got); env["SITE_URL"] != "https://web-acme.sites.example.dev" {
		t.Errorf("build env SITE_URL = %q", env["SITE_URL"])
	}
	if !strings.Contains(out.String(), "SITE_URL=https://web-acme.sites.example.dev") {
		t.Errorf("the build does not say what it resolved:\n%s", out.String())
	}
}

// A build never fails for want of a hostname: the variable is present but
// empty (so a shell value cannot leak in), and the build says why and what
// carries it next.
func TestResolveBuildFrontendURLsWithoutAHostname(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   deploytarget.HostedEnvStatus
		err  error
		want string
	}{
		{"never deployed", deploytarget.HostedEnvStatus{}, deploytarget.ErrHostedEnvironmentNotFound, "env prod has never been deployed"},
		{"control plane unreadable", deploytarget.HostedEnvStatus{}, errors.New("not signed in"), "not signed in"},
		{"no hostname yet", deploytarget.HostedEnvStatus{Workloads: []deploytarget.HostedWorkloadStatus{{Name: "web", Tier: string(deploytarget.HostedTierStatic)}}}, nil, "frontend web has no hostname in env prod yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubFrontendURLStatus(t, tc.st, tc.err)
			f := siteURLFrontend()
			var out bytes.Buffer
			got := resolveBuildFrontendURLs(context.Background(), &out, "prod", nil, f)
			env := frontendBuildEnv(got)
			if v, ok := env["SITE_URL"]; !ok || v != "" {
				t.Errorf("build env SITE_URL = %q (present %v), want present and empty", v, ok)
			}
			if !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), "build the next release") {
				t.Errorf("message does not name %q and the remedy:\n%s", tc.want, out.String())
			}
		})
	}
}

// A frontend with no kept FrontendURL never reads the control plane.
func TestResolveBuildFrontendURLsReadsNothingWithoutAReference(t *testing.T) {
	calls := stubFrontendURLStatus(t, deploytarget.HostedEnvStatus{}, nil)
	f := FrontendEntity{Name: "web", EnvVars: []KCLEnvVar{{Name: "SITE_URL", Value: "https://example.com"}}}
	got := resolveBuildFrontendURLs(context.Background(), &bytes.Buffer{}, "prod", nil, f)
	if *calls != 0 || got.EnvVars[0].Value != "https://example.com" {
		t.Errorf("calls = %d, vars = %+v", *calls, got.EnvVars)
	}
}

const hostedFrontendURLBundle = `    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane { endpoint = "https://cp.example"}
    frontends = [forge.Frontend {
        name = "web"
        image = "web"
        path = "."
        type = "vite"
        public_dir = "dist"
        env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "web"}}]
        runtime = forge.OnHosted {}
    }]`

// TestFrontendURLRendersToTheHostedBuildEnv: through the real KCL module, a
// forge.FrontendURL to a hosted site is kept by the render and resolved by the
// release build into the site's build-time env, from the env's live status.
func TestFrontendURLRendersToTheHostedBuildEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := workloadURLProject(t, "prod", hostedFrontendURLBundle)
	t.Chdir(dir)
	entities, err := RenderKCL(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(entities.Frontends) != 1 || len(entities.Frontends[0].EnvVars) != 1 {
		t.Fatalf("frontends = %+v", entities.Frontends)
	}
	if ev := entities.Frontends[0].EnvVars[0]; ev.FrontendURL == nil || ev.FrontendURL.Frontend != "web" || ev.Value != "" {
		t.Fatalf("rendered SITE_URL = %+v, want the reference kept for the build", ev)
	}
	stubFrontendURLStatus(t, deploytarget.HostedEnvStatus{Workloads: []deploytarget.HostedWorkloadStatus{{
		Name: "web", Tier: string(deploytarget.HostedTierStatic), URL: "https://web-acme.sites.example.dev",
		Domains: []topology.CustomDomain{{Domain: "www.example.com", State: "live"}},
	}}}, nil)
	var got []deploytarget.StaticSiteFrontend
	prev := hostedStaticPusher
	hostedStaticPusher = func(_ context.Context, _, _ string, fe deploytarget.StaticSiteFrontend) (string, error) {
		got = append(got, fe)
		return hostedStaticDigest, nil
	}
	t.Cleanup(func() { hostedStaticPusher = prev })
	t.Setenv("FORGE_HOME", t.TempDir())
	if err := buildHostedStaticSites(context.Background(), dir, entities, buildOptions{env: "prod", release: "v0.1.0", pushPlan: pushPlan{push: true, env: "prod"}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BuildEnv["SITE_URL"] != "https://www.example.com" {
		t.Fatalf("pushed sites = %+v, want one built with SITE_URL=https://www.example.com", got)
	}
}

// frontendURLPathsBundle renders, through the real KCL module, one frontend
// per runtime forge builds on `forge env deploy` (your bucket, Firebase,
// BuildOnly), each taking SITE_URL from the hosted site's FrontendURL.
const frontendURLPathsBundle = `    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane { endpoint = "https://cp.example"}
    frontends = [
        forge.Frontend {name = "site", image = "site", path = "site", type = "vite", runtime = forge.OnHosted {}}
        forge.Frontend {
            name = "docs"
            path = "docs"
            type = "vite"
            runtime = forge.OnBucket {bucket = "gs://acme-docs"}
            env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "site"}}]
        }
        forge.Frontend {
            name = "fb"
            path = "fb"
            type = "vite"
            runtime = forge.OnFirebase {project = "acme", site = "acme"}
            env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "site"}}]
        }
        forge.Frontend {
            name = "parts"
            path = "parts"
            type = "vite"
            runtime = forge.BuildOnly {}
            env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "site"}}]
        }
    ]`

// TestFrontendURLReachesEveryDeployBuild: the rendered reference survives
// the env-var renderer into each frontend entity, and `forge env deploy`'s
// own builds (frontendDeployTargets) resolve it through frontendForBuild, the
// preparation the hosted release build shares, into each build env.
func TestFrontendURLReachesEveryDeployBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := workloadURLProject(t, "prod", frontendURLPathsBundle)
	t.Chdir(dir)
	entities, err := RenderKCL(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, f := range entities.Frontends {
		if f.Name == "site" {
			continue
		}
		if len(f.EnvVars) != 1 || f.EnvVars[0].FrontendURL == nil || f.EnvVars[0].FrontendURL.Frontend != "site" || f.EnvVars[0].Value != "" {
			t.Fatalf("frontend %s rendered SITE_URL as %+v, want the reference to site kept", f.Name, f.EnvVars)
		}
	}
	stubFrontendURLStatus(t, deploytarget.HostedEnvStatus{Workloads: []deploytarget.HostedWorkloadStatus{{
		Name: "site", Tier: string(deploytarget.HostedTierStatic), URL: "https://site-acme.sites.example.dev",
		Domains: []topology.CustomDomain{{Domain: "www.example.com", State: "live"}},
	}}}, nil)
	var out bytes.Buffer
	targets, err := frontendDeployTargets(context.Background(), &out, dir, "prod", entities, nil, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	fes, sites, buildOnly := targets.firebase, targets.sites, targets.buildOnly
	if len(fes) != 1 || len(sites) != 1 || len(buildOnly) != 1 {
		t.Fatalf("targets: firebase %d, bucket %d, build-only %d; want one each", len(fes), len(sites), len(buildOnly))
	}
	const want = "https://www.example.com"
	for name, env := range map[string]map[string]string{"firebase": fes[0].BuildEnv, "bucket": sites[0].BuildEnv, "build-only": buildOnly[0].BuildEnv} {
		if env["SITE_URL"] != want {
			t.Errorf("%s build env SITE_URL = %q, want %q", name, env["SITE_URL"], want)
		}
	}
	if sites[0].Project != "acme" || sites[0].BuildEnv[envForgeRelease] != "v1.0.0" {
		t.Errorf("bucket target lost its project or release: %+v", sites[0])
	}
}

// A snapshot freezes its frontends' build env, so it refuses a FrontendURL by
// name, saying what to write instead.
func TestSnapshotRefusesFrontendURL(t *testing.T) {
	f := FrontendEntity{Name: "docs", EnvVars: []KCLEnvVar{{Name: "SITE_URL", FrontendURL: &KCLFrontendURL{Frontend: "site"}}}}
	err := validateSnapshotFrontend(release.Snapshot{}, snapshotFrontend{Entity: snapshotFrontendEntity(f)})
	if err == nil || !strings.Contains(err.Error(), "forge.FrontendURL to site, which a snapshot cannot freeze") || !strings.Contains(err.Error(), "literal value") {
		t.Errorf("err = %v", err)
	}
	if err := refuseSnapshotFrontendURL(FrontendEntity{Name: "docs", EnvVars: []KCLEnvVar{{Name: "SITE_URL", Value: "https://example.com"}}}); err != nil {
		t.Errorf("a literal SITE_URL was refused: %v", err)
	}
}

// A workload's env (a container build or process) cannot hold a FrontendURL
// at all: its value type admits none, so the render refuses it rather than
// shipping the variable unresolved.
func TestFrontendURLRefusedInWorkloadEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := workloadURLProject(t, "prod", `    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane { endpoint = "https://cp.example"}
    workloads = [fw.Workload {
        name = "web"
        image = "ghcr.io/acme/web:v1"
        ports = [fw.Port {name = "http", port = 3000, expose = True}]
        env = {SITE_URL = forge.FrontendURL {frontend = "site"}}
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {name = "site", image = "site", path = "site", type = "vite", runtime = forge.OnHosted {}}]`)
	_, err := RenderKCL(context.Background(), dir, "prod")
	if err == nil || !strings.Contains(err.Error(), "got FrontendURL") {
		t.Fatalf("render err = %v, want the workload env's type refusing FrontendURL", err)
	}
}
