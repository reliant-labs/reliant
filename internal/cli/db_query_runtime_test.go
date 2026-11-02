package cli

import (
	"context"
	"strings"
	"testing"
)

// A mixed env's selected workload, not its name or another workload's runtime,
// determines the database route. No network or database connection is opened.
func TestDBQueryRouteFollowsSelectedRuntimeNotEnvName(t *testing.T) {
	for _, env := range []string{"dev", "prod", "canary-blue"} {
		t.Run(env, func(t *testing.T) {
			withKCLFixture(t, `{"output":{"data_access":{"db_query":"open"},"workloads":[
{"name":"api","kind":"service","runtime":{"type":"host"},"spec":{"kind":"service","env":[{"name":"DATABASE_URL","value":"postgres://app@declared.example/app"}]}},
{"name":"remote","kind":"service","runtime":{"type":"hosted"},"spec":{"kind":"service"}},
{"name":"clustered","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}
]}}`)
			fake := &fakeQueryDatabase{reply: queryDatabaseReply}
			withHostedDB(t, fake)
			for _, tc := range []struct{ workload, transport string }{
				{"api", "direct"}, {"remote", "control-plane"},
			} {
				target, err := resolveDBQueryTarget(context.Background(), dbQueryOptions{env: env, workload: tc.workload})
				if err != nil {
					t.Fatalf("%s/%s: %v", env, tc.workload, err)
				}
				if target.scope.Transport != tc.transport || target.scope.Env != env {
					t.Errorf("%s/%s: scope %+v", env, tc.workload, target.scope)
				}
				if tc.transport == "direct" && target.dsn != "postgres://app@declared.example/app" {
					t.Errorf("direct target must use selected workload's DATABASE_URL: %q", target.dsn)
				}
			}
			_, err := resolveDBQueryTarget(context.Background(), dbQueryOptions{env: env, workload: "clustered"})
			if exitCodeForError(err) != 2 || !strings.Contains(err.Error(), "not reachable") {
				t.Fatalf("a cluster database must not fall back to the local workload: %v", err)
			}
		})
	}
}
