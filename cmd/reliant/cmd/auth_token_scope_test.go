// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/cliauth"
)

func TestTokenScopesFromFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      []string
		want    []string
		wantErr []string
	}{
		{"default is reliant:api", nil, []string{"reliant:api"}, nil},
		{"repeated", []string{"runs:read", "triggers:fire"}, []string{"runs:read", "triggers:fire"}, nil},
		{"comma list and duplicates", []string{"runs:start,runs:read", "runs:start"}, []string{"runs:start", "runs:read"}, nil},
		{"explicit reliant:api", []string{"reliant:api"}, []string{"reliant:api"}, nil},
		{"manage", []string{"triggers:manage"}, []string{"triggers:manage"}, nil},
		{"each inbox scope", []string{"inbox:read", "inbox:manage", "approvals:decide", "questions:answer"},
			[]string{"inbox:read", "inbox:manage", "approvals:decide", "questions:answer"}, nil},
		{"org scope forge knows", []string{"deploy:write"}, nil, []string{`"deploy:write" cannot be carried`, "valid scopes: reliant:api, runs:read"}},
		{"daemon scope forge knows", []string{"daemon:connect"}, nil, []string{"cannot be carried"}},
		{"unknown", []string{"runs:everything"}, nil, []string{`"runs:everything" is not a known scope`, "triggers:manage", "questions:answer"}},
		{"one bad among good", []string{"runs:read,bogus"}, nil, []string{`"bogus"`}},
		{"empty", []string{""}, nil, []string{"needs a value", "valid scopes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenScopesFromFlags(tc.in)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("got %v, want an error", got)
				}
				for _, w := range tc.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("err = %v, want containing %q", err, w)
					}
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestAuthTokenCreate_ScopeFlagParsing(t *testing.T) {
	cmd := newAuthTokenCreateCmd()
	if err := cmd.ParseFlags([]string{"--name", "x", "--scope", "runs:read", "--scope", "triggers:fire,inbox:read"}); err != nil {
		t.Fatal(err)
	}
	got, err := cmd.Flags().GetStringSlice("scope")
	if err != nil || !reflect.DeepEqual(got, []string{"runs:read", "triggers:fire", "inbox:read"}) {
		t.Fatalf("scope flag = %v, %v", got, err)
	}
	def := newAuthTokenCreateCmd().Flags().Lookup("scope")
	if def == nil || def.DefValue != "[]" {
		t.Fatalf("scope flag must exist with no default list (the default token is reliant:api): %+v", def)
	}
	if !strings.Contains(newAuthTokenCreateCmd().Example, "--scope runs:read --scope triggers:fire") {
		t.Error("help must show the --scope example")
	}
}

func TestAuthTokenCreate_SendsTheNamedScopes(t *testing.T) {
	isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_NARROW0000000000")

	out, err := executeRoot(t, "auth", "token", "create", "--name", "ci",
		"--scope", "runs:read", "--scope", "triggers:fire", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("token create: %v\n%s", err, out)
	}
	if got := cp.lastRequest().Get("scope"); got != "runs:read triggers:fire" {
		t.Errorf("authorize scope = %q, want exactly runs:read triggers:fire", got)
	}
	if !strings.Contains(out, "rlat_NARROW0000000000") || !strings.Contains(out, "Scopes: runs:read triggers:fire") {
		t.Errorf("output must show the token and its scopes:\n%s", out)
	}
}

func TestAuthTokenCreate_DefaultRequestsReliantAPIOnly(t *testing.T) {
	isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_DEFAULT000000000")

	if out, err := executeRoot(t, "auth", "token", "create", "--name", "ci", "--server", cp.srv.URL); err != nil {
		t.Fatalf("token create: %v\n%s", err, out)
	}
	if got := cp.lastRequest().Get("scope"); got != cliauth.ScopeAPI {
		t.Errorf("authorize scope = %q, want %q", got, cliauth.ScopeAPI)
	}
}

func TestAuthTokenCreate_InvalidScopeFailsBeforeAnyLogin(t *testing.T) {
	isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_NEVER0000000000")

	out, err := executeRoot(t, "auth", "token", "create", "--name", "ci", "--scope", "deploy:write", "--server", cp.srv.URL)
	if err == nil || !strings.Contains(err.Error(), "valid scopes:") {
		t.Fatalf("want an error listing valid scopes; got %v\n%s", err, out)
	}
	cp.mu.Lock()
	n := len(cp.requests)
	cp.mu.Unlock()
	if n != 0 || strings.Contains(out, "rlat_NEVER") {
		t.Errorf("an invalid scope must not reach the control plane (requests=%d)\n%s", n, out)
	}
}

func TestMissingScopes(t *testing.T) {
	if got := missingScopes([]string{"runs:read", "triggers:fire"}, []string{"runs:read"}); !reflect.DeepEqual(got, []string{"triggers:fire"}) {
		t.Errorf("got %v", got)
	}
	if got := missingScopes([]string{"runs:read"}, []string{"runs:read", "extra"}); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
