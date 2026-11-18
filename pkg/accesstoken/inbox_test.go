package accesstoken

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

func TestInboxScopes_ParseAndAreInTheClosedSet(t *testing.T) {
	for raw, want := range map[string]Scope{
		"inbox:read":       ScopeInboxRead,
		"inbox:manage":     ScopeInboxManage,
		"approvals:decide": ScopeApprovalsDecide,
		"questions:answer": ScopeQuestionsAnswer,
	} {
		got, ok := ParseScope(raw)
		if !ok || got != want {
			t.Errorf("ParseScope(%q) = %q, %v", raw, got, ok)
		}
		set, err := NewSet([]string{raw})
		if err != nil || !set.Has(want) {
			t.Errorf("NewSet(%q) = %v, %v", raw, set, err)
		}
		if !RequiresActingUser(want) {
			t.Errorf("%s must act as a person", raw)
		}
		if !isInboxScope(want) {
			t.Errorf("%s missing from InboxScopes", raw)
		}
	}
	if len(InboxScopes) != 4 {
		t.Fatalf("InboxScopes = %v", InboxScopes)
	}
	for _, s := range InboxScopes {
		found := false
		for _, a := range AllScopes {
			found = found || a == s
		}
		if !found {
			t.Errorf("%s is missing from AllScopes", s)
		}
	}
}

func TestReliantAPI_PermitsEveryInboxScope(t *testing.T) {
	api := SetOf(ScopeReliantAPI)
	for _, s := range InboxScopes {
		if !api.Permits(s) {
			t.Errorf("reliant:api no longer permits %s", s)
		}
		if api.Has(s) {
			t.Errorf("reliant:api's stored grant was widened to hold %s", s)
		}
		if SetOf(s).Permits(ScopeReliantAPI) {
			t.Errorf("%s permitted reliant:api", s)
		}
	}
	if got := api.Strings(); len(got) != 1 || got[0] != "reliant:api" {
		t.Fatalf("reliant:api serialises as %v", got)
	}
}

func TestInboxScopes_NarrowTokensPermitOnlyTheirSlice(t *testing.T) {
	cases := map[string]struct {
		held   Scope
		permit []Scope
	}{
		"inbox:read":       {ScopeInboxRead, []Scope{ScopeInboxRead}},
		"inbox:manage":     {ScopeInboxManage, []Scope{ScopeInboxManage, ScopeInboxRead}},
		"approvals:decide": {ScopeApprovalsDecide, []Scope{ScopeApprovalsDecide}},
		"questions:answer": {ScopeQuestionsAnswer, []Scope{ScopeQuestionsAnswer}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			set := SetOf(c.held)
			allowed := SetOf(c.permit...)
			for _, other := range AllScopes {
				if got, want := set.Permits(other), allowed.Has(other); got != want {
					t.Errorf("%s permits %s = %v, want %v", c.held, other, got, want)
				}
			}
		})
	}
	// Nothing else implies an inbox scope.
	for _, other := range AllScopes {
		if other == ScopeReliantAPI || isInboxScope(other) {
			continue
		}
		for _, s := range InboxScopes {
			if SetOf(other).Permits(s) {
				t.Errorf("%s permitted %s", other, s)
			}
		}
	}
}

func TestInboxScopes_GrantRules(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	g := Grant{OrgID: "o", Name: "app", Scopes: SetOf(ScopeInboxRead, ScopeApprovalsDecide), ActingUserID: "u"}
	if err := g.Validate(now); err != nil {
		t.Fatalf("a narrow token was refused: %v", err)
	}
	g.ActingUserID = ""
	if err := g.Validate(now); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("an inbox token without an acting user: %v", err)
	}
	g.ActingUserID = "u"
	g.Resource = &Resource{Kind: ResourceEnvironment, ID: "e"}
	if err := g.Validate(now); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("an inbox token bound to an environment: %v", err)
	}

	// A narrow token can mint only what it holds; inbox:read does not mint
	// inbox:manage, and manage does not mint decide.
	for _, c := range []struct{ held, want Scope }{
		{ScopeInboxRead, ScopeInboxManage},
		{ScopeInboxManage, ScopeApprovalsDecide},
		{ScopeInboxManage, ScopeInboxRead},
	} {
		mint := Grant{OrgID: "o", Name: "x", Scopes: SetOf(c.want), ActingUserID: "u", GrantorScopes: SetOf(c.held)}
		if err := mint.Validate(now); !errors.Is(err, ErrEscalation) {
			t.Errorf("%s minted %s: %v", c.held, c.want, err)
		}
	}
	mint := Grant{OrgID: "o", Name: "x", Scopes: SetOf(ScopeQuestionsAnswer), ActingUserID: "u", GrantorScopes: SetOf(ScopeReliantAPI)}
	if err := mint.Validate(now); !errors.Is(err, ErrEscalation) {
		t.Errorf("reliant:api minted questions:answer without holding it: %v", err)
	}

	daemon := Grant{ActingUserID: "u", Resource: &Resource{Kind: ResourceDaemon, ID: "d"}}.Admissible()
	for _, s := range InboxScopes {
		if !daemon.Has(s) {
			t.Errorf("daemon wildcard lacks %s", s)
		}
	}
	for _, s := range (Grant{}).Admissible().Strings() {
		if isInboxScope(Scope(s)) {
			t.Errorf("an org automation wildcard resolved to %s", s)
		}
	}
}

func TestRequireScope_Inbox(t *testing.T) {
	ctxWith := func(scopes ...Scope) context.Context {
		return WithPrincipal(context.Background(), &Principal{ActingUserID: "u", Scopes: SetOf(scopes...)})
	}
	for name, c := range map[string]struct {
		ctx   context.Context
		scope Scope
		ok    bool
	}{
		"read":                      {ctxWith(ScopeInboxRead), ScopeInboxRead, true},
		"manage implies read":       {ctxWith(ScopeInboxManage), ScopeInboxRead, true},
		"manage":                    {ctxWith(ScopeInboxManage), ScopeInboxManage, true},
		"read cannot manage":        {ctxWith(ScopeInboxRead), ScopeInboxManage, false},
		"manage cannot decide":      {ctxWith(ScopeInboxManage), ScopeApprovalsDecide, false},
		"read cannot decide":        {ctxWith(ScopeInboxRead), ScopeApprovalsDecide, false},
		"decide cannot read":        {ctxWith(ScopeApprovalsDecide), ScopeInboxRead, false},
		"decide cannot answer":      {ctxWith(ScopeApprovalsDecide), ScopeQuestionsAnswer, false},
		"answer cannot decide":      {ctxWith(ScopeQuestionsAnswer), ScopeApprovalsDecide, false},
		"runs:control is unrelated": {ctxWith(ScopeRunsControl), ScopeInboxRead, false},
		"reliant:api, decide":       {ctxWith(ScopeReliantAPI), ScopeApprovalsDecide, true},
		"reliant:api, answer":       {ctxWith(ScopeReliantAPI), ScopeQuestionsAnswer, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := RequireScope(c.ctx, c.scope)
			if c.ok {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if !errors.Is(err, svcerr.ErrPermissionDenied) {
				t.Fatalf("err = %v, want permission denied", err)
			}
		})
	}
}
