package accesstokenclient

import (
	"context"
	"testing"
	"time"
)

// member_since and org_live_members on the wire (control-plane#2662): a
// member's answer carries both; a negative carries neither, whatever the
// server sent; an older server sends neither, and they read as unknown.
// The stale-grant sweep (internal/orgcredentials) decides deletions from
// MemberSince, so a parse that dropped it would delete nothing, and one that
// kept it on a negative would judge a non-member by a stale epoch.
func TestResolveOrgMembership_MemberSinceAndLiveMembersDecode(t *testing.T) {
	srv := fakeCP(t, func(_ string, body map[string]any) (int, string) {
		switch body["orgId"] {
		case "org-member":
			return 200, `{"orgId":"org-member","member":true,"role":"member","memberSince":"2026-11-12T09:00:00Z","orgLiveMembers":3}`
		case "org-old":
			return 200, `{"orgId":"org-old","member":true,"role":"member"}`
		default:
			return 200, `{"orgId":"org-left","memberSince":"2026-11-12T09:00:00Z","orgLiveMembers":3}`
		}
	})
	defer srv.Close()
	c := New(Deps{BaseURL: srv.URL, Sign: sign})
	ctx := context.Background()

	got, err := c.ResolveOrgMembership(ctx, "u", "org-member")
	want := OrgMembership{OrgID: "org-member", Member: true, Role: "member",
		MemberSince: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), OrgLiveMembers: 3, OrgLiveMembersKnown: true}
	if err != nil || got != want {
		t.Fatalf("member: %+v, %v; want %+v", got, err, want)
	}
	got, err = c.ResolveOrgMembership(ctx, "u", "org-old")
	if err != nil || !got.MemberSince.IsZero() || got.OrgLiveMembersKnown {
		t.Fatalf("an older server: %+v, %v; want both unknown", got, err)
	}
	got, err = c.ResolveOrgMembership(ctx, "u", "org-left")
	if err != nil || got != (OrgMembership{OrgID: "org-left"}) {
		t.Fatalf("a negative: %+v, %v; want nothing carried", got, err)
	}
}
