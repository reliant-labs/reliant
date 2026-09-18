// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These tests never invoke forge. Every one of them substitutes the
// forgeCommandRunner seam, which is what makes the suite independent of which
// forge version is pinned: the JSON flags these handlers depend on
// (`env topology`, `env verify --json`, `secret list --json`) do not exist in
// the pinned v0.1.13 at the time of writing, and a test that shelled out would
// fail for that reason rather than for a defect in this layer.
//
// What is under test is this layer: payload parsing, the argv handed to forge,
// the five states a caller must tell apart, and the secret-path guarantees.

// stubForge installs a runner that records its invocation and returns a canned
// result. The previous runner is restored on cleanup.
func stubForge(t *testing.T, res forgeCommandResult, err error) *forgeCall {
	t.Helper()
	call := &forgeCall{}
	prev := runForge
	runForge = func(_ context.Context, projectDir string, args []string) (forgeCommandResult, error) {
		call.Count++
		call.ProjectDir = projectDir
		call.Args = args
		return res, err
	}
	t.Cleanup(func() { runForge = prev })
	return call
}

type forgeCall struct {
	Count      int
	ProjectDir string
	Args       []string
}

// forgeProject creates a directory containing a forge.yaml.
func forgeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: demo\n"), 0o600); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	return dir
}

func decodeForgeResponse(t *testing.T, raw []byte) forgeReportResponse {
	t.Helper()
	var got forgeReportResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, raw)
	}
	return got
}

// handle dispatches through the real registry, which also proves each command
// name is actually registered rather than only that its function exists.
func handle(t *testing.T, command string, payload any) ([]byte, error) {
	t.Helper()
	blob, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return DefaultRegistry().Handle(context.Background(), command, blob)
}

// --- registration ---

func TestForgeCommandsAreRegistered(t *testing.T) {
	// The registry is the daemon's whole dispatch surface; a handler that is
	// written but not registered is invisible to the RPC layer above.
	for _, name := range []string{
		"forge.topology",
		"forge.env_verify",
		"forge.secret_list",
		"forge.audit",
		"forge.env_status",
	} {
		t.Run(name, func(t *testing.T) {
			stubForge(t, forgeCommandResult{Stdout: []byte(`{"ok":true}`)}, nil)
			// A non-forge dir short-circuits before forge would run, so
			// this asserts dispatch only.
			if _, err := handle(t, name, map[string]string{
				"project_path": t.TempDir(), "env": "dev",
			}); err != nil {
				t.Fatalf("%s not dispatchable: %v", name, err)
			}
		})
	}
}

// --- state 1: not a forge project ---

func TestForgeNotAForgeProjectIsNotAnError(t *testing.T) {
	// Most reliant projects are not forge projects. Erroring here would
	// light up the UI for every ordinary repository, so absence of
	// forge.yaml must be a normal, structured answer.
	dir := t.TempDir() // exists, no forge.yaml
	call := stubForge(t, forgeCommandResult{}, nil)

	raw, err := handle(t, "forge.topology", forgeTopologyRequest{ProjectPath: dir})
	if err != nil {
		t.Fatalf("expected success for a non-forge project, got %v", err)
	}
	got := decodeForgeResponse(t, raw)

	if got.IsForgeProject {
		t.Error("is_forge_project must be false without forge.yaml")
	}
	if got.Supported {
		t.Error("supported must be false when no forge command ran")
	}
	if len(got.Report) != 0 {
		t.Errorf("no report expected, got %s", got.Report)
	}
	if call.Count != 0 {
		t.Errorf("forge must not be invoked for a non-forge project, ran %d time(s)", call.Count)
	}
}

// --- state 2: path missing ---

func TestForgeMissingPathUsesStableErrorPrefix(t *testing.T) {
	// Daemon errors cross the transport as plain strings, so the prefix is
	// the only thing the proxy can map onto NotFound. A missing path must
	// NOT read as "this project has no environments".
	stubForge(t, forgeCommandResult{}, nil)
	missing := filepath.Join(t.TempDir(), "definitely-absent")

	_, err := handle(t, "forge.topology", forgeTopologyRequest{ProjectPath: missing})
	if err == nil {
		t.Fatal("expected an error for a missing project path")
	}
	if !strings.HasPrefix(err.Error(), forgeProjectDirNotExistPrefix) {
		t.Errorf("error must start with %q, got %q", forgeProjectDirNotExistPrefix, err)
	}
}

