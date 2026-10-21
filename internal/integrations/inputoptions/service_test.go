package inputoptions_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// acme is a made-up integration: nothing here is GitHub's, which is the
// point. Four types cover the declared behaviours: a whole list paged by the
// service, a provider-paged list, a provider-searched one with a lookup, and
// a scoped free-text one.
const acmeManifest = `
id: acme
version: 1
display_name: Acme
icon: acme
category: crm
connection:
  base_url: https://api.acme.example
  auth:
    - api_key: { in: header, name: X-Api-Key, label: Acme key }
types:
  - id: team
    value: { type: string, pattern: "^[a-z]+$" }
    options:
      action: teams.list
      search: client
      value: item.slug
      label: item.name
      description: item.blurb
      group: item.region
  - id: widget
    value: { type: string, pattern: "^w-[0-9]+$" }
    options:
      action: widgets.list
      params:
        team: "{{ has(scope.team) ? dyn(scope.team) : dyn(null) }}"
      search: client
      paging: { next_token_path: next, token_param: cursor }
      value: item.id
      label: item.name
      annotations: { can_edit: item.can_edit, owner: item.owner }
      warnings:
        - when: "context.acting_identity == 'app' && !option.annotations.can_edit"
          message: The bot cannot edit this widget.
      scope:
        team: { type: team }
  - id: person
    value: { type: string, pattern: "^[0-9]+$" }
    options:
      action: people.search
      search: param:q
      value: string(int(item.id))
      label: item.login
      resolve:
        action: people.get
        params: { person_id: "{{ int(value) }}" }
        label: item.login
  - id: tag
    free_text: true
    value: { type: string, minLength: 1 }
    options:
      action: tags.list
      params: { team: "{{ scope.team }}" }
      value: item.name
      scope:
        team: { type: team, required: true }
actions:
  - id: teams.list
    display_name: List teams
    placement: server
    mutates: false
    params:
      type: object
      properties: {}
    request: { method: GET, path: /teams }
    output:
      schema:
        type: object
        properties:
          items:
            type: array
            items:
              type: object
              properties:
                slug: { type: string }
                name: { type: string }
                blurb: { type: string }
                region: { type: string }
  - id: widgets.list
    display_name: List widgets
    placement: server
    mutates: false
    params:
      type: object
      properties:
        team: { type: string }
        cursor: { type: string }
    request: { method: GET, path: /widgets }
    output:
      schema:
        type: object
        properties:
          next: { type: string }
          items:
            type: array
            items:
              type: object
              properties:
                id: { type: string }
                name: { type: string }
                can_edit: { type: boolean }
                owner: { type: string }
  - id: people.search
    display_name: Search people
    placement: server
    mutates: false
    params:
      type: object
      required: [q]
      properties:
        q: { type: string }
    request: { method: GET, path: /people }
    output:
      schema:
        type: object
        properties:
          items:
            type: array
            items:
              type: object
              properties:
                id: { type: integer }
                login: { type: string }
  - id: people.get
    display_name: Get a person
    placement: server
    mutates: false
    params:
      type: object
      required: [person_id]
      properties:
        person_id: { type: integer }
    request: { method: GET, path: "/people/{{ params.person_id }}" }
    output:
      schema:
        type: object
        properties:
          id: { type: integer }
          login: { type: string }
  - id: tags.list
    display_name: List tags
    placement: server
    mutates: false
    params:
      type: object
      required: [team]
      properties:
        team: { type: string }
    request: { method: GET, path: /tags }
    output:
      schema:
        type: object
        properties:
          items:
            type: array
            items:
              type: object
              properties:
                name: { type: string }
  - id: teams.delete
    display_name: Delete a team
    placement: server
    mutates: true
    params: { type: object, properties: {} }
    request: { method: DELETE, path: /teams }
`

func acmeTypes(t *testing.T) *catalogindex.Index {
	t.Helper()
	m, err := manifest.Parse([]byte(acmeManifest), manifest.TrustCurated)
	require.NoError(t, err)
	idx, err := catalogindex.Build([]*reliantv1.IntegrationManifest{m})
	require.NoError(t, err)
	return idx
}

// call is one action call the fake runner saw.
type call struct {
	action string
	params map[string]any
	user   string
}

