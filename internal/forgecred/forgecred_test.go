// Copyright (c) 2025 Reliant Labs
package forgecred

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// fakeExchanger answers per server and counts calls.
type fakeExchanger struct {
	now     func() time.Time
	ttl     time.Duration
	refuse  map[string]error
	calls   []string
	minted  int
	lastAud string
}

func (f *fakeExchanger) Exchange(_ context.Context, server, bearer, audience string, scopes []string) (Exchanged, error) {
	f.calls = append(f.calls, server)
	f.lastAud = audience
	if err := f.refuse[server]; err != nil {
		return Exchanged{}, err
	}
	f.minted++
	return Exchanged{
		Token:     "rlat_exchanged_" + strings.Repeat("x", f.minted) + "_from_" + bearer[len(bearer)-4:],
		ExpiresAt: f.now().Add(f.ttl),
		Scopes:    scopes,
	}, nil
}

func newTestHelper(t *testing.T, sessions []Session, ex *fakeExchanger, clock *time.Time) Service {
	t.Helper()
	ex.now = func() time.Time { return *clock }
	return New(Deps{
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
