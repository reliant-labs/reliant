package session

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// What a sign-in handoff needs of the Manager: the issuer session sealed and
// kept across refreshes, a session created with another's ProvedAt and a
// channel, Open, InfoByHandle and the store.

func TestIssuerSessionIsSealedAndSurvivesRefresh(t *testing.T) {
	clk := newClock()
	is := newIssuer(clk)
	store := NewMemoryStore()
	m := newTestManager(t, store, clk, is, nil)
	ctx := context.Background()
	tokens := is.mint()
	tokens.IssuerSessionID, tokens.IssuerSessionToken = "zs-1", "zs-token-secret"
	s, err := m.Create(ctx, tokens, Meta{Subject: "u1", IssuerSession: "zs-1", Channel: ChannelHandoff})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := deriveKeys(s.ID)
	rec, _ := store.Get(ctx, k.lookup)
	if rec.IssuerSession != "zs-1" {
		t.Errorf("the clear reference is %q", rec.IssuerSession)
	}
	if bytes.Contains(rec.Sealed, []byte("zs-token-secret")) || strings.Contains(rec.IssuerSession, "zs-token-secret") {
		t.Fatal("the issuer session token is stored in the clear")
	}
	// A refresh (which returns no issuer session) keeps it, and the channel.
	clk.Advance(time.Hour - time.Minute)
	if tok, err := m.AccessToken(ctx, s.ID); err != nil || tok != "at-2" {
		t.Fatalf("refresh: %q %v", tok, err)
	}
	got, info, err := m.Open(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IssuerSessionID != "zs-1" || got.IssuerSessionToken != "zs-token-secret" || got.RefreshToken != "rt-2" {
		t.Errorf("after a refresh Open = %+v", got)
	}
	if info.Channel != ChannelHandoff {
		t.Errorf("the channel was lost on refresh: %q", info.Channel)
	}
}

func TestCreateWithAnotherSessionsProof(t *testing.T) {
	clk := newClock()
	m := newTestManager(t, NewMemoryStore(), clk, nil, nil)
	ctx := context.Background()
	proved := clk.Now().Add(-3 * time.Hour)
	s, err := m.Create(ctx, Tokens{AccessToken: "a", Expiry: clk.Now().Add(time.Hour)},
		Meta{Subject: "u1", ProvedAt: proved, Channel: ChannelHandoff})
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.Info(ctx, s.ID)
	if err != nil || !info.ProvedAt.Equal(proved) || info.Channel != ChannelHandoff || !info.CreatedAt.Equal(clk.Now()) {
		t.Fatalf("Info = %+v, %v", info, err)
	}
	// List cannot open the row, so it reports no channel.
	if list, _ := m.List(ctx, "u1"); len(list) != 1 || list[0].Channel != "" || !list[0].ProvedAt.Equal(proved) {
		t.Fatalf("List = %+v", list)
	}
	// A ProvedAt in the future is read as now; zero is now.
	future, _ := m.Create(ctx, Tokens{AccessToken: "a"}, Meta{Subject: "u1", ProvedAt: clk.Now().Add(time.Hour)})
	plain, _ := m.Create(ctx, Tokens{AccessToken: "a"}, Meta{Subject: "u1"})
	for _, id := range []string{future.ID, plain.ID} {
		if info, _ := m.Info(ctx, id); !info.ProvedAt.Equal(clk.Now()) || info.Channel != "" {
			t.Errorf("Info = %+v", info)
		}
	}
}

func TestOpenAndInfoByHandle(t *testing.T) {
	clk := newClock()
	m := newTestManager(t, NewMemoryStore(), clk, nil, nil)
	ctx := context.Background()
	s := mustCreate(t, m, Tokens{AccessToken: "a", RefreshToken: "r", Expiry: clk.Now().Add(time.Hour)}, "u1")
	if info, err := m.InfoByHandle(ctx, s.Handle); err != nil || info.Subject != "u1" || info.Handle != s.Handle {
		t.Fatalf("InfoByHandle = %+v, %v", info, err)
	}
	for _, bad := range []string{"", "zz", s.Handle[:10], s.ID} {
		if _, err := m.InfoByHandle(ctx, bad); !errors.Is(err, ErrNoSession) {
			t.Errorf("InfoByHandle(%q) = %v", bad, err)
		}
	}
	// Open does not renew: an expired access token comes back as stored.
	clk.Advance(2 * time.Hour)
	if got, _, err := m.Open(ctx, s.ID); err != nil || got.AccessToken != "a" || got.RefreshToken != "r" {
		t.Fatalf("Open = %+v, %v", got, err)
	}
	if m.Store() == nil {
		t.Fatal("Store() is nil")
	}
	if _, _, err := m.Revoke(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(ctx, s.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("Open after Revoke = %v", err)
	}
	if _, err := m.InfoByHandle(ctx, s.Handle); !errors.Is(err, ErrNoSession) {
		t.Errorf("InfoByHandle after Revoke = %v", err)
	}
	if _, _, err := m.Open(ctx, "fs_nope"); !errors.Is(err, ErrNoSession) {
		t.Errorf("Open(malformed) = %v", err)
	}
	// Past its end, a session is gone to both.
	s = mustCreate(t, m, Tokens{AccessToken: "a"}, "u1")
	clk.Advance(DefaultLifetime)
	if _, _, err := m.Open(ctx, s.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("Open of an ended session = %v", err)
	}
	if _, err := m.InfoByHandle(ctx, s.Handle); !errors.Is(err, ErrNoSession) {
		t.Errorf("InfoByHandle of an ended session = %v", err)
	}
}

func TestWithSchemaQualifiesTheHandoffTable(t *testing.T) {
	if got := NewPostgresStore(nil).handoffs; got != "auth_handoffs" {
		t.Errorf("handoffs = %s", got)
	}
	if got := NewPostgresStore(nil, WithSchema("app")).handoffs; got != `"app".auth_handoffs` {
		t.Errorf("handoffs = %s", got)
	}
}