// fakeRunner answers an action with a canned function and records the call,
// including whose credential it was run with.
type fakeRunner struct {
	mu    sync.Mutex
	calls []call
	reply map[string]func(params map[string]any) (*httpaction.Result, error)
	// gate, when set, blocks a call until it is closed.
	gate chan struct{}
}

func (f *fakeRunner) RunAuthenticated(ctx context.Context, _ *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, _ httpaction.CallSite) (*httpaction.Result, error) {
	if f.gate != nil {
		<-f.gate
	}
	cred, err := src.Credential(ctx, httpaction.CredentialRequest{ActionID: a.GetId(), IntegrationID: "acme", ServerPlaced: true, Params: params, ConnectionID: connParam(params)})
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{action: a.GetId(), params: copyMap(params), user: cred.ConnectionID()})
	h, ok := f.reply[a.GetId()]
	if !ok {
		return nil, fmt.Errorf("no canned reply for %s", a.GetId())
	}
	return h(params)
}

func connParam(p map[string]any) string { s, _ := p[httpaction.ConnectionParam].(string); return s }

func copyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (f *fakeRunner) count(action string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.action == action {
			n++
		}
	}
	return n
}

func (f *fakeRunner) last(action string) call {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].action == action {
			return f.calls[i]
		}
	}
	return call{}
}

func ok(data map[string]any) func(map[string]any) (*httpaction.Result, error) {
	return func(map[string]any) (*httpaction.Result, error) { return &httpaction.Result{Data: data}, nil }
}

// userCreds is a credential source per user: it names the user in the
// credential's connection id so a test can see whose credential ran a call, and
// refuses a user with none.
type userCreds struct{ without map[string]bool }

func (u userCreds) ForUser(userID string) httpaction.CredentialSource {
	return userSrc{id: userID, none: u.without[userID]}
}

type userSrc struct {
	id   string
	none bool
}

type cred struct{ id string }

func (c cred) ConnectionID() string    { return c.id }
func (cred) Apply(*http.Request) error { return nil }
func (cred) Scrub(s string) string     { return s }
func (cred) Params() map[string]string { return nil }

func (s userSrc) Credential(_ context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	if s.none {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "no acme connection"}
	}
	if req.ConnectionID != "" && req.ConnectionID != "conn-"+s.id {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "no such connection"}
	}
	return cred{id: "user:" + s.id}, nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	svc    inputoptions.Service
	runner *fakeRunner
	clock  *clock
}

func newFixture(t *testing.T, ttl time.Duration, without ...string) *fixture {
	t.Helper()
	f := &fixture{runner: &fakeRunner{reply: map[string]func(map[string]any) (*httpaction.Result, error){}}, clock: &clock{t: time.Unix(1_700_000_000, 0)}}
	w := map[string]bool{}
	for _, u := range without {
		w[u] = true
	}
	svc, err := inputoptions.New(inputoptions.Deps{Types: acmeTypes(t), Runner: f.runner, Credentials: userCreds{without: w}, TTL: ttl, Now: f.clock.now})
	require.NoError(t, err)
	f.svc = svc
	return f
}

func teams(n int) map[string]any {
	items := make([]any, n)
	for i := range items {
		items[i] = map[string]any{"slug": fmt.Sprintf("t%c", 'a'+i), "name": fmt.Sprintf("Team %d", i), "blurb": "b", "region": "eu"}
	}
	return map[string]any{"items": items}
}

func values(opts []inputoptions.Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Value)
	}
	return out
}

func TestNewRequiresItsCollaborators(t *testing.T) {
	_, err := inputoptions.New(inputoptions.Deps{})
	require.Error(t, err)
}

func TestListProjectsValueLabelDescriptionGroup(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = ok(map[string]any{"items": []any{
		map[string]any{"slug": "alpha", "name": "Alpha", "blurb": "the first", "region": "eu"},
		map[string]any{"slug": "beta", "name": "Beta", "blurb": "the second", "region": "us"},
	}})
	res, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateConnected, res.State)
	assert.Equal(t, []inputoptions.Option{
		{Value: "alpha", Label: "Alpha", Description: "the first", Group: "eu"},
		{Value: "beta", Label: "Beta", Description: "the second", Group: "us"},
	}, res.Options)
	assert.Empty(t, res.NextPageToken)
}

