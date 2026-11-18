package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedSiteSnapshot is a snapshot whose hosted tree holds one static site,
// web (a StaticSite record, as the deploy path seals it), whose artifact
// records builtBy as its release ("" records none), and one hosted service,
// api, which forge has no witness for. selected lists the consumers this
// snapshot built.
func hostedSiteSnapshot(t *testing.T, builtBy string, selected ...string) release.Snapshot {
	t.Helper()
	s := release.Snapshot{Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	add := func(workload, slot string, a release.SnapshotArtifact) {
		id, err := release.ArtifactIdentity(a)
		if err != nil {
			t.Fatal(err)
		}
		s.Artifacts[id] = a
		k := release.ConsumerKey{Cluster: release.BundleHostedCluster, Workload: workload}
		s.Consumers = append(s.Consumers, release.Consumer{Key: k, Slots: []string{slot}, DependenciesDeclared: true})
		s.Bindings = append(s.Bindings, release.ConsumerBinding{Consumer: k, Slot: slot, Artifact: id})
	}
	add("web", "frontend", release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.example/acme/static.v1/web",
		Content: "sha256:" + strings.Repeat("a", 64), Origin: release.ArtifactOrigin{Release: builtBy}})
	add("api", "container:api", release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.example/acme/api",
		Content: "sha256:" + strings.Repeat("b", 64), Origin: release.ArtifactOrigin{Release: builtBy}})
	web := release.ConsumerKey{Cluster: release.BundleHostedCluster, Workload: "web"}
	s.Resources = append(s.Resources, release.SnapshotResource{
		Key:    release.ResourceKey{Cluster: release.BundleHostedCluster, Group: "forge.dev", Kind: "StaticSite", Name: "web"},
		Owners: []release.ConsumerKey{web},
	})
	for _, name := range selected {
		s.Selected = append(s.Selected, release.ConsumerKey{Cluster: release.BundleHostedCluster, Workload: name})
	}
	return s
}

func siteVerdict(t *testing.T, results []imageVerification) imageVerification {
	t.Helper()
	for _, r := range results {
		if r.Image == "web/frontend" {
			return r
		}
	}
	t.Fatalf("no verdict for web/frontend in %+v", results)
	return imageVerification{}
}

// forge#2231: a hosted static site is verified against the release marker its
// live URL serves, instead of being UNREACHABLE forever.
func TestEnvStatusWitnessesAHostedStaticSiteByItsReleaseMarker(t *testing.T) {
	marker := func(rel string) func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error) {
		return func(_ context.Context, site string) (deploytarget.StaticSiteReleaseMarker, error) {
			if site != "https://web.acme.example" {
				t.Errorf("fetched %s, want the site's live URL", site)
			}
			return deploytarget.StaticSiteReleaseMarker{Schema: deploytarget.StaticSiteReleaseMarkerSchema, Frontend: "web", Release: rel}, nil
		}
	}
	liveURL := func(context.Context, string) (map[string]string, error) {
		return map[string]string{"web": "https://web.acme.example"}, nil
	}
	for name, tc := range map[string]struct {
		urls     func(context.Context, string) (map[string]string, error)
		fetch    func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error)
		want     imageState
		detail   string
		exitCode int
	}{
		"the site serves the bound release": {urls: liveURL, fetch: marker("r2"), want: imageMatch, detail: "serves release r2"},
		"the site serves another release":   {urls: liveURL, fetch: marker("r1"), want: imageDrift, detail: "serves release r1; the snapshot binds the site built by release r2", exitCode: 1},
		"the site serves no marker": {urls: liveURL, want: imageUnreachable, detail: "serves no forge release marker", exitCode: 2,
			fetch: func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error) {
				return deploytarget.StaticSiteReleaseMarker{}, errNoReleaseMarker
			}},
		"the control plane reports no URL yet": {want: imageUnreachable, detail: "no live URL", exitCode: 2, fetch: marker("r2"),
			urls: func(context.Context, string) (map[string]string, error) { return map[string]string{}, nil }},
		"the control plane cannot be read": {want: imageUnreachable, detail: "connection refused", exitCode: 2, fetch: marker("r2"),
			urls: func(context.Context, string) (map[string]string, error) { return nil, errors.New("connection refused") }},
	} {
		t.Run(name, func(t *testing.T) {
			s := hostedSiteSnapshot(t, "r2")
			results := snapshotStatusImages(context.Background(), s, "prod", "r2", envStatusOptions{StaticSiteURLs: tc.urls, FetchReleaseMarker: tc.fetch})
			got := siteVerdict(t, results)
			if got.State != tc.want || !strings.Contains(got.Detail, tc.detail) {
				t.Fatalf("web/frontend = %s (%s), want %s containing %q", got.State, got.Detail, tc.want, tc.detail)
			}
			// The hosted service still has no witness: unreachable, as before.
			for _, r := range results {
				if r.Image == "api/container:api" && r.State != imageUnreachable {
					t.Errorf("api/container:api = %s, want unreachable", r.State)
				}
			}
			only := []imageVerification{got}
			err := envStatusReleaseVerdict("prod", "r2", nil, tallyEnvStatusRelease(only))
			if code := exitCodeForError(err); (err == nil && tc.exitCode != 0) || (err != nil && code != tc.exitCode) {
				t.Errorf("exit = %d (%v), want %d", code, err, tc.exitCode)
			}
		})
	}
}

