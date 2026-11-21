package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

var updateAppLifecycleGoldens = flag.Bool("update-app-lifecycle", false, "rewrite the forge app / forge env retained golden files")

// fakeAppCaller answers control-plane rpcs by short procedure name. envErrs
// fails one env's call: key "<Rpc>/<environmentId>".
type fakeAppCaller struct {
	calls   []lifecycleCall
	replies map[string]string
	errs    map[string]error
	envErrs map[string]error
}

func (f *fakeAppCaller) Call(_ context.Context, proc string, req, out any) error {
	m, _ := req.(map[string]any)
	f.calls = append(f.calls, lifecycleCall{proc, m})
	short := proc[strings.LastIndex(proc, "/")+1:]
	if id, ok := m["environmentId"].(string); ok {
		if err := f.envErrs[short+"/"+id]; err != nil {
			return err
		}
	}
	if err := f.errs[short]; err != nil {
		return err
	}
	reply := f.replies[short]
	if reply == "" {
		reply = "{}"
	}
	return jsonUnmarshalString(reply, out)
}

func (f *fakeAppCaller) called(short string) []lifecycleCall {
	var out []lifecycleCall
	for _, c := range f.calls {
		if strings.HasSuffix(c.proc, "/"+short) {
			out = append(out, c)
		}
	}
	return out
}

const shopEnvs = `{"environments":[
 {"id":"env_stg","name":"staging","project":"shop"},
 {"id":"env_prod","name":"prod","project":"shop"},
 {"id":"env_other","name":"prod","project":"other"}]}`

// runAppCLI runs `forge <args>` against f, as app "shop" on a fake control
// plane, with stdin as the typed confirmation. It returns stdout and the
// error.
func runAppCLI(t *testing.T, f *fakeAppCaller, interactive bool, stdin string, args ...string) (string, error) {
	t.Helper()
	return runAppCLISetup(t, f, interactive, stdin, nil, args...)
}

