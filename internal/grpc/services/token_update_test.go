// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// THE SETTINGS TOKEN LIST NEEDS PERMISSIONS, AND EDITING THEM MUST NOT
// REISSUE THE SECRET.
//
// A daemon credential is PERMANENT (control-plane migration 00112), so there
// is no expiry to bound it and "what can this token do" becomes the only
// remaining audit question. A list that hides the permissions cannot answer
// it, and an edit that reissued the secret would mean re-registering the
// daemon — a browser login a remote pod cannot perform.

// TestListTokens_ReportsTheScopes: the permissions must reach the UI.
func TestListTokens_ReportsTheScopes(t *testing.T) {
	svc, _ := newTokenSvc()

	if _, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "build-box", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	})); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	resp, err := svc.ListTokens(tokenAuthCtx("user-1"), req(&reliantv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(resp.Msg.GetTokens()) != 1 {
		t.Fatalf("listed %d tokens, want 1", len(resp.Msg.GetTokens()))
	}
	scopes := resp.Msg.GetTokens()[0].GetScopes()
	if len(scopes) == 0 {
		t.Fatal("the token list reports no scopes; a permanent credential's permissions " +
			"are the only thing left to audit and the UI cannot show them")
	}
	found := false
	for _, s := range scopes {
		if s == string(fat.ScopeDaemonConnect) {
			found = true
		}
	}
	if !found {
		t.Errorf("scopes = %v, want to include daemon:connect", scopes)
	}
}

// TestUpdateToken_RenamesWithoutTouchingScopes. The UI's rename path: scopes
// absent must leave authority alone, which is why the field is a wrapper — a
// bare repeated field cannot distinguish "not set" from "set to empty", and a
// rename would silently strip every permission.
func TestUpdateToken_RenamesWithoutTouchingScopes(t *testing.T) {
	svc, _ := newTokenSvc()
	created, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "old-name", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	id := created.Msg.GetInfo().GetId()
	before := created.Msg.GetInfo().GetScopes()

	newName := "new-name"
	resp, err := svc.UpdateToken(tokenAuthCtx("user-1"), req(&reliantv1.UpdateTokenRequest{
		Id: id, Name: &newName,
	}))
	if err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	if got := resp.Msg.GetInfo().GetName(); got != newName {
		t.Errorf("name = %q, want %q", got, newName)
	}
	if got := len(resp.Msg.GetInfo().GetScopes()); got != len(before) {
		t.Fatalf("a rename changed the scope count from %d to %d", len(before), got)
	}
}

// TestUpdateToken_ChangesScopesWithoutReissuingTheSecret is the property that
// makes editing usable on a remote daemon: the credential keeps working, with
// different authority, and nothing has to log in again.
func TestUpdateToken_ChangesScopesWithoutReissuingTheSecret(t *testing.T) {
	svc, authority := newTokenSvc()
	created, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "build-box", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	raw := created.Msg.GetToken()
	id := created.Msg.GetInfo().GetId()

	resp, err := svc.UpdateToken(tokenAuthCtx("user-1"), req(&reliantv1.UpdateTokenRequest{
		Id: id,
		Scopes: &reliantv1.ScopeList{Scopes: []string{
			string(fat.ScopeDaemonConnect), string(fat.ScopeDeployRead),
		}},
	}))
	if err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	if n := len(resp.Msg.GetInfo().GetScopes()); n != 2 {
		t.Fatalf("scopes = %v, want two", resp.Msg.GetInfo().GetScopes())
	}

	// THE SAME SECRET still authenticates, now with the new authority.
	p, err := authority.Introspect(tokenAuthCtx("user-1"), raw)
	if err != nil {
		t.Fatalf("the original secret stopped working after an edit: %v", err)
	}
	if !p.Scopes.Permits(fat.ScopeDeployRead) {
		t.Errorf("principal scopes = %v, want the edited set including deploy:read", p.Scopes)
	}
}

// TestUpdateToken_ScopedToTheCaller: one user must not be able to rename or
// re-scope another user's credential.
func TestUpdateToken_ScopedToTheCaller(t *testing.T) {
	svc, _ := newTokenSvc()
	created, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "victim", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	newName := "stolen"
	if _, err := svc.UpdateToken(tokenAuthCtx("user-2"), req(&reliantv1.UpdateTokenRequest{
		Id: created.Msg.GetInfo().GetId(), Name: &newName,
	})); err == nil {
		t.Fatal("user-2 edited user-1's token")
	}
}

// TestUpdateToken_RejectsAnEmptyUpdate. Neither field set is a request that
// asks for nothing; answering OK would hide a UI bug that sends no change.
func TestUpdateToken_RejectsAnEmptyUpdate(t *testing.T) {
	svc, _ := newTokenSvc()
	created, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "build-box", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := svc.UpdateToken(tokenAuthCtx("user-1"), req(&reliantv1.UpdateTokenRequest{
		Id: created.Msg.GetInfo().GetId(),
	})); err == nil {
		t.Fatal("an update that changes nothing was accepted")
	}
}

// TestUpdateToken_RequiresAnID.
func TestUpdateToken_RequiresAnID(t *testing.T) {
	svc, _ := newTokenSvc()
	newName := "x"
	if _, err := svc.UpdateToken(tokenAuthCtx("user-1"), req(&reliantv1.UpdateTokenRequest{
		Name: &newName,
	})); err == nil {
		t.Fatal("an update with no token id was accepted")
	}
}

// TestUpdateToken_MachineCallerCannotWiden. A machine credential must not be
// able to edit its OWN scopes — that would be a token granting itself
// authority, which is the one thing the no-mint-beyond-your-scopes rule
// exists to prevent.
func TestUpdateToken_MachineCallerCannotWiden(t *testing.T) {
	svc, _ := newTokenSvc()
	created, err := svc.CreateToken(tokenAuthCtx("user-1"), req(&reliantv1.CreateTokenRequest{
		Name: "build-box", Kind: reliantv1.TokenKind_TOKEN_KIND_DAEMON,
	}))
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := svc.UpdateToken(machineAuthCtx("user-1"), req(&reliantv1.UpdateTokenRequest{
		Id: created.Msg.GetInfo().GetId(),
		Scopes: &reliantv1.ScopeList{Scopes: []string{
			string(fat.ScopeDaemonConnect), string(fat.ScopeDeployWrite),
		}},
	})); err == nil {
		t.Fatal("a machine credential widened its own permissions")
	}
}
