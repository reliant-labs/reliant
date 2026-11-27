// Copyright (c) 2026 Reliant Labs

package ghcontents

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/netguard"
)

const (
	// DefaultAPIBaseURL is GitHub's REST API.
	DefaultAPIBaseURL = "https://api.github.com"

	apiVersion = "2022-11-28"
	userAgent  = "reliant-pipeline-config"
	// rawMediaType asks for the file's bytes rather than base64 in JSON.
	rawMediaType = "application/vnd.github.raw"
)

var (
	// namePattern is a GitHub owner or repository name.
	namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	// refPattern is a ref GitHub accepts in ?ref=: a sha, branch or tag.
	refPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
)

// Reader reads GitHub repository files as the App.
type Reader struct {
	tokens   Tokens
	base     string
	client   *http.Client
	maxBytes int64
}

// New builds a Reader over deps.Tokens.
//
// forge:constructor
func New(deps Deps) Service {
	base := strings.TrimSuffix(strings.TrimSpace(deps.APIBaseURL), "/")
	if base == "" {
		base = DefaultAPIBaseURL
	}
	client := deps.HTTPClient
	if client == nil {
		client = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 20 * time.Second, Base: netguard.New().Transport()})
	}
	maxBytes := deps.MaxBytes
	if maxBytes <= 0 {
		maxBytes = MaxBytes
	}
	return &Reader{tokens: deps.Tokens, base: base, client: client, maxBytes: maxBytes}
}

// Read returns the bytes of req.Path at req.Ref.
func (r *Reader) Read(ctx context.Context, req Request) ([]byte, error) {
	if !validName(req.Owner) || !validName(req.Repo) || !validPath(req.Path) || !refPattern.MatchString(req.Ref) || strings.Contains(req.Ref, "..") {
		return nil, ErrInvalid
	}
	tok, err := r.tokens.MintInstallationToken(ctx, gitcredentialclient.InstallationRequest{
		ExternalUserID: req.UserID, Owner: req.Owner, Repo: req.Repo, OrgID: req.OrgID,
		Permissions: map[string]string{"contents": "read"},
	})
	if err != nil {
		return nil, fmt.Errorf("ghcontents: mint a token for %s/%s: %w", req.Owner, req.Repo, err)
	}
	var token string
	if err := tok.Token.Use(func(access string) error { token = access; return nil }); err != nil || token == "" {
		return nil, fmt.Errorf("ghcontents: the minted token for %s/%s is empty", req.Owner, req.Repo)
	}
	escaped := make([]string, 0, strings.Count(req.Path, "/")+1)
	for _, seg := range strings.Split(req.Path, "/") {
		escaped = append(escaped, url.PathEscape(seg))
	}
	path := "/repos/" + url.PathEscape(req.Owner) + "/" + url.PathEscape(req.Repo) + "/contents/" + strings.Join(escaped, "/")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+path+"?ref="+url.QueryEscape(req.Ref), nil)
	if err != nil {
		return nil, fmt.Errorf("ghcontents: build request for %s: %w", path, err)
	}
	httpReq.Header.Set("Accept", rawMediaType)
	httpReq.Header.Set("X-GitHub-Api-Version", apiVersion)
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := r.client.Do(httpReq)
	if err != nil {
		// A transport error can quote the URL, never the header.
		return nil, fmt.Errorf("ghcontents: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w (%s at %s)", ErrNotFound, req.Path, req.Ref)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// 403/429 are GitHub's rate limits (or an org's SSO enforcement);
		// 5xx is GitHub. Neither says the file is gone.
		return nil, fmt.Errorf("ghcontents: GET %s answered %d", path, resp.StatusCode)
	case resp.ContentLength > r.maxBytes:
		return nil, fmt.Errorf("%w (%d bytes, at most %d)", ErrTooLarge, resp.ContentLength, r.maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, r.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ghcontents: read %s: %w", path, err)
	}
	if int64(len(body)) > r.maxBytes {
		return nil, fmt.Errorf("%w (more than %d bytes)", ErrTooLarge, r.maxBytes)
	}
	return body, nil
}

func validName(s string) bool { return namePattern.MatchString(s) && strings.Trim(s, ".") != "" }

// validPath is a repository-relative path with no empty, "." or ".."
// segment.
func validPath(p string) bool {
	if p == "" || len(p) > 4096 {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
