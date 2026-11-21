package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// The witness for a hosted static site in `forge env status` (forge#2231).
//
// forge cannot read the control plane's static origin, so for a hosted
// frontend the snapshot path used to report UNREACHABLE (exit 2) forever. The
// site itself is the witness: `env build` stamps the release it builds into
// the site (deploytarget.StaticSiteReleaseMarkerPath), and status fetches that
// marker from the env's live URL, through the CDN users reach, and compares
// it with the release that built the artifact the snapshot binds:
//
//	marker release == binding's release   MATCH
//	marker release != binding's release   DRIFT (exit 1)
//	no URL, no answer, no marker          UNREACHABLE (exit 2), saying which
//
// A site built before forge stamped markers serves none, so it stays
// UNREACHABLE until its next release, with a detail that says so.
//
// Which bindings are static sites is read from the sealed snapshot: a
// frontend slot whose consumer has a StaticSite record among the hosted
// resources (sealedStaticSite). Which release built one is the artifact's,
// else the snapshot's (staticSiteRelease).

// errNoReleaseMarker is a live site that answers 404 for the marker.
var errNoReleaseMarker = errors.New("serves no forge release marker")

// readDeclaredStaticSiteURLs is the live URL of each hosted static site of
// env, by frontend name, as the env's control plane reports it.
func readDeclaredStaticSiteURLs(ctx context.Context, env string) (map[string]string, error) {
	target, err := resolveDeclaredWaitTarget(ctx, env)
	if err != nil {
		return nil, err
	}
	st, err := deploytarget.ReadHostedStatus(ctx, target.Client, hostedProjectName(), env)
	if err != nil {
		return nil, err
	}
	urls := map[string]string{}
	for _, w := range st.Workloads {
		if w.Tier == string(deploytarget.HostedTierStatic) && w.URL != "" {
			urls[w.Name] = w.URL
		}
	}
	return urls, nil
}

// releaseMarkerURL is where siteURL serves its release marker.
func releaseMarkerURL(siteURL string) string {
	return strings.TrimRight(siteURL, "/") + "/" + deploytarget.StaticSiteReleaseMarkerPath
}

// fetchStaticReleaseMarker reads the release marker siteURL serves. The
// marker is served no-cache; the request says no-cache too, so a cache in
// between revalidates rather than answering for the previous release.
func fetchStaticReleaseMarker(ctx context.Context, siteURL string) (deploytarget.StaticSiteReleaseMarker, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseMarkerURL(siteURL), http.NoBody)
	if err != nil {
		return deploytarget.StaticSiteReleaseMarker{}, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return deploytarget.StaticSiteReleaseMarker{}, err
	}
	// The body is read once and fully, or not needed: a close error says
	// nothing about the marker.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return deploytarget.StaticSiteReleaseMarker{}, errNoReleaseMarker
	}
	if resp.StatusCode != http.StatusOK {
		return deploytarget.StaticSiteReleaseMarker{}, fmt.Errorf("answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return deploytarget.StaticSiteReleaseMarker{}, err
	}
	return deploytarget.ParseStaticSiteReleaseMarker(body)
}

// verifyHostedSnapshotBindings is the verdict for every binding of the
// snapshot's hosted tree: a hosted static site's frontend slot through its
// release marker, and everything else UNREACHABLE, as forge has no witness
// for it. sealed is the release the promotion names (the snapshot's seal).
func verifyHostedSnapshotBindings(ctx context.Context, s release.Snapshot, env, sealed string, opts envStatusOptions) []imageVerification {
	readURLs := opts.StaticSiteURLs
	if readURLs == nil {
		readURLs = readDeclaredStaticSiteURLs
	}
	fetch := opts.FetchReleaseMarker
	if fetch == nil {
		fetch = fetchStaticReleaseMarker
	}
	var (
		urls    map[string]string
		urlErr  error
		urlRead bool
	)
	var out []imageVerification
	for _, b := range s.Bindings {
		if b.Consumer.Cluster != release.BundleHostedCluster {
			continue
		}
		a := s.Artifacts[b.Artifact]
		result := imageVerification{Image: b.Consumer.Workload + "/" + b.Slot, Declared: a.Content, State: imageUnreachable,
			Detail: "snapshot observation requires an explicit per-consumer witness for destination " + release.BundleHostedCluster}
		if !sealedStaticSite(s, b) {
			out = append(out, result)
			continue
		}
		want, why := staticSiteRelease(s, b, a, sealed)
		if want == "" {
			result.Detail = why
			out = append(out, result)
			continue
		}
		if !urlRead {
			urlRead = true
			urls, urlErr = readURLs(ctx, env)
		}
		out = append(out, verifyStaticSite(ctx, result, b.Consumer.Workload, want, urls, urlErr, fetch))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Image < out[j].Image })
	return out
}

