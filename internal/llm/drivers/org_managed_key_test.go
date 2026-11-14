// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// twoOrgMembers is person u, a live member of org-a and org-b.
func twoOrgMembers() *fakeMembers {
	return &fakeMembers{ok: true, defOrg: "org-a", answer: map[string]accesstokenclient.OrgMembership{
		"org-a": {OrgID: "org-a", Member: true, Role: "member", MemberSince: memberSince},
		"org-b": {OrgID: "org-b", Member: true, Role: "member", MemberSince: memberSince},
		"org-c": {OrgID: "org-c", Member: true, Role: "member", MemberSince: memberSince},
	}}
}

// The per-(person, org) managed key (org-provider-accounts §3.2, P10): a
// person in two orgs spends, in each org's chats, the key minted in that org
// and never the org-less one, which bills their default org. A chat of no
// org, and an org whose key they do not hold yet, keep the org-less key.
func TestLoader_EachOrgsChatsSpendThatOrgsManagedKey(t *testing.T) {
	t.Parallel()
	repo := &orgFakeRepo{user: "u", mine: []core.AccountWithSecrets{
		managedKey("managed", "u", ""),
		managedKey("managed-a", "u", "org-a"),
		managedKey("managed-b", "u", "org-b"),
	}}
	members := twoOrgMembers()

	for _, attended := range []bool{true, false} {
		a := Scope{UserID: "u", OrgID: "org-a", Attended: attended}
		assert.Equal(t, []string{"managed-a"}, loadedIDs(loaded(t, repo, members, a)), "org-a's chats, attended=%v", attended)
		b := Scope{UserID: "u", OrgID: "org-b", Attended: attended}
		assert.Equal(t, []string{"managed-b"}, loadedIDs(loaded(t, repo, members, b)), "org-b's chats, attended=%v", attended)
	}
	assert.Equal(t, []string{"managed"}, loadedIDs(loaded(t, repo, members, PersonalScope("u"))), "a chat of no org")
	assert.Equal(t, []string{"managed"}, loadedIDs(loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-c", Attended: true})),
		"an org whose key is not held yet keeps the org-less key")

	avail := loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-b", Attended: true})
	require.Len(t, avail.Accounts["reliant"], 1)
	assert.Equal(t, "org-b", avail.Accounts["reliant"][0].AttributedOrgID, "the gateway call carries org-b's key")
}

// A chat in an org whose managed key the person does not hold yet mints it
// (EnsureOrgReliantKey) and spends it at once. Nobody is minted a key who
// does not use Reliant credits, nor in an org they are not a member of, and a
// failed mint leaves the org-less key serving.
func TestLoader_MintsTheOrgsManagedKeyTheFirstTimeItIsNeeded(t *testing.T) {
	var asked []string
	var repo *orgFakeRepo
	fail := false
	prev := ensureOrgReliantKey
	ensureOrgReliantKey = func(_ context.Context, userID, orgID string) (bool, error) {
		asked = append(asked, userID+"/"+orgID)
		if fail {
			return false, errors.New("control-plane down")
		}
		repo.mine = append(repo.mine, managedKey("managed-"+orgID, userID, orgID))
		return true, nil
	}
	t.Cleanup(func() { ensureOrgReliantKey = prev })

	repo = &orgFakeRepo{user: "u", mine: []core.AccountWithSecrets{managedKey("managed", "u", "")}}
	members := twoOrgMembers()
	assert.Equal(t, []string{"managed-org-b"}, loadedIDs(loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-b", Attended: true})))
	assert.Equal(t, []string{"managed-org-b"}, loadedIDs(loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-b", Attended: true})))
	assert.Equal(t, []string{"u/org-b"}, asked, "minted once: the second chat finds it")

	asked = nil
	notMember := &fakeMembers{ok: false}
	loaded(t, repo, notMember, Scope{UserID: "u", OrgID: "org-z", Attended: true})
	loaded(t, repo, members, PersonalScope("u"))
	noCredits := &orgFakeRepo{user: "u", mine: []core.AccountWithSecrets{personalKey("personal", "u", "openai")}}
	loaded(t, noCredits, members, Scope{UserID: "u", OrgID: "org-c", Attended: true})
	assert.Empty(t, asked, "no mint for a non-member, a chat of no org, or a person without Reliant credits")

	fail = true
	assert.Equal(t, []string{"managed"}, loadedIDs(loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-c", Attended: true})),
		"a failed mint leaves the org-less key serving")
	assert.Equal(t, []string{"u/org-c"}, asked)
}