func TestForgeProjectPathThatIsAFileIsRejected(t *testing.T) {
	stubForge(t, forgeCommandResult{}, nil)
	file := filepath.Join(t.TempDir(), "forge.yaml")
	if err := os.WriteFile(file, []byte("name: demo\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := handle(t, "forge.audit", forgeAuditRequest{ProjectPath: file})
	if err == nil || !strings.HasPrefix(err.Error(), forgeProjectDirNotExistPrefix) {
		t.Errorf("a file path must be rejected with the dir-not-exist prefix, got %v", err)
	}
}

// --- state 3: forge too old ---

func TestForgeUnsupportedFlagReportsStructuredResult(t *testing.T) {
	// Verified shape from the pinned forge v0.1.13:
	//   reliant forge env topology --json -> stderr "unknown flag: --json", exit 1
	// This must not crash and must not look like an empty success — an
	// empty success renders as "you have no environments", which is a lie.
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{
		Stderr:   []byte("unknown flag: --json\n"),
		ExitCode: 1,
	}, nil)

	raw, err := handle(t, "forge.topology", forgeTopologyRequest{ProjectPath: dir})
	if err != nil {
		t.Fatalf("an unsupported forge version must not be an error: %v", err)
	}
	got := decodeForgeResponse(t, raw)

	if !got.IsForgeProject {
		t.Error("is_forge_project must be true when forge.yaml exists")
	}
	if got.Supported {
		t.Error("supported must be false when the pinned forge lacks the flag")
	}
	if got.ForgeVersion == "" {
		t.Error("forge_version must be stamped so the UI can explain WHY the view is unavailable")
	}
	if !strings.Contains(got.UnsupportedReason, "unknown flag") {
		t.Errorf("unsupported_reason should carry forge's complaint, got %q", got.UnsupportedReason)
	}
}

func TestForgeUnsupportedSubcommandUsesTheSameMechanism(t *testing.T) {
	// A missing subcommand and a missing flag are the same condition from a
	// caller's point of view: this forge cannot answer the question. They
	// must not be two mechanisms.
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{
		Stderr:   []byte("unknown command \"topology\" for \"reliant forge env\"\n"),
		ExitCode: 1,
	}, nil)

	raw, err := handle(t, "forge.topology", forgeTopologyRequest{ProjectPath: dir})
	if err != nil {
		t.Fatalf("unsupported subcommand must not be an error: %v", err)
	}
	if got := decodeForgeResponse(t, raw); got.Supported {
		t.Error("supported must be false for an unknown subcommand")
	}
}