// sealedStaticSite reports whether binding b is a hosted static site: its
// frontend slot, and a StaticSite record for its consumer in the snapshot's
// sealed hosted resources. That record is what the deploy path publishes
// for a frontend on forge.OnHosted's static tier (deploytarget.HostedRecords,
// HostedTierStatic), so it decides, not the slot's name: a hosted frontend
// published another way has no StaticSite record and no release marker.
func sealedStaticSite(s release.Snapshot, b release.ConsumerBinding) bool {
	if b.Slot != "frontend" {
		return false
	}
	for _, r := range s.Resources {
		if r.Key.Cluster != release.BundleHostedCluster || r.Key.Kind != "StaticSite" {
			continue
		}
		if r.Key.Name == b.Consumer.Workload {
			return true
		}
		for _, o := range r.Owners {
			if o == b.Consumer {
				return true
			}
		}
	}
	return false
}

// staticSiteRelease is the release whose build stamped the marker the site
// serves, or "" and why the snapshot does not say:
//
//   - the artifact's own release (its origin), which survives carry-forward,
//     so a site carried from an earlier release is judged by that release;
//   - else a build event of this snapshot for the same consumer and
//     artifact that records one;
//   - else, when this snapshot built the consumer (it is selected), the
//     release the snapshot is sealed as: that is the release env build cut,
//     and the one it stamped into the site.
//
// A carried site whose artifact records no release, and that no build
// event names, has nothing to compare its marker with.
func staticSiteRelease(s release.Snapshot, b release.ConsumerBinding, a release.SnapshotArtifact, sealed string) (string, string) {
	if r := strings.TrimSpace(a.Origin.Release); r != "" {
		return r, ""
	}
	for _, e := range s.BuildEvents {
		if e.Consumer == b.Consumer && e.Artifact == b.Artifact {
			if r := strings.TrimSpace(e.Origin.Release); r != "" {
				return r, ""
			}
		}
	}
	for _, k := range s.Selected {
		if k == b.Consumer {
			if r := strings.TrimSpace(sealed); r != "" {
				return r, ""
			}
			return "", "the snapshot built this site but the promotion names no release to compare its marker with"
		}
	}
	return "", "the site is carried from an earlier release, and neither its artifact nor a build event records which; " +
		"its release marker cannot be checked until a release rebuilds it"
}

// verifyStaticSite judges one hosted static site by the marker its live URL
// serves against want, the release that built it. result arrives
// UNREACHABLE and leaves that way unless the marker says otherwise.
func verifyStaticSite(ctx context.Context, result imageVerification, name, want string, urls map[string]string, urlErr error,
	fetch func(context.Context, string) (deploytarget.StaticSiteReleaseMarker, error)) imageVerification {
	switch {
	case urlErr != nil:
		result.Detail = "read the site's live URL from the control plane: " + urlErr.Error()
		return result
	case urls[name] == "":
		result.Detail = "the control plane reports no live URL for static site " + name + " yet"
		return result
	}
	site := urls[name]
	result.Workloads = []string{site}
	m, err := fetch(ctx, site)
	switch {
	case errors.Is(err, errNoReleaseMarker):
		result.Detail = fmt.Sprintf("%s %s; a release built before forge stamped one has none, so this site is verifiable from its next release",
			site, errNoReleaseMarker)
		return result
	case err != nil:
		result.Detail = fmt.Sprintf("read %s: %v", releaseMarkerURL(site), err)
		return result
	}
	result.Running = "release " + m.Release
	if m.Frontend != "" && m.Frontend != name {
		result.State = imageDrift
		result.Detail = fmt.Sprintf("%s serves frontend %s's release marker, not %s's", site, m.Frontend, name)
		return result
	}
	if m.Release != want {
		result.State = imageDrift
		result.Detail = fmt.Sprintf("%s serves release %s; the snapshot binds the site built by release %s", site, m.Release, want)
		return result
	}
	result.State = imageMatch
	result.Detail = fmt.Sprintf("%s serves release %s (its release marker)", site, m.Release)
	return result
}
