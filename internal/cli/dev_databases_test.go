package cli

import (
	"strings"
	"testing"
)

// svcWithClusterDSN builds a cluster-bound workload whose env declares a
// DATABASE_URL — the shape a k8s workload uses.
func svcWithClusterDSN(name, dsn string) WorkloadEntity {
	return clusterWL(name, "k3d-dev", "dev", withEnv("DATABASE_URL", dsn))
}

// svcWithHostDSN builds a host-bound workload whose env declares a DSN.
func svcWithHostDSN(name, dsn string) WorkloadEntity {
	return hostWL(name, withEnv("DATABASE_URL", dsn))
}

// The regression this change exists for. An env with two DISTINCT databases
// got exactly one created, because resolveSeedDSN returns the first match
// and stops. The second surfaced only as a crash-looping pod:
//
//	FATAL: database "reliant_my_new_feature" does not exist (SQLSTATE 3D000)
func TestDevDatabaseDSNs_CollectsEveryDistinctDatabase(t *testing.T) {
	primary := "postgres://postgres:postgres@localhost:5434/control_plane_dev?sslmode=disable"
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		svcWithHostDSN("admin-server", primary),
		svcWithClusterDSN("daemon-gateway",
			"postgres://postgres:postgres@host.k3d.internal:5434/reliant_my_new_feature?sslmode=disable"),
		svcWithClusterDSN("workspace-controller",
			"postgres://postgres:postgres@host.k3d.internal:5434/control_plane_wt?sslmode=disable"),
	}}

	got := devDatabaseDSNs(entities, primary)
	if len(got) != 3 {
		t.Fatalf("want 3 distinct databases, got %d: %v", len(got), got)
	}
	if got[0] != primary {
		t.Errorf("primary must come first, got %q", got[0])
	}
	for _, want := range []string{"reliant_my_new_feature", "control_plane_wt"} {
		found := false
		for _, dsn := range got {
			if strings.Contains(dsn, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("database %q was never collected: %v", want, got)
		}
	}
}

// A cluster DSN reaches the developer's machine via the docker host-gateway
// alias, which does NOT resolve on the host. Dialing it verbatim fails with
// "no such host", so it must be rewritten to loopback.
func TestDevDatabaseDSNs_RewritesHostGatewayAliasToLoopback(t *testing.T) {
	primary := "postgres://postgres:postgres@localhost:5434/app?sslmode=disable"
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		svcWithClusterDSN("gw",
			"postgres://postgres:postgres@host.k3d.internal:5434/other?sslmode=disable"),
	}}

	got := devDatabaseDSNs(entities, primary)
	if len(got) != 2 {
		t.Fatalf("want 2 DSNs, got %v", got)
	}
	if strings.Contains(got[1], "host.k3d.internal") {
		t.Errorf("host-gateway alias must be rewritten for host-side dialing, got %q", got[1])
	}
	if !strings.Contains(got[1], "localhost:5434/other") {
		t.Errorf("want localhost:5434/other, got %q", got[1])
	}
}

// The same database reached under two spellings is ONE database. Creating it
// twice is harmless but the second CREATE is a needless round trip, and the
// dedup is what keeps the primary from being re-ensured under its alias.
func TestDevDatabaseDSNs_DeduplicatesBySeverAndName(t *testing.T) {
	primary := "postgres://postgres:postgres@localhost:5434/app?sslmode=disable"
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		// Same server+name as the primary, via the alias and different creds.
		svcWithClusterDSN("a", "postgres://other:pw@host.k3d.internal:5434/app?sslmode=require"),
		svcWithHostDSN("b", primary),
	}}

	got := devDatabaseDSNs(entities, primary)
	if len(got) != 1 {
		t.Fatalf("the same database under two spellings must collapse to one, got %v", got)
	}
}

// A DSN forge has no route to is NOT forge's to create. Guessing a route is
// how CREATE DATABASE lands on someone else's server.
func TestDevDatabaseDSNs_SkipsUnreachableHosts(t *testing.T) {
	primary := "postgres://postgres:postgres@localhost:5434/app?sslmode=disable"
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		svcWithClusterDSN("managed",
			"postgres://u:p@prod-db.abc123.us-east-1.rds.amazonaws.com:5432/prod?sslmode=require"),
		svcWithClusterDSN("in-cluster",
			"postgres://u:p@postgres.svc.cluster.local:5432/incluster?sslmode=disable"),
	}}

	got := devDatabaseDSNs(entities, primary)
	if len(got) != 1 {
		t.Fatalf("only the primary is reachable; got %v", got)
	}
}

// A single-database project must behave exactly as before.
func TestDevDatabaseDSNs_SingleDatabaseUnchanged(t *testing.T) {
	primary := "postgres://postgres:postgres@localhost:5432/app?sslmode=disable"
	entities := &KCLEntities{Workloads: []WorkloadEntity{svcWithHostDSN("api", primary)}}

	got := devDatabaseDSNs(entities, primary)
	if len(got) != 1 || got[0] != primary {
		t.Fatalf("want exactly the primary, got %v", got)
	}
}

func TestHostReachableDSN(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"loopback":           {"postgres://u:p@localhost:5434/d", "postgres://u:p@localhost:5434/d"},
		"127.0.0.1":          {"postgres://u:p@127.0.0.1:5434/d", "postgres://u:p@127.0.0.1:5434/d"},
		"k3d gateway":        {"postgres://u:p@host.k3d.internal:5434/d", "postgres://u:p@localhost:5434/d"},
		"docker gateway":     {"postgres://u:p@host.docker.internal:5434/d", "postgres://u:p@localhost:5434/d"},
		"cluster-internal":   {"postgres://u:p@postgres.svc.cluster.local:5432/d", ""},
		"managed cloud host": {"postgres://u:p@db.example.com:5432/d", ""},
		"garbage":            {"not-a-dsn", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hostReachableDSN(tc.in); got != tc.want {
				t.Errorf("hostReachableDSN(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
