package lint

import (
	"strings"
	"testing"
)

func TestCheckIdentityShares(t *testing.T) {
	one := func(id envIdentity) []envIdentity {
		if id.Name == "" {
			id.Name = "identity"
		}
		return []envIdentity{id}
	}
	hosted := one(envIdentity{Runtime: "hosted"})
	ops := envIdentity{Name: "identity", Runtime: "hosted"}
	users := envIdentity{Name: "users", Runtime: "hosted"}
	share := func(id envIdentity, owner string) envIdentity { id.ShareWith = owner; return id }
	for name, tc := range map[string]struct {
		in   map[string][]envIdentity
		want string // substring of the single finding, "" = none
	}{
		"sharer of a hosted owner": {map[string][]envIdentity{"staging": hosted, "preview": one(envIdentity{Runtime: "hosted", ShareWith: "staging"})}, ""},
		"no sharers":               {map[string][]envIdentity{"staging": hosted, "dev": nil}, ""},
		"unknown owner":            {map[string][]envIdentity{"preview": one(envIdentity{Runtime: "hosted", ShareWith: "staging"})}, `names no env of this project`},
		"owner declares nothing":   {map[string][]envIdentity{"staging": nil, "preview": one(envIdentity{Runtime: "hosted", ShareWith: "staging"})}, `declares no identity`},
		"chain":                    {map[string][]envIdentity{"prod": hosted, "staging": one(envIdentity{Runtime: "hosted", ShareWith: "prod"}), "preview": one(envIdentity{Runtime: "hosted", ShareWith: "staging"})}, `does not chain`},
		"host-run owner":           {map[string][]envIdentity{"dev": one(envIdentity{Runtime: "host"}), "preview": one(envIdentity{Runtime: "hosted", ShareWith: "dev"})}, `only a hosted instance`},
		// One identity on each side is matched whatever the names, as it
		// always was.
		"single identities of different names": {map[string][]envIdentity{"staging": hosted, "preview": {share(envIdentity{Name: "ops", Runtime: "hosted"}, "staging")}}, ""},
		// Several: each sharer reuses the owner's identity of ITS name.
		"several, per name":                    {map[string][]envIdentity{"staging": {ops, users}, "preview": {share(ops, "staging"), share(users, "staging")}}, ""},
		"several, the owner lacks the name":    {map[string][]envIdentity{"staging": {ops}, "preview": {share(ops, "staging"), share(users, "staging")}}, `identities[users].share_with "staging": that env declares no identity named "users"`},
		"several, the named owner is a sharer": {map[string][]envIdentity{"prod": {ops, users}, "staging": {ops, share(users, "prod")}, "preview": {share(ops, "staging"), share(users, "staging")}}, `identities[users].share_with "staging": that env itself shares "prod"'s instance`},
	} {
		t.Run(name, func(t *testing.T) {
			got := checkIdentityShares(tc.in)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no finding, got %+v", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0].Message, tc.want) || got[0].Fix == "" {
				t.Fatalf("want one finding containing %q with a fix, got %+v", tc.want, got)
			}
		})
	}
}
