package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// fakeRunCaller answers GetRun with a canned response or error, and records
// the request so a test can assert what was asked.
type fakeRunCaller struct {
	resp   map[string]any
	err    error
	asked  []map[string]any
	called []string
}

func (f *fakeRunCaller) Call(_ context.Context, proc string, req, out any) error {
	f.called = append(f.called, proc)
	if m, ok := req.(map[string]any); ok {
		f.asked = append(f.asked, m)
	}
	if f.err != nil {
		return f.err
	}
	raw, _ := json.Marshal(f.resp)
	return json.Unmarshal(raw, out)
}

func stageJSON(kind, name, status string, start time.Time, finish *time.Time) map[string]any {
	s := map[string]any{"kind": kind, "name": name, "status": status, "startedAt": start.Format(time.RFC3339)}
	if finish != nil {
		s["finishedAt"] = finish.Format(time.RFC3339)
	}
	return s
}

func runResp(stages ...map[string]any) map[string]any {
	return map[string]any{
		"run":     map[string]any{"id": "github:acme/app/1/1", "url": "https://github.com/acme/app/actions/runs/1", "provider": "github"},
		"release": map[string]any{"version": "v1.4.0"},
		"stages":  stages,
	}
}

func ciRun(t *testing.T, c cloudCaller, jsonOut bool) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	var err error
	stdout := captureStdout(t, func() { err = runCIRun(context.Background(), c, "github:acme/app/1/1", jsonOut, &buf) })
	return buf.String() + stdout, err
}

// TestCIRun_ExitCodes pins §3.7's table: 0 passed, 1 failed/errored, 5
// running — derived from release.RunTimeline.Verdict, not re-ranked here.
func TestCIRun_ExitCodes(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(90 * time.Second)
	cases := []struct {
		name   string
		stages []map[string]any
		want   int
	}{
		{"all passed", []map[string]any{
			stageJSON("cut", "v1.4.0", "passed", t0, &t0),
			stageJSON("promote", "prod", "passed", t0, &t0),
			stageJSON("rollout", "prod", "passed", t0, &t1),
		}, exitOK},
		{"superseded rollout is skipped, still a pass", []map[string]any{
			stageJSON("promote", "prod", "passed", t0, &t0),
			stageJSON("rollout", "prod", "skipped", t0, &t1),
		}, exitOK},
		{"degraded rollout fails", []map[string]any{
			stageJSON("promote", "prod", "passed", t0, &t0),
			stageJSON("rollout", "prod", "failed", t0, nil),
		}, exitWrong},
		{"errored check fails, not 2", []map[string]any{
			stageJSON("check", "smoke", "error", t0, &t1),
		}, exitWrong},
		{"rollout still converging", []map[string]any{
			stageJSON("promote", "prod", "passed", t0, &t0),
			stageJSON("rollout", "prod", "running", t0, nil),
		}, exitTimedOut},
		{"failure outranks running", []map[string]any{
			stageJSON("check", "lint", "failed", t0, &t1),
			stageJSON("rollout", "prod", "running", t0, nil),
		}, exitWrong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ciRun(t, &fakeRunCaller{resp: runResp(tc.stages...)}, false)
			if got := exitCodeForError(err); got != tc.want {
				t.Fatalf("exit = %d, want %d (%v)", got, tc.want, err)
			}
		})
	}
}

// An id nothing has written under is an EMPTY timeline — never a pass.
func TestCIRun_EmptyRunIsNotAPass(t *testing.T) {
	out, err := ciRun(t, &fakeRunCaller{resp: map[string]any{"stages": []any{}}}, false)
	if got := exitCodeForError(err); got != exitTimedOut {
		t.Fatalf("an empty run must exit %d, got %d (%v)", exitTimedOut, got, err)
	}
	if !strings.Contains(out, "no stage carries this run id") {
		t.Errorf("an empty run must say so, got:\n%s", out)
	}
}

// A status this forge does not know becomes `error` — never a pass — and the
// raw word survives for the reader.
func TestCIRun_UnknownStatusIsNeverAPass(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	out, err := ciRun(t, &fakeRunCaller{resp: runResp(
		stageJSON("promote", "prod", "passed", t0, &t0),
		stageJSON("rollout", "prod", "quarantined", t0, nil),
	)}, false)
	if got := exitCodeForError(err); got != exitWrong {
		t.Fatalf("an unknown status must not pass: exit %d (%v)", got, err)
	}
	if !strings.Contains(out, `"quarantined"`) {
		t.Errorf("the unknown status must be reported verbatim, got:\n%s", out)
	}
}