// runAppCLISetup is runAppCLI with setup run after the seams are installed.
func runAppCLISetup(t *testing.T, f *fakeAppCaller, interactive bool, stdin string, setup func(), args ...string) (string, error) {
	t.Helper()
	prevResolve, prevInteractive, prevStdin, prevName, prevPreflight := resolveAppTarget, lifecycleInteractive, lifecycleStdin, hostedProjectName, appTeardownPreflight
	t.Cleanup(func() {
		resolveAppTarget, lifecycleInteractive, lifecycleStdin, hostedProjectName, appTeardownPreflight = prevResolve, prevInteractive, prevStdin, prevName, prevPreflight
	})
	hostedProjectName = func() string { return "shop" }
	resolveAppTarget = func(_, _, _ string) (appTarget, error) {
		return appTarget{Client: f, Endpoint: "https://cp.example", Project: "shop"}, nil
	}
	lifecycleInteractive = func() bool { return interactive }
	lifecycleStdin = strings.NewReader(stdin)
	appTeardownPreflight = func(context.Context, string) error { return nil }
	if setup != nil {
		setup()
	}

	var out, errb bytes.Buffer
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// appGolden compares a run's stdout and error with testdata/app_lifecycle/name.
func appGolden(t *testing.T, name, stdout string, err error) {
	t.Helper()
	got := "--- stdout\n" + stdout + "--- error\n"
	if err != nil {
		got += err.Error() + "\n"
	}
	path := filepath.Join("testdata", "app_lifecycle", name)
	if *updateAppLifecycleGoldens {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wErr := os.WriteFile(path, []byte(got), 0o644); wErr != nil {
			t.Fatal(wErr)
		}
	}
	want, rErr := os.ReadFile(path)
	if rErr != nil {
		t.Fatalf("%v (run with -update-app-lifecycle)", rErr)
	}
	if string(want) != got {
		t.Errorf("%s differs\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

// ---------------------------------------------------------------------------
// stop

func TestAppStop_StopsEveryEnvOfTheApp(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListEnvironments": shopEnvs, "SetEnvironmentRunState": twoDeployments}}
	out, err := runAppCLI(t, f, true, "shop\n", "app", "stop")
	appGolden(t, "stop.golden", out, err)
	calls := f.called("SetEnvironmentRunState")
	if len(calls) != 2 {
		t.Fatalf("SetEnvironmentRunState calls = %+v, want one per env of app shop", calls)
	}
	for i, id := range []string{"env_prod", "env_stg"} {
		if calls[i].req["environmentId"] != id || calls[i].req["runState"] != runStateSuspended {
			t.Errorf("call %d = %v, want %s suspended", i, calls[i].req, id)
		}
	}
}

// An env the caller may not write is reported and left running; the others
// are still stopped, and the command fails.
func TestAppStop_RefusedEnvIsLeftRunning(t *testing.T) {
	f := &fakeAppCaller{
		replies: map[string]string{"ListEnvironments": shopEnvs, "SetEnvironmentRunState": twoDeployments},
		envErrs: map[string]error{"SetEnvironmentRunState/env_prod": &cloud.Error{Code: cloud.CodePermissionDenied, Message: "deploy:write on prod is required"}},
	}
	out, err := runAppCLI(t, f, false, "", "app", "stop", "--yes")
	if err == nil {
		t.Fatal("a refused env must fail the command")
	}
	appGolden(t, "stop_partial.golden", out, err)
	if n := len(f.called("SetEnvironmentRunState")); n != 2 {
		t.Errorf("SetEnvironmentRunState calls = %d, want both envs tried", n)
	}
}

func TestAppStop_MismatchedConfirmationStopsNothing(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListEnvironments": shopEnvs}}
	out, err := runAppCLI(t, f, true, "prod\n", "app", "stop")
	appGolden(t, "stop_mismatch.golden", out, err)
	if n := len(f.called("SetEnvironmentRunState")); n != 0 {
		t.Fatalf("SetEnvironmentRunState called %d times despite a mismatched confirmation", n)
	}
}

func TestAppStop_NoEnvsIsANoOp(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListEnvironments": `{"environments":[]}`}}
	out, err := runAppCLI(t, f, true, "", "app", "stop")
	if err != nil || !strings.Contains(out, "nothing to stop") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

// Without a terminal and without --yes, every confirming verb refuses before
// it resolves a credential or calls anything.
func TestAppLifecycle_NonInteractiveWithoutYesRefusesFirst(t *testing.T) {
	for _, args := range [][]string{
		{"app", "stop"}, {"app", "teardown"}, {"app", "delete"}, {"env", "retained", "purge", "rdb_1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := &fakeAppCaller{}
			_, err := runAppCLI(t, f, false, "", args...)
			if err == nil || !strings.Contains(err.Error(), "without confirmation") || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("err = %v", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("calls = %+v, want none", f.calls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// teardown

func TestAppTeardown_DeletesEveryEnvWithItsData(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{
		"ListEnvironments": shopEnvs, "ListDeployments": twoDeployments, "ListDomains": `{"domains":[]}`,
	}}
	out, err := runAppCLI(t, f, true, "shop\n", "app", "teardown")
	appGolden(t, "teardown.golden", out, err)
	calls := f.called("DeleteEnvironment")
	if len(calls) != 2 {
		t.Fatalf("DeleteEnvironment calls = %+v", calls)
	}
	for _, c := range calls {
		if c.req["force"] != true {
			t.Errorf("delete %v without force", c.req)
		}
		if _, asked := c.req["keepData"]; asked {
			t.Errorf("a default teardown asked to keep the data: %v", c.req)
		}
	}
}

func TestAppTeardown_KeepDataRetainsEveryEnvsDatabases(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{
		"ListEnvironments": shopEnvs, "ListDeployments": twoDeployments, "ListDomains": `{"domains":[]}`,
	}}
	out, err := runAppCLI(t, f, false, "", "app", "teardown", "--yes", "--keep-data")
	appGolden(t, "teardown_keep_data.golden", out, err)
	for _, c := range f.called("DeleteEnvironment") {
		if c.req["keepData"] != true {
			t.Errorf("delete %v without keepData", c.req)
		}
	}
}

// A bound custom domain would make the control plane refuse that env's
// delete halfway through the loop, so nothing is deleted and the fix is named.
func TestAppTeardown_BoundDomainDeletesNothing(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{
		"ListEnvironments": shopEnvs, "ListDeployments": twoDeployments,
		"ListDomains": `{"domains":[{"hostname":"shop.example","binding":{"environmentId":"env_prod"}},{"hostname":"elsewhere.example","binding":{"environmentId":"env_other"}}]}`,
	}}
	out, err := runAppCLI(t, f, true, "shop\n", "app", "teardown")
	appGolden(t, "teardown_bound_domain.golden", out, err)
	if n := len(f.called("DeleteEnvironment")); n != 0 {
		t.Fatalf("DeleteEnvironment called %d times with a domain bound", n)
	}
}

// A domain list that cannot be read — no domain service at all, a bare 404,
// or the service failing — leaves the preview incomplete: a bound env would be
// refused halfway through the deletes, so nothing is deleted.
func TestAppTeardown_UnreadableDomainsDeleteNothing(t *testing.T) {
	for _, tc := range []struct {
		name, golden string
		err          error
	}{
		{"unimplemented", "teardown_domains_unimplemented.golden", &cloud.Error{Code: cloud.CodeUnimplemented, HTTPStatus: 501}},
		{"bare 404", "teardown_domains_unimplemented.golden", &cloud.Error{HTTPStatus: 404}},
		{"unavailable", "", &cloud.Error{Code: cloud.CodeUnavailable, HTTPStatus: 503, Message: "domain service down"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAppCaller{
				replies: map[string]string{"ListEnvironments": shopEnvs, "ListDeployments": twoDeployments},
				errs:    map[string]error{"ListDomains": tc.err},
			}
			out, err := runAppCLI(t, f, false, "", "app", "teardown", "--yes")
			if err == nil || !strings.Contains(err.Error(), "preview is incomplete; nothing was deleted") {
				t.Fatalf("err = %v", err)
			}
			if tc.golden != "" {
				appGolden(t, tc.golden, out, err)
			}
			if n := len(f.called("DeleteEnvironment")); n != 0 {
				t.Fatalf("DeleteEnvironment called %d times with the domains unread", n)
			}
		})
	}
}

func TestAppTeardown_PreflightRefusalDeletesNothing(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListEnvironments": shopEnvs}}
	_, err := runAppCLISetup(t, f, false, "", func() {
		appTeardownPreflight = func(_ context.Context, env string) error {
			if env == "prod" {
				return errors.New("env \"prod\" also runs parts on clusters you operate")
			}
			return nil
		}
	}, "app", "teardown", "--yes")
	if err == nil || !strings.Contains(err.Error(), "clusters you operate") {
		t.Fatalf("err = %v, want the preflight refusal", err)
	}
	if n := len(f.called("DeleteEnvironment")); n != 0 {
		t.Fatalf("DeleteEnvironment called %d times after a preflight refusal", n)
	}
}

func TestAppTeardown_RefusedEnvIsReported(t *testing.T) {
	f := &fakeAppCaller{
		replies: map[string]string{"ListEnvironments": shopEnvs, "ListDeployments": twoDeployments, "ListDomains": `{"domains":[]}`},
		envErrs: map[string]error{"DeleteEnvironment/env_stg": &cloud.Error{Code: cloud.CodePermissionDenied, Message: "only an org owner or admin may delete an environment"}},
	}
	out, err := runAppCLI(t, f, false, "", "app", "teardown", "--yes")
	if err == nil {
		t.Fatal("a refused env must fail the teardown")
	}
	appGolden(t, "teardown_partial.golden", out, err)
}

func TestAppTeardown_MismatchedConfirmationDeletesNothing(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{
		"ListEnvironments": shopEnvs, "ListDeployments": twoDeployments, "ListDomains": `{"domains":[]}`,
	}}
	_, err := runAppCLI(t, f, true, "staging\n", "app", "teardown")
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.called("DeleteEnvironment")); n != 0 {
		t.Fatalf("DeleteEnvironment called %d times despite a mismatched confirmation", n)
	}
}

// ---------------------------------------------------------------------------
// delete

const scheduledDeletion = `{"deletion":{"project":"shop","scheduledAt":"2026-11-18T10:00:00Z","purgeAfter":"2026-11-25T10:00:00Z","scheduledBy":"usr_ada"}}`

func TestAppDelete_SchedulesAfterTheTypedName(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{
		"ListEnvironments":    shopEnvs,
		"ScheduleAppDeletion": `{"deletion":{"project":"shop","scheduledAt":"2026-11-18T10:00:00Z","purgeAfter":"2026-11-25T10:00:00Z","scheduledBy":"usr_ada"},"stoppedDeploymentIds":["dep_api","dep_web","dep_stg"]}`,
	}}
	out, err := runAppCLI(t, f, true, "shop\n", "app", "delete")
	appGolden(t, "delete.golden", out, err)
	calls := f.called("ScheduleAppDeletion")
	if len(calls) != 1 || calls[0].req["project"] != "shop" {
		t.Fatalf("ScheduleAppDeletion calls = %+v", calls)
	}
}

func TestAppDelete_MismatchedConfirmationSchedulesNothing(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"ListEnvironments": shopEnvs}}
	_, err := runAppCLI(t, f, true, "nope\n", "app", "delete")
	if err == nil || !strings.Contains(err.Error(), "nothing was scheduled") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.called("ScheduleAppDeletion")); n != 0 {
		t.Fatal("ScheduleAppDeletion was called despite a mismatched confirmation")
	}
}

func TestAppDelete_AlreadyScheduledShowsItAndAsksNothing(t *testing.T) {
	f := &fakeAppCaller{replies: map[string]string{"GetAppDeletion": scheduledDeletion}}
	out, err := runAppCLI(t, f, true, "", "app", "delete")
	appGolden(t, "delete_already_scheduled.golden", out, err)
	if n := len(f.called("ScheduleAppDeletion")); n != 0 {
		t.Fatal("an already-scheduled app was scheduled again")
	}
}

// A control plane without app deletion: the read finds it before the prompt,
// and nothing is changed. Both spellings of "no such rpc" count.
func TestAppDelete_UnimplementedIsReportedBeforeThePrompt(t *testing.T) {
	for name, unserved := range map[string]error{
		"unimplemented": &cloud.Error{Code: cloud.CodeUnimplemented, HTTPStatus: 501},
		"bare 404":      &cloud.Error{HTTPStatus: 404},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeAppCaller{errs: map[string]error{"GetAppDeletion": unserved}}
			out, err := runAppCLI(t, f, true, "shop\n", "app", "delete")
			appGolden(t, "delete_unimplemented.golden", out, err)
			if len(f.calls) != 1 {
				t.Errorf("calls = %+v, want only the GetAppDeletion probe", f.calls)
			}
		})
	}
}