func TestForgeReportWinsOverStderrNoise(t *testing.T) {
	// A forge that produced a full report and also wrote something to
	// stderr has ANSWERED. Downgrading that to "unsupported" would discard
	// a valid report over an incidental warning.
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{
		Stdout:   []byte(`{"env":"prod","ok":true}`),
		Stderr:   []byte("unknown flag: --something-else\n"),
		ExitCode: 1,
	}, nil)

	raw, err := handle(t, "forge.env_verify", forgeEnvVerifyRequest{ProjectPath: dir, Env: "prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeForgeResponse(t, raw)
	if !got.Supported {
		t.Error("a parseable report must be treated as supported despite stderr noise")
	}
	if len(got.Report) == 0 {
		t.Error("the report must be preserved")
	}
}

// --- state 4: a non-zero verdict is DATA ---

func TestForgeDriftVerdictReturnsAsDataNotError(t *testing.T) {
	// This is the single most important behavior on this path. Drift exits
	// 1; if that were swallowed as an error, "your prod has drifted" would
	// be indistinguishable from "the daemon broke" — backwards, because the
	// drift verdict is the most valuable thing this path delivers.
	dir := forgeProject(t)
	report := `{"env":"prod","bound":true,"ok":false,` +
		`"images":[{"image":"api","state":"drift"}],` +
		`"tally":{"match":2,"drift":1,"missing":0,"untagged":0,"unreachable":0}}`
	stubForge(t, forgeCommandResult{Stdout: []byte(report), ExitCode: 1}, nil)

	raw, err := handle(t, "forge.env_verify", forgeEnvVerifyRequest{ProjectPath: dir, Env: "prod"})
	if err != nil {
		t.Fatalf("a drift verdict must be returned as data, got error %v", err)
	}
	got := decodeForgeResponse(t, raw)

	if !got.Supported {
		t.Error("supported must be true — forge ran and answered")
	}
	if got.ExitCode != 1 {
		t.Errorf("exit_code 1 must be carried so the UI can tell 'found problems' from 'all good', got %d", got.ExitCode)
	}

	// And the verdict must survive verbatim — the daemon is a transport and
	// must not reinterpret drift.
	var passthrough map[string]any
	if err := json.Unmarshal(got.Report, &passthrough); err != nil {
		t.Fatalf("report must be valid JSON: %v", err)
	}
	if passthrough["ok"] != false {
		t.Errorf("forge's ok:false must pass through unchanged, got %v", passthrough["ok"])
	}
	tally, _ := passthrough["tally"].(map[string]any)
	if tally["drift"] != float64(1) {
		t.Errorf("forge's tally must pass through unchanged, got %v", tally)
	}
}

func TestForgeUnreachableVerdictReturnsExitCodeTwo(t *testing.T) {
	// Exit 2 is "could not read the cluster" — an unknown, not a pass and
	// not a drift. It must reach the caller intact.
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{
		Stdout:   []byte(`{"env":"prod","ok":false,"detail":"cluster unreachable"}`),
		ExitCode: 2,
	}, nil)

	raw, err := handle(t, "forge.env_verify", forgeEnvVerifyRequest{ProjectPath: dir, Env: "prod"})
	if err != nil {
		t.Fatalf("an unreachable verdict must be data, got %v", err)
	}
	if got := decodeForgeResponse(t, raw); got.ExitCode != 2 {
		t.Errorf("exit_code must be 2, got %d", got.ExitCode)
	}
}

