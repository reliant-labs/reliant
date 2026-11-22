package deploytarget

import (
	"context"
	"strings"
	"testing"
)

const backupStatusBody = `{"deployments":[
 {"deployment":{"id":"dep-db","name":"db","tier":"DEPLOY_TIER_DATABASE",
  "observed":{"state":"DEPLOY_OBSERVED_STATE_READY","backup":{"lastSuccessfulBackup":"2026-11-20T02:00:00Z","earliestRestorableTime":"2026-11-13T02:00:00Z","lastError":"no bucket permission"}}},
  "verdict":"DEPLOY_VERDICT_CONVERGED"},
 {"deployment":{"id":"dep-api","name":"api","tier":"DEPLOY_TIER_BACKEND","observed":{"state":"DEPLOY_OBSERVED_STATE_READY"}},
  "verdict":"DEPLOY_VERDICT_CONVERGED"}]}`

// TestReadHostedStatusCarriesBackups pins NS HO-19: a managed database's
// DeployBackupStatus reaches `forge env status` (and its --json), and the
// silent failure — WAL archiving stopped — is spelled out.
func TestReadHostedStatusCarriesBackups(t *testing.T) {
	cp := &fakeCP{
		envs:   `{"environments":[{"id":"env-1","name":"prod"}]}`,
		status: func(int) string { return backupStatusBody },
	}
	st, err := ReadHostedStatus(context.Background(), cp, "", "prod")
	if err != nil {
		t.Fatal(err)
	}
	var db *HostedWorkloadStatus
	for i := range st.Workloads {
		if st.Workloads[i].Name == "db" {
			db = &st.Workloads[i]
		} else if st.Workloads[i].Backup != nil {
			t.Errorf("%s has no backup state, got %+v", st.Workloads[i].Name, st.Workloads[i].Backup)
		}
	}
	if db == nil || db.Backup == nil {
		t.Fatalf("db workload or its backup missing: %+v", st.Workloads)
	}
	b := db.Backup
	if b.LastSuccessfulBackup != "2026-11-20T02:00:00Z" || b.EarliestRestorableTime != "2026-11-13T02:00:00Z" || b.WALArchivingHealthy || b.LastError != "no bucket permission" {
		t.Fatalf("backup = %+v", b)
	}
	out := FormatBackups("  ", st.Workloads)
	for _, want := range []string{"db:", "last base backup:  2026-11-20T02:00:00Z", "NOT ARCHIVING", "restorable from:   2026-11-13T02:00:00Z", "no bucket permission"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatBackups lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "api:") {
		t.Errorf("a workload with no backup state was printed:\n%s", out)
	}
}