func TestAppDelete_StepUpRefusalSaysHowToStepUp(t *testing.T) {
	f := &fakeAppCaller{
		replies: map[string]string{"ListEnvironments": shopEnvs},
		errs: map[string]error{"ScheduleAppDeletion": &cloud.Error{Code: cloud.CodeFailedPrecondition, Reason: reasonStepUpRequired,
			Message: "this environment is protected: confirm with a step-up code (BeginStepUp/CompleteStepUp) first"}},
	}
	out, err := runAppCLI(t, f, false, "", "app", "delete", "--yes")
	appGolden(t, "delete_step_up.golden", out, err)
}

func TestAppDelete_PermissionDeniedNamesWhoMay(t *testing.T) {
	f := &fakeAppCaller{
		replies: map[string]string{"ListEnvironments": shopEnvs},
		errs: map[string]error{"ScheduleAppDeletion": &cloud.Error{Code: cloud.CodePermissionDenied,
			Message: "deleting an app needs a signed-in org owner or admin; machine credentials cannot schedule or cancel it"}},
	}
	_, err := runAppCLI(t, f, false, "", "app", "delete", "--yes")
	if err == nil || !strings.Contains(err.Error(), "machine credentials cannot") || !strings.Contains(err.Error(), "org owner or admin") {
		t.Fatalf("err = %v", err)
	}
}

