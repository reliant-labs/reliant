// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	cpv1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	deployv1 "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1"
	obsv1 "github.com/reliant-labs/reliant/gen/controlplane/services/observability/v1"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// fakeDeploy is DeployService.ListEnvironments over envs: it honours the
// request's project filter as the control plane does, and records requests.
type fakeDeploy struct {
	envs []*cpv1.DeployEnvironment
	err  error
	reqs []*connect.Request[deployv1.ListDeployEnvironmentsRequest]
}

func (f *fakeDeploy) ListEnvironments(_ context.Context, req *connect.Request[deployv1.ListDeployEnvironmentsRequest]) (*connect.Response[deployv1.ListDeployEnvironmentsResponse], error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	var out []*cpv1.DeployEnvironment
	for _, e := range f.envs {
		if req.Msg.Project != nil && e.GetProject() != req.Msg.GetProject() {
			continue
		}
		out = append(out, e)
	}
	return connect.NewResponse(&deployv1.ListDeployEnvironmentsResponse{Environments: out}), nil
}

func env(id, name, project string) *cpv1.DeployEnvironment {
	return &cpv1.DeployEnvironment{Id: id, Name: name, Project: project}
}

// orgEnvs: "prod" in two projects and project-less, plus staging and a
// preview.
func orgEnvs() []*cpv1.DeployEnvironment {
	return []*cpv1.DeployEnvironment{
		env("env-web-prod", "prod", "web"),
		env("env-api-prod", "prod", "api"),
		env("env-prod", "prod", ""),
		env("env-web-staging", "staging", "web"),
		env("env-web-pr-12", "pr-12", "web"),
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestResolver(cp DeployClient, c *clock) EnvResolver {
	return NewEnvResolver(EnvResolverDeps{CP: cp, Token: token(testToken), Now: c.now, Log: discard()})
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func logTo(b *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(b, nil)) }

func TestResolverMatchesEveryProject(t *testing.T) {
	cp := &fakeDeploy{envs: orgEnvs()}
	ids, err := newTestResolver(cp, &clock{to}).Resolve(context.Background(), tenantZero, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"env-api-prod", "env-prod", "env-web-prod"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	req := cp.reqs[0]
	if req.Msg.GetOrgId() != tenantZero || req.Msg.Project != nil || req.Msg.Search != nil {
		t.Errorf("request = %v", req.Msg)
	}
	if got := req.Header().Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got)
	}
}

func TestResolverProjectFilter(t *testing.T) {
	cp := &fakeDeploy{envs: orgEnvs()}
	ids, err := newTestResolver(cp, &clock{to}).Resolve(context.Background(), tenantZero, "prod", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"env-web-prod"}) {
		t.Fatalf("ids = %v", ids)
	}
	if cp.reqs[0].Msg.GetProject() != "web" {
		t.Errorf("project not sent: %v", cp.reqs[0].Msg)
	}
	// A control plane that ignored the project filter is held to it.
	loose := &fakeDeploy{envs: orgEnvs()}
	ids, err = newTestResolver(ignoreProject{loose}, &clock{to}).Resolve(context.Background(), tenantZero, "prod", "api")
	if err != nil || !slices.Equal(ids, []string{"env-api-prod"}) {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
}

// ignoreProject is a DeployService that drops the request's project filter.
type ignoreProject struct{ f *fakeDeploy }

func (i ignoreProject) ListEnvironments(ctx context.Context, req *connect.Request[deployv1.ListDeployEnvironmentsRequest]) (*connect.Response[deployv1.ListDeployEnvironmentsResponse], error) {
	in := &deployv1.ListDeployEnvironmentsRequest{OrgId: req.Msg.OrgId}
	return i.f.ListEnvironments(ctx, connect.NewRequest(in))
}

func TestResolverUnknownName(t *testing.T) {
	cp := &fakeDeploy{envs: orgEnvs()}
	r := newTestResolver(cp, &clock{to})
	_, err := r.Resolve(context.Background(), tenantZero, "production", "")
	var ve *gtm.ValidationError
	if !errors.As(err, &ve) || ve.Field != "environment" {
		t.Fatalf("err = %v, want a *gtm.ValidationError on environment", err)
	}
	if want := "no environment named production in the org; it has: pr-12, prod, staging"; ve.Reason != want {
		t.Errorf("reason = %q, want %q", ve.Reason, want)
	}
	_, err = r.Resolve(context.Background(), tenantZero, "staging", "api")
	if !errors.As(err, &ve) || ve.Reason != "no environment named staging in project api; it has: prod" {
		t.Fatalf("err = %v", err)
	}
	_, err = newTestResolver(&fakeDeploy{}, &clock{to}).Resolve(context.Background(), tenantZero, "prod", "")
	if !errors.As(err, &ve) || !strings.HasSuffix(ve.Reason, "it has no environments") {
		t.Fatalf("err = %v", err)
	}
	// An unknown name is not cached: a new environment shows up at once.
	cp.envs = append(cp.envs, env("env-new", "production", ""))
	if ids, err := r.Resolve(context.Background(), tenantZero, "production", ""); err != nil || !slices.Equal(ids, []string{"env-new"}) {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
}

func TestResolverListsOnlyPlainNames(t *testing.T) {
	envs := []*cpv1.DeployEnvironment{env("e1", "prod", ""), env("e2", "weird name; x", "")}
	for i := range 25 {
		envs = append(envs, env("p"+string(rune('a'+i)), "pr-"+string(rune('a'+i)), ""))
	}
	_, err := newTestResolver(&fakeDeploy{envs: envs}, &clock{to}).Resolve(context.Background(), tenantZero, "nope", "")
	var ve *gtm.ValidationError
	if !errors.As(err, &ve) || strings.Contains(ve.Reason, "weird") || !strings.HasSuffix(ve.Reason, ", ...") ||
		strings.Count(ve.Reason, ",") != maxListedEnvNames {
		t.Fatalf("reason = %v", err)
	}
}

func TestResolverPermissionDenied(t *testing.T) {
	var log bytes.Buffer
	cp := &fakeDeploy{err: connect.NewError(connect.CodePermissionDenied, errors.New("scope deploy:read required"))}
	r := NewEnvResolver(EnvResolverDeps{CP: cp, Token: token(testToken), Log: logTo(&log)})
	_, err := r.Resolve(context.Background(), tenantZero, "prod", "")
	if err != errTokenNeedsDeployRead || !errors.Is(err, gtm.ErrAnalyticsFailed) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "deploy:read") || !strings.Contains(log.String(), "scope deploy:read required") {
		t.Errorf("err = %v, log = %s", err, log.String())
	}
	assertNoToken(t, log.String(), err)
	for code, want := range map[connect.Code]error{
		connect.CodeUnauthenticated:    errCredentialRefused,
		connect.CodeDeadlineExceeded:   errTimedOut,
		connect.CodeInternal:           errEnvLookupFailed,
		connect.CodeFailedPrecondition: errEnvLookupFailed,
	} {
		cp := &fakeDeploy{err: connect.NewError(code, errors.New("ch-0.internal"))}
		if _, err := newTestResolver(cp, &clock{to}).Resolve(context.Background(), tenantZero, "prod", ""); err != want {
			t.Errorf("%v: err = %v, want %v", code, err, want)
		}
	}
}

func TestResolverFailsClosedWithoutTokenOrClient(t *testing.T) {
	for name, r := range map[string]EnvResolver{
		"no token":  NewEnvResolver(EnvResolverDeps{CP: &fakeDeploy{envs: orgEnvs()}, Token: token(" ")}),
		"no client": NewEnvResolver(EnvResolverDeps{Token: token(testToken)}),
	} {
		if _, err := r.Resolve(context.Background(), tenantZero, "prod", ""); err != errEnvFilterUnavailable {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestResolverCache(t *testing.T) {
	cp := &fakeDeploy{envs: orgEnvs()}
	c := &clock{to}
	r := newTestResolver(cp, c)
	ctx := context.Background()
	first, _ := r.Resolve(ctx, tenantZero, "prod", "")
	first[0] = "mutated" // a caller's slice is its own
	again, err := r.Resolve(ctx, tenantZero, "prod", "")
	if err != nil || len(cp.reqs) != 1 || again[0] != "env-api-prod" {
		t.Fatalf("second resolve: ids = %v, err = %v, %d requests", again, err, len(cp.reqs))
	}
	// Keys are (org, name, project).
	_, _ = r.Resolve(ctx, tenantZero, "prod", "web")
	_, _ = r.Resolve(ctx, "other-org", "prod", "")
	_, _ = r.Resolve(ctx, tenantZero, "staging", "")
	if len(cp.reqs) != 4 {
		t.Fatalf("%d requests, want 4 (one per key)", len(cp.reqs))
	}
	// Within the TTL the cached ids stand; after it the control plane is asked again.
	cp.envs = append(cp.envs, env("env-z-prod", "prod", "z"))
	c.t = to.Add(DefaultEnvCacheTTL - time.Second)
	if ids, _ := r.Resolve(ctx, tenantZero, "prod", ""); len(ids) != 3 {
		t.Fatalf("within TTL: %v", ids)
	}
	c.t = to.Add(DefaultEnvCacheTTL)
	if ids, _ := r.Resolve(ctx, tenantZero, "prod", ""); len(ids) != 4 {
		t.Fatalf("after TTL: %v", ids)
	}
	// Failures are not cached.
	cp.err = connect.NewError(connect.CodeUnavailable, errors.New("down"))
	if _, err := r.Resolve(ctx, tenantZero, "pr-12", ""); err == nil {
		t.Fatal("no error")
	}
	cp.err = nil
	if ids, err := r.Resolve(ctx, tenantZero, "pr-12", ""); err != nil || len(ids) != 1 {
		t.Fatalf("after a failure: %v %v", ids, err)
	}
}

func TestResolverTooManyIDs(t *testing.T) {
	var envs []*cpv1.DeployEnvironment
	for i := range maxScopeEnvIDs + 1 {
		envs = append(envs, env("e"+strings.Repeat("x", i+1), "prod", "p"+strings.Repeat("x", i+1)))
	}
	_, err := newTestResolver(&fakeDeploy{envs: envs}, &clock{to}).Resolve(context.Background(), tenantZero, "prod", "")
	var ve *gtm.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
}

// stubEnvs is an EnvResolver that answers ids or err and records calls.
type stubEnvs struct {
	ids   []string
	err   error
	calls []string
}

func (s *stubEnvs) Resolve(_ context.Context, orgID, name, projectID string) ([]string, error) {
	s.calls = append(s.calls, orgID+"|"+name+"|"+projectID)
	return s.ids, s.err
}

// TestScopeWiring: every panel query carries the resolved env ids (and the
// project); no Environment leaves env_ids unset; a resolver failure fails the
// read without a panel query.
func TestScopeWiring(t *testing.T) {
	queries := map[string]gtm.AnalyticsQuery{
		"funnel": {Template: gtm.AnalyticsFunnel, Events: []string{"a", "b"}, From: from, To: to, Limit: 10},
	}
	for name, q := range sqlQueries {
		queries[name] = q
	}
	for name, q := range queries {
		t.Run(name, func(t *testing.T) {
			resp := emptyFor(q)
			if q.Template == gtm.AnalyticsFunnel {
				resp = funnelResp(q.Events)
			}
			envs := &stubEnvs{ids: []string{"env-api-prod", "env-web-prod"}}
			cp := &fakeCP{resp: resp}
			c := newClient(Deps{CP: cp, Token: token(testToken), Envs: envs})

			named := q
			named.Environment, named.ProjectID = "prod", "web"
			res, err := c.Query(context.Background(), tenantZero, named)
			if err != nil {
				t.Fatal(err)
			}
			s := cp.reqs[0].Msg.GetScope()
			if !slices.Equal(s.GetEnvIds(), envs.ids) || s.ProjectId == nil || s.GetProjectId() != "web" || s.GetOrgId() != tenantZero {
				t.Errorf("scope = %v", s)
			}
			if !slices.Equal(envs.calls, []string{tenantZero + "|prod|web"}) {
				t.Errorf("resolver calls = %v", envs.calls)
			}
			if !slices.Equal(res.EnvironmentIDs, envs.ids) || res.Environment != "prod" || res.ProjectID != "web" {
				t.Errorf("result = %+v", res)
			}

			all := q
			res, err = c.Query(context.Background(), tenantZero, all)
			if err != nil {
				t.Fatal(err)
			}
			s = cp.reqs[1].Msg.GetScope()
			if len(s.GetEnvIds()) != 0 || s.ProjectId != nil || len(res.EnvironmentIDs) != 0 || res.Environment != "" {
				t.Errorf("all environments: scope = %v, result = %+v", s, res)
			}
			if len(envs.calls) != 1 {
				t.Errorf("resolver called without an Environment")
			}

			project := q
			project.ProjectID = "web"
			if _, err := c.Query(context.Background(), tenantZero, project); err != nil {
				t.Fatal(err)
			}
			s = cp.reqs[2].Msg.GetScope()
			if len(s.GetEnvIds()) != 0 || s.GetProjectId() != "web" {
				t.Errorf("project only: scope = %v", s)
			}
		})
	}
}

// funnelResp is a funnel answer echoing events.
func funnelResp(events []string) *obsv1.QueryPanelResponse {
	resp := &obsv1.QueryPanelResponse{Kind: "funnel"}
	for _, e := range events {
		resp.FunnelSteps = append(resp.FunnelSteps, &obsv1.FunnelStepResult{Event: e, Count: 10, RateFromFirst: 1})
	}
	return resp
}

func TestEnvironmentFailuresNeverWiden(t *testing.T) {
	q := sqlQueries["event_counts"]
	q.Environment = "prod"
	for name, tc := range map[string]struct {
		envs EnvResolver
		want error
	}{
		"no resolver":      {nil, errEnvFilterUnavailable},
		"no deploy:read":   {&stubEnvs{err: errTokenNeedsDeployRead}, errTokenNeedsDeployRead},
		"unknown name":     {&stubEnvs{err: unknownEnvError("prod", "", []string{"staging"})}, nil},
		"resolves to none": {&stubEnvs{}, errEnvLookupFailed},
		"lookup failure":   {&stubEnvs{err: errEnvLookupFailed}, errEnvLookupFailed},
	} {
		t.Run(name, func(t *testing.T) {
			cp := &fakeCP{resp: kRows(0, 0)}
			_, err := newClient(Deps{CP: cp, Token: token(testToken), Envs: tc.envs}).Query(context.Background(), tenantZero, q)
			if err == nil {
				t.Fatal("no error")
			}
			if tc.want != nil && err != tc.want {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(cp.reqs) != 0 {
				t.Fatal("ran a panel query")
			}
		})
	}
}

// TestEnvErrorsSurviveTheService: the resolver's errors reach the caller of
// gtm.Service.ReadProductAnalytics with their text.
func TestEnvErrorsSurviveTheService(t *testing.T) {
	q := gtm.AnalyticsQuery{Template: gtm.AnalyticsEventCounts, Environment: "production"}
	for name, cp := range map[string]*fakeDeploy{
		"unknown name":   {envs: orgEnvs()},
		"no deploy:read": {err: connect.NewError(connect.CodePermissionDenied, errors.New("denied"))},
	} {
		t.Run(name, func(t *testing.T) {
			envs := NewEnvResolver(EnvResolverDeps{CP: cp, Token: token(testToken), Log: discard()})
			a := newClient(Deps{CP: &fakeCP{}, Token: token(testToken), Envs: envs, Log: discard()})
			_, err := service(a).ReadProductAnalytics(context.Background(), agent, q)
			var ve *gtm.ValidationError
			switch name {
			case "unknown name":
				if !errors.As(err, &ve) || !strings.Contains(err.Error(), "it has: pr-12, prod, staging") {
					t.Fatalf("err = %v", err)
				}
			default:
				if err != errTokenNeedsDeployRead {
					t.Fatalf("err = %v", err)
				}
			}
		})
	}
}

// TestStaleEnvIDIsEvicted: an environment deleted and recreated has a new id;
// the control plane refuses the cached one, the read names the environment,
// the cache entry goes, and the retry resolves the new id.
func TestStaleEnvIDIsEvicted(t *testing.T) {
	for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeInvalidArgument} {
		t.Run(code.String(), func(t *testing.T) {
			deploy := &fakeDeploy{envs: []*cpv1.DeployEnvironment{env("env-old", "prod", "web")}}
			envs := newTestResolver(deploy, &clock{to})
			cp := &fakeCP{resp: kRows(0, 0)}
			c := newClient(Deps{CP: cp, Token: token(testToken), Envs: envs, Log: discard()})
			q := sqlQueries["event_counts"]
			q.Environment, q.ProjectID = "prod", "web"
			ctx := context.Background()

			if _, err := c.Query(ctx, tenantZero, q); err != nil {
				t.Fatal(err)
			}
			// Recreated: the control plane lists a new id and refuses the old.
			deploy.envs = []*cpv1.DeployEnvironment{env("env-new", "prod", "web")}
			cp.err = connect.NewError(code, errors.New("env-old is not a live environment of the org"))
			_, err := c.Query(ctx, tenantZero, q)
			if !IsEnvNotQueryable(err) || !errors.Is(err, gtm.ErrAnalyticsFailed) {
				t.Fatalf("err = %v", err)
			}
			if want := "the product analytics query failed: environment prod could not be queried; it may have been recreated, retry"; err.Error() != want {
				t.Errorf("err = %q, want %q", err.Error(), want)
			}
			if strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "env-old") {
				t.Errorf("err = %q", err)
			}
			// Passes through the Service with its text.
			if _, serr := service(c).ReadProductAnalytics(ctx, agent, q); serr == nil || serr.Error() != err.Error() {
				t.Errorf("through the Service: %v", serr)
			}

			cp.err = nil
			if _, err := c.Query(ctx, tenantZero, q); err != nil {
				t.Fatal(err)
			}
			last := cp.reqs[len(cp.reqs)-1].Msg.GetScope().GetEnvIds()
			if !slices.Equal(last, []string{"env-new"}) {
				t.Fatalf("retry read env ids %v, want the new one", last)
			}
		})
	}
	t.Run("no Environment keeps the credential error", func(t *testing.T) {
		cp := &fakeCP{err: connect.NewError(connect.CodePermissionDenied, errors.New("no"))}
		c := newClient(Deps{CP: cp, Token: token(testToken), Envs: &stubEnvs{}, Log: discard()})
		if _, err := c.Query(context.Background(), tenantZero, sqlQueries["event_counts"]); err != errCredentialRefused {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a resolver without Invalidate", func(t *testing.T) {
		cp := &fakeCP{err: connect.NewError(connect.CodeInvalidArgument, errors.New("no"))}
		c := newClient(Deps{CP: cp, Token: token(testToken), Envs: &stubEnvs{ids: []string{"e1"}}, Log: discard()})
		q := sqlQueries["event_counts"]
		q.Environment = "prod"
		if _, err := c.Query(context.Background(), tenantZero, q); !IsEnvNotQueryable(err) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestResolverCacheIsBounded: the cache never holds more than
// maxEnvCacheEntries; when full it drops expired entries first, and empties
// itself only when none has expired.
func TestResolverCacheIsBounded(t *testing.T) {
	var envs []*cpv1.DeployEnvironment
	for i := range maxEnvCacheEntries + 10 {
		envs = append(envs, env("id-"+strconv.Itoa(i), "n"+strconv.Itoa(i), ""))
	}
	c := &clock{to}
	r := newTestResolver(&fakeDeploy{envs: envs}, c).(*envResolver)
	ctx := context.Background()
	resolve := func(i int) {
		t.Helper()
		if _, err := r.Resolve(ctx, tenantZero, "n"+strconv.Itoa(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	size := func() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.cache) }

	// Half the entries, then let them expire, then the other half.
	for i := range maxEnvCacheEntries / 2 {
		resolve(i)
	}
	c.t = to.Add(DefaultEnvCacheTTL)
	for i := maxEnvCacheEntries / 2; i < maxEnvCacheEntries; i++ {
		resolve(i)
	}
	if got := size(); got != maxEnvCacheEntries {
		t.Fatalf("size = %d, want %d", got, maxEnvCacheEntries)
	}
	// Full: the next store drops the expired half and keeps the live one.
	resolve(maxEnvCacheEntries)
	if got := size(); got != maxEnvCacheEntries/2+1 {
		t.Fatalf("after dropping expired: size = %d, want %d", got, maxEnvCacheEntries/2+1)
	}
	if _, ok := r.cached(envKey{tenantZero, "n" + strconv.Itoa(maxEnvCacheEntries-1), ""}); !ok {
		t.Error("a live entry was dropped")
	}
	// Fill with live entries only: the store past the bound empties the cache.
	for i := 0; size() < maxEnvCacheEntries; i++ {
		resolve(i)
	}
	resolve(maxEnvCacheEntries + 5)
	if got := size(); got != 1 {
		t.Fatalf("after a full live cache: size = %d, want 1", got)
	}
	for i := range maxEnvCacheEntries + 10 {
		resolve(i)
		if got := size(); got > maxEnvCacheEntries {
			t.Fatalf("size %d over the bound", got)
		}
	}
}
