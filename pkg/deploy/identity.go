package deploy

import (
	"fmt"
	"sort"
	"strings"
)

// CheckDatabaseIdentityNames refuses a Workload and a ManagedDatabase that
// share a name in one namespace.
//
// # They would share an identity
//
// A Workload runs as a ServiceAccount named after it (see identity), and a
// ManagedDatabase's Postgres pods run as one named after the database: CNPG
// names a Cluster's ServiceAccount after the Cluster, and the Barman Cloud
// plugin binds its backup Role to the ServiceAccount named after the Cluster
// unconditionally (plugin-barman-cloud specs.BuildRoleBinding, v0.15.0 and
// main), whatever the Cluster's spec.serviceAccountName says. So a Workload
// named like a database in the same namespace runs its pods as the database's
// identity: the database's Kubernetes RBAC (its Secrets, its backup
// ObjectStore) and, under GKE Workload Identity, whatever cloud grants name
// that ServiceAccount, such as write and delete on the database's backups.
//
// Renaming the database's ServiceAccount cannot close this, because the
// plugin's binding follows the Cluster's name, not the ServiceAccount's. The
// name pair itself has to be refused.
//
// The check is by NAME, across the two lists; each list's own duplicates are
// its renderer's business.
func CheckDatabaseIdentityNames(workloads, databases []string) error {
	db := map[string]bool{}
	for _, d := range databases {
		db[d] = true
	}
	var clash []string
	seen := map[string]bool{}
	for _, w := range workloads {
		if db[w] && !seen[w] {
			clash = append(clash, w)
			seen[w] = true
		}
	}
	if len(clash) == 0 {
		return nil
	}
	sort.Strings(clash)
	return fmt.Errorf("%s: a Workload and a ManagedDatabase may not share a name in one namespace. "+
		"Each runs as a ServiceAccount named after it, so the workload's pods would run as the database's identity "+
		"(its Secrets, its backups, and any cloud grant bound to that ServiceAccount). Rename the workload or the database",
		strings.Join(quoteAll(clash), ", "))
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}