func TestAppDelete_NotFoundIsNotUnimplemented(t *testing.T) {
	f := &fakeAppCaller{errs: map[string]error{"GetAppDeletion": &cloud.Error{Code: cloud.CodeNotFound, HTTPStatus: 404, Message: "app shop"}}}
	_, err := runAppCLI(t, f, false, "", "app", "delete", "--status")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
}

func TestAppDeleteStatus(t *testing.T) {
	t.Run("scheduled, purge started, secret destroy paused", func(t *testing.T) {
		f := &fakeAppCaller{replies: map[string]string{"GetAppDeletion": `{"deletion":{"project":"shop","scheduledAt":"2026-11-18T10:00:00Z",` +
			`"purgeAfter":"2026-11-25T10:00:00Z","scheduledBy":"usr_ada","purgeStartedAt":"2026-11-25T10:05:00Z","secretDestroyPaused":true}}`}}
		out, err := runAppCLI(t, f, false, "", "app", "delete", "--status")
		appGolden(t, "delete_status_paused.golden", out, err)
	})
	t.Run("nothing scheduled", func(t *testing.T) {
		f := &fakeAppCaller{}
		out, err := runAppCLI(t, f, false, "", "app", "delete", "--status")
		appGolden(t, "delete_status_none.golden", out, err)
	})
	t.Run("unimplemented", func(t *testing.T) {
		f := &fakeAppCaller{errs: map[string]error{"GetAppDeletion": &cloud.Error{Code: cloud.CodeUnimplemented}}}
		out, err := runAppCLI(t, f, false, "", "app", "delete", "--status")
		appGolden(t, "delete_unimplemented_status.golden", out, err)
	})
}

