package dashspec

import (
	"strings"
	"testing"
)

func TestDashboardScope(t *testing.T) {
	base := `{"id":"infra","title":"Infra",%s"panels":[{"id":"up","title":"Up","kind":"number","signal":"metrics","series":[{"label":"up","agg":"max","field":"up","metricType":"gauge"}]}]}`
	for _, tc := range []struct {
		name, scope string
		want        Scope
		wantErr     string
	}{
		{"absent is project", ``, "", ""},
		{"project", `"scope":"project",`, ScopeProject, ""},
		{"org", `"scope":"org",`, ScopeOrg, ""},
		{"platform", `"scope":"platform",`, ScopePlatform, ""},
		{"unknown value", `"scope":"global",`, "", `"global" is not one of: project, org, platform`},
		{"the retired spelling env", `"scope":"env",`, "", `"env" is not one of: project, org, platform`},
		{"not a string", `"scope":true,`, "", "must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseDashboard(tc.name, []byte(strings.Replace(base, "%s", tc.scope, 1)))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Scope != tc.want {
				t.Fatalf("scope = %q, want %q", d.Scope, tc.want)
			}
		})
	}
}
