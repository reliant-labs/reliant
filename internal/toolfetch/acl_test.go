package toolfetch

import (
	"strings"
	"testing"
)

func TestEvaluateACL(t *testing.T) {
	t.Parallel()
	const everyone, users, authUsers = "S-1-1-0", "S-1-5-32-545", "S-1-5-11"
	ace := func(sid string, mask uint32) winACE { return winACE{Type: aceTypeAllowed, Mask: mask, SID: sid} }
	cases := []struct {
		name string
		sd   winSecurity
		role aclRole
		want string // "" = trusted, else a substring of the refusal
	}{
		{"per-user profile ACL", privateSD(), aclOwned, ""},
		{"owned by SYSTEM", winSecurity{Owner: sidSystem, ACEs: privateSD().ACEs}, aclOwned, ""},
		{"owned by Administrators", winSecurity{Owner: sidAdmins, ACEs: privateSD().ACEs}, aclOwned, ""},
		{"owned by TrustedInstaller", winSecurity{Owner: sidInstaller, ACEs: privateSD().ACEs}, aclAncestor, ""},
		{"owned by another user", winSecurity{Owner: "S-1-5-21-9-9-9-1002", ACEs: privateSD().ACEs}, aclOwned, "is owned by"},
		{"owner unknown", winSecurity{ACEs: privateSD().ACEs}, aclOwned, "is owned by"},
		{"NULL DACL", winSecurity{Owner: testUserSID, NullDACL: true}, aclOwned, "no DACL"},
		{"NULL DACL on an ancestor", winSecurity{Owner: sidSystem, NullDACL: true}, aclAncestor, "no DACL"},
		{"empty DACL grants nothing", winSecurity{Owner: testUserSID}, aclOwned, ""},
		{"Everyone read+execute", withGrant(privateSD(), everyone, 0x001200A9), aclOwned, ""},
		{"Everyone write data", withGrant(privateSD(), everyone, fileWriteData), aclOwned, "grants S-1-1-0"},
		{"Users append", withGrant(privateSD(), users, fileAppendData), aclOwned, "grants S-1-5-32-545"},
		{"Users delete", withGrant(privateSD(), users, accessDelete), aclOwned, "grants"},
		{"Users delete-child", withGrant(privateSD(), users, fileDeleteChild), aclOwned, "grants"},
		{"Users WRITE_DAC", withGrant(privateSD(), users, writeDAC), aclOwned, "grants"},
		{"Users WRITE_OWNER", withGrant(privateSD(), users, writeOwner), aclOwned, "grants"},
		{"Users GENERIC_WRITE", withGrant(privateSD(), users, genericWrite), aclOwned, "grants"},
		{"Users GENERIC_ALL", withGrant(privateSD(), users, genericAll), aclOwned, "grants"},
		{"Users write attributes only", withGrant(privateSD(), users, fileWriteAttrs), aclOwned, ""},
		{"inherit-only grant applies to children, not here", winSecurity{Owner: testUserSID, ACEs: []winACE{
			{Type: aceTypeAllowed, Flags: aceInheritOnly, Mask: genericAll, SID: authUsers}}}, aclOwned, ""},
		{"CREATOR OWNER full control", withGrant(privateSD(), sidCreatorOwner, genericAll), aclOwned, ""},
		{"a deny ACE does not cancel a grant", winSecurity{Owner: testUserSID, ACEs: []winACE{
			{Type: aceTypeDenied, Mask: fileAllAccess, SID: everyone}, ace(everyone, fileWriteData)}}, aclOwned, "grants"},
		{"unknown ACE type", winSecurity{Owner: testUserSID, ACEs: []winACE{{Type: 9, Mask: 1}}}, aclOwned, "type 9"},
		{"unknown inherit-only ACE type", winSecurity{Owner: testUserSID, ACEs: []winACE{{Type: 9, Flags: aceInheritOnly}}}, aclOwned, ""},
		// Ancestors may let others add children (C:\'s default grants
		// Authenticated Users "create folders"), never replace them.
		{"ancestor: users add folders", withGrant(privateSD(), authUsers, fileAppendData), aclAncestor, ""},
		{"ancestor: users add files", withGrant(privateSD(), authUsers, fileWriteData), aclAncestor, ""},
		{"ancestor: users GENERIC_WRITE (add only)", withGrant(privateSD(), authUsers, genericWrite), aclAncestor, ""},
		{"ancestor: users delete-child", withGrant(privateSD(), authUsers, fileDeleteChild), aclAncestor, "grants"},
		{"ancestor: users delete", withGrant(privateSD(), authUsers, accessDelete), aclAncestor, "grants"},
		{"ancestor: users WRITE_DAC", withGrant(privateSD(), authUsers, writeDAC), aclAncestor, "grants"},
		{"ancestor: users GENERIC_ALL", withGrant(privateSD(), authUsers, genericAll), aclAncestor, "grants"},
		// The default C:\ ACL, as Windows ships it.
		{"ancestor: C:\\ default", winSecurity{Owner: sidInstaller, ACEs: []winACE{
			ace(sidAdmins, fileAllAccess), ace(sidSystem, fileAllAccess), ace(users, 0x001200A9),
			{Type: aceTypeAllowed, Flags: aceInheritOnly | 0x03, Mask: 0x001301BF, SID: authUsers},
			ace(authUsers, fileAppendData)}}, aclAncestor, ""},
	}
	for _, c := range cases {
		err := evaluateACL(`C:\x`, c.sd, testUserSID, c.role)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want a refusal containing %q", c.name, err, c.want)
		}
	}
	if err := evaluateACL(`C:\x`, privateSD(), "", aclOwned); err == nil {
		t.Error("an unknown current user was trusted")
	}
}
