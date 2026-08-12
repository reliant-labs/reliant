// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/pat"
)

// fakeTokenStore is a minimal in-memory pat.Store for exercising the
// TokenService handlers without a database. Only the api-kind lifecycle paths
// (create / list / revoke-by-user) are backed; the daemon-only methods are
// no-ops. This keeps the handler tests hermetic — the mint/validate behavior
// itself is covered by internal/pat's own service tests.
type fakeTokenStore struct {
	mu   sync.Mutex
	rows map[string]*db.DaemonPAT // by ID
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{rows: map[string]*db.DaemonPAT{}}
}

func (f *fakeTokenStore) CreateDaemonPAT(_ context.Context, p *db.DaemonPAT) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *p
	f.rows[p.ID] = &cp
	return nil
}

func (f *fakeTokenStore) GetDaemonPATByTokenHash(_ context.Context, hash string) (*db.DaemonPAT, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.rows {
		if p.TokenHash == hash {
			cp := *p
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (f *fakeTokenStore) ListDaemonPATsByUserIDAndKind(_ context.Context, userID, kind string) ([]*db.DaemonPAT, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*db.DaemonPAT
	for _, p := range f.rows {
		if p.UserID == userID && p.Kind == kind {
			cp := *p
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeTokenStore) RevokeDaemonPATByUserID(_ context.Context, userID, id, kind string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.rows[id]
	if !ok || p.UserID != userID || p.Kind != kind || p.RevokedAt != nil {
		return false, nil
	}
	now := time.Now().UTC()
	p.RevokedAt = &now
	return true, nil
}

func (f *fakeTokenStore) RevokeDaemonPATsByUserID(context.Context, string, bool) error { return nil }
func (f *fakeTokenStore) RevokeDaemonPATsByDaemonID(context.Context, string) (int, error) {
	return 0, nil
}
func (f *fakeTokenStore) UpdateDaemonPATLastUsed(context.Context, string) error { return nil }

func newTokenSvc() *TokenService {
	return NewTokenService(pat.NewService(newFakeTokenStore()))
}

// tokenAuthCtx returns a context carrying an authenticated identity, as the
// auth interceptor would populate after validating a bearer.
func tokenAuthCtx(userID string) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	return context.WithValue(ctx, auth.UserEmailContextKey, userID+"@example.com")
}

// sessionReq tags a request with a non-PAT (interactive-session) bearer.
func sessionReq[T any](msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set("Authorization", "Bearer session-jwt")
	return r
}

// patBearerReq tags a request with an rlnt_pat_ bearer.
func patBearerReq[T any](msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set("Authorization", "Bearer rlnt_pat_"+strings.Repeat("a", 40))
	return r
}

func TestTokenServiceCreateListRevokeFlow(t *testing.T) {
	svc := newTokenSvc()

	// Create (session-authed).
	createResp, err := svc.CreateToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.CreateTokenRequest{Name: "ci"}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !auth.IsPATFormat(createResp.Msg.GetToken()) {
		t.Errorf("raw token %q is not unified rlnt_pat_ format", createResp.Msg.GetToken())
	}
	info := createResp.Msg.GetInfo()
	if info.GetId() == "" || info.GetName() != "ci" {
		t.Errorf("create response missing metadata: %+v", info)
	}
	if !strings.HasPrefix(createResp.Msg.GetToken(), info.GetTokenPrefix()) {
		t.Errorf("token prefix %q does not prefix the raw token", info.GetTokenPrefix())
	}

	// List returns the token, never the secret.
	listResp, err := svc.ListTokens(tokenAuthCtx("user-1"), sessionReq(&reliantv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(listResp.Msg.GetTokens()) != 1 || listResp.Msg.GetTokens()[0].GetId() != info.GetId() {
		t.Fatalf("list = %+v, want the created token", listResp.Msg.GetTokens())
	}

	// Another user sees nothing.
	otherList, err := svc.ListTokens(tokenAuthCtx("user-2"), sessionReq(&reliantv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens(other): %v", err)
	}
	if len(otherList.Msg.GetTokens()) != 0 {
		t.Fatalf("other user sees %d tokens, want 0", len(otherList.Msg.GetTokens()))
	}

	// Foreign revoke 404s; owner revoke succeeds; double revoke 404s.
	if _, err := svc.RevokeToken(tokenAuthCtx("user-2"), sessionReq(&reliantv1.RevokeTokenRequest{Id: info.GetId()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("foreign revoke code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := svc.RevokeToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.RevokeTokenRequest{Id: info.GetId()})); err != nil {
		t.Errorf("owner revoke: %v", err)
	}
	if _, err := svc.RevokeToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.RevokeTokenRequest{Id: info.GetId()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("double revoke code = %v, want NotFound", connect.CodeOf(err))
	}
}

// TestTokenServiceCreateRejectsPAT pins the JWT-only rule for issuance: a PAT
// bearer can never mint another PAT, enforced in the handler because the
// interceptor accepts both credential kinds on this service.
func TestTokenServiceCreateRejectsPAT(t *testing.T) {
	svc := newTokenSvc()

	_, err := svc.CreateToken(tokenAuthCtx("user-1"), patBearerReq(&reliantv1.CreateTokenRequest{Name: "x"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("PAT-authed create code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	if err == nil || !strings.Contains(err.Error(), "interactive session") {
		t.Errorf("error = %v, want an interactive-session message", err)
	}
}

// TestTokenServiceListRevokeAcceptPAT is the complement: list and revoke work
// with a PAT bearer (a headless CI caller can inspect and rotate its own
// tokens without a browser login).
func TestTokenServiceListRevokeAcceptPAT(t *testing.T) {
	svc := newTokenSvc()

	created, err := svc.CreateToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.CreateTokenRequest{Name: "ci"}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	id := created.Msg.GetInfo().GetId()

	if _, err := svc.ListTokens(tokenAuthCtx("user-1"), patBearerReq(&reliantv1.ListTokensRequest{})); err != nil {
		t.Errorf("PAT-authed list: %v", err)
	}
	if _, err := svc.RevokeToken(tokenAuthCtx("user-1"), patBearerReq(&reliantv1.RevokeTokenRequest{Id: id})); err != nil {
		t.Errorf("PAT-authed revoke: %v", err)
	}
}

func TestTokenServiceCreateValidation(t *testing.T) {
	svc := newTokenSvc()

	if _, err := svc.CreateToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.CreateTokenRequest{Name: ""})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("empty name code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if _, err := svc.CreateToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.CreateTokenRequest{Name: "x", TtlSeconds: -5})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("negative ttl code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	// Duplicate active name is rejected (keeps revoke-by-name unambiguous).
	if _, err := svc.CreateToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.CreateTokenRequest{Name: "dup"})); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.CreateToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.CreateTokenRequest{Name: "dup"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("duplicate name code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

func TestTokenServiceRequiresIdentity(t *testing.T) {
	svc := newTokenSvc()
	anon := context.Background()

	// CreateToken clears the PAT gate (session bearer) but still needs a user.
	if _, err := svc.CreateToken(anon, sessionReq(&reliantv1.CreateTokenRequest{Name: "x"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("create without identity code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	if _, err := svc.ListTokens(anon, sessionReq(&reliantv1.ListTokensRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("list without identity code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	if _, err := svc.RevokeToken(anon, sessionReq(&reliantv1.RevokeTokenRequest{Id: "x"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("revoke without identity code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestTokenServiceRevokeRequiresID(t *testing.T) {
	svc := newTokenSvc()
	if _, err := svc.RevokeToken(tokenAuthCtx("user-1"), sessionReq(&reliantv1.RevokeTokenRequest{Id: ""})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("empty id code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}