func TestForgeStatusUnknownCheckIsNotCollapsed(t *testing.T) {
	// status "unknown" means the check could not be measured. Collapsing it
	// to pass paints green over something nobody verified; collapsing it to
	// fail cries wolf. The passthrough must preserve it exactly.
	dir := forgeProject(t)
	report := `{"env":"dev","checks":[{"name":"traces","status":"unknown","message":"no endpoint"}]}`
	stubForge(t, forgeCommandResult{Stdout: []byte(report)}, nil)

	raw, err := handle(t, "forge.env_status", forgeEnvStatusRequest{ProjectPath: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeForgeResponse(t, raw)

	var parsed struct {
		Checks []struct {
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(got.Report, &parsed); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(parsed.Checks) != 1 || parsed.Checks[0].Status != "unknown" {
		t.Errorf(`check status must stay "unknown", got %+v`, parsed.Checks)
	}
}

// --- argv construction ---

func TestForgeTopologyArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  forgeTopologyRequest
		want []string
	}{
		{
			name: "default is ledger-only",
			want: []string{"env", "topology", "--json"},
		},
		{
			// --verify is opt-in for a load-bearing reason: without it
			// every image reads not_verified, which is UNKNOWN. The
			// handler must not quietly add it.
			name: "verify opts in to reading clusters",
			req:  forgeTopologyRequest{Verify: true},
			want: []string{"env", "topology", "--json", "--verify"},
		},
		{
			name: "envs narrow the report",
			req:  forgeTopologyRequest{Envs: []string{"prod", "staging"}},
			want: []string{"env", "topology", "--json", "prod", "staging"},
		},
		{
			name: "blank env names are dropped",
			req:  forgeTopologyRequest{Envs: []string{"prod", "  ", ""}},
			want: []string{"env", "topology", "--json", "prod"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := forgeProject(t)
			call := stubForge(t, forgeCommandResult{Stdout: []byte(`{"ok":true}`)}, nil)
			tc.req.ProjectPath = dir

			if _, err := handle(t, "forge.topology", tc.req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(call.Args, tc.want) {
				t.Errorf("args:\n got %v\nwant %v", call.Args, tc.want)
			}
			// The project dir is how forge resolves the project (it
			// walks up from the child's CWD), so it must be passed
			// through exactly.
			if call.ProjectDir != dir {
				t.Errorf("project dir: got %q want %q", call.ProjectDir, dir)
			}
		})
	}
}

func TestForgePerCommandArgs(t *testing.T) {
	for _, tc := range []struct {
		command string
		payload map[string]string
		want    []string
	}{
		{"forge.env_verify", map[string]string{"env": "prod"}, []string{"env", "verify", "prod", "--json"}},
		{"forge.secret_list", map[string]string{"env": "dev"}, []string{"secret", "list", "dev", "--json"}},
		{"forge.audit", nil, []string{"project", "audit", "--json"}},
		{"forge.env_status", map[string]string{"env": "dev"}, []string{"env", "status", "dev", "--json"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			dir := forgeProject(t)
			call := stubForge(t, forgeCommandResult{Stdout: []byte(`{"ok":true}`)}, nil)

			payload := map[string]string{"project_path": dir}
			for k, v := range tc.payload {
				payload[k] = v
			}
			if _, err := handle(t, tc.command, payload); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(call.Args, tc.want) {
				t.Errorf("args:\n got %v\nwant %v", call.Args, tc.want)
			}
		})
	}
}

// --- payload validation ---

func TestForgeInvalidPayloadIsRejected(t *testing.T) {
	stubForge(t, forgeCommandResult{}, nil)
	if _, err := DefaultRegistry().Handle(context.Background(), "forge.topology", []byte("{not json")); err == nil {
		t.Fatal("expected an error for a malformed payload")
	}
}

func TestForgeRequiredFieldsAreValidated(t *testing.T) {
	dir := forgeProject(t)

	t.Run("project_path required", func(t *testing.T) {
		stubForge(t, forgeCommandResult{}, nil)
		if _, err := handle(t, "forge.audit", forgeAuditRequest{}); err == nil {
			t.Error("expected an error when project_path is empty")
		}
	})

	// env-scoped commands must not silently ask forge about the wrong thing.
	for _, command := range []string{"forge.env_verify", "forge.secret_list", "forge.env_status"} {
		t.Run(command+" env required", func(t *testing.T) {
			call := stubForge(t, forgeCommandResult{}, nil)
			if _, err := handle(t, command, map[string]string{"project_path": dir}); err == nil {
				t.Error("expected an error when env is empty")
			}
			if call.Count != 0 {
				t.Error("forge must not run without an env")
			}
		})
	}
}

// --- failure mapping ---

func TestForgeRunFailureUsesStableErrorPrefix(t *testing.T) {
	// The process could not be run at all (or was killed): no verdict
	// exists, so this is a genuine failure and must be prefix-matched.
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{}, fmt.Errorf("run forge: exec format error"))

	_, err := handle(t, "forge.audit", forgeAuditRequest{ProjectPath: dir})
	if err == nil {
		t.Fatal("expected an error when forge could not be run")
	}
	if !strings.HasPrefix(err.Error(), forgeCommandFailedPrefix) {
		t.Errorf("error must start with %q, got %q", forgeCommandFailedPrefix, err)
	}
}

func TestForgeNonZeroWithNoReportIsAnError(t *testing.T) {
	// Non-zero AND nothing parseable is not a verdict — there is no report
	// to interpret, so it must not masquerade as one.
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{
		Stderr:   []byte("render KCL: boom\n"),
		ExitCode: 1,
	}, nil)

	_, err := handle(t, "forge.env_status", forgeEnvStatusRequest{ProjectPath: dir, Env: "dev"})
	if err == nil {
		t.Fatal("expected an error for a non-zero exit with no report")
	}
	if !strings.HasPrefix(err.Error(), forgeCommandFailedPrefix) {
		t.Errorf("error must start with %q, got %q", forgeCommandFailedPrefix, err)
	}
}

func TestForgeExitZeroWithUnparseableOutputIsAnError(t *testing.T) {
	// Passing a truncated body upstream as a "report" would surface as a UI
	// parse error far from its cause.
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{Stdout: []byte("not json at all")}, nil)

	if _, err := handle(t, "forge.audit", forgeAuditRequest{ProjectPath: dir}); err == nil {
		t.Fatal("expected an error for exit 0 with unparseable output")
	}
}

// --- secrets guard ---