// A carried site keeps the release that built it: the marker is compared with
// the artifact's release, not the env's newest one.
func TestEnvStatusComparesAStaticSiteWithTheReleaseThatBuiltIt(t *testing.T) {
	s := hostedSiteSnapshot(t, "r1")
	got := siteVerdict(t, snapshotStatusImages(context.Background(), s, "prod", "r2", envStatusOptions{
		StaticSiteURLs: func(context.Context, string) (map[string]string, error) {
			return map[string]string{"web": "https://web.acme.example"}, nil
		},
		FetchReleaseMarker: func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error) {
			return deploytarget.StaticSiteReleaseMarker{Schema: deploytarget.StaticSiteReleaseMarkerSchema, Frontend: "web", Release: "r1"}, nil
		},
	}))
	if got.State != imageMatch {
		t.Fatalf("carried site = %s (%s), want match", got.State, got.Detail)
	}
}

func liveWeb(context.Context, string) (map[string]string, error) {
	return map[string]string{"web": "https://web.acme.example"}, nil
}

func serves(rel string) func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error) {
	return func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error) {
		return deploytarget.StaticSiteReleaseMarker{Schema: deploytarget.StaticSiteReleaseMarkerSchema, Frontend: "web", Release: rel}, nil
	}
}

// A site whose artifact records no release (an external or provenance-less
// artifact) is still judged when the snapshot says which release built it:
// one it selected was built, and stamped, as the release it is sealed as.
func TestEnvStatusStaticSiteWithNoOriginReleaseIsJudgedByTheSnapshot(t *testing.T) {
	built := hostedSiteSnapshot(t, "", "web")
	opts := envStatusOptions{StaticSiteURLs: liveWeb, FetchReleaseMarker: serves("r2")}
	if got := siteVerdict(t, snapshotStatusImages(context.Background(), built, "prod", "r2", opts)); got.State != imageMatch {
		t.Errorf("selected site, marker r2, sealed r2 = %s (%s), want match", got.State, got.Detail)
	}
	opts.FetchReleaseMarker = serves("r1")
	if got := siteVerdict(t, snapshotStatusImages(context.Background(), built, "prod", "r2", opts)); got.State != imageDrift ||
		!strings.Contains(got.Detail, "serves release r1") {
		t.Errorf("selected site, marker r1, sealed r2 = %s (%s), want drift", got.State, got.Detail)
	}

	// A build event that records the release names it too.
	evented := hostedSiteSnapshot(t, "")
	evented.BuildEvents = []release.ArtifactBuildEvent{{Consumer: evented.Bindings[0].Consumer, Artifact: evented.Bindings[0].Artifact,
		Origin: release.ArtifactOrigin{Release: "r1"}}}
	opts.FetchReleaseMarker = serves("r1")
	if got := siteVerdict(t, snapshotStatusImages(context.Background(), evented, "prod", "r2", opts)); got.State != imageMatch {
		t.Errorf("build event names r1, marker r1 = %s (%s), want match", got.State, got.Detail)
	}

	// Carried, with no record of its release anywhere: unreachable, saying why,
	// and the site is not even fetched.
	carried := hostedSiteSnapshot(t, "")
	opts.FetchReleaseMarker = func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error) {
		t.Error("a site with no known release must not be fetched")
		return deploytarget.StaticSiteReleaseMarker{}, nil
	}
	if got := siteVerdict(t, snapshotStatusImages(context.Background(), carried, "prod", "r2", opts)); got.State != imageUnreachable ||
		!strings.Contains(got.Detail, "carried from an earlier release") {
		t.Errorf("carried site with no release = %s (%s), want unreachable saying why", got.State, got.Detail)
	}
}

