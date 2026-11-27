// Copyright (c) 2026 Reliant Labs

package ghblobs

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
	"github.com/reliant-labs/reliant/internal/netguard"
)

const (
	// DefaultAPIBaseURL is GitHub's REST API.
	DefaultAPIBaseURL = "https://api.github.com"

	apiVersion = "2022-11-28"
	userAgent  = "reliant-coverage-source"
	// rawMediaType asks for the blob's bytes rather than base64 in JSON.
	rawMediaType = "application/vnd.github.raw"
)

var (
	// namePattern is a GitHub owner or repository name.
	namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	// blobPattern is a git object id: SHA-1, or SHA-256.
	blobPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// Reader reads GitHub blobs with the caller's own token.
type Reader struct {
	tokens   TokenSource
	base     string
	client   *http.Client
	maxBytes int64
	// slots holds one token per read in flight (MaxInFlight).
	slots chan struct{}
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
	return &Reader{tokens: deps.Tokens, base: base, client: client, maxBytes: maxBytes, slots: make(chan struct{}, MaxInFlight)}
}

// Read returns the bytes of blob blobID in owner/repo, read as userID.
func (r *Reader) Read(ctx context.Context, userID, owner, repo, blobID string) ([]byte, error) {
	blobID = strings.ToLower(strings.TrimSpace(blobID))
	if !namePattern.MatchString(owner) || !namePattern.MatchString(repo) || strings.Trim(owner, ".") == "" || strings.Trim(repo, ".") == "" ||
		!blobPattern.MatchString(blobID) {
		return nil, ErrInvalid
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	token, err := r.tokens.Token(ctx, userID)
	if err != nil {
		return nil, err
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/git/blobs/" + blobID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("ghblobs: build request for %s: %w", path, err)
	}
	req.Header.Set("Accept", rawMediaType)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := r.client.Do(req)
	if err != nil {
		// A transport error can quote the URL, never the header.
		return nil, fmt.Errorf("ghblobs: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w (%s)", ErrNotFound, path)
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w (%s)", ErrTokenRejected, path)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// 403/429 are GitHub's rate limits (or an org's SSO enforcement);
		// 5xx is GitHub. Neither says the blob is gone.
		return nil, fmt.Errorf("ghblobs: GET %s answered %d", path, resp.StatusCode)
	case resp.ContentLength > r.maxBytes:
		return nil, fmt.Errorf("%w (%d bytes, at most %d)", ErrTooLarge, resp.ContentLength, r.maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, r.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ghblobs: read %s: %w", path, err)
	}
	if int64(len(body)) > r.maxBytes {
		return nil, fmt.Errorf("%w (more than %d bytes)", ErrTooLarge, r.maxBytes)
	}
	return body, nil
}
