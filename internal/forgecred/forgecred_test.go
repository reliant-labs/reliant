// Copyright (c) 2025 Reliant Labs
package forgecred

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// fakeExchanger answers per server and counts calls.
type fakeExchanger struct {
	now    func() time.Time
	ttl    time.Duration
	refuse map[string]error
	// grant, when set, is the most the server hands out (it omits the rest).
	grant   []string
	calls   []string
	minted  int
	lastAud string
	orgs    []string
}

func (f *fakeExchanger) Exchange(_ context.Context, server, bearer, audience string, scopes []string, orgID string) (Exchanged, error) {
	f.calls = append(f.calls, server)
	f.orgs = append(f.orgs, orgID)
	f.lastAud = audience
	if err := f.refuse[server]; err != nil {
		return Exchanged{}, err
	}
	f.minted++
	return Exchanged{
		Token:     "rlat_exchanged_" + strings.Repeat("x", f.minted) + "_from_" + bearer[len(bearer)-4:],
		ExpiresAt: f.now().Add(f.ttl),
		Scopes:    f.narrow(scopes),
	}, nil
}

func (f *fakeExchanger) narrow(asked []string) []string {
	if f.grant == nil {
		return asked
	}
	var out []string
	for _, s := range asked {
		if slices.Contains(f.grant, s) {
			out = append(out, s)
		}
	}
	return out
}

func newTestHelper(t *testing.T, sessions []Session, ex *fakeExchanger, clock *time.Time) Service {
	t.Helper()
	return newOrgHelper(t, sessions, ex, clock, "")
}

func newOrgHelper(t *testing.T, sessions []Session, ex *fakeExchanger, clock *time.Time, org string) Service {
	t.Helper()
	ex.now = func() time.Time { return *clock }
	return New(Deps{
		Org:       org,
		Sessions:  func() ([]Session, error) { return sessions, nil },
		Exchanger: ex,
		CachePath: filepath.Join(t.TempDir(), "forge-token-cache.json"),
		Now:       func() time.Time { return *clock },
	})
}

var req = cloudcred.Request{Endpoint: "https://ADMIN.example.com/", Scopes: []string{"deploy:write"}}