// The credential is the CALLER's: the run is made with the user's source and
// nothing the request body says can change whose it is.
func TestListRunsAsTheCaller(t *testing.T) {
	f := newFixture(t, -1)
	f.runner.reply["teams.list"] = ok(teams(1))
	_, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Equal(t, "user:alice", f.runner.last("teams.list").user)
	_, err = f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "bob", Type: "acme.team"})
	require.NoError(t, err)
	assert.Equal(t, "user:bob", f.runner.last("teams.list").user)
}

func TestListNamedConnectionIsPassedToTheAction(t *testing.T) {
	f := newFixture(t, -1)
	f.runner.reply["teams.list"] = ok(teams(1))
	_, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team", ConnectionID: "conn-alice"})
	require.NoError(t, err)
	assert.Equal(t, "conn-alice", f.runner.last("teams.list").params[httpaction.ConnectionParam])
}

// Another user's connection id reads exactly like one that does not exist.
func TestListAnotherUsersConnectionIsNotFound(t *testing.T) {
	f := newFixture(t, -1)
	f.runner.reply["teams.list"] = ok(teams(1))
	_, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "bob", Type: "acme.team", ConnectionID: "conn-alice"})
	require.ErrorIs(t, err, inputoptions.ErrConnectionNotFound)
	_, err = f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "bob", Type: "acme.team", ConnectionID: "conn-nobody"})
	require.ErrorIs(t, err, inputoptions.ErrConnectionNotFound)
	assert.Zero(t, f.runner.count("teams.list"), "no listing was made")
}

func TestListNeedsConnectionWhenThereIsNoCredential(t *testing.T) {
	f := newFixture(t, time.Minute, "carol")
	res, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "carol", Type: "acme.team"})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateNeedsConnection, res.State)
	assert.Empty(t, res.Options)
	assert.Zero(t, f.runner.count("teams.list"))
}

func TestListNeedsConnectionWhenTheConnectionNeedsReauth(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = func(map[string]any) (*httpaction.Result, error) {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeNeedsReauth, Message: "reconnect"}
	}
	res, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateNeedsConnection, res.State)
}

func TestListUnknownAndOptionlessTypes(t *testing.T) {
	f := newFixture(t, time.Minute)
	_, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.nothing"})
	require.ErrorIs(t, err, inputoptions.ErrUnknownType)
	_, err = f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: ""})
	require.ErrorIs(t, err, inputoptions.ErrBadRequest)
	_, err = f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "", Type: "acme.team"})
	require.ErrorIs(t, err, inputoptions.ErrBadRequest)
}

// An upstream failure is an error to the caller, with the provider's own
// retryability, and nothing is cached.
func TestListUpstreamFailureIsTypedAndNotCached(t *testing.T) {
	f := newFixture(t, time.Minute)
	fail := true
	f.runner.reply["teams.list"] = func(map[string]any) (*httpaction.Result, error) {
		if fail {
			return &httpaction.Result{IsError: true, Retryable: true, StatusCode: 502, Content: "acme is down"}, nil
		}
		return &httpaction.Result{Data: teams(1)}, nil
	}
	_, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	var ue *inputoptions.UpstreamError
	require.ErrorAs(t, err, &ue)
	assert.True(t, ue.Retryable)
	assert.Equal(t, "acme is down", ue.Message)

	fail = false
	res, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Len(t, res.Options, 1, "the failure was not remembered")
}

// --- paging -------------------------------------------------------------------

func TestListPagesAWholeListWithAnOffsetToken(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = ok(teams(5))
	ctx := context.Background()

	p1, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team", PageSize: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"ta", "tb"}, values(p1.Options))
	require.NotEmpty(t, p1.NextPageToken)

	p2, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team", PageSize: 2, PageToken: p1.NextPageToken})
	require.NoError(t, err)
	assert.Equal(t, []string{"tc", "td"}, values(p2.Options))

	p3, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team", PageSize: 2, PageToken: p2.NextPageToken})
	require.NoError(t, err)
	assert.Equal(t, []string{"te"}, values(p3.Options))
	assert.Empty(t, p3.NextPageToken, "the last page has no token")

	assert.Equal(t, 1, f.runner.count("teams.list"), "all three pages came from one upstream call")
}

