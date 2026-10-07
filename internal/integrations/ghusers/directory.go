// Copyright (c) 2025 Reliant Labs

package ghusers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/reliant-labs/reliant/internal/netguard"
)

const (
	// DefaultAPIBaseURL is GitHub's REST API.
	DefaultAPIBaseURL = "https://api.github.com"

	apiVersion = "2022-11-28"
	userAgent  = "reliant-trigger-senders"
	// maxBody bounds one user response, which is a few KB.
	maxBody = 1 << 20
	// parallel bounds the requests one Resolve has in flight.
	parallel = 4
)

var (
	// loginPattern is a GitHub login (alphanumerics and single hyphens, at
	// most 39) or an App's bot account ("dependabot[bot]"). Anything else is
	// not a login and is never sent to GitHub.
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)
	idPattern    = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
)

// Options configures a Directory.
type Options struct {
	// APIBaseURL overrides GitHub's API (tests). Empty is DefaultAPIBaseURL.
	APIBaseURL string
	// HTTPClient performs the calls. nil is an SSRF-guarded client with a
	// 15s timeout.
	HTTPClient *http.Client
}

// Directory looks GitHub users up with the caller's own token.
type Directory struct {
	tokens TokenSource
	base   string
	client *http.Client
}

// New builds a Directory over tokens.
func New(tokens TokenSource, opts Options) *Directory {
	base := strings.TrimSuffix(strings.TrimSpace(opts.APIBaseURL), "/")
	if base == "" {
		base = DefaultAPIBaseURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, Transport: netguard.New().Transport()}
	}
	return &Directory{tokens: tokens, base: base, client: client}
}

// Resolve looks up logins (to their ids) and ids (to their current logins)
// as userID. A login may carry a leading "@". A query GitHub does not know —
// no such user, an organization (which never sends an event), or a string
// that is not a login or an id at all — is absent from the result rather than
// an error. An error is the token (TokenSource's, or ErrTokenRejected) or
// GitHub being unreachable.
func (d *Directory) Resolve(ctx context.Context, userID string, logins, ids []string) ([]Found, error) {
	type query struct{ asked, path string }
	var queries []query
	seen := map[string]bool{}
	add := func(asked, path string) {
		if !seen[path] {
			seen[path] = true
			queries = append(queries, query{asked, path})
		}
	}
	for _, raw := range logins {
		login := strings.TrimPrefix(strings.TrimSpace(raw), "@")
		if loginPattern.MatchString(login) {
			add(raw, "/users/"+url.PathEscape(login))
		}
	}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if idPattern.MatchString(id) {
			add(raw, "/user/"+id)
		}
	}
	if len(queries) == 0 {
		return nil, nil
	}
	if len(queries) > MaxQueries {
		return nil, fmt.Errorf("%w (%d, at most %d)", ErrTooManyQueries, len(queries), MaxQueries)
	}

	token, err := d.tokens.Token(ctx, userID)
	if err != nil {
		return nil, err
	}

	found := make([]*Found, len(queries))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	for i, q := range queries {
		g.Go(func() error {
			u, ok, err := d.get(gctx, token, q.path)
			if err != nil || !ok {
				return err
			}
			found[i] = &Found{Query: q.asked, User: u}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := make([]Found, 0, len(found))
	for _, f := range found {
		if f != nil {
			out = append(out, *f)
		}
	}
	return out, nil
}

// get reads one user. ok is false when GitHub has no such user, or the
// account is not one that can send an event.
func (d *Directory) get(ctx context.Context, token, path string) (User, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+path, nil)
	if err != nil {
		return User{}, false, fmt.Errorf("ghusers: build request for %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := d.client.Do(req)
	if err != nil {
		// A transport error can quote the URL, never the header.
		return User{}, false, fmt.Errorf("ghusers: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return User{}, false, fmt.Errorf("ghusers: read %s: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return User{}, false, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return User{}, false, fmt.Errorf("%w (%s)", ErrTokenRejected, path)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// 403/429 are GitHub's rate limits; 5xx is GitHub. Neither says
		// anything about the person, so the whole lookup fails and is
		// retried rather than reporting "no such user".
		return User{}, false, fmt.Errorf("ghusers: GET %s answered %d", path, resp.StatusCode)
	case len(body) > maxBody:
		return User{}, false, fmt.Errorf("ghusers: %s response exceeds %d bytes", path, maxBody)
	}
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return User{}, false, fmt.Errorf("ghusers: decode %s: %w", path, err)
	}
	if u.ID <= 0 || u.Login == "" || u.Type == "Organization" {
		return User{}, false, nil
	}
	return User{ID: strconv.FormatInt(u.ID, 10), Login: u.Login}, true, nil
}
