package launch

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
)

func TestChatOrg(t *testing.T) {
	org := func(s string) *string { return &s }
	cases := map[string]struct {
		spec    Spec
		project *db.Project
		want    string // "" is no org
		refused bool
	}{
		"the project's org decides":             {spec: Spec{DefaultOrgID: "home"}, project: &db.Project{OrgID: org("acme")}, want: "acme"},
		"naming the project's org":              {spec: Spec{OrgID: "acme"}, project: &db.Project{OrgID: org("acme")}, want: "acme"},
		"naming another org than the project's": {spec: Spec{OrgID: "home"}, project: &db.Project{OrgID: org("acme")}, refused: true},
		"a project with no org: the named org":  {spec: Spec{OrgID: "acme", DefaultOrgID: "home"}, project: &db.Project{}, want: "acme"},
		"nothing named: the default org":        {spec: Spec{DefaultOrgID: "home"}, project: &db.Project{}, want: "home"},
		"no orgs at all":                        {spec: Spec{}, project: &db.Project{}, want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := chatOrg(tc.spec, tc.project)
			if tc.refused {
				if svcerr.Code(err) != connect.CodeFailedPrecondition {
					t.Fatalf("err = %v, want FailedPrecondition", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tc.want == "") || (got != nil && *got != tc.want) {
				t.Fatalf("org = %v, want %q", got, tc.want)
			}
		})
	}
}