func TestListPageSizeDefaultsAndIsCapped(t *testing.T) {
	f := newFixture(t, time.Minute)
	items := make([]any, 300)
	for i := range items {
		items[i] = map[string]any{"slug": fmt.Sprintf("s%d", i), "name": "n"}
	}
	f.runner.reply["teams.list"] = ok(map[string]any{"items": items})
	ctx := context.Background()

	res, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Len(t, res.Options, inputoptions.DefaultPageSize)

	res, err = f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team", PageSize: 10_000})
	require.NoError(t, err)
	assert.Len(t, res.Options, inputoptions.MaxPageSize)

	_, err = f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team", PageSize: -1})
	require.ErrorIs(t, err, inputoptions.ErrBadRequest)
}

// A token only works for the listing it was issued for.
func TestListPageTokenIsBoundToItsListing(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = ok(teams(5))
	f.runner.reply["widgets.list"] = ok(map[string]any{"items": []any{}})
	ctx := context.Background()
	p1, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team", PageSize: 2, Query: "team"})
	require.NoError(t, err)
	require.NotEmpty(t, p1.NextPageToken)

	for name, req := range map[string]inputoptions.ListRequest{
		"another query":      {UserID: "alice", Type: "acme.team", PageSize: 2, Query: "other", PageToken: p1.NextPageToken},
		"another type":       {UserID: "alice", Type: "acme.widget", PageSize: 2, Query: "team", PageToken: p1.NextPageToken},
		"another connection": {UserID: "alice", Type: "acme.team", PageSize: 2, Query: "team", ConnectionID: "conn-alice", PageToken: p1.NextPageToken},
		"garbage":            {UserID: "alice", Type: "acme.team", PageToken: "!!!not-a-token"},
		"valid base64":       {UserID: "alice", Type: "acme.team", PageToken: "aGVsbG8"},
	} {
		_, err := f.svc.List(ctx, req)
		assert.ErrorIs(t, err, inputoptions.ErrBadPageToken, name)
	}
}

// A provider that pages itself returns its page and hands on its cursor, and
// the next request carries that cursor to the action.
func TestListProviderPagedTypeCarriesTheCursor(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["widgets.list"] = func(p map[string]any) (*httpaction.Result, error) {
		if p["cursor"] == "c2" {
			return &httpaction.Result{Data: map[string]any{"items": []any{map[string]any{"id": "w-3", "name": "Three"}}}}, nil
		}
		return &httpaction.Result{Data: map[string]any{"next": "c2", "items": []any{
			map[string]any{"id": "w-1", "name": "One"}, map[string]any{"id": "w-2", "name": "Two"}}}}, nil
	}
	ctx := context.Background()
	p1, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.widget"})
	require.NoError(t, err)
	assert.Equal(t, []string{"w-1", "w-2"}, values(p1.Options))
	require.NotEmpty(t, p1.NextPageToken)
	assert.NotContains(t, f.runner.last("widgets.list").params, "cursor", "the first page asks for no cursor")

	p2, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.widget", PageToken: p1.NextPageToken})
	require.NoError(t, err)
	assert.Equal(t, []string{"w-3"}, values(p2.Options))
	assert.Empty(t, p2.NextPageToken)
	assert.Equal(t, "c2", f.runner.last("widgets.list").params["cursor"])
}

// --- search -------------------------------------------------------------------

func TestClientSearchFiltersTheProjectedOptionsAndReusesTheCall(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = ok(map[string]any{"items": []any{
		map[string]any{"slug": "alpha", "name": "Alpha Squad", "blurb": "ships", "region": "eu"},
		map[string]any{"slug": "beta", "name": "Beta", "blurb": "alphabet", "region": "us"},
		map[string]any{"slug": "gamma", "name": "Gamma", "blurb": "x", "region": "us"},
	}})
	ctx := context.Background()
	res, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team", Query: "ALPHA"})
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, values(res.Options), "label, description and value match, case-insensitively")

	res, err = f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team", Query: "gam"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gamma"}, values(res.Options))
	assert.Equal(t, 1, f.runner.count("teams.list"), "every keystroke is served from the one listing")
}

