package templates

import (
	"strings"
	"testing"
)

// TestCloudEnvDatabaseNeverSharesAWorkloadName pins the managed database's
// name against the workloads the same env scaffolds. The database is named
// after the project, and the API workload is always `api` and the migration
// job `migrate`: a project named either would declare a Workload and a
// ManagedDatabase of one name, which the platform refuses
// (deploy.CheckDatabaseIdentityNames), so `forge project new api` then `forge
// env deploy prod --claimable --plan-only` failed on the scaffold as written.
func TestCloudEnvDatabaseNeverSharesAWorkloadName(t *testing.T) {
	for _, tc := range []struct{ project, wantDB string }{
		{"shop", "shop"},
		{"api", "api-db"},
		{"migrate", "migrate-db"},
	} {
		for _, claimable := range []bool{false, true} {
			out, err := DeployTemplates().Render("kcl/cloud/main.k.tmpl", EnvTemplateData{
				ProjectName: tc.project, EnvName: "prod", Claimable: claimable,
				Bindings: "    _hosted(wl.migrate)\n    _hosted(_api)",
			})
			if err != nil {
				t.Fatalf("%s: render: %v", tc.project, err)
			}
			want := "_db = forge.ManagedDatabase {\n    name = \"" + tc.wantDB + "\"\n"
			if !strings.Contains(string(out), want) {
				t.Errorf("project %q (claimable=%v): want the database declared as %q; got:\n%s", tc.project, claimable, tc.wantDB, out)
			}
		}
	}
}

func TestScaffoldDatabaseName(t *testing.T) {
	for _, tc := range []struct{ project, primary, want string }{
		{"shop", "api", "shop"},
		{"api", "api", "api-db"},
		{"migrate", "api", "migrate-db"},
		// A caller that names another primary workload reserves that name
		// too; the cloud env's `_api` stays `api` whatever it says.
		{"acme", "acme", "acme-db"},
		{"api", "acme", "api-db"},
	} {
		if got := scaffoldDatabaseName(tc.project, tc.primary); got != tc.want {
			t.Errorf("scaffoldDatabaseName(%q, %q) = %q, want %q", tc.project, tc.primary, got, tc.want)
		}
	}
}
