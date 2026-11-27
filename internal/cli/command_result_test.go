package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/benefits"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/exitcode"
)

// named spells "forge " the way this binary's Name() does.
func named(s string) string { return strings.ReplaceAll(s, "forge ", Name()+" ") }

func TestCommandResultLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    commandResult
		want string
	}{
		{"ok, keys in order, next last",
			commandResult{Status: "ok", Env: "prod", URL: "https://shop.sites.reliantlabs.dev", Release: "v0.3.0", Next: "forge obs check prod --wait"},
			"RESULT: ok env=prod url=https://shop.sites.reliantlabs.dev release=v0.3.0 next: forge obs check prod --wait"},
		{"a value with spaces is quoted",
			commandResult{Status: "failed", Stage: "build", Env: "prod", Reason: `npm run build exited 1: "next" not found`, Next: "fix it"},
			`RESULT: failed stage=build env=prod reason="npm run build exited 1: \"next\" not found" next: fix it`},
		{"newlines collapse",
			commandResult{Status: "queued", Env: "prod", Action: "https://x/y", Next: "hand it\n  to a person"},
			"RESULT: queued env=prod action=https://x/y next: hand it to a person"},
		{"no status reads as failed, never ok",
			commandResult{Env: "dev"},
			"RESULT: failed env=dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Line(); got != tc.want {
				t.Errorf("Line()\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestResultStatusFollowsTheExitCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "ok"},
		{errors.New("boom"), "failed"},
		{&exitCodeError{code: exitUndetermined, msg: "x"}, "undetermined"},
		{&exitCodeError{code: exitPlanUnconfirmed, msg: "x"}, "unconfirmed"},
		{&deployQueuedError{Env: "prod"}, "queued"},
		{&exitCodeError{code: exitTimedOut, msg: "x"}, "pending"},
		{&exitCodeError{code: exitConflict, msg: "x"}, "failed"},
		{&exitCodeError{code: 0, msg: "declined"}, "declined"},
		{fmt.Errorf("up: %w", context.Canceled), "cancelled"},
	} {
		if got := resultStatus(tc.err); got != tc.want {
			t.Errorf("resultStatus(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// The line rides the error so it is the last line under any host, and the
// error keeps its identity and exit code.
func TestWithResultLineIsLastAndKeepsTheExitCode(t *testing.T) {
	inner := &exitCodeError{code: exitQueued, msg: "deploy to prod is QUEUED\n  billing: https://x"}
	err := withResultLine(inner, commandResult{Status: "queued", Env: "prod"})
	lines := strings.Split(err.Error(), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "RESULT: queued env=prod") {
		t.Errorf("last line = %q, want the RESULT line", last)
	}
	if exitcode.Of(err) != exitQueued {
		t.Errorf("exit code = %d, want %d", exitcode.Of(err), exitQueued)
	}
	var target *exitCodeError
	if !errors.As(err, &target) {
		t.Error("the wrapped error is no longer reachable")
	}
	// Wrapping twice adds no second line.
	if again := withResultLine(err, commandResult{Status: "failed"}); strings.Count(again.Error(), "RESULT:") != 1 {
		t.Errorf("wrapped twice:\n%s", again)
	}
	if withResultLine(nil, commandResult{}) != nil {
		t.Error("a nil error must stay nil")
	}
}

func TestReasonAndFixOf(t *testing.T) {
	err := errors.New(strings.Join([]string{
		"build env prod for deploy: hosted static site web: push failed",
		"  more detail",
		"  fix: forge login, then forge env deploy prod",
		"",
	}, "\n"))
	if got := reasonOf(err); got != "build env prod for deploy: hosted static site web: push failed" {
		t.Errorf("reasonOf = %q", got)
	}
	if got := fixOf(err); got != "forge login, then forge env deploy prod" {
		t.Errorf("fixOf = %q", got)
	}
	if fixOf(errors.New("no remedy here")) != "" {
		t.Error("fixOf invented a remedy")
	}
	long := errors.New(strings.Repeat("x", 400))
	if n := len([]rune(reasonOf(long))); n != maxResultReason {
		t.Errorf("reason is %d runes, want it bounded to %d", n, maxResultReason)
	}
}

func TestResultReasonIncludesTheDetailIntroducedByAColon(t *testing.T) {
	t.Parallel()
	err := errStackHostsThisCommand("dev", []ancestorSkip{{pid: 300, command: "reliant serve"}})
	if got := reasonOf(err); got != "pid 300 (reliant serve)" {
		t.Errorf("restart refusal loses its cause in the RESULT reason: %q", got)
	}
	err = errors.New("failed to build:\n\n  link exited 1\n  fix: inspect log")
	if got := reasonOf(err); got != "failed to build: link exited 1" {
		t.Errorf("RESULT reason = %q, want exactly one detail line", got)
	}
}

// ─── env deploy ─────────────────────────────────────────────────────────────

func TestFinishDeployOutcome(t *testing.T) {
	ctx, o := withDeployOutcome(context.Background(), "prod")
	noteDeployPhase(ctx, "build")
	noteDeployRelease(ctx, "v0.3.0")

	t.Run("a failure carries the stage it failed in, last", func(t *testing.T) {
		err := finishDeployOutcome(ctx, o, deployCmdFlags{}, errors.New("npm run build exited 1"), &bytes.Buffer{})
		lines := strings.Split(err.Error(), "\n")
		want := named(`RESULT: failed stage=build env=prod release=v0.3.0 reason="npm run build exited 1" next: fix the error above, then forge env deploy prod`)
		if got := lines[len(lines)-1]; got != want {
			t.Errorf("last line\n got %s\nwant %s", got, want)
		}
	})
	t.Run("a queued deploy names the action link, not a stage", func(t *testing.T) {
		q := &deployQueuedError{Env: "prod", Release: "v0.3.0", Holds: []deploytarget.HostedHold{{ActionURL: "https://forge.reliantlabs.io/billing"}}}
		err := finishDeployOutcome(ctx, o, deployCmdFlags{}, q, &bytes.Buffer{})
		if exitcode.Of(err) != exitQueued {
			t.Fatalf("exit code = %d, want 7", exitcode.Of(err))
		}
		last := err.Error()[strings.LastIndex(err.Error(), "\n")+1:]
		if !strings.HasPrefix(last, "RESULT: queued env=prod release=v0.3.0 action=https://forge.reliantlabs.io/billing") || strings.Contains(last, "stage=") {
			t.Errorf("last line = %s", last)
		}
	})
	t.Run("--json and the previews print nothing", func(t *testing.T) {
		boom := errors.New("boom")
		for _, f := range []deployCmdFlags{{jsonOut: true}, {dryRun: true}, {explain: true}, {promote: promoteCmdFlags{plan: true}}, {promote: promoteCmdFlags{planOnly: true}}} {
			var out bytes.Buffer
			if err := finishDeployOutcome(ctx, o, f, boom, &out); err != boom || out.Len() != 0 {
				t.Errorf("%+v: err=%v out=%q, want the error untouched and no output", f, err, out.String())
			}
		}
	})
	t.Run("a self-managed success ends with the line", func(t *testing.T) {
		var out bytes.Buffer
		if err := finishDeployOutcome(ctx, o, deployCmdFlags{}, nil, &out); err != nil {
			t.Fatal(err)
		}
		want := named("RESULT: ok env=prod release=v0.3.0 next: forge env status prod\n")
		if !strings.HasSuffix(out.String(), want) {
			t.Errorf("output %q does not end with %q", out.String(), want)
		}
	})
	t.Run("a claimable success names the link and how to see it again", func(t *testing.T) {
		cctx, co := withDeployOutcome(context.Background(), "prod")
		noteDeployClaim(cctx, &deployClaimJSON{URL: "https://amber-quokka.reliantpreview.dev", ClaimURL: "https://app.reliant.dev/claim#rcl_x"})
		var out bytes.Buffer
		if err := finishDeployOutcome(cctx, co, deployCmdFlags{}, nil, &out); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"RESULT: ok env=prod url=https://amber-quokka.reliantpreview.dev claim=https://app.reliant.dev/claim#rcl_x", "env status prod --claimable"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out.String())
			}
		}
		if strings.Contains(out.String(), "Included with this release") {
			t.Errorf("a claimable deploy got the signed-in facts block:\n%s", out.String())
		}
	})
}

// The facts are the benefits' own titles: each id must be a shipped,
// enforced benefit the deploy shape really has.
func TestDeployFactIDsAreShippedBenefits(t *testing.T) {
	reg, err := benefits.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{serviceDeployFactIDs, staticDeployFactIDs} {
		if got := benefitTitles(reg, ids); len(got) != len(ids) {
			t.Errorf("ids %v resolve to %d enforced benefits (%v): rename the id here or the benefit", ids, len(got), got)
		}
	}
}

func TestDeployFactLines(t *testing.T) {
	t.Setenv("FORGE_CONSOLE_URL", "https://forge.reliantlabs.io")
	service := deployFactLines(deployFactsInput{Env: "prod", Release: "v0.3.0", Endpoint: "https://api.example", Project: "shop", Service: true})
	static := deployFactLines(deployFactsInput{Env: "prod", Release: "v0.3.0", Endpoint: "https://api.example", Project: "shop", Static: true})
	for name, lines := range map[string][]string{"service": service, "static": static} {
		// A leading blank line, then at most four.
		if n := len(lines) - 1; n < 2 || n > 4 {
			t.Errorf("%s block is %d lines, want 2-4:\n%s", name, n, strings.Join(lines, "\n"))
		}
	}
	joined := strings.Join(service, "\n")
	for _, want := range []string{"Every request traced and measured", "Grouped errors with real stacks", "Dashboards: https://forge.reliantlabs.io/forge/p/shop/observability/dashboards?env=prod"} {
		if !strings.Contains(joined, want) {
			t.Errorf("service block lacks %q:\n%s", want, joined)
		}
	}
	if joined := strings.Join(static, "\n"); !strings.Contains(joined, "Release: https://forge.reliantlabs.io/forge/p/shop/releases/v0.3.0?env=prod") || strings.Contains(joined, "traced") {
		t.Errorf("static block names the wrong things:\n%s", joined)
	}
	if lines := deployFactLines(deployFactsInput{Env: "prod"}); lines != nil {
		t.Errorf("an env running neither shape got facts: %v", lines)
	}
	// A control plane forge knows no web app for gets no link at all.
	t.Setenv("FORGE_CONSOLE_URL", "")
	if joined := strings.Join(deployFactLines(deployFactsInput{Env: "prod", Endpoint: "https://cp.self-hosted.example", Project: "shop", Service: true}), "\n"); strings.Contains(joined, "https://") {
		t.Errorf("a self-hosted control plane was sent to a console:\n%s", joined)
	}
}

// ─── env build --release ───────────────────────────────────────────────────

func TestFinishReleaseCut(t *testing.T) {
	var out bytes.Buffer
	if err := finishReleaseCut(&out, "prod", "v1.4.0", false, nil); err != nil {
		t.Fatal(err)
	}
	if want := named("RESULT: ok env=prod release=v1.4.0 next: forge env deploy prod v1.4.0\n"); !strings.HasSuffix(out.String(), want) {
		t.Errorf("output %q, want it to end %q", out.String(), want)
	}
	err := finishReleaseCut(&bytes.Buffer{}, "prod", "v1.4.0", true, errors.New("no image digest was captured for api"))
	if last := err.Error()[strings.LastIndex(err.Error(), "\n")+1:]; !strings.HasPrefix(last, `RESULT: failed stage=cut env=prod release=v1.4.0 reason="no image digest was captured for api"`) {
		t.Errorf("last line = %s", last)
	}
}

// ─── env up ─────────────────────────────────────────────────────────────────

func TestUpResult(t *testing.T) {
	rows := []upServiceRow{
		{Name: "api", Kind: "host", URL: "http://localhost:8080", Port: 8080},
		{Name: "web", Kind: "frontend", URL: "http://localhost:3000", Port: 3000},
	}
	if got, want := upResult("dev", rows, true).Line(), named("RESULT: ok env=dev url=http://localhost:3000 next: forge obs endpoints dev"); got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	site := []upServiceRow{{Name: "web", Kind: "frontend", URL: "http://localhost:3000", Port: 3000}}
	if got := upResult("dev", site, true).Line(); got != named("RESULT: ok env=dev url=http://localhost:3000 next: forge env status dev") {
		t.Errorf("site: %s", got)
	}

	err := upResultOnError(errors.New("api did not bind :8080 within 2m0s"), "dev", "readiness")
	if last := err.Error()[strings.LastIndex(err.Error(), "\n")+1:]; last != named(`RESULT: failed stage=readiness env=dev reason="api did not bind :8080 within 2m0s" next: fix the error above, then forge env up dev`) {
		t.Errorf("last line = %s", last)
	}
	if upResultOnError(nil, "dev", "") != nil {
		t.Error("a nil error must stay nil")
	}
}

// The first span that ended in an error is the stage a failed run reports.
func TestUpTimingsFailedStage(t *testing.T) {
	tm := newUpTimings(func() time.Time { return time.Unix(0, 0) })
	ctx := withUpTimings(context.Background(), tm)
	_, ok := startUpSpan(ctx, "render")
	ok.end(nil)
	_, inner := startUpSpan(ctx, "database")
	inner.end(errors.New("port taken"))
	_, outer := startUpSpan(ctx, "infra")
	outer.end(errors.New("infra failed"))
	if got := tm.failedStage(); got != "database" {
		t.Errorf("failedStage = %q, want the first failure, database", got)
	}
	var none *upTimings
	if none.failedStage() != "" {
		t.Error("a nil recorder has no stage")
	}
}