func TestParamSearchSendsTheQueryAndListsNothingWithout(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["people.search"] = func(p map[string]any) (*httpaction.Result, error) {
		return &httpaction.Result{Data: map[string]any{"items": []any{map[string]any{"id": 583231.0, "login": "octo-" + p["q"].(string)}}}}, nil
	}
	ctx := context.Background()

	res, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.person"})
	require.NoError(t, err)
	assert.Empty(t, res.Options)
	assert.Equal(t, inputoptions.StateUnknown, res.State, "nothing was asked, so nothing is known")
	assert.Zero(t, f.runner.count("people.search"))

	res, err = f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.person", Query: "  cat "})
	require.NoError(t, err)
	assert.Equal(t, "cat", f.runner.last("people.search").params["q"], "trimmed, and sent as the param the type names")
	assert.Equal(t, []inputoptions.Option{{Value: "583231", Label: "octo-cat"}}, res.Options, "the stored value is the id")
}

// A provider-side search is a different call for each query, so each is
// cached on its own.
func TestParamSearchCachesPerQuery(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["people.search"] = ok(map[string]any{"items": []any{}})
	ctx := context.Background()
	for _, q := range []string{"a", "a", "b", "a"} {
		_, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.person", Query: q})
		require.NoError(t, err)
	}
	assert.Equal(t, 2, f.runner.count("people.search"))
}

// --- scope --------------------------------------------------------------------

func TestScopeRendersParamsAndAnUnsetOptionalScopeLeavesTheParamOut(t *testing.T) {
	f := newFixture(t, -1)
	f.runner.reply["widgets.list"] = ok(map[string]any{"items": []any{}})
	ctx := context.Background()

	_, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.widget", Scope: map[string]any{"team": "core"}})
	require.NoError(t, err)
	assert.Equal(t, "core", f.runner.last("widgets.list").params["team"])

	_, err = f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.widget", Scope: map[string]any{"team": ""}})
	require.NoError(t, err)
	assert.NotContains(t, f.runner.last("widgets.list").params, "team")

	_, err = f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.widget"})
	require.NoError(t, err)
	assert.NotContains(t, f.runner.last("widgets.list").params, "team")
}

func TestScopeRequiredAndValidated(t *testing.T) {
	f := newFixture(t, -1)
	f.runner.reply["tags.list"] = ok(map[string]any{"items": []any{map[string]any{"name": "bug"}}})
	ctx := context.Background()

	_, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.tag"})
	require.ErrorIs(t, err, inputoptions.ErrBadScope)
	assert.Contains(t, inputoptions.Message(err), "team is required")

	_, err = f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.tag", Scope: map[string]any{"team": "NOT valid!"}})
	require.ErrorIs(t, err, inputoptions.ErrBadScope, "a scope value must be a valid value of its own type")

	_, err = f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.tag", Scope: map[string]any{"team": 7}})
	require.ErrorIs(t, err, inputoptions.ErrBadScope)

	res, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.tag", Scope: map[string]any{"team": "core", "extra": "ignored"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"bug"}, values(res.Options))
	assert.NotContains(t, f.runner.last("tags.list").params, "extra", "a key the type does not declare never reaches the action")
}

// --- warnings and annotations -------------------------------------------------

func TestWarningsFollowTheActingIdentity(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["widgets.list"] = ok(map[string]any{"items": []any{
		map[string]any{"id": "w-1", "name": "Editable", "can_edit": true, "owner": "me"},
		map[string]any{"id": "w-2", "name": "Locked", "can_edit": false, "owner": "them"},
	}})
	ctx := context.Background()

	owner, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.widget"})
	require.NoError(t, err)
	assert.Empty(t, owner.Options[0].Warnings)
	assert.Empty(t, owner.Options[1].Warnings, "listing is the owner's own read: no warning")

	app, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.widget", ActingIdentity: "app"})
	require.NoError(t, err)
	assert.Empty(t, app.Options[0].Warnings)
	assert.Equal(t, []string{"The bot cannot edit this widget."}, app.Options[1].Warnings)
	assert.Equal(t, map[string]any{"can_edit": false, "owner": "them"}, app.Options[1].Annotations)
	assert.Equal(t, 1, f.runner.count("widgets.list"), "the identity does not change the call, so it does not bust the cache")
}

