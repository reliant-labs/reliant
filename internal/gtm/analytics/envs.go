// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"

	deployv1 "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1"
	deployconnect "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// AllEnvironments is how a result read with no Environment is labelled: it
// covers every environment of the org (or of ProjectID), and also events
// with no environment id and events from deleted environments (see the
// package doc).
const AllEnvironments = "all environments"

// EnvResolver maps a deploy environment name to the ids of the org's live
// environments that carry it. Names are unique per project, not per org, so
// with no projectID every project's match comes back (project-less ones
// included); with one, only that project's. An unknown name is a
// *gtm.ValidationError that lists the names there are.
type EnvResolver interface {
	Resolve(ctx context.Context, orgID, name, projectID string) ([]string, error)
}

// DeployClient is the one DeployService rpc the resolver calls.
// controlplanev1connect.DeployServiceClient satisfies it.
type DeployClient interface {
	ListEnvironments(context.Context, *connect.Request[deployv1.ListDeployEnvironmentsRequest]) (*connect.Response[deployv1.ListDeployEnvironmentsResponse], error)
}

// NewDeployClient is the control plane's DeployService at baseURL
// (ControlPlaneConfig.url), with the same URL and redirect rules as
// NewObservabilityClient.
func NewDeployClient(baseURL string) (DeployClient, error) {
	return NewDeployClientWith(observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second}), baseURL)
}

// NewDeployClientWith is NewDeployClient over a copy of httpClient.
func NewDeployClientWith(httpClient *http.Client, baseURL string) (DeployClient, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := checkControlPlaneURL(base); err != nil {
		return nil, err
	}
	hc := *httpClient
	hc.CheckRedirect = checkRedirect
	return deployconnect.NewDeployServiceClient(&hc, base), nil
}

// DefaultEnvCacheTTL is how long a resolution is reused.
const DefaultEnvCacheTTL = 5 * time.Minute

// maxEnvCacheEntries bounds the cache; past it, expired entries are dropped,
// and if that is not enough the cache is emptied.
const maxEnvCacheEntries = 1024

// maxListedEnvNames is how many names an unknown-name error lists.
const maxListedEnvNames = 20

// maxScopeEnvIDs is QueryScope.env_ids' limit.
const maxScopeEnvIDs = 50

// EnvResolverDeps are what NewEnvResolver reads through.
type EnvResolverDeps struct {
	// CP is the control plane's DeployService (NewDeployClient).
	CP DeployClient
	// Token returns the analytics access token: the same one the reader
	// sends (GtmConfig.analytics_token), minted with obs:read and
	// deploy:read and used only by the analytics read path.
	Token func() string
	// TTL is how long a resolution is cached; 0 is DefaultEnvCacheTTL.
	TTL time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Log receives causes the returned errors leave out; nil is
	// slog.Default().
	Log *slog.Logger
}

// NewEnvResolver is EnvResolver over DeployService.ListEnvironments, with a
// per (org, name, project) cache.
func NewEnvResolver(d EnvResolverDeps) EnvResolver {
	r := &envResolver{cp: d.CP, token: d.Token, ttl: d.TTL, now: d.Now, log: d.Log, cache: map[envKey]envEntry{}}
	if r.ttl <= 0 {
		r.ttl = DefaultEnvCacheTTL
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r
}

type envKey struct{ org, name, project string }

type envEntry struct {
	ids     []string
	expires time.Time
}

type envResolver struct {
	cp    DeployClient
	token func() string
	ttl   time.Duration
	now   func() time.Time
	log   *slog.Logger

	mu    sync.Mutex
	cache map[envKey]envEntry
}

// Resolve implements EnvResolver. Only a resolution is cached; an unknown
// name or a failure asks the control plane again next time.
func (r *envResolver) Resolve(ctx context.Context, orgID, name, projectID string) ([]string, error) {
	key := envKey{orgID, name, projectID}
	if ids, ok := r.cached(key); ok {
		return ids, nil
	}
	token := ""
	if r.token != nil {
		token = strings.TrimSpace(r.token())
	}
	if r.cp == nil || token == "" {
		return nil, errEnvFilterUnavailable
	}
	in := &deployv1.ListDeployEnvironmentsRequest{OrgId: &orgID}
	if projectID != "" {
		in.Project = &projectID
	}
	req := connect.NewRequest(in)
	req.Header().Set("Authorization", "Bearer "+token)
	resp, err := r.cp.ListEnvironments(ctx, req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		r.log.WarnContext(ctx, "gtm analytics: ListEnvironments failed", "code", connect.CodeOf(err).String(), "error", err)
		switch connect.CodeOf(err) {
		case connect.CodePermissionDenied:
			return nil, errTokenNeedsDeployRead
		case connect.CodeUnauthenticated:
			return nil, errCredentialRefused
		case connect.CodeDeadlineExceeded:
			return nil, errTimedOut
		}
		return nil, errEnvLookupFailed
	}
	var ids, names []string
	seenID, seenName := map[string]bool{}, map[string]bool{}
	for _, e := range resp.Msg.GetEnvironments() {
		if projectID != "" && e.GetProject() != projectID {
			continue // the server filters by project; this holds it to that
		}
		if n := e.GetName(); !seenName[n] {
			seenName[n] = true
			names = append(names, n)
		}
		if e.GetName() == name && e.GetId() != "" && !seenID[e.GetId()] {
			seenID[e.GetId()] = true
			ids = append(ids, e.GetId())
		}
	}
	if len(ids) == 0 {
		return nil, unknownEnvError(name, projectID, names)
	}
	if len(ids) > maxScopeEnvIDs {
		return nil, &gtm.ValidationError{Field: "environment", Reason: "the name matches more environments than one query reads (50); give a project"}
	}
	sort.Strings(ids)
	r.store(key, ids)
	return append([]string(nil), ids...), nil
}

func (r *envResolver) cached(key envKey) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[key]
	if !ok || !r.now().Before(e.expires) {
		return nil, false
	}
	return append([]string(nil), e.ids...), true
}

func (r *envResolver) store(key envKey, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if len(r.cache) >= maxEnvCacheEntries {
		for k, e := range r.cache {
			if !now.Before(e.expires) {
				delete(r.cache, k)
			}
		}
		if len(r.cache) >= maxEnvCacheEntries {
			r.cache = map[envKey]envEntry{}
		}
	}
	r.cache[key] = envEntry{ids: ids, expires: now.Add(r.ttl)}
}

// unknownEnvError is the *gtm.ValidationError for a name no live
// environment carries, listing the names there are (those that are plain
// names, sorted, at most maxListedEnvNames).
func unknownEnvError(name, projectID string, names []string) error {
	var listed []string
	for _, n := range names {
		if validName(n) {
			listed = append(listed, n)
		}
	}
	sort.Strings(listed)
	more := ""
	if len(listed) > maxListedEnvNames {
		more = ", ..."
		listed = listed[:maxListedEnvNames]
	}
	where := "the org"
	if projectID != "" {
		where = "project " + projectID
	}
	reason := "no environment named " + name + " in " + where
	if len(listed) == 0 {
		reason += "; it has no environments"
	} else {
		reason += "; it has: " + strings.Join(listed, ", ") + more
	}
	return &gtm.ValidationError{Field: "environment", Reason: reason}
}