// A failed READ is 2 (could not look) — except a request no retry can fix.
func TestCIRun_ReadFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unreachable", &cloud.Error{Code: cloud.CodeUnavailable, Message: "connection refused"}, exitUndetermined},
		{"auth refused", &cloud.Error{Code: cloud.CodeUnauthenticated}, exitUndetermined},
		{"control plane predates GetRun", &cloud.Error{Code: cloud.CodeUnimplemented}, exitUndetermined},
		{"run too large", &cloud.Error{Code: cloud.CodeResourceExhausted}, exitWrong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ciRun(t, &fakeRunCaller{err: tc.err}, false)
			if got := exitCodeForError(err); got != tc.want {
				t.Fatalf("exit = %d, want %d (%v)", got, tc.want, err)
			}
		})
	}
}

// --json is one document: the envelope agrees with the exit code, the
// verdict is carried, and the stages decode through the closed enums.
func TestCIRun_JSON(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	out, err := ciRun(t, &fakeRunCaller{resp: runResp(
		stageJSON("promote", "prod", "passed", t0, &t0),
		stageJSON("rollout", "prod", "running", t0, nil),
	)}, true)
	if got := exitCodeForError(err); got != exitTimedOut {
		t.Fatalf("exit = %d, want %d", got, exitTimedOut)
	}
	var doc struct {
		OK       bool                `json:"ok"`
		ExitCode int                 `json:"exit_code"`
		Verdict  release.StageStatus `json:"verdict"`
		Run      release.Run         `json:"run"`
		Release  string              `json:"release"`
		Stages   []release.Stage     `json:"stages"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout must be exactly one JSON document: %v\n%s", jerr, out)
	}
	if doc.OK || doc.ExitCode != exitTimedOut || doc.Verdict != release.StageRunning {
		t.Errorf("ok/exit/verdict = %v/%d/%s, want false/%d/running", doc.OK, doc.ExitCode, doc.Verdict, exitTimedOut)
	}
	if doc.Run.ID != "github:acme/app/1/1" || doc.Run.URL == "" || doc.Release != "v1.4.0" {
		t.Errorf("run/release = %+v %q", doc.Run, doc.Release)
	}
	if len(doc.Stages) != 2 || doc.Stages[1].Env != "prod" || doc.Stages[1].Kind != release.StageRollout {
		t.Errorf("stages = %+v", doc.Stages)
	}
}

// The request names the run and nothing else, against the real Connect wire.
func TestCIRun_RealWire(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runResp(stageJSON("cut", "v1.4.0", "passed", time.Now(), nil)))
	}))
	t.Cleanup(srv.Close)
	client := cloud.NewClient(cloud.Endpoint{Env: "prod", URL: srv.URL}, cloud.Credential{Token: "t"})

	if _, err := ciRun(t, client, false); err != nil {
		t.Fatalf("a passed run over the wire must exit 0: %v", err)
	}
	if gotPath != "/"+procGetRun {
		t.Errorf("path = %q, want /%s", gotPath, procGetRun)
	}
	if len(gotBody) != 1 || gotBody["runId"] != "github:acme/app/1/1" {
		t.Errorf("request body = %v, want exactly {runId}", gotBody)
	}
}

// `forge ci run` is registered — and it is the ONLY `run` forge has. The
// top-level dev-server `run` was deleted (its lifecycle is `forge env up`),
// which is what makes `forge run ...` unambiguous rather than a collision.
func TestCIRun_IsUnderCIAndNotUnderRun(t *testing.T) {
	ci := newCICmd()
	sub, _, err := ci.Find([]string{"run"})
	if err != nil || sub == nil || sub.Name() != "run" {
		t.Fatalf("`forge ci run` is not registered: %v", err)
	}
	// No top-level `run` may resolve: a resurrected dev-server alias would
	// shadow nothing here, but it would reintroduce the second spelling of
	// `env up` that V1 removed.
	for _, c := range newTestRootCmd(t).Commands() {
		if c.Name() == "run" {
			t.Fatal("a top-level `forge run` is registered again — the local lifecycle is `forge env up <env>`")
		}
	}
}