// One odd row must not empty the picker, and a repeated value is offered once.
func TestAnItemWithoutAValueIsSkippedAndDuplicatesCollapse(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = ok(map[string]any{"items": []any{
		map[string]any{"slug": "alpha", "name": "A"},
		map[string]any{"name": "no slug"},
		map[string]any{"slug": "alpha", "name": "again"},
		map[string]any{"slug": "beta", "name": "B"},
	}})
	res, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, values(res.Options))
	assert.Equal(t, "A", res.Options[0].Label, "the first wins")
}

// --- cache --------------------------------------------------------------------

func TestCacheIsPerUserConnectionTypeAndScope(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["widgets.list"] = ok(map[string]any{"items": []any{}})
	f.runner.reply["teams.list"] = ok(teams(1))
	ctx := context.Background()
	list := func(r inputoptions.ListRequest) {
		t.Helper()
		_, err := f.svc.List(ctx, r)
		require.NoError(t, err)
	}
	base := inputoptions.ListRequest{UserID: "alice", Type: "acme.widget", Scope: map[string]any{"team": "core"}}

	list(base)
	list(base)
	assert.Equal(t, 1, f.runner.count("widgets.list"), "an identical request is served from the cache")

	other := base
	other.UserID = "bob"
	list(other)
	assert.Equal(t, 2, f.runner.count("widgets.list"), "another user never reads alice's listing")
	assert.Equal(t, "user:bob", f.runner.last("widgets.list").user)

	conn := base
	conn.ConnectionID = "conn-alice"
	list(conn)
	assert.Equal(t, 3, f.runner.count("widgets.list"), "another connection is another listing")

	scoped := base
	scoped.Scope = map[string]any{"team": "ops"}
	list(scoped)
	assert.Equal(t, 4, f.runner.count("widgets.list"), "another scope is another listing")

	list(base)
	assert.Equal(t, 4, f.runner.count("widgets.list"), "the original is still cached")
}

func TestCacheExpires(t *testing.T) {
	f := newFixture(t, 30*time.Second)
	f.runner.reply["teams.list"] = ok(teams(1))
	ctx := context.Background()
	req := inputoptions.ListRequest{UserID: "alice", Type: "acme.team"}
	_, _ = f.svc.List(ctx, req)
	f.clock.advance(29 * time.Second)
	_, _ = f.svc.List(ctx, req)
	assert.Equal(t, 1, f.runner.count("teams.list"))
	f.clock.advance(2 * time.Second)
	_, _ = f.svc.List(ctx, req)
	assert.Equal(t, 2, f.runner.count("teams.list"), "a listing older than the TTL is read again")
}

func TestNegativeTTLDisablesTheCache(t *testing.T) {
	f := newFixture(t, -1)
	f.runner.reply["teams.list"] = ok(teams(1))
	for range 3 {
		_, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
		require.NoError(t, err)
	}
	assert.Equal(t, 3, f.runner.count("teams.list"))
}

// A user with no credential is asked again once they connect: nothing about
// "not connected" is remembered.
func TestNeedsConnectionIsNotCached(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = func(map[string]any) (*httpaction.Result, error) {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "no acme connection"}
	}
	r1, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateNeedsConnection, r1.State)

	f.runner.reply["teams.list"] = ok(teams(1))
	r2, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateConnected, r2.State)
	assert.Len(t, r2.Options, 1)
}