func TestAppDeleteCancel(t *testing.T) {
	t.Run("resumes what the deletion stopped", func(t *testing.T) {
		f := &fakeAppCaller{replies: map[string]string{"GetAppDeletion": scheduledDeletion,
			"CancelAppDeletion": `{"resumedDeploymentIds":["dep_api","dep_web"]}`}}
		out, err := runAppCLI(t, f, false, "", "app", "delete", "--cancel")
		appGolden(t, "delete_cancel.golden", out, err)
		if calls := f.called("CancelAppDeletion"); len(calls) != 1 || calls[0].req["project"] != "shop" {
			t.Fatalf("CancelAppDeletion calls = %+v", calls)
		}
	})
	t.Run("resume refused", func(t *testing.T) {
		f := &fakeAppCaller{replies: map[string]string{"GetAppDeletion": scheduledDeletion,
			"CancelAppDeletion": `{"resumeRefusedReason":"failed_precondition: your plan does not include running 3 workloads"}`}}
		out, err := runAppCLI(t, f, false, "", "app", "delete", "--cancel")
		appGolden(t, "delete_cancel_resume_refused.golden", out, err)
	})
	t.Run("nothing scheduled", func(t *testing.T) {
		f := &fakeAppCaller{}
		out, err := runAppCLI(t, f, false, "", "app", "delete", "--cancel")
		if err != nil || !strings.Contains(out, "nothing to cancel") {
			t.Fatalf("out = %q, err = %v", out, err)
		}
		if n := len(f.called("CancelAppDeletion")); n != 0 {
			t.Error("cancelled a deletion that was never scheduled")
		}
	})
	t.Run("unimplemented", func(t *testing.T) {
		f := &fakeAppCaller{errs: map[string]error{"GetAppDeletion": &cloud.Error{Code: cloud.CodeUnimplemented}}}
		_, err := runAppCLI(t, f, false, "", "app", "delete", "--cancel")
		if err == nil || !strings.Contains(err.Error(), "cannot delete apps") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestAppDelete_CancelAndStatusAreExclusive(t *testing.T) {
	_, err := runAppCLI(t, &fakeAppCaller{}, false, "", "app", "delete", "--cancel", "--status")
	if err == nil || !strings.Contains(err.Error(), "--cancel and --status") {
		t.Fatalf("err = %v", err)
	}
}

func TestRPCUnserved(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"unimplemented":       {&cloud.Error{Code: cloud.CodeUnimplemented}, true},
		"bare 404":            {&cloud.Error{HTTPStatus: 404}, true},
		"connect not_found":   {&cloud.Error{Code: cloud.CodeNotFound, HTTPStatus: 404}, false},
		"permission denied":   {&cloud.Error{Code: cloud.CodePermissionDenied, HTTPStatus: 403}, false},
		"not a control error": {context.Canceled, false},
	} {
		if got := rpcUnserved(tc.err); got != tc.want {
			t.Errorf("%s: rpcUnserved = %v, want %v", name, got, tc.want)
		}
	}
}
