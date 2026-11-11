// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A denial says why, so a caller can tell an answer (the owner left the org)
// from an outage, which must never be acted on as one. The yes/no questions
// agree with it.
func TestDenialsSayWhy(t *testing.T) {
	acme, globex := "acme", "globex"
	outage := WithMembership(context.Background(), members{err: errors.New("control-plane unavailable")})
	live := WithMembership(context.Background(), members{})
	for name, tc := range map[string]struct {
		ctx    context.Context
		caller string
		owner  string
		org    *string
		want   error
	}{
		"a live member":                     {ctx: live, caller: "alice", owner: "alice", org: &acme},
		"no org":                            {ctx: live, caller: "alice", owner: "alice"},
		"another owner's":                   {ctx: live, caller: "bob", owner: "alice", org: &acme, want: ErrDenied},
		"the owner, who left the org":       {ctx: live, caller: "alice", owner: "alice", org: &globex, want: ErrNotOrgMember},
		"an outage":                         {ctx: outage, caller: "alice", owner: "alice", org: &acme, want: ErrMembershipUnavailable},
		"no membership wired":               {ctx: context.Background(), caller: "alice", owner: "alice", org: &acme, want: ErrMembershipUnavailable},
		"another owner's, during an outage": {ctx: outage, caller: "bob", owner: "alice", org: &acme, want: ErrDenied},
	} {
		t.Run(name, func(t *testing.T) {
			project := &core.Project{UserID: tc.owner, OrgID: tc.org}
			trigger := &core.Trigger{UserID: tc.owner, OrgID: tc.org}
			for kind, got := range map[string]error{
				"project": WriteProjectDenial(tc.ctx, tc.caller, project),
				"trigger": WriteTriggerDenial(tc.ctx, tc.caller, trigger),
			} {
				if !errors.Is(got, tc.want) || (tc.want == nil) != (got == nil) {
					t.Errorf("%s denial = %v, want %v", kind, got, tc.want)
				}
			}
			if CanWriteProject(tc.ctx, tc.caller, project) != (tc.want == nil) {
				t.Errorf("CanWriteProject disagrees with its denial %v", tc.want)
			}
			if CanWriteTrigger(tc.ctx, tc.caller, trigger) != (tc.want == nil) {
				t.Errorf("CanWriteTrigger disagrees with its denial %v", tc.want)
			}
		})
	}
}
