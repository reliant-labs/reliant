package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// asForgeBinary makes Name() "forge" whether or not a root command has
// recorded its mount route, as it is in the shipped binary. Under `go test`
// os.Args[0] is cli.test, so an explanation built before a command ran says
// "cli.test forge" and one built during it "cli.test".
func asForgeBinary(t *testing.T) {
	t.Helper()
	old := os.Args[0]
	os.Args[0] = "forge"
	t.Cleanup(func() { os.Args[0] = old })
}

func runExplain(t *testing.T, args ...string) (string, error) {
	t.Helper()
	asForgeBinary(t)
	root := newTestRootCmd(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"explain"}, args...))
	err := root.Execute()
	return out.String(), err
}

// A known reason prints the explain_error tool's text: meaning, the next
// command naming --env, and whether a retry helps.
func TestExplainCmd_Reason(t *testing.T) {
	out, err := runExplain(t, cloud.ReasonClaimableThrottled, "--env", "staging")
	if err != nil {
		t.Fatal(err)
	}
	want := explainForgeError(-1, cloud.ReasonClaimableThrottled, "", "staging").text() + "\n"
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	for _, s := range []string{"retry_at", "run: " + Name() + " env deploy staging --claimable", "retry as is: later"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

// An exit code alone gets that code's answer; a reason this forge does not
// know falls back to the code given beside it, in either order.
func TestExplainCmd_ExitCode(t *testing.T) {
	asForgeBinary(t)
	want := explainForgeError(exitTimedOut, "", "", "prod").text() + "\n"
	for _, args := range [][]string{
		{strconv.Itoa(exitTimedOut)},
		{"a_reason_from_a_newer_forge", strconv.Itoa(exitTimedOut)},
		{strconv.Itoa(exitTimedOut), "a_reason_from_a_newer_forge"},
	} {
		out, err := runExplain(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out != want {
			t.Errorf("%v: output:\n%s\nwant:\n%s", args, out, want)
		}
	}
	// A known reason decides over the code, as in explain_error.
	out, err := runExplain(t, cloud.ReasonClaimableThrottled, strconv.Itoa(exitRefused))
	if err != nil || !strings.Contains(out, "retry_at") {
		t.Errorf("reason with code: %v\n%s", err, out)
	}
}

// --error explains the texts forge prints without an error_reason.
func TestExplainCmd_ErrorText(t *testing.T) {
	out, err := runExplain(t, "--error", "Error: forge.yaml not found in current directory")
	if err != nil || !strings.Contains(out, "not a forge project") {
		t.Errorf("--error: %v\n%s", err, out)
	}
}

// Nothing known exits 1 (exitWrong) and names every known reason and code;
// so do malformed arguments.
func TestExplainCmd_Unknown(t *testing.T) {
	for _, args := range [][]string{
		{"no_such_reason"},
		{"99"},
		{"no_such_reason", "99"},
		{"--error", "something forge never prints"},
		{"1", "2"},
		{"a_reason", "another_reason"},
	} {
		out, err := runExplain(t, args...)
		var ec exitCodeError
		if !errors.As(err, &ec) || ec.ExitCode() != exitWrong {
			t.Errorf("%v: err = %v, want an exitCodeError with code %d", args, err, exitWrong)
			continue
		}
		if out != "" {
			t.Errorf("%v: printed to stdout on failure:\n%s", args, out)
		}
	}
	_, err := runExplain(t, "no_such_reason")
	for _, s := range append(knownErrorReasons(), Name()+" explain", "9") {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("unknown-input error lacks %q:\n%v", s, err)
		}
	}
}

// --json is the document explain_error returns as structured content, for
// the same input.
func TestExplainCmd_JSONMatchesMCPTool(t *testing.T) {
	out, err := runExplain(t, cloud.ReasonClaimableThrottled, "4", "--env", "staging", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var cli map[string]any
	if err := json.Unmarshal([]byte(out), &cli); err != nil {
		t.Fatalf("--json is not JSON: %v\n%s", err, out)
	}
	cs := connectMCP(t, t.TempDir())
	text, mcpDoc, isErr, err := callMCPStructured(context.Background(), t, cs, "explain_error",
		map[string]any{"exit_code": 4, "error_reason": cloud.ReasonClaimableThrottled, "env": "staging"}, nil)
	if err != nil || isErr {
		t.Fatalf("explain_error: %v\n%s", err, text)
	}
	if !reflect.DeepEqual(cli, mcpDoc) {
		t.Errorf("forge explain --json:\n%v\nexplain_error:\n%v", cli, mcpDoc)
	}
}

// The content check's refusals of an unclaimed deploy are explained, the same
// way by `forge explain --json` and explain_error: a refusal claiming may
// lift (not retryable as is), and an unavailable check (retry later). The
// reason alone decides: the test names no exit code, and the answer is the
// same whichever code accompanies the reason.
func TestExplainCmd_ClaimableContentReasons(t *testing.T) {
	cs := connectMCP(t, t.TempDir())
	for _, tc := range []struct {
		reason, retry string
		mentions      []string
	}{
		{cloud.ReasonClaimableContentRefused, "no", []string{"Claim:", "claiming lifts the refusal", "change it"}},
		{cloud.ReasonClaimableScanUnavailable, "later", []string{"again later"}},
	} {
		ex, ok := explainForgeErrorMatch(-1, tc.reason, "", "prod")
		if !ok {
			t.Errorf("%s: not explained", tc.reason)
			continue
		}
		for _, code := range []int{exitWrong, exitRefused} {
			if got, _ := explainForgeErrorMatch(code, tc.reason, "", "prod"); !reflect.DeepEqual(got, ex) {
				t.Errorf("%s with exit %d: %+v, want the reason's answer %+v", tc.reason, code, got, ex)
			}
		}
		if ex.Retry != tc.retry {
			t.Errorf("%s: retry = %q, want %q", tc.reason, ex.Retry, tc.retry)
		}
		for _, s := range tc.mentions {
			if !strings.Contains(ex.Meaning, s) {
				t.Errorf("%s: meaning lacks %q: %s", tc.reason, s, ex.Meaning)
			}
		}
		out, err := runExplain(t, tc.reason, "--env", "prod", "--json")
		if err != nil {
			t.Fatalf("%s: %v", tc.reason, err)
		}
		var cli map[string]any
		if err := json.Unmarshal([]byte(out), &cli); err != nil {
			t.Fatalf("%s: --json is not JSON: %v\n%s", tc.reason, err, out)
		}
		text, mcpDoc, isErr, err := callMCPStructured(context.Background(), t, cs, "explain_error",
			map[string]any{"error_reason": tc.reason, "env": "prod"}, nil)
		if err != nil || isErr {
			t.Fatalf("%s: explain_error: %v\n%s", tc.reason, err, text)
		}
		if !reflect.DeepEqual(cli, mcpDoc) {
			t.Errorf("%s: forge explain --json:\n%v\nexplain_error:\n%v", tc.reason, cli, mcpDoc)
		}
	}
}

// With nothing to explain it lists every reason and code the table knows,
// in text and as JSON, and succeeds.
func TestExplainCmd_List(t *testing.T) {
	out, err := runExplain(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range knownErrorReasons() {
		if !strings.Contains(out, "  "+r+" ") {
			t.Errorf("listing lacks reason %q", r)
		}
	}
	for _, c := range knownExitCodes() {
		if !strings.Contains(out, "\n  "+strconv.Itoa(c)+"  ") {
			t.Errorf("listing lacks exit code %d", c)
		}
	}

	out, err = runExplain(t, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var l explainListing
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("--json listing: %v\n%s", err, out)
	}
	if len(l.ErrorReasons) != len(knownErrorReasons()) || len(l.ExitCodes) != len(knownExitCodes()) {
		t.Fatalf("listing has %d reasons and %d codes, want %d and %d",
			len(l.ErrorReasons), len(l.ExitCodes), len(knownErrorReasons()), len(knownExitCodes()))
	}
	for _, r := range l.ErrorReasons {
		if want := explainForgeError(-1, r.ErrorReason, "", "prod"); !reflect.DeepEqual(r.forgeErrorExplanation, want) || r.Retry == "" {
			t.Errorf("listed %s = %+v, want %+v", r.ErrorReason, r.forgeErrorExplanation, want)
		}
	}
}

// The table covers every exit code exitcodes.go names, and a listed reason
// never repeats.
func TestExplainTableCoversEveryExitCode(t *testing.T) {
	for code := exitOK; code <= exitNeedsHuman; code++ {
		if _, ok := explainForgeErrorMatch(code, "", "", "prod"); !ok {
			t.Errorf("exit %d has no explanation", code)
		}
	}
	seen := map[string]bool{}
	for _, r := range knownErrorReasons() {
		if seen[r] {
			t.Errorf("reason %q is listed twice", r)
		}
		seen[r] = true
	}
	if _, ok := explainForgeErrorMatch(-1, "", "", "prod"); ok {
		t.Error("nothing at all matched")
	}
}
