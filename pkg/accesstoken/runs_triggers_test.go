package accesstoken

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

func TestRunsTriggersScopes_ParseAndAreInTheClosedSet(t *testing.T) {
	for raw, want := range map[string]Scope{
		"runs:start":      ScopeRunsStart,
		"runs:read":       ScopeRunsRead,
		"runs:control":    ScopeRunsControl,
		"triggers:fire":   ScopeTriggersFire,
		"triggers:manage": ScopeTriggersManage,
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
	}
	if len(RunsAndTriggersScopes) != 5 {
		t.Fatalf("RunsAndTriggersScopes = %v", RunsAndTriggersScopes)
	}
	for _, s := range RunsAndTriggersScopes {
		found := false
		for _, a := range AllScopes {
			found = found || a == s
		}
		if !found {
			t.Errorf("%s is missing from AllScopes", s)
		}
	}
}

// reliant:api tokens were minted before these scopes existed; they must keep
// every permission, and the stored grant must not be widened.
func TestReliantAPI_PermitsEveryRunsAndTriggersScope(t *testing.T) {
	api := SetOf(ScopeReliantAPI)
	for _, s := range RunsAndTriggersScopes {
		if !api.Permits(s) {
			t.Errorf("reliant:api no longer permits %s", s)
		}
		if api.Has(s) {
			t.Errorf("reliant:api's stored grant was widened to hold %s", s)
		}
	}
	if got := api.Strings(); len(got) != 1 || got[0] != "reliant:api" {
		t.Fatalf("reliant:api serialises as %v", got)
	}
	// The reverse is not true: a narrow scope is not the whole API.
	for _, s := range RunsAndTriggersScopes {
		if SetOf(s).Permits(ScopeReliantAPI) {
			t.Errorf("%s permitted reliant:api", s)
		}
	}
}

func TestRunsTriggersScopes_NarrowTokensPermitOnlyTheirSlice(t *testing.T) {
	cases := map[string]struct {
		held   Scope
		permit []Scope
	}{
		"runs:start":      {ScopeRunsStart, []Scope{ScopeRunsStart, ScopeRunsRead}},
		"runs:read":       {ScopeRunsRead, []Scope{ScopeRunsRead}},
		"runs:control":    {ScopeRunsControl, []Scope{ScopeRunsControl, ScopeRunsRead}},
		"triggers:fire":   {ScopeTriggersFire, []Scope{ScopeTriggersFire}},
		"triggers:manage": {ScopeTriggersManage, []Scope{ScopeTriggersManage}},
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
	// Nothing else implies a runs or triggers scope.
	for _, other := range AllScopes {
		if other == ScopeReliantAPI || isRunsOrTriggersScope(other) {
			continue
		}
		for _, s := range RunsAndTriggersScopes {
			if SetOf(other).Permits(s) {
				t.Errorf("%s permitted %s", other, s)
			}
		}
	}
}

func TestRunsTriggersScopes_GrantRules(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	g := Grant{OrgID: "o", Name: "app", Scopes: SetOf(ScopeRunsStart, ScopeTriggersFire), ActingUserID: "u"}
	if err := g.Validate(now); err != nil {
		t.Fatalf("a narrow token was refused: %v", err)
	}
	g.ActingUserID = ""
	if err := g.Validate(now); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("a runs token without an acting user: %v", err)
	}
	g.ActingUserID = "u"
	g.Resource = &Resource{Kind: ResourceEnvironment, ID: "e"}
	if err := g.Validate(now); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("a runs token bound to an environment: %v", err)
	}

	// A narrow token can mint only what it holds.
	mint := Grant{OrgID: "o", Name: "x", Scopes: SetOf(ScopeRunsControl), ActingUserID: "u",
		GrantorScopes: SetOf(ScopeRunsStart)}
	if err := mint.Validate(now); !errors.Is(err, ErrEscalation) {
		t.Fatalf("runs:start minted runs:control: %v", err)
	}
	// reliant:api is exact-membership under Covers, as before.
	mint.GrantorScopes = SetOf(ScopeReliantAPI)
	if err := mint.Validate(now); !errors.Is(err, ErrEscalation) {
		t.Fatalf("reliant:api minted runs:control without holding it: %v", err)
	}

	// A daemon credential holds every admissible scope, these included.
	daemon := Grant{ActingUserID: "u", Resource: &Resource{Kind: ResourceDaemon, ID: "d"}}.Admissible()
	for _, s := range RunsAndTriggersScopes {
		if !daemon.Has(s) {
			t.Errorf("daemon wildcard lacks %s", s)
		}
	}
	for _, s := range (Grant{}).Admissible().Strings() {
		if isRunsOrTriggersScope(Scope(s)) {
			t.Errorf("an org automation wildcard resolved to %s", s)
		}
	}
}

func TestRequireScope(t *testing.T) {
	ctxWith := func(scopes ...Scope) context.Context {
		return WithPrincipal(context.Background(), &Principal{ActingUserID: "u", Scopes: SetOf(scopes...)})
	}

	for name, c := range map[string]struct {
		ctx   context.Context
		scope Scope
		ok    bool
	}{
		"exact":                             {ctxWith(ScopeRunsStart), ScopeRunsStart, true},
		"start implies read":                {ctxWith(ScopeRunsStart), ScopeRunsRead, true},
		"control implies read":              {ctxWith(ScopeRunsControl), ScopeRunsRead, true},
		"reliant:api, every scope":          {ctxWith(ScopeReliantAPI), ScopeTriggersManage, true},
		"read cannot start":                 {ctxWith(ScopeRunsRead), ScopeRunsStart, false},
		"start cannot control":              {ctxWith(ScopeRunsStart), ScopeRunsControl, false},
		"fire cannot manage":                {ctxWith(ScopeTriggersFire), ScopeTriggersManage, false},
		"manage cannot fire":                {ctxWith(ScopeTriggersManage), ScopeTriggersFire, false},
		"unrelated scope":                   {ctxWith(ScopeDeployWrite), ScopeRunsRead, false},
		"narrow token is not the whole API": {ctxWith(ScopeRunsStart), ScopeReliantAPI, false},
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
			ce := svcerr.ToConnect(err)
			if connect.CodeOf(ce) != connect.CodePermissionDenied {
				t.Fatalf("code = %v", connect.CodeOf(ce))
			}
			var cerr *connect.Error
			if !errors.As(ce, &cerr) {
				t.Fatalf("not a connect error: %T", ce)
			}
			if got := cerr.Meta().Get(svcerr.ReasonHeader); got != ReasonScopeMissing {
				t.Errorf("reason = %q", got)
			}
			if got := cerr.Meta().Get(ScopesHeader); got != string(c.scope) {
				t.Errorf("%s = %q, want %q", ScopesHeader, got, c.scope)
			}
		})
	}

	// Fails closed with no principal, and with a typed-nil one.
	for name, ctx := range map[string]context.Context{
		"none":      context.Background(),
		"typed nil": WithPrincipal(context.Background(), nil),
	} {
		if err := RequireScope(ctx, ScopeRunsRead); !errors.Is(err, svcerr.ErrUnauthenticated) {
			t.Errorf("%s: err = %v, want unauthenticated", name, err)
		}
	}
	if err := (*Principal)(nil).Require(ScopeRunsRead); !errors.Is(err, svcerr.ErrUnauthenticated) {
		t.Errorf("nil principal: %v", err)
	}
}
