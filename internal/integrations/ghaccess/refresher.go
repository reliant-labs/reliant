// Copyright (c) 2025 Reliant Labs
package ghaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/netguard"
)

const (
	// DefaultAPIBaseURL is GitHub's REST API.
	DefaultAPIBaseURL = "https://api.github.com"

	// RefreshInterval is how often a trigger owner's access is re-read. It
	// sits well inside webhook.AccessFreshness (an hour), so a healthy
	// snapshot never ages out, and three failed refreshes in a row still
	// leave time to recover before routing stops.
	RefreshInterval = 10 * time.Minute
	// pruneAfter drops grants nobody has refreshed in a day: routing stopped
	// trusting them long before.
	pruneAfter = 24 * time.Hour
	// leaseFor bounds one replica's claim on refreshing one user.
	leaseFor = 2 * time.Minute

	// maxPages bounds one listing (100 per page): 5,000 installations or
	// repositories per installation. A listing longer than this is an
	// error, never a silently partial set.
	maxPages = 50
	perPage  = 100
	// maxBody bounds one response.
	maxBody = 8 << 20

	apiVersion = "2022-11-28"
	userAgent  = "reliant-trigger-access"
)

// Options configure a Refresher.
type Options struct {
	// APIBaseURL overrides GitHub's API (tests). Empty is DefaultAPIBaseURL.
	APIBaseURL string
	// HTTPClient performs the calls. nil is an SSRF-guarded client with a
	// 30s timeout.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Refresher re-reads users' GitHub access.
type Refresher struct {
	store  Store
	tokens TokenSource
	base   string
	client *http.Client
	logger *slog.Logger
	now    func() time.Time
}

// New builds a Refresher.
func New(store Store, tokens TokenSource, opts Options) *Refresher {
	base := strings.TrimSuffix(strings.TrimSpace(opts.APIBaseURL), "/")
	if base == "" {
		base = DefaultAPIBaseURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: netguard.New().Transport()}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Refresher{store: store, tokens: tokens, base: base, client: client, logger: logger, now: time.Now}
}

// Refresh re-reads userID's access and replaces their grants with it.
//
// A permanent failure (see IsPermanent) clears the grants: access that can no
// longer be confirmed is not kept. A transient failure writes nothing, so the
// last snapshot stands until it ages out of webhook.AccessFreshness.
func (r *Refresher) Refresh(ctx context.Context, userID string) error {
	at := r.now().UTC()
	grants, subject, err := r.read(ctx, userID)
	if err != nil {
		if IsPermanent(err) {
			if clearErr := r.store.ReplaceIntegrationAccess(ctx, userID, IntegrationID, "", at, nil); clearErr != nil {
				return errors.Join(err, fmt.Errorf("clear access: %w", clearErr))
			}
		}
		return err
	}
	if err := r.store.ReplaceIntegrationAccess(ctx, userID, IntegrationID, subject, at, grants); err != nil {
		return fmt.Errorf("ghaccess: record access: %w", err)
	}
	return nil
}

// RefreshDue refreshes every user with an enabled GitHub trigger whose
// refresh is due and whose lease this replica wins, then prunes grants
// nobody has refreshed in a day.
func (r *Refresher) RefreshDue(ctx context.Context) {
	owners, err := r.store.ListIntegrationTriggerOwners(ctx, IntegrationID)
	if err != nil {
		r.logger.Warn("listing GitHub trigger owners failed", "error", err)
		return
	}
	for _, userID := range owners {
		if ctx.Err() != nil {
			return
		}
		now := r.now().UTC()
		ok, err := r.store.ClaimIntegrationAccessRefresh(ctx, userID, IntegrationID, now, now.Add(leaseFor), now.Add(-RefreshInterval))
		if err != nil {
			r.logger.Warn("claiming a GitHub access refresh failed", "user_id", userID, "error", err)
			continue
		}
		if !ok {
			continue
		}
		refreshErr := r.Refresh(ctx, userID)
		if refreshErr != nil {
			r.logger.Warn("GitHub access refresh failed", "user_id", userID, "permanent", IsPermanent(refreshErr), "error", refreshErr)
		}
		if err := r.store.FinishIntegrationAccessRefresh(context.WithoutCancel(ctx), userID, IntegrationID, r.now().UTC(), refreshErr); err != nil {
			r.logger.Warn("recording a GitHub access refresh failed", "user_id", userID, "error", err)
		}
	}
	if _, err := r.store.PruneIntegrationAccess(ctx, r.now().Add(-pruneAfter)); err != nil {
		r.logger.Warn("pruning stale GitHub access failed", "error", err)
	}
}

// Run refreshes due users every minute until ctx is done. Every api-server
// replica may run it: leases keep two from refreshing one user at once.
func (r *Refresher) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		r.RefreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// read lists what userID's token can see.
func (r *Refresher) read(ctx context.Context, userID string) ([]core.IntegrationAccessGrant, string, error) {
	token, err := r.tokens.Token(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if token == "" {
		return nil, "", fmt.Errorf("%w: empty token", ErrNotConnected)
	}
	var me struct {
		ID int64 `json:"id"`
	}
	if _, err := r.get(ctx, token, r.base+"/user", &me); err != nil {
		return nil, "", err
	}
	if me.ID == 0 {
		return nil, "", errors.New("ghaccess: GitHub returned no user id")
	}

	var installations []int64
	err = r.paginate(ctx, token, r.base+"/user/installations", func(body []byte) (int, error) {
		var page struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, err
		}
		for _, in := range page.Installations {
			if in.ID != 0 {
				installations = append(installations, in.ID)
			}
		}
		return len(page.Installations), nil
	})
	if err != nil {
		return nil, "", err
	}

	var grants []core.IntegrationAccessGrant
	for _, id := range installations {
		account := strconv.FormatInt(id, 10)
		err := r.paginate(ctx, token, r.base+"/user/installations/"+account+"/repositories", func(body []byte) (int, error) {
			var page struct {
				Repositories []struct {
					ID       int64  `json:"id"`
					FullName string `json:"full_name"`
				} `json:"repositories"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return 0, err
			}
			for _, repo := range page.Repositories {
				if repo.ID != 0 {
					grants = append(grants, core.IntegrationAccessGrant{
						AccountKey: account, ResourceKey: strconv.FormatInt(repo.ID, 10), ResourceLabel: repo.FullName,
					})
				}
			}
			return len(page.Repositories), nil
		})
		var se *statusError
		if errors.As(err, &se) && (se.status == http.StatusNotFound || se.status == http.StatusForbidden) {
			// The installation went away (or the user's access to it) between
			// the two listings: it contributes nothing.
			continue
		}
		if err != nil {
			return nil, "", err
		}
	}
	return grants, strconv.FormatInt(me.ID, 10), nil
}

// paginate GETs first and every rel="next" page, handing each body to page,
// which returns how many items it held.
func (r *Refresher) paginate(ctx context.Context, token, first string, page func([]byte) (int, error)) error {
	next := withPerPage(first)
	for i := 0; next != ""; i++ {
		if i == maxPages {
			return fmt.Errorf("ghaccess: %s has more than %d pages", pathOf(first), maxPages)
		}
		var raw json.RawMessage
		h, err := r.get(ctx, token, next, &raw)
		if err != nil {
			return err
		}
		if _, err := page(raw); err != nil {
			return fmt.Errorf("ghaccess: %s: %w", pathOf(first), err)
		}
		next, err = r.nextPage(h.Get("Link"))
		if err != nil {
			return err
		}
	}
	return nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// nextPage is the rel="next" URL of a Link header, which must stay on the API
// host: following a link elsewhere would send the user's token there.
func (r *Refresher) nextPage(link string) (string, error) {
	m := linkNext.FindStringSubmatch(link)
	if m == nil {
		return "", nil
	}
	next, err := url.Parse(m[1])
	if err != nil {
		return "", fmt.Errorf("ghaccess: bad next link: %w", err)
	}
	base, _ := url.Parse(r.base)
	if !strings.EqualFold(next.Host, base.Host) || next.Scheme != base.Scheme {
		return "", fmt.Errorf("ghaccess: next link leaves %s", base.Host)
	}
	return next.String(), nil
}

type statusError struct {
	path   string
	status int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("ghaccess: GitHub answered %s with %d", e.path, e.status)
}

// get performs one authenticated GET and decodes the JSON body into out.
// Errors never carry the token.
func (r *Refresher) get(ctx context.Context, token, rawURL string, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ghaccess: build request for %s: %w", pathOf(rawURL), err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := r.client.Do(req)
	if err != nil {
		// A transport error can quote the URL, never the header.
		return nil, fmt.Errorf("ghaccess: GET %s: %w", pathOf(rawURL), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("ghaccess: read %s: %w", pathOf(rawURL), err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w (%s)", ErrTokenRejected, pathOf(rawURL))
	case resp.StatusCode == http.StatusForbidden && pathOf(rawURL) == "/user/installations":
		// A token that is not an App user token (a PAT) cannot list them.
		return nil, fmt.Errorf("%w (%s answered 403)", ErrUnsupportedToken, pathOf(rawURL))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, &statusError{path: pathOf(rawURL), status: resp.StatusCode}
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("ghaccess: %s response exceeds %d bytes", pathOf(rawURL), maxBody)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("ghaccess: decode %s: %w", pathOf(rawURL), err)
	}
	return resp.Header, nil
}

func withPerPage(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("per_page", strconv.Itoa(perPage))
	u.RawQuery = q.Encode()
	return u.String()
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	return u.Path
}
