package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

type lifecycleCall struct {
	proc string
	req  map[string]any
}

type fakeLifecycleCaller struct {
	calls   []lifecycleCall
	replies map[string]string // by short procedure name
	errs    map[string]error
	// statuses answers GetStatus in order; the last repeats.
	statuses []string
}

func (f *fakeLifecycleCaller) Call(_ context.Context, proc string, req, out any) error {
	m, _ := req.(map[string]any)
	f.calls = append(f.calls, lifecycleCall{proc, m})
	short := proc[strings.LastIndex(proc, "/")+1:]
	if err := f.errs[short]; err != nil {
		return err
	}
	reply := f.replies[short]
	if short == "GetStatus" && len(f.statuses) > 0 {
		reply = f.statuses[0]
		if len(f.statuses) > 1 {
			f.statuses = f.statuses[1:]
		}
	}
	if reply == "" {
		reply = "{}"
	}
	return jsonUnmarshalString(reply, out)
}

const twoDeployments = `{"deployments":[
 {"id":"dep_api","name":"api","tier":"DEPLOY_TIER_BACKEND","runState":"DEPLOY_RUN_STATE_SUSPENDED","observed":{"state":"DEPLOY_OBSERVED_STATE_READY"}},
 {"id":"dep_web","name":"web","tier":"DEPLOY_TIER_STATIC","runState":"DEPLOY_RUN_STATE_SUSPENDED","observed":{"state":"DEPLOY_OBSERVED_STATE_READY"}}]}`

