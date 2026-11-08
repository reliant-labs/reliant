package validation

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestWorkflowPermissionsAreChecked(t *testing.T) {
	for name, tc := range map[string]struct {
		perms []string
		ok    bool
	}{
		"none":                            {nil, true},
		"approvals:decide":                {[]string{"approvals:decide"}, true},
		"unknown":                         {[]string{"runs:start"}, false},
		"reliant:api is not a permission": {[]string{"reliant:api"}, false},
		"twice":                           {[]string{"approvals:decide", "approvals:decide"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			result := StaticAnalysis(&reliantv1.Workflow{Name: "w", Permissions: tc.perms}, nil)
			found := false
			for _, issue := range result.Errors() {
				if issue.Field == "permissions" {
					found = true
				}
			}
			require.Equal(t, !tc.ok, found)
		})
	}
}