// A hosted frontend with no StaticSite record is not on the static-site path:
// it is never judged by a release marker, whatever its slot is called.
func TestEnvStatusHostedFrontendThatIsNotAStaticSiteIsNotMarkerChecked(t *testing.T) {
	s := hostedSiteSnapshot(t, "r2", "web")
	s.Resources = []release.SnapshotResource{{
		Key:    release.ResourceKey{Cluster: release.BundleHostedCluster, Group: "forge.dev", Kind: "Workload", Name: "web"},
		Owners: []release.ConsumerKey{s.Bindings[0].Consumer},
	}}
	opts := envStatusOptions{
		StaticSiteURLs: func(context.Context, string) (map[string]string, error) {
			t.Error("a non-static frontend must not read static site URLs")
			return nil, nil
		},
		FetchReleaseMarker: func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error) {
			t.Error("a non-static frontend must not be fetched")
			return deploytarget.StaticSiteReleaseMarker{}, nil
		},
	}
	got := siteVerdict(t, snapshotStatusImages(context.Background(), s, "prod", "r2", opts))
	if got.State != imageUnreachable || !strings.Contains(got.Detail, "explicit per-consumer witness") {
		t.Errorf("non-static hosted frontend = %s (%s), want the generic hosted verdict", got.State, got.Detail)
	}
}

// The HTTP half: the marker path, a 404 as "no marker", and an HTML page in
// its place as unreadable, never as a release.
func TestFetchStaticReleaseMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok/.well-known/forge-release.json":
			if r.Header.Get("Cache-Control") != "no-cache" {
				t.Errorf("request Cache-Control = %q, want no-cache", r.Header.Get("Cache-Control"))
			}
			_, _ = w.Write([]byte(`{"schema":"forge.static-site-release/v1","frontend":"web","release":"r2"}`))
		case "/html/.well-known/forge-release.json":
			_, _ = w.Write([]byte("<!doctype html><title>spa</title>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if m, err := fetchStaticReleaseMarker(ctx, srv.URL+"/ok/"); err != nil || m.Release != "r2" || m.Frontend != "web" {
		t.Errorf("ok: %+v, %v", m, err)
	}
	if _, err := fetchStaticReleaseMarker(ctx, srv.URL+"/missing"); !errors.Is(err, errNoReleaseMarker) {
		t.Errorf("missing: err = %v, want errNoReleaseMarker", err)
	}
	if _, err := fetchStaticReleaseMarker(ctx, srv.URL+"/html"); err == nil || errors.Is(err, errNoReleaseMarker) {
		t.Errorf("html: err = %v, want a parse error", err)
	}
}