func TestRunLifecycle_StopWholeEnvCallsSetEnvironmentRunState(t *testing.T) {
	f := &fakeLifecycleCaller{replies: map[string]string{"SetEnvironmentRunState": twoDeployments}}
	var out bytes.Buffer
	err := runLifecycleRunState(context.Background(), f, "env_1", runStateSuspended, lifecycleOptions{Env: "prod"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].proc != procSetEnvironmentRunState {
		t.Fatalf("calls = %+v", f.calls)
	}
	if f.calls[0].req["environmentId"] != "env_1" || f.calls[0].req["runState"] != "DEPLOY_RUN_STATE_SUSPENDED" {
		t.Errorf("body = %v", f.calls[0].req)
	}
	for _, want := range []string{"api", "web", "suspended"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunLifecycle_StartTargetResolvesNameToDeploymentID(t *testing.T) {
	f := &fakeLifecycleCaller{replies: map[string]string{
		"ListDeployments": twoDeployments,
		"Scale":           `{"deployment":{"id":"dep_web","name":"web","runState":"DEPLOY_RUN_STATE_RUNNING"}}`,
	}}
	var out bytes.Buffer
	err := runLifecycleRunState(context.Background(), f, "env_1", runStateRunning, lifecycleOptions{Env: "prod", Target: "web"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || f.calls[0].proc != procListDeployments || f.calls[1].proc != procScale {
		t.Fatalf("calls = %+v", f.calls)
	}
	if f.calls[1].req["deploymentId"] != "dep_web" || f.calls[1].req["runState"] != "DEPLOY_RUN_STATE_RUNNING" {
		t.Errorf("scale body = %v", f.calls[1].req)
	}
}

func TestRunLifecycle_UnknownTargetListsDeployments(t *testing.T) {
	f := &fakeLifecycleCaller{replies: map[string]string{"ListDeployments": twoDeployments}}
	err := runLifecycleRunState(context.Background(), f, "env_1", runStateRunning, lifecycleOptions{Env: "prod", Target: "nope"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "api, web") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunLifecycle_BillingRefusalOnStartIsARunbook(t *testing.T) {
	refusal := &cloud.Error{Code: cloud.CodeFailedPrecondition, Reason: "billing_not_entitled", Message: "your plan does not include running 3 workloads"}
	f := &fakeLifecycleCaller{errs: map[string]error{"SetEnvironmentRunState": refusal}}
	err := runLifecycleRunState(context.Background(), f, "env_1", runStateRunning, lifecycleOptions{Env: "prod"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, want := range []string{"your plan does not include running 3 workloads", "billing_not_entitled", "fix:", "Nothing was started"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q:\n%v", want, err)
		}
	}
}

func TestRunLifecycle_WaitPollsUntilObservedMatches(t *testing.T) {
	progressing := `{"deployments":[{"deployment":{"name":"api","observed":{"state":"DEPLOY_OBSERVED_STATE_PROGRESSING"}}}]}`
	suspended := `{"deployments":[{"deployment":{"name":"api","observed":{"state":"DEPLOY_OBSERVED_STATE_SUSPENDED"}}}]}`
	f := &fakeLifecycleCaller{
		replies:  map[string]string{"SetEnvironmentRunState": twoDeployments},
		statuses: []string{progressing, suspended},
	}
	var out bytes.Buffer
	err := runLifecycleRunState(context.Background(), f, "env_1", runStateSuspended,
		lifecycleOptions{Env: "prod", Wait: true, Timeout: time.Minute, Interval: time.Millisecond}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "api=progressing") || !strings.Contains(out.String(), "every deployment is suspended") {
		t.Errorf("progress not printed:\n%s", out.String())
	}
}

func TestRunLifecycle_WaitTimesOutNonZero(t *testing.T) {
	stuck := `{"deployments":[{"deployment":{"name":"api","observed":{"state":"DEPLOY_OBSERVED_STATE_PROGRESSING"}}}]}`
	f := &fakeLifecycleCaller{replies: map[string]string{"SetEnvironmentRunState": twoDeployments}, statuses: []string{stuck}}
	err := runLifecycleRunState(context.Background(), f, "env_1", runStateSuspended,
		lifecycleOptions{Env: "prod", Wait: true, Timeout: 20 * time.Millisecond, Interval: 5 * time.Millisecond}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}

// The default delete destroys the data and says so; it never asks the control
// plane to keep it.
func TestRunEnvDelete_CallsDeleteEnvironmentForce(t *testing.T) {
	f := &fakeLifecycleCaller{replies: map[string]string{"ListDeployments": twoDeployments}}
	var out bytes.Buffer
	if err := runEnvDelete(context.Background(), f, "env_1", "preview", envDeleteOptions{Yes: true}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if last.proc != procDeleteEnvironment || last.req["environmentId"] != "env_1" || last.req["force"] != true {
		t.Errorf("delete call = %+v", last)
	}
	if _, asked := last.req["keepData"]; asked {
		t.Errorf("a default delete asked to keep the data: %+v", last.req)
	}
	for _, want := range []string{"AND its managed databases' data", "IRREVERSIBLE", "--keep-data", "release images"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("delete summary is missing %q:\n%s", want, out.String())
		}
	}
}

// --keep-data asks the control plane to retain the databases and says what
// that costs.
func TestRunEnvDelete_KeepDataRetainsTheDatabases(t *testing.T) {
	f := &fakeLifecycleCaller{replies: map[string]string{"ListDeployments": twoDeployments}}
	var out bytes.Buffer
	if err := runEnvDelete(context.Background(), f, "env_1", "preview", envDeleteOptions{Yes: true, KeepData: true}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if last.proc != procDeleteEnvironment || last.req["keepData"] != true {
		t.Errorf("delete call = %+v, want keepData=true", last)
	}
	for _, want := range []string{"Kept (--keep-data)", "billed as storage", "NOT reattached"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--keep-data summary is missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunEnvDelete_TypedNameMustMatch(t *testing.T) {
	f := &fakeLifecycleCaller{replies: map[string]string{"ListDeployments": twoDeployments}}
	err := runEnvDelete(context.Background(), f, "env_1", "preview", envDeleteOptions{}, strings.NewReader("prod\n"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("a wrong typed name must abort")
	}
	for _, c := range f.calls {
		if c.proc == procDeleteEnvironment {
			t.Fatal("DeleteEnvironment was called despite a mismatched confirmation")
		}
	}
	f = &fakeLifecycleCaller{replies: map[string]string{"ListDeployments": twoDeployments}}
	if err := runEnvDelete(context.Background(), f, "env_1", "preview", envDeleteOptions{}, strings.NewReader("preview\n"), &bytes.Buffer{}); err != nil {
		t.Fatalf("matching name should proceed: %v", err)
	}
}

func TestEnvDelete_NonInteractiveWithoutYesFailsFastBeforeAnyNetwork(t *testing.T) {
	oldResolve, oldInteractive := resolveLifecycleTarget, lifecycleInteractive
	t.Cleanup(func() { resolveLifecycleTarget, lifecycleInteractive = oldResolve, oldInteractive })
	resolved := false
	resolveLifecycleTarget = func(context.Context, string, string, string) (lifecycleTarget, error) {
		resolved = true
		return lifecycleTarget{}, errors.New("must not be reached")
	}
	lifecycleInteractive = func() bool { return false }

	cmd := newEnvDeleteCmd()
	cmd.SetArgs([]string{"preview"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want a --yes refusal", err)
	}
	if resolved {
		t.Error("the refusal must happen before resolving the control plane")
	}
}

func TestRequireHostedEnv_RefusesLocalAndPointsAtDown(t *testing.T) {
	for _, parts := range []placementSummary{{}, {Cluster: true}} {
		err := requireHostedEnv("dev", "delete", parts)
		if err == nil || !strings.Contains(err.Error(), "forge env down dev") {
			t.Errorf("parts %+v: err = %v", parts, err)
		}
	}
	if err := requireHostedEnv("prod", "stop", placementSummary{Hosted: true}); err != nil {
		t.Errorf("hosted env refused: %v", err)
	}
}

// A MIXED env (hosted parts beside cluster parts) is a hosted env for
// stop/start, which act on the hosted parts only, and is refused for delete,
// which would drop the ledger of the cluster parts it never tears down.
func TestRequireHostedEnv_MixedEnv(t *testing.T) {
	mixed := placementSummary{Hosted: true, Cluster: true}
	for _, verb := range []string{"stop", "start"} {
		if err := requireHostedEnv("prod", verb, mixed); err != nil {
			t.Errorf("%s on a mixed env refused: %v", verb, err)
		}
	}
	err := requireHostedEnv("prod", "delete", mixed)
	if err == nil || !strings.Contains(err.Error(), "clusters you operate") {
		t.Fatalf("delete on a mixed env: err = %v, want the cluster-parts refusal", err)
	}
}

func TestEnvDownHelpSaysItNeverTouchesHosted(t *testing.T) {
	long := newEnvDownCmd().Long
	for _, want := range []string{"never touches a HOSTED environment", "forge env stop", "forge env delete"} {
		if !strings.Contains(long, want) {
			t.Errorf("env down --help missing %q", want)
		}
	}
}

// Wire check against the real client: the body forge sends is proto3 JSON.
func TestLifecycle_RealClientSendsProto3JSON(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		gotBody = b.String()
		_, _ = w.Write([]byte(`{"deployments":[]}`))
	}))
	t.Cleanup(srv.Close)
	ep, _ := cloud.ResolveEndpoint("prod", &cloud.Declaration{Endpoint: srv.URL})
	c := cloud.NewClient(ep, cloud.Credential{Token: "t"})
	if err := runLifecycleRunState(context.Background(), c, "env_9", runStateSuspended, lifecycleOptions{Env: "prod"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "DeployService/SetEnvironmentRunState") || !strings.Contains(gotBody, `"environmentId":"env_9"`) || !strings.Contains(gotBody, `"runState":"DEPLOY_RUN_STATE_SUSPENDED"`) {
		t.Errorf("path=%s body=%s", gotPath, gotBody)
	}
}

func jsonUnmarshalString(s string, out any) error { return json.Unmarshal([]byte(s), out) }
