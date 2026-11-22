package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// GitHubSource finds releases through the GitHub releases API.
//
// Channels: "" and "latest" are the newest non-prerelease release
// (/releases/latest); "prerelease" is the newest release of any kind, drafts
// excluded. Any other channel is an error.
type GitHubSource struct {
	// Repo is "owner/name".
	Repo string
	// Bundle names the release's signature asset, e.g.
	// "forge-release.sigstore.json"; it becomes [Release.Bundle].
	Bundle string
	// APIBaseURL defaults to https://api.github.com. It must be an https URL
	// with no userinfo, query or fragment (a GitHub Enterprise path such as
	// https://ghe.example.com/api/v3 is fine).
	APIBaseURL string
	// Token, optional, raises the API's rate limit; never required for a
	// public repository. It is sent only to the API's origin (scheme, host
	// and port of APIBaseURL): never on a redirect off that origin, and never
	// with an asset download.
	Token  string
	Client *http.Client
}

// defaultGitHubAPI is the API GitHubSource talks to when APIBaseURL is empty.
const defaultGitHubAPI = "https://api.github.com"

var (
	ghOwnerRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	ghNameRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// repoParts validates Repo as exactly owner/name, in GitHub's own alphabet.
func (g GitHubSource) repoParts() (owner, name string, err error) {
	owner, name, ok := strings.Cut(g.Repo, "/")
	if !ok || !ghOwnerRE.MatchString(owner) || !ghNameRE.MatchString(name) || name == "." || name == ".." {
		return "", "", fmt.Errorf("selfupdate: GitHubSource.Repo must be owner/name, got %q", g.Repo)
	}
	return owner, name, nil
}

// apiBase parses and validates APIBaseURL.
func (g GitHubSource) apiBase() (*url.URL, error) {
	raw := g.APIBaseURL
	if raw == "" {
		raw = defaultGitHubAPI
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, fmt.Errorf("selfupdate: GitHubSource.APIBaseURL %q: %w", raw, err)
	case u.Scheme != "https":
		return nil, fmt.Errorf("selfupdate: GitHubSource.APIBaseURL must be https, got %q", raw)
	case u.User != nil:
		return nil, errors.New("selfupdate: GitHubSource.APIBaseURL must not carry userinfo")
	case u.Host == "" || u.Opaque != "":
		return nil, fmt.Errorf("selfupdate: GitHubSource.APIBaseURL %q has no host", raw)
	case u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return nil, fmt.Errorf("selfupdate: GitHubSource.APIBaseURL %q must not carry a query or fragment", raw)
	}
	return u, nil
}

// sameOrigin compares scheme, host and effective port.
func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// Latest returns the channel's newest release.
func (g GitHubSource) Latest(ctx context.Context, channel string) (Release, error) {
	owner, name, err := g.repoParts()
	if err != nil {
		return Release{}, err
	}
	base, err := g.apiBase()
	if err != nil {
		return Release{}, err
	}
	releases := base.JoinPath("repos", owner, name, "releases")
	var rel ghRelease
	switch channel {
	case "", "latest":
		if err := g.get(ctx, base, releases.JoinPath("latest"), &rel); err != nil {
			return Release{}, err
		}
	case "prerelease":
		var list []ghRelease
		releases.RawQuery = "per_page=30"
		if err := g.get(ctx, base, releases, &list); err != nil {
			return Release{}, err
		}
		found := false
		for _, r := range list {
			if r.Draft {
				continue
			}
			if !found || laterTag(r, rel) {
				rel, found = r, true
			}
		}
		if !found {
			return Release{}, fmt.Errorf("selfupdate: %s has no releases", g.Repo)
		}
	default:
		return Release{}, fmt.Errorf("selfupdate: GitHubSource has no channel %q (use latest or prerelease)", channel)
	}
	if rel.TagName == "" {
		return Release{}, errors.New("selfupdate: GitHub returned a release with no tag")
	}
	out := Release{
		Version: strings.TrimPrefix(rel.TagName, "v"),
		Tag:     rel.TagName,
		Channel: channel,
	}
	for _, a := range rel.Assets {
		asset := Asset{Name: a.Name, URL: a.URL, Size: a.Size}
		out.Assets = append(out.Assets, asset)
		if g.Bundle != "" && a.Name == g.Bundle {
			out.Bundle = asset
		}
	}
	return out, nil
}

// laterTag orders two releases by semver tag, falling back to publish time.
func laterTag(a, b ghRelease) bool {
	ca, cb := canonicalVersion(a.TagName), canonicalVersion(b.TagName)
	if ca != "" && cb != "" {
		isNewer, _ := newer(Build{Version: cb}, Release{Version: ca})
		return isNewer
	}
	return a.PublishedAt.After(b.PublishedAt)
}

// get fetches target into into. The token goes only to base's origin: the
// redirect hook strips it from any hop that leaves it.
func (g GitHubSource) get(ctx context.Context, base, target *url.URL, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" && sameOrigin(target, base) {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	client := g.Client
	if client == nil {
		client = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second})
	}
	resp, err := apiClient(client, base).Do(req)
	if err != nil {
		return fmt.Errorf("selfupdate: GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("selfupdate: GitHub releases: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(into); err != nil {
		return fmt.Errorf("selfupdate: GitHub releases: decode: %w", err)
	}
	return nil
}

// apiClient wraps c: redirects stay on https, and the Authorization header
// never follows one off the API's origin.
func apiClient(c *http.Client, base *url.URL) *http.Client {
	inner := httpsOnly(c)
	check := inner.CheckRedirect
	cp := *inner
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameOrigin(req.URL, base) {
			req.Header.Del("Authorization")
		}
		return check(req, via)
	}
	return &cp
}