// Concurrent identical listings share one upstream call.
func TestConcurrentIdenticalListingsShareOneCall(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.gate = make(chan struct{})
	f.runner.reply["teams.list"] = ok(teams(2))
	var wg sync.WaitGroup
	results := make([]*inputoptions.ListResult, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := f.svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
			assert.NoError(t, err)
			results[i] = res
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(f.runner.gate)
	wg.Wait()
	assert.Equal(t, 1, f.runner.count("teams.list"))
	for _, r := range results {
		require.NotNil(t, r)
		assert.Len(t, r.Options, 2)
	}
}

func TestACancelledCallerReturnsWithoutWaiting(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.gate = make(chan struct{})
	t.Cleanup(func() { close(f.runner.gate) })
	f.runner.reply["teams.list"] = ok(teams(1))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.List(ctx, inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled request kept waiting on the provider")
	}
}

// --- resolve ------------------------------------------------------------------

func TestResolveByLookupLabelsAndMarksGoneValuesUnreachable(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["people.get"] = func(p map[string]any) (*httpaction.Result, error) {
		if p["person_id"] == 583231 || p["person_id"] == int64(583231) || p["person_id"] == 583231.0 {
			return &httpaction.Result{Data: map[string]any{"id": 583231.0, "login": "octocat"}}, nil
		}
		return &httpaction.Result{IsError: true, StatusCode: 404, Content: "not found"}, nil
	}
	res, err := f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{
		UserID: "alice", Type: "acme.person", Values: []string{"583231", "999", "583231", "not-an-id"},
	})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateConnected, res.State)
	assert.Equal(t, []inputoptions.Option{{Value: "583231", Label: "octocat"}}, res.Options)
	assert.ElementsMatch(t, []string{"999", "not-an-id"}, res.Unreachable)
	assert.Equal(t, 2, f.runner.count("people.get"), "a value the type's schema refuses is not looked up, and a repeat is looked up once")
}

// A failure that is not a definite "gone" is an error, never an unreachable
// value.
func TestResolveByLookupUpstreamErrorIsNotUnreachable(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["people.get"] = func(map[string]any) (*httpaction.Result, error) {
		return &httpaction.Result{IsError: true, Retryable: true, StatusCode: 429, Content: "rate limited"}, nil
	}
	_, err := f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{UserID: "alice", Type: "acme.person", Values: []string{"1"}})
	var ue *inputoptions.UpstreamError
	require.ErrorAs(t, err, &ue)
	assert.True(t, ue.Retryable)
}

func TestResolveFromTheListing(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = ok(map[string]any{"items": []any{
		map[string]any{"slug": "alpha", "name": "Alpha"}, map[string]any{"slug": "beta", "name": "Beta"},
	}})
	res, err := f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{UserID: "alice", Type: "acme.team", Values: []string{"beta", "gone"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"beta"}, values(res.Options))
	assert.Equal(t, []string{"gone"}, res.Unreachable, "the whole list was read and it is not in it")
}

// A list the provider paged is read to its end before anything is called gone.
func TestResolveFromAProviderPagedListingReadsEveryPage(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["widgets.list"] = func(p map[string]any) (*httpaction.Result, error) {
		if p["cursor"] == "c2" {
			return &httpaction.Result{Data: map[string]any{"items": []any{map[string]any{"id": "w-9", "name": "Nine"}}}}, nil
		}
		return &httpaction.Result{Data: map[string]any{"next": "c2", "items": []any{map[string]any{"id": "w-1", "name": "One"}}}}, nil
	}
	res, err := f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{UserID: "alice", Type: "acme.widget", Values: []string{"w-9", "w-5"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"w-9"}, values(res.Options))
	assert.Equal(t, []string{"w-5"}, res.Unreachable)
}