// TestForgeSecretListNeverEchoesStderr pins the rule that the secret path is
// the one place where forge's diagnostic output is withheld. forge's report is
// designed so no field can hold a value; a stderr passthrough would be a
// channel around that design.
func TestForgeSecretListNeverEchoesStderr(t *testing.T) {
	dir := forgeProject(t)
	const canary = "hunter2-should-never-appear"

	t.Run("on failure", func(t *testing.T) {
		stubForge(t, forgeCommandResult{
			Stderr:   []byte("failed to parse store: bad value " + canary + "\n"),
			ExitCode: 1,
		}, nil)

		_, err := handle(t, "forge.secret_list", forgeSecretListRequest{ProjectPath: dir, Env: "dev"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), canary) {
			t.Errorf("stderr must never reach the error string on the secret path: %q", err)
		}
		if !strings.HasPrefix(err.Error(), forgeCommandFailedPrefix) {
			t.Errorf("error must still be prefix-matched, got %q", err)
		}
	})

	t.Run("on unsupported version", func(t *testing.T) {
		stubForge(t, forgeCommandResult{
			Stderr:   []byte("unknown flag: --json (" + canary + ")\n"),
			ExitCode: 1,
		}, nil)

		raw, err := handle(t, "forge.secret_list", forgeSecretListRequest{ProjectPath: dir, Env: "dev"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(string(raw), canary) {
			t.Errorf("stderr must never reach the response on the secret path: %s", raw)
		}
		got := decodeForgeResponse(t, raw)
		if got.Supported {
			t.Error("supported must still be false")
		}
		if got.ForgeVersion == "" {
			t.Error("forge_version must still be stamped")
		}
	})

	t.Run("on run failure", func(t *testing.T) {
		stubForge(t, forgeCommandResult{Stderr: []byte(canary)}, fmt.Errorf("killed: %s", canary))

		_, err := handle(t, "forge.secret_list", forgeSecretListRequest{ProjectPath: dir, Env: "dev"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), canary) {
			t.Errorf("stderr/err must not leak on the secret path: %q", err)
		}
	})
}

func TestForgeSecretListMissingVerdictIsData(t *testing.T) {
	// A declared secret with no value exits non-zero. That is the answer to
	// "what is missing", not a failure.
	dir := forgeProject(t)
	report := `{"env":"dev","provider":"file","store_exists":false,` +
		`"secrets":[{"name":"GITHUB_CLIENT_SECRET","present":false}],` +
		`"missing":["GITHUB_CLIENT_SECRET"],"missing_count":1,"ok":false}`
	stubForge(t, forgeCommandResult{Stdout: []byte(report), ExitCode: 1}, nil)

	raw, err := handle(t, "forge.secret_list", forgeSecretListRequest{ProjectPath: dir, Env: "dev"})
	if err != nil {
		t.Fatalf("a missing-secret verdict must be data, got %v", err)
	}
	got := decodeForgeResponse(t, raw)
	if !got.Supported || got.ExitCode != 1 {
		t.Errorf("expected supported with exit_code 1, got supported=%v exit=%d", got.Supported, got.ExitCode)
	}

	var parsed struct {
		Secrets []struct {
			Name    string `json:"name"`
			Present bool   `json:"present"`
		} `json:"secrets"`
		MissingCount int `json:"missing_count"`
	}
	if err := json.Unmarshal(got.Report, &parsed); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if parsed.MissingCount != 1 || len(parsed.Secrets) != 1 || parsed.Secrets[0].Present {
		t.Errorf("forge's presence facts must pass through unchanged, got %+v", parsed)
	}
}

// TestForgeResponseStructsCarryNoSecretValueFields pins the structural half of
// the secrets guard. forge's own report cannot express a value; this asserts
// that the daemon's wrapper does not introduce a field that could. It is a
// reflection test rather than a review convention precisely because the risk is
// a future edit adding an innocent-looking passthrough field.
func TestForgeResponseStructsCarryNoSecretValueFields(t *testing.T) {
	// Substrings that would indicate a field capable of carrying a secret.
	// "values" is deliberately absent from the report side: forge's shape is
	// passed through opaquely as json.RawMessage, so the only fields that
	// can exist here are the ones this package declares.
	banned := []string{"value", "secret_value", "plaintext", "content", "data", "payload", "env_vars"}

	var walk func(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool)
	walk = func(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool) {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true

		// json.RawMessage is the opaque passthrough of forge's own
		// document. It is a []byte, has no fields of ours, and is what
		// makes this wrapper incapable of naming a value field.
		if typ == reflect.TypeOf(json.RawMessage{}) {
			return
		}

		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if tag == "" || tag == "-" {
				tag = field.Name
			}
			lower := strings.ToLower(tag)
			for _, bad := range banned {
				if lower == bad || strings.HasSuffix(lower, "_"+bad) {
					t.Errorf("%s.%s (json:%q) is a value-shaped field — the secret report must never be able to carry a value",
						path, field.Name, tag)
				}
			}
			walk(t, field.Type, path+"."+field.Name, seen)
		}
	}

	for _, typ := range []reflect.Type{
		reflect.TypeOf(forgeReportResponse{}),
		reflect.TypeOf(forgeResponseMeta{}),
		reflect.TypeOf(forgeSecretListRequest{}),
	} {
		walk(t, typ, typ.Name(), map[reflect.Type]bool{})
	}
}

// --- unsupported-detection unit coverage ---

func TestForgeUnsupportedReasonClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  forgeCommandResult
		want bool
	}{
		{
			name: "unknown flag",
			res:  forgeCommandResult{Stderr: []byte("unknown flag: --json"), ExitCode: 1},
			want: true,
		},
		{
			name: "unknown command",
			res:  forgeCommandResult{Stderr: []byte(`unknown command "topology" for "reliant forge env"`), ExitCode: 1},
			want: true,
		},
		{
			name: "unknown shorthand flag",
			res:  forgeCommandResult{Stderr: []byte("unknown shorthand flag: 'C' in -C"), ExitCode: 1},
			want: true,
		},
		{
			// A real runtime failure is NOT a version problem. Calling
			// it one would tell the user to upgrade forge over a
			// broken KCL file.
			name: "ordinary failure is not a version problem",
			res:  forgeCommandResult{Stderr: []byte("render KCL: boom"), ExitCode: 1},
			want: false,
		},
		{
			name: "exit zero is never unsupported",
			res:  forgeCommandResult{Stderr: []byte("unknown flag: --json"), ExitCode: 0},
			want: false,
		},
		{
			name: "a report present means forge answered",
			res:  forgeCommandResult{Stdout: []byte(`{"ok":true}`), Stderr: []byte("unknown flag: --x"), ExitCode: 1},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got := forgeUnsupportedReason(tc.res)
			if got != tc.want {
				t.Errorf("unsupported = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestForgeStderrExcerptIsBoundedAndSingleLine(t *testing.T) {
	// Errors land in a UI toast; an unbounded multi-line forge failure is
	// not something to paste into one.
	long := strings.Repeat("abcde\n", 500)
	got := stderrExcerpt([]byte(long))
	if strings.Contains(got, "\n") {
		t.Error("excerpt must be single-line")
	}
	if len(got) > forgeStderrExcerptLimit+8 {
		t.Errorf("excerpt must be bounded, got %d bytes", len(got))
	}
	if stderrExcerpt([]byte("   \n  ")) != "" {
		t.Error("blank stderr must produce an empty excerpt")
	}
}

func TestForgeVersionIsStampedOnEveryResponse(t *testing.T) {
	// The version stamp is what lets the UI say "your forge is too old for
	// this view" instead of rendering a blank screen, so it must be present
	// on every path — including the two that never run forge.
	t.Run("non-forge project", func(t *testing.T) {
		stubForge(t, forgeCommandResult{}, nil)
		raw, err := handle(t, "forge.audit", forgeAuditRequest{ProjectPath: t.TempDir()})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decodeForgeResponse(t, raw).ForgeVersion == "" {
			t.Error("forge_version must be stamped even for a non-forge project")
		}
	})

	t.Run("successful report", func(t *testing.T) {
		dir := forgeProject(t)
		stubForge(t, forgeCommandResult{Stdout: []byte(`{"ok":true}`)}, nil)
		raw, err := handle(t, "forge.audit", forgeAuditRequest{ProjectPath: dir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decodeForgeResponse(t, raw).ForgeVersion == "" {
			t.Error("forge_version must be stamped on a successful report")
		}
	})
}
