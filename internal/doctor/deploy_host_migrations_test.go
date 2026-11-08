package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// CheckDeployMigrations asks "does this environment have SOME way to apply its
// migrations", and answers by reading the rendered k8s manifests: a migration
// Job, an initContainer, a migrate command, or AUTO_MIGRATE=true.
//
// That misses the app entirely in a HOST-mode environment. A host env
// (workloads bound to forge.OnHost) runs its services as processes on the
// developer's machine and gives the cluster only the pieces that must be in
// it — an operator that needs a projected SA token, a proxy sidecar. Those
// in-cluster pieces are real containers sharing the same database, so the env
// genuinely has a schema to keep current; the thing that KEEPS it current is
// the host service's AUTO_MIGRATE=true, which never becomes a container.
//
// The result was a check reporting an environment that migrates on every boot
// as one where "a schema-changing release deploys new code against the old
// schema", with a fix instructing the author to add a migration Job to an
// environment whose app does not run in the cluster.
//
// A host workload is visible in the `output` JSON contract
// (`workloads[runtime.type=host]`) — the same document `forge env deploy`
// consumes — so the fix is to read it, and to judge it by the same two
// mechanisms the manifest path accepts.
func TestCheckDeployMigrations_HostServiceWithAutoMigrateCounts(t *testing.T) {
	dir := t.TempDir()
	writeMigrationFile(t, dir)

	// A host-mode render: an in-cluster operator (a real container, no
	// migration step) plus the host-deployed app that carries AUTO_MIGRATE.
	body := `{"output":{
	  "manifests":[
	    {"apiVersion":"apps/v1","kind":"Deployment",
	     "metadata":{"name":"controller","namespace":"dev"},
	     "spec":{"template":{"spec":{"containers":[{"name":"controller","image":"c:1"}]}}}}
	  ],
	  "workloads":[
	    {"name":"admin-server","runtime":{"type":"host","runner":"air"},
	     "spec":{"kind":"service","args":["server"],"env":[{"name":"AUTO_MIGRATE","value":"true"}]}}
	  ]
	}}`

	env := envWithRender([]envRender{renderFromJSON(t, "dev", body)})
	env.ProjectDir = dir

	got := CheckDeployMigrations(context.Background(), env)
	if got.Status == StatusFail {
		t.Fatalf("a HOST service with AUTO_MIGRATE=true applies migrations on every "+
			"boot — reporting the env unmigrated sends someone to add a Job to an "+
			"environment whose app does not run in the cluster.\nmessage: %s\nevidence: %s",
			got.Message, got.Evidence)
	}
}

// A host workload's env is its spec.env, read among its other variables —
// the same list its container would get on any runtime.
func TestCheckDeployMigrations_HostEnvLivesOnTheSpec(t *testing.T) {
	dir := t.TempDir()
	writeMigrationFile(t, dir)

	body := `{"output":{
	  "manifests":[
	    {"apiVersion":"apps/v1","kind":"Deployment",
	     "metadata":{"name":"controller","namespace":"dev"},
	     "spec":{"template":{"spec":{"containers":[{"name":"controller","image":"c:1"}]}}}}
	  ],
	  "workloads":[
	    {"name":"admin-server","runtime":{"type":"host","runner":"air"},
	     "spec":{"kind":"service","args":["server"],
	       "env":[{"name":"PORT","value":"8090"},{"name":"AUTO_MIGRATE","value":"true"}]}}
	  ]
	}}`

	env := envWithRender([]envRender{renderFromJSON(t, "dev", body)})
	env.ProjectDir = dir

	if got := CheckDeployMigrations(context.Background(), env); got.Status == StatusFail {
		t.Fatalf("AUTO_MIGRATE in a host workload's spec.env must count.\nevidence: %s", got.Evidence)
	}
}

// A host workload whose ARGS are the migrate step counts too — the same
// mechanism the manifest path accepts via a container command.
func TestCheckDeployMigrations_HostMigrateCommandCounts(t *testing.T) {
	dir := t.TempDir()
	writeMigrationFile(t, dir)

	body := `{"output":{
	  "manifests":[
	    {"apiVersion":"apps/v1","kind":"Deployment",
	     "metadata":{"name":"controller","namespace":"dev"},
	     "spec":{"template":{"spec":{"containers":[{"name":"controller","image":"c:1"}]}}}}
	  ],
	  "workloads":[
	    {"name":"schema","runtime":{"type":"host"},
	     "spec":{"kind":"job","args":["db","migrate","up"]}}
	  ]
	}}`

	env := envWithRender([]envRender{renderFromJSON(t, "dev", body)})
	env.ProjectDir = dir

	if got := CheckDeployMigrations(context.Background(), env); got.Status == StatusFail {
		t.Fatalf("a host service running `db migrate up` is a migration step: %s", got.Evidence)
	}
}

// The guard on the other side. Widening must not blunt the check:
//
//   - a host service with AUTO_MIGRATE explicitly FALSE is not a migration step
//   - a CLUSTER-deployed service is judged by its manifest, not by the contract,
//     so putting AUTO_MIGRATE on a non-host entry must not excuse the env
func TestCheckDeployMigrations_StillFailsWithoutARealStep(t *testing.T) {
	dir := t.TempDir()
	writeMigrationFile(t, dir)

	clusterDeployment := `{"apiVersion":"apps/v1","kind":"Deployment",
	     "metadata":{"name":"api","namespace":"prod"},
	     "spec":{"template":{"spec":{"containers":[{"name":"api","image":"api:1"}]}}}}`

	for _, tt := range []struct {
		name string
		body string
	}{
		{
			name: "host service with AUTO_MIGRATE=false",
			body: `{"output":{"manifests":[` + clusterDeployment + `],"workloads":[` +
				`{"name":"app","runtime":{"type":"host"},` +
				`"spec":{"args":["server"],"env":[{"name":"AUTO_MIGRATE","value":"false"}]}}]}}`,
		},
		{
			name: "AUTO_MIGRATE on a cluster service in the contract",
			body: `{"output":{"manifests":[` + clusterDeployment + `],"workloads":[` +
				`{"name":"api","runtime":{"type":"cluster","cluster":"k3d-x"},` +
				`"spec":{"args":["server"],"env":[{"name":"AUTO_MIGRATE","value":"true"}]}}]}}`,
		},
		{
			name: "no contract at all",
			body: `{"output":{"manifests":[` + clusterDeployment + `]}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := envWithRender([]envRender{renderFromJSON(t, "prod", tt.body)})
			env.ProjectDir = dir
			got := CheckDeployMigrations(context.Background(), env)
			if got.Status != StatusFail {
				t.Fatalf("an env that runs its app in-cluster with no migration step must "+
					"still fail — that is a release deploying new code against the old "+
					"schema. status = %q (%s)", got.Status, got.Message)
			}
		})
	}
}

func writeMigrationFile(t *testing.T, projectDir string) {
	t.Helper()
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "0001_init.up.sql"),
		[]byte("CREATE TABLE t (id int);"), 0o644); err != nil {
		t.Fatal(err)
	}
}