// A listing cut short at the action's page limit proves nothing about a value
// it does not contain.
func TestResolveFromATruncatedListingConfirmsNothingGone(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["teams.list"] = func(map[string]any) (*httpaction.Result, error) {
		return &httpaction.Result{Truncated: true, Data: map[string]any{"items": []any{map[string]any{"slug": "alpha", "name": "Alpha"}}}}, nil
	}
	res, err := f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{UserID: "alice", Type: "acme.team", Values: []string{"alpha", "zulu"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha"}, values(res.Options))
	assert.Empty(t, res.Unreachable)
	assert.Equal(t, []string{"zulu"}, res.Unlisted)
}

// A free-text value that is not among the options is legal.
func TestResolveFreeTextValueIsNeverUnreachable(t *testing.T) {
	f := newFixture(t, time.Minute)
	f.runner.reply["tags.list"] = ok(map[string]any{"items": []any{map[string]any{"name": "bug"}}})
	res, err := f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{
		UserID: "alice", Type: "acme.tag", Scope: map[string]any{"team": "core"}, Values: []string{"bug", "brand-new"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"bug"}, values(res.Options))
	assert.Empty(t, res.Unreachable)
	assert.Equal(t, []string{"brand-new"}, res.Unlisted)
}

// A type that can only be searched has nothing to read end to end.
func TestResolveWithoutALookupOnASearchOnlyTypeConfirmsNothing(t *testing.T) {
	m, err := manifest.Parse([]byte(searchOnlyManifest), manifest.TrustCurated)
	require.NoError(t, err)
	idx, err := catalogindex.Build([]*reliantv1.IntegrationManifest{m})
	require.NoError(t, err)
	f := &fakeRunner{reply: map[string]func(map[string]any) (*httpaction.Result, error){}}
	svc, err := inputoptions.New(inputoptions.Deps{Types: idx, Runner: f, Credentials: userCreds{}})
	require.NoError(t, err)

	res, err := svc.Resolve(context.Background(), inputoptions.ResolveRequest{UserID: "alice", Type: "so.thing", Values: []string{"a"}})
	require.NoError(t, err)
	assert.Empty(t, res.Unreachable)
	assert.Equal(t, []string{"a"}, res.Unlisted)
	assert.Zero(t, f.count("things.search"))
}

func TestResolveNeedsConnectionAndLimits(t *testing.T) {
	f := newFixture(t, time.Minute, "carol")
	res, err := f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{UserID: "carol", Type: "acme.team", Values: []string{"alpha"}})
	require.NoError(t, err)
	assert.Equal(t, inputoptions.StateNeedsConnection, res.State)

	empty, err := f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{UserID: "alice", Type: "acme.team"})
	require.NoError(t, err)
	assert.Empty(t, empty.Options)

	many := make([]string, inputoptions.MaxResolveValues+1)
	for i := range many {
		many[i] = fmt.Sprintf("v%d", i)
	}
	_, err = f.svc.Resolve(context.Background(), inputoptions.ResolveRequest{UserID: "alice", Type: "acme.team", Values: many})
	require.ErrorIs(t, err, inputoptions.ErrBadRequest)
}

// Reading the type's declaration at call time refuses a list action that
// mutates, whatever the loader let through.
func TestAMutatingListActionIsRefusedAtCallTime(t *testing.T) {
	m, err := manifest.Parse([]byte(acmeManifest), manifest.TrustCurated)
	require.NoError(t, err)
	for _, a := range m.GetActions() {
		if a.GetId() == "teams.list" {
			a.Mutates = true
		}
	}
	idx, err := catalogindex.Build([]*reliantv1.IntegrationManifest{m})
	require.NoError(t, err)
	f := &fakeRunner{reply: map[string]func(map[string]any) (*httpaction.Result, error){}}
	svc, err := inputoptions.New(inputoptions.Deps{Types: idx, Runner: f, Credentials: userCreds{}})
	require.NoError(t, err)

	_, err = svc.List(context.Background(), inputoptions.ListRequest{UserID: "alice", Type: "acme.team"})
	require.ErrorIs(t, err, inputoptions.ErrInvalidType)
	assert.Zero(t, f.count("teams.list"), "nothing was run")
}

func TestUpstreamErrorMessageIsTheErrorString(t *testing.T) {
	err := error(&inputoptions.UpstreamError{Message: "boom"})
	assert.Equal(t, "boom", err.Error())
	assert.Equal(t, "boom", inputoptions.Message(err))
	assert.Equal(t, "plain", inputoptions.Message(errors.New("plain")))
}

const searchOnlyManifest = `
id: so
version: 1
display_name: SO
icon: so
category: crm
connection:
  base_url: https://api.so.example
  auth:
    - api_key: { in: header, name: X-Api-Key, label: Key }
types:
  - id: thing
    value: { type: string }
    options:
      action: things.search
      search: param:q
      value: item.id
actions:
  - id: things.search
    display_name: Search
    placement: server
    mutates: false
    params:
      type: object
      required: [q]
      properties:
        q: { type: string }
    request: { method: GET, path: /things }
    output:
      schema:
        type: object
        properties:
          items:
            type: array
            items:
              type: object
              properties:
                id: { type: string }
`
