package lint

import (
	"strings"
	"testing"
)

func TestCheckIdentityShares(t *testing.T) {
	hosted := &envIdentity{Runtime: "hosted"}
	for name, tc := range map[string]struct {
		in   map[string]*envIdentity
		want string // substring of the single finding, "" = none
	}{
		"sharer of a hosted owner": {map[string]*envIdentity{"staging": hosted, "preview": {Runtime: "hosted", ShareWith: "staging"}}, ""},
		"no sharers":               {map[string]*envIdentity{"staging": hosted, "dev": nil}, ""},
		"unknown owner":            {map[string]*envIdentity{"preview": {Runtime: "hosted", ShareWith: "staging"}}, `names no env of this project`},
		"owner declares nothing":   {map[string]*envIdentity{"staging": nil, "preview": {Runtime: "hosted", ShareWith: "staging"}}, `declares no identity`},
		"chain":                    {map[string]*envIdentity{"prod": hosted, "staging": {Runtime: "hosted", ShareWith: "prod"}, "preview": {Runtime: "hosted", ShareWith: "staging"}}, `does not chain`},
		"host-run owner":           {map[string]*envIdentity{"dev": {Runtime: "host"}, "preview": {Runtime: "hosted", ShareWith: "dev"}}, `only a hosted instance`},
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
