package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// retainedOld is what a control plane without purge_after sends; storageGib
// is protojson's int64 string.
const retainedOld = `{"retainedDatabases":[
 {"id":"rdb_2","environmentId":"env_prev","environmentName":"preview","project":"shop","databaseName":"orders","clusterId":"wc-1","namespace":"shop-preview","storageGib":"10","retainedAt":"2026-11-10T09:00:00Z"},
 {"id":"rdb_1","environmentId":"env_old","environmentName":"old","project":"shop","databaseName":"main","clusterId":"wc-1","namespace":"shop-old","storageGib":"3","retainedAt":"2026-11-01T12:30:00Z"},
 {"id":"rdb_9","environmentId":"env_x","environmentName":"prod","project":"other","databaseName":"main","storageGib":"1","retainedAt":"2026-11-02T00:00:00Z"}]}`

// retainedNew carries purge_after on one database (the server sets it only
// on what it will purge by itself).
const retainedNew = `{"retainedDatabases":[
 {"id":"rdb_2","environmentId":"env_prev","environmentName":"preview","project":"shop","databaseName":"orders","storageGib":"10","retainedAt":"2026-11-10T09:00:00Z","purgeAfter":"2026-11-17T09:00:00Z"},
 {"id":"rdb_1","environmentId":"env_old","environmentName":"old","project":"shop","databaseName":"main","storageGib":"3","retainedAt":"2026-11-01T12:30:00Z"}]}`

func TestEnvRetainedList(t *testing.T) {
	for _, tc := range []struct {
		name, golden, reply string
		args                []string
	}{
		{"this app, older server", "retained_list.golden", retainedOld, nil},
		{"organization", "retained_list_all.golden", retainedOld, []string{"--all"}},
		{"purge_after sent", "retained_list_purge_after.golden", retainedNew, nil},
		{"none", "retained_list_empty.golden", `{}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": tc.reply}}
			out, err := runAppCLI(t, f, false, "", append([]string{"env", "retained", "list"}, tc.args...)...)
			appGolden(t, tc.golden, out, err)
		})
	}
}

func TestEnvRetainedList_Unimplemented(t *testing.T) {
	f := &fakeAppCaller{errs: map[string]error{"ListRetainedDatabases": &cloud.Error{Code: cloud.CodeUnimplemented}}}
	out, err := runAppCLI(t, f, false, "", "env", "retained", "list")
	appGolden(t, "retained_list_unimplemented.golden", out, err)
}

func TestEnvRetainedPurge_ByIDAfterTheTypedName(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": retainedNew}}
	out, err := runAppCLI(t, f, true, "orders\n", "env", "retained", "purge", "rdb_2")
	appGolden(t, "retained_purge.golden", out, err)
	calls := f.called("DeleteRetainedDatabase")
	if len(calls) != 1 || calls[0].req["id"] != "rdb_2" {
		t.Fatalf("DeleteRetainedDatabase calls = %+v", calls)
	}
}

// A database name resolves among this app's retained databases only: "main"
// is also the name of another app's database.
func TestEnvRetainedPurge_ByNameWithinTheApp(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": retainedOld}}
	if _, err := runAppCLI(t, f, false, "", "env", "retained", "purge", "main", "--yes"); err != nil {
		t.Fatal(err)
	}
	if calls := f.called("DeleteRetainedDatabase"); len(calls) != 1 || calls[0].req["id"] != "rdb_1" {
		t.Fatalf("DeleteRetainedDatabase calls = %+v, want rdb_1", calls)
	}
}

func TestEnvRetainedPurge_AmbiguousNameRefuses(t *testing.T) {
	twoMains := `{"retainedDatabases":[
 {"id":"rdb_1","environmentName":"old","project":"shop","databaseName":"main"},
 {"id":"rdb_3","environmentName":"older","project":"shop","databaseName":"main"}]}`
	f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": twoMains}}
	_, err := runAppCLI(t, f, false, "", "env", "retained", "purge", "main", "--yes")
	if err == nil || !strings.Contains(err.Error(), "rdb_1 (app shop, env old), rdb_3 (app shop, env older)") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.called("DeleteRetainedDatabase")); n != 0 {
		t.Fatal("purged an ambiguous name")
	}
}

func TestEnvRetainedPurge_UnknownRefuses(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": retainedOld}}
	_, err := runAppCLI(t, f, false, "", "env", "retained", "purge", "rdb_nope", "--yes")
	if err == nil || !strings.Contains(err.Error(), "nothing was purged") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvRetainedPurge_MismatchedConfirmationPurgesNothing(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": retainedOld}}
	out, err := runAppCLI(t, f, true, "rdb_2\n", "env", "retained", "purge", "rdb_2")
	appGolden(t, "retained_purge_mismatch.golden", out, err)
	if n := len(f.called("DeleteRetainedDatabase")); n != 0 {
		t.Fatal("DeleteRetainedDatabase was called despite a mismatched confirmation")
	}
}

func TestEnvRetainedPurge_Unimplemented(t *testing.T) {
	f := &fakeAppCaller{
		replies: map[string]string{"ListRetainedDatabases": retainedOld},
		errs:    map[string]error{"DeleteRetainedDatabase": &cloud.Error{Code: cloud.CodeUnimplemented}},
	}
	_, err := runAppCLI(t, f, false, "", "env", "retained", "purge", "rdb_2", "--yes")
	if err == nil || !strings.Contains(err.Error(), "does not serve DeployService/DeleteRetainedDatabase") {
		t.Fatalf("err = %v", err)
	}
}

// The list rpc is org-wide: an id of ANOTHER app's retained database is
// refused from this app, naming its owner, and nothing is deleted.
func TestEnvRetainedPurge_AnotherAppsIDIsRefused(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": retainedOld}}
	out, err := runAppCLI(t, f, false, "", "env", "retained", "purge", "rdb_9", "--yes")
	appGolden(t, "retained_purge_other_app.golden", out, err)
	if err == nil || !strings.Contains(err.Error(), `belongs to app "other"`) || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.called("DeleteRetainedDatabase")); n != 0 {
		t.Fatal("purged another app's retained database")
	}
}

// --all resolves org-wide, and the confirmation names the owning app.
func TestEnvRetainedPurge_AllReachesAnotherAppAndNamesIt(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": retainedOld}}
	out, err := runAppCLI(t, f, true, "main\n", "env", "retained", "purge", "rdb_9", "--all")
	appGolden(t, "retained_purge_all.golden", out, err)
	if calls := f.called("DeleteRetainedDatabase"); len(calls) != 1 || calls[0].req["id"] != "rdb_9" {
		t.Fatalf("DeleteRetainedDatabase calls = %+v", calls)
	}
}

// A name under --all is ambiguous across apps, and each match names its app.
func TestEnvRetainedPurge_AllNameAcrossAppsIsAmbiguous(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListRetainedDatabases": retainedOld}}
	_, err := runAppCLI(t, f, false, "", "env", "retained", "purge", "main", "--all", "--yes")
	if err == nil || !strings.Contains(err.Error(), "rdb_9 (app other, env prod)") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.called("DeleteRetainedDatabase")); n != 0 {
		t.Fatal("purged an ambiguous name")
	}
}