func TestMint_ReusesForHalfTheLifeThenRemints(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	ex := &fakeExchanger{ttl: time.Hour}
	h := newTestHelper(t, []Session{{Server: "https://api.example.com", Token: "rlat_session_AAAA", Kind: "daemon"}}, ex, &clock)

	first, err := h.Mint(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if ex.lastAud != "https://admin.example.com" {
		t.Errorf("audience = %q, want the normalized endpoint", ex.lastAud)
	}
	clock = clock.Add(29 * time.Minute) // 31 minutes left
	second, err := h.Mint(context.Background(), req)
	if err != nil || second.Token != first.Token || ex.minted != 1 {
		t.Fatalf("a token with 31m left must be reused: %v minted=%d", err, ex.minted)
	}
	clock = clock.Add(2 * time.Minute) // 29 minutes left
	third, err := h.Mint(context.Background(), req)
	if err != nil || third.Token == first.Token || ex.minted != 2 {
		t.Fatalf("a token with 29m left must be replaced: %v minted=%d", err, ex.minted)
	}
}

func TestMint_CacheIsKeyedBySession(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	ex := &fakeExchanger{ttl: time.Hour}
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	mk := func(token string) Service {
		ex.now = func() time.Time { return clock }
		return New(Deps{
			Sessions: func() ([]Session, error) {
				return []Session{{Server: "https://api.example.com", Token: token, Kind: "cli"}}, nil
			},
			Exchanger: ex, CachePath: cachePath, Now: func() time.Time { return clock },
		})
	}
	a, err := mk("rlat_alice_AAAA").Mint(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// A re-sign-in as someone else must not inherit the previous token.
	b, err := mk("rlat_bob_BBBB").Mint(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.Token == b.Token || ex.minted != 2 {
		t.Fatalf("a different session reused another's token (minted=%d)", ex.minted)
	}
	raw, _ := os.ReadFile(cachePath)
	for _, secret := range []string{"rlat_alice_AAAA", "rlat_bob_BBBB"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the cache stored a session credential (%s)", secret)
		}
	}
}

func TestMint_TriesSessionsInOrderAndClassifies(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	sessions := []Session{
		{Server: "https://api.other.com", Token: "rlat_other_OOOO", Kind: "daemon"},
		{Server: "https://api.narrow.com", Token: "rlat_narrow_NNNN", Kind: "cli"},
		{Server: "https://api.good.com", Token: "rlat_good_GGGG", Kind: "daemon", Account: "acct-2"},
	}
	ex := &fakeExchanger{ttl: time.Hour, refuse: map[string]error{
		"https://api.other.com":  connect.NewError(connect.CodeFailedPrecondition, errors.New("tokens from this server are valid at https://admin.other.com")),
		"https://api.narrow.com": connect.NewError(connect.CodePermissionDenied, errors.New("cannot authorize deploy:write")),
	}}
	h := newTestHelper(t, sessions, ex, &clock)
	tok, err := h.Mint(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(tok.Token, "GGGG") || tok.Source != "Reliant session (daemon at https://api.good.com, account acct-2)" {
		t.Fatalf("got %+v, want the first session that could answer", tok)
	}

	// Every session refuses: the most actionable code wins, every reason shown.
	ex.refuse["https://api.good.com"] = errors.New("dial tcp: connection refused")
	clock = clock.Add(2 * time.Hour) // past the cache
	_, err = h.Mint(context.Background(), req)
	var he *cloudcred.HelperError
	if !errors.As(err, &he) || he.Code != cloudcred.CodeDenied {
		t.Fatalf("err = %v, want a denial (a session exists and can be fixed)", err)
	}
	for _, want := range []string{"admin.other.com", "cannot authorize deploy:write", "connection refused"} {
		if !strings.Contains(he.Message, want) {
			t.Errorf("message missing %q:\n%s", want, he.Message)
		}
	}
}

func TestMint_OldServerIsNamedAsSuch(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	ex := &fakeExchanger{ttl: time.Hour, refuse: map[string]error{
		"https://api.example.com": connect.NewError(connect.CodeUnimplemented, errors.New("404 page not found")),
	}}
	h := newTestHelper(t, []Session{{Server: "https://api.example.com", Token: "rlat_s_SSSS", Kind: "cli"}}, ex, &clock)
	_, err := h.Mint(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "predates credential exchange") {
		t.Fatalf("err = %v, want the server named as too old", err)
	}
}

func TestForgetServer(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	ex := &fakeExchanger{ttl: time.Hour}
	h := newTestHelper(t, []Session{{Server: "https://api.example.com", Token: "rlat_s_SSSS", Kind: "cli"}}, ex, &clock)
	if _, err := h.Mint(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := h.ForgetServer("https://API.example.com/"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Mint(context.Background(), req); err != nil || ex.minted != 2 {
		t.Fatalf("after ForgetServer the next request must mint again (minted=%d, %v)", ex.minted, err)
	}
}

func scopeRefusal(scopes string) error {
	ce := connect.NewError(connect.CodePermissionDenied, errors.New("this Reliant credential cannot authorize deploy:write"))
	ce.Meta().Set(svcerr.ReasonHeader, fat.ReasonScopeMissing)
	ce.Meta().Set(fat.ScopesHeader, scopes)
	return ce
}

// A denial the server explained as scope_missing reaches forge in structure —
// HelperError.Reason and .Scopes, which forge reports as error_reason /
// error_scopes — so the deploy panel can name the grant. The message is the
// same one forge has always printed.
func TestMint_DenialCarriesTheServersScopeMissing(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	sessions := []Session{
		{Server: "https://api.a.com", Token: "rlat_a_AAAA", Kind: "daemon"},
		{Server: "https://api.b.com", Token: "rlat_b_BBBB", Kind: "cli"},
		{Server: "https://api.other.com", Token: "rlat_o_OOOO", Kind: "cli"},
	}
	ex := &fakeExchanger{ttl: time.Hour, refuse: map[string]error{
		"https://api.a.com":     scopeRefusal("deploy:write"),
		"https://api.b.com":     scopeRefusal("deploy:write,secret:read"),
		"https://api.other.com": connect.NewError(connect.CodeFailedPrecondition, errors.New("not this control plane")),
	}}
	h := newTestHelper(t, sessions, ex, &clock)
	_, err := h.Mint(context.Background(), req)
	var he *cloudcred.HelperError
	if !errors.As(err, &he) || he.Code != cloudcred.CodeDenied {
		t.Fatalf("err = %v, want a denial", err)
	}
	if he.Reason != fat.ReasonScopeMissing || strings.Join(he.Scopes, ",") != "deploy:write,secret:read" {
		t.Fatalf("reason=%q scopes=%v, want scope_missing with the union of the scopes", he.Reason, he.Scopes)
	}
	want := "you are signed in to Reliant, but no session on this machine can authorize https://admin.example.com:" +
		"\n  - Reliant session (daemon at https://api.a.com): this Reliant credential cannot authorize deploy:write" +
		"\n  - Reliant session (CLI login at https://api.b.com): this Reliant credential cannot authorize deploy:write" +
		"\n  - Reliant session (CLI login at https://api.other.com): not this control plane"
	if he.Message != want {
		t.Errorf("message changed:\n got %q\nwant %q", he.Message, want)
	}

	// One denial for another reason makes "ask for a grant" a guess: no reason.
	ex.refuse["https://api.b.com"] = connect.NewError(connect.CodeUnauthenticated, errors.New("session expired"))
	clock = clock.Add(2 * time.Hour)
	_, err = h.Mint(context.Background(), req)
	if !errors.As(err, &he) || he.Reason != "" || he.Scopes != nil {
		t.Fatalf("err = %+v, want a denial with no structured reason", he)
	}
}

func TestMint_RequestedScopesPassThroughAndKeyTheCache(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	ex := &fakeExchanger{ttl: time.Hour}
	h := newTestHelper(t, []Session{{Server: "https://api.example.com", Token: "rlat_session_AAAA", Kind: "daemon"}}, ex, &clock)
	ask := func(scopes ...string) cloudcred.Token {
		t.Helper()
		tok, err := h.Mint(context.Background(), cloudcred.Request{Endpoint: req.Endpoint, Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	deploy := ask("deploy:write")
	obs := ask("obs:read", "db:query")
	if strings.Join(obs.Scopes, ",") != "obs:read,db:query" {
		t.Fatalf("requested scopes must reach the exchange unchanged, got %v", obs.Scopes)
	}
	if obs.Token == deploy.Token || ex.minted != 2 {
		t.Fatalf("a deploy token answered an obs request (minted=%d)", ex.minted)
	}
	if again := ask("db:query", "obs:read"); again.Token != obs.Token || ex.minted != 2 {
		t.Fatalf("the same scope set in another order must reuse the token (minted=%d)", ex.minted)
	}
	if again := ask("deploy:write"); again.Token != deploy.Token || ex.minted != 2 {
		t.Fatalf("alternating requests must not evict each other (minted=%d)", ex.minted)
	}
}

func TestMint_ANarrowerTokenThanAskedIsNotCached(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	ex := &fakeExchanger{ttl: time.Hour, grant: []string{"obs:read"}}
	h := newTestHelper(t, []Session{{Server: "https://api.example.com", Token: "rlat_session_AAAA", Kind: "daemon"}}, ex, &clock)
	ask := cloudcred.Request{Endpoint: req.Endpoint, Scopes: []string{"obs:read", "db:query"}}

	first, err := h.Mint(context.Background(), ask)
	if err != nil || strings.Join(first.Scopes, ",") != "obs:read" {
		t.Fatalf("first = %+v %v", first, err)
	}
	// The admin grants db:query a minute later: the next command must ask again.
	ex.grant = []string{"obs:read", "db:query"}
	clock = clock.Add(time.Minute)
	second, err := h.Mint(context.Background(), ask)
	if err != nil || strings.Join(second.Scopes, ",") != "obs:read,db:query" || ex.minted != 2 {
		t.Fatalf("second = %+v %v minted=%d: the narrow token was served from the cache", second, err, ex.minted)
	}
	// A complete answer is cached as before.
	if third, _ := h.Mint(context.Background(), ask); third.Token != second.Token || ex.minted != 2 {
		t.Fatalf("a token that covered the ask must be reused (minted=%d)", ex.minted)
	}
}

// The org a token acts in is part of what makes it reusable: a token minted
// for org A must never be handed out when forge asked for org B, and the
// exchange must name the org asked for (and none when none was).
func TestMint_ExchangeNamesTheOrgAndTheCacheIsKeyedByIt(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	sessions := []Session{{Server: "https://api.example.com", Token: "rlat_session_AAAA", Kind: "daemon"}}
	path := filepath.Join(t.TempDir(), "forge-token-cache.json")
	ex := &fakeExchanger{ttl: time.Hour, now: func() time.Time { return clock }}
	helper := func(org string) Service {
		return New(Deps{
			Org: org, Sessions: func() ([]Session, error) { return sessions, nil },
			Exchanger: ex, CachePath: path, Now: func() time.Time { return clock },
		})
	}

	a1, err := helper("org-a").Mint(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	b1, err := helper("org-b").Mint(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Token == b1.Token || ex.minted != 2 {
		t.Fatalf("org-b was served org-a's cached token (minted=%d)", ex.minted)
	}
	// Each org then reuses its own.
	a2, _ := helper("org-a").Mint(context.Background(), req)
	b2, _ := helper("org-b").Mint(context.Background(), req)
	if a2.Token != a1.Token || b2.Token != b1.Token || ex.minted != 2 {
		t.Fatalf("each org should reuse its own token: minted=%d", ex.minted)
	}
	// No org asked: a third identity, and the exchange names none.
	if _, err := helper("").Mint(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ex.orgs, ","); got != "org-a,org-b," {
		t.Fatalf("exchange orgs = %q, want org-a,org-b,<none>", got)
	}
}
