package cli

// Tests for `forge env status` as the ONE read view (ADR docs/adr/env-verbs.md,
// task V4).
//
// `env status`, `env verify`, `env wait`, `env rollout`, `env topology` and
// `env history` were six views of one question, and a reader had to know which
// verb owned which half before they could ask it. They are now modes of one
// command, and these tests pin the two properties that make the merge safe:
//
//   - every mode still reaches the SAME run function the deleted verb called,
//     with the same options — so no behaviour was reimplemented in the move;
//   - every exit code the deleted verbs produced survives, including the
//     distinctions the whole table exists for (1 "we looked and it is wrong"
//     vs 2 "we could not look", and 5/6 which are deliberately not 1).
//
// The deleted spellings are pinned too: a command that still resolves would
// make the removalguard entry a lie.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// ─── The mode surface ────────────────────────────────────────────────────────

// The five absorbed verbs are GONE from `forge env`, with no alias and no
// hidden name. Pre-1.0: a reader who types the old spelling gets an error that
// names the replacement, not a command that quietly still works.
func TestEnvStatus_AbsorbedVerbsAreDeleted(t *testing.T) {
	env := newEnvCmd()
	for _, verb := range []string{"verify", "wait", "rollout", "topology", "history"} {
		found, _, err := env.Find([]string{verb})
		// cobra's Find falls back to the group itself for an unknown
		// child, so "resolved to something other than env" is the test.
		if err == nil && found != nil && found.Name() == verb {
			t.Errorf("`forge env %s` still resolves — it was absorbed into `forge env status`", verb)
		}
	}
	// And status is still there, carrying the modes that absorbed them.
	status, _, err := env.Find([]string{"status"})
	if err != nil || status.Name() != "status" {
		t.Fatalf("`forge env status` must resolve, got %v (%v)", status, err)
	}
	for _, flag := range []string{"wait", "history", "json"} {
		if status.Flags().Lookup(flag) == nil {
			t.Errorf("`forge env status` has no --%s flag", flag)
		}
	}
}

// Without an env, status shows every env — the old `env topology`. With one,
// it reports that env. So the positional argument is OPTIONAL, which is the
// one arity change the merge makes.
func TestEnvStatus_EnvArgumentIsOptional(t *testing.T) {
	cmd := newEnvStatusCmd()
	if err := cmd.Args(cmd, []string{}); err != nil {
		t.Errorf("no env must be accepted (it is the all-envs view): %v", err)
	}
	if err := cmd.Args(cmd, []string{"prod"}); err != nil {
		t.Errorf("one env must be accepted: %v", err)
	}
	if err := cmd.Args(cmd, []string{"prod", "staging"}); err != nil {
		t.Errorf("several envs must be accepted (the old `topology staging preprod`): %v", err)
	}
}

// --wait and --history are different questions about the same env, and asking
// both at once has no answer. Refused rather than silently preferring one.
func TestEnvStatus_WaitAndHistoryAreMutuallyExclusive(t *testing.T) {
	cmd := newEnvStatusCmd()
	cmd.SetArgs([]string{"prod", "--wait", "--history"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("--wait with --history must be refused: they are two different reads")
	}
}

// ─── --wait: the old `env wait`, including the old `env rollout` ─────────────

// --wait reaches the SAME wait F3 wrote, with the same options. Asserted at
// the seam rather than against a control plane, so a wait that grew a second
// implementation in the move would fail here.
func TestEnvStatusWait_DelegatesToTheWaitHelper(t *testing.T) {
	var got []envWaitOptions
	prev := runEnvWaitForCmd
	runEnvWaitForCmd = func(_ context.Context, _ string, opts envWaitOptions) error {
		got = append(got, opts)
		return nil
	}
	t.Cleanup(func() { runEnvWaitForCmd = prev })

	cmd := newEnvStatusCmd()
	cmd.SetArgs([]string{"prod", "--wait", "--promotion", "promo-7", "--json", "--fail-fast"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("--wait must run exactly one wait, ran %d", len(got))
	}
	if got[0].PromotionID != "promo-7" || !got[0].JSON || !got[0].FailFast {
		t.Errorf("the wait's flags did not reach it: %+v", got[0])
	}
	if got[0].Once {
		t.Errorf("an unset --timeout is a BLOCKING wait, not a single read: %+v", got[0])
	}
	if got[0].Timeout <= 0 {
		t.Errorf("a blocking wait must carry a positive budget, got %s", got[0].Timeout)
	}
}

// `forge env rollout` WAS `env wait --timeout 0`, and that is how it survives:
// a single read on the named promotion, no budget and nothing to fail fast on.
//
// The distinction an explicit 0 makes is only visible here, where cobra's
// Changed is: Timeout's zero value already means "unset, use the default", so
// "0" and "not given" arrive identically in the struct. Both directions are
// bugs — an unset flag becoming a single read would turn every plain --wait
// into a non-blocking poll, and an explicit 0 becoming 15m would make the
// snapshot block for a quarter of an hour.
func TestEnvStatusWait_ExplicitZeroTimeoutIsTheRolloutSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantOnce bool
	}{
		{"unset keeps the default budget", []string{"prod", "--wait"}, false},
		{"explicit zero is a single read", []string{"prod", "--wait", "--timeout", "0"}, true},
		{"explicit zero, long form", []string{"prod", "--wait", "--timeout=0s"}, true},
		{"an explicit non-zero budget blocks", []string{"prod", "--wait", "--timeout", "30s"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen envWaitOptions
			prev := runEnvWaitForCmd
			runEnvWaitForCmd = func(_ context.Context, _ string, opts envWaitOptions) error {
				seen = opts
				return nil
			}
			t.Cleanup(func() { runEnvWaitForCmd = prev })

			cmd := newEnvStatusCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if seen.Once != tc.wantOnce {
				t.Fatalf("Once = %v, want %v (args %v)", seen.Once, tc.wantOnce, tc.args)
			}
			if !tc.wantOnce && seen.Timeout <= 0 {
				t.Errorf("a blocking wait must carry a positive budget, got %s", seen.Timeout)
			}
		})
	}
}

// Every flag the old `env wait` declared is still reachable, so a pipeline
// written against §3.2 runs after the merge. The defaults are read off the
// command rather than asserted in prose: changing --timeout's default to 0
// looks harmless ("zero means use the default") and would silently turn every
// --wait into a single read.
func TestEnvStatusWait_DeclaresEveryWaitFlag(t *testing.T) {
	cmd := newEnvStatusCmd()
	for _, name := range []string{"promotion", "release", "timeout", "stable-for", "fail-fast", "include-unpinned", "interval", "watch-json"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s is not declared on `forge env status`", name)
		}
	}
	// --timeout's DECLARED default is the zero sentinel, because one flag
	// serves two modes with different budgets. What matters is the budget
	// a --wait actually RESOLVES to, which the test above pins; here we
	// only check the help names both numbers, so "0s" in `--help` is not
	// read as "no timeout".
	timeoutFlag := cmd.Flags().Lookup("timeout")
	for _, want := range []string{envWaitDefaultTimeout.String(), defaultEnvStatusReleaseTimeout.String()} {
		if !strings.Contains(timeoutFlag.Usage, want) {
			t.Errorf("--timeout help does not name the %s default:\n  %s", want, timeoutFlag.Usage)
		}
	}
	if got := cmd.Flags().Lookup("fail-fast").DefValue; got != "false" {
		t.Errorf("--fail-fast default = %s, want false (a transient crash loop must not fail the gate)", got)
	}
	// --promotion and --release name the promotion two different ways.
	cmd.SetArgs([]string{"prod", "--wait", "--promotion", "p-1", "--release", "v1"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "promotion") {
		t.Fatalf("--promotion with --release must be refused, got %v", err)
	}
}

// The wait's exit codes, through the status command, unchanged. 5 and 6 are
// deliberately not 1: "we never saw this finish" and "the release was
// overtaken" are not "the release is bad", and reporting them as a failure
// would turn fine releases red.
func TestEnvStatusWait_ExitCodesAreUnchanged(t *testing.T) {
	cases := []struct {
		phase string
		want  int
	}{
		{wireRolloutPhaseSucceeded, exitOK},
		{wireRolloutPhaseDegraded, exitWrong},
		{wireRolloutPhaseProgressing, exitTimedOut},
		{wireRolloutPhaseSuperseded, exitSuperseded},
		{wireRolloutPhaseUnknown, exitUndetermined},
	}
	for _, tc := range cases {
		t.Run(rolloutPhaseName(tc.phase), func(t *testing.T) {
			fake := newFakeRollout(tc.phase)
			opts := waitOpts(fake)
			opts.Once = true
			opts.Timeout = 0
			var err error
			captureStdout(t, func() { err = runEnvWait(context.Background(), "prod", opts) })
			if got := exitCodeForError(err); got != tc.want {
				t.Fatalf("phase %s: exit %d, want %d (%v)", rolloutPhaseName(tc.phase), got, tc.want, err)
			}
		})
	}
}

// ─── --history: the old `env history` ───────────────────────────────────────

// --history pages the promotion ledger, newest first, through the same reader
// both backends implement.
func TestEnvStatusHistory_PagesTheLedger(t *testing.T) {
	dir := t.TempDir()
	store := testBindings(t, dir)
	for _, v := range []string{"v1", "v2", "v3"} {
		writeBinding(t, dir, "prod", v, map[string]string{"api": sha("a")})
	}

	var buf bytes.Buffer
	if err := runEnvHistory(context.Background(), store, "prod", historyQuery{Limit: 2}, false, &buf); err != nil {
		t.Fatalf("history: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "v3") || !strings.Contains(out, "v2") {
		t.Errorf("the newest page must carry v3 and v2:\n%s", out)
	}
	if strings.Contains(out, " v1 ") {
		t.Errorf("--limit 2 served three entries:\n%s", out)
	}
	// The paging hint names the NEW verb — a hint that names a deleted
	// command is a copy-pasteable line that dies on "unknown command".
	if !strings.Contains(out, "forge env status prod --history --before") {
		t.Errorf("the next-page hint must name the merged verb:\n%s", out)
	}
}

// --release means "this version" in BOTH per-env modes, so it is one flag —
// and it has to reach whichever mode is running. A --release that was wired
// only to the wait would make `--history --release v1` return EVERY
// promotion while looking like it filtered: the worst kind of wrong answer,
// because it is plausible. Asserted through the command's own flag parsing,
// which is the only place the relay happens.
func TestEnvStatusHistory_ReleaseFilterReachesTheQuery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"),
		[]byte("name: demo\nmodule_path: github.com/example/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declareEnvDir(t, dir, "prod")
	t.Chdir(dir)
	// prod: v1, v2, v1 — so a working filter serves TWO entries, and a
	// filter that silently no-ops serves three.
	for _, v := range []string{"v1", "v2", "v1"} {
		writeBinding(t, dir, "prod", v, map[string]string{"api": sha("a")})
	}

	cmd := newEnvStatusCmd()
	var out bytes.Buffer
	cmd.SetArgs([]string{"prod", "--history", "--release", "v1"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("history --release: %v", err)
	}
	if got := strings.Count(out.String(), "v1"); got != 2 {
		t.Errorf("--release v1 served %d v1 rows, want 2 — the filter did not reach the query:\n%s", got, out.String())
	}
	if strings.Contains(out.String(), "v2") {
		t.Errorf("--release v1 served a v2 promotion:\n%s", out.String())
	}
}

// Every --history flag is reachable on the merged command.
func TestEnvStatusHistory_DeclaresEveryHistoryFlag(t *testing.T) {
	cmd := newEnvStatusCmd()
	for _, name := range []string{"limit", "before"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s is not declared on `forge env status`", name)
		}
	}
	// --release is shared with --wait (it means "this version" in both),
	// so it is declared once and asserted by the wait test above.
	if cmd.Flags().Lookup("release") == nil {
		t.Error("--release is not declared on `forge env status`")
	}
}

// ─── The composed default view ──────────────────────────────────────────────

// The default view carries the RELEASE half: the bound release, the per-image
// verify verdicts, and the ledger freshness. Those were `env verify`'s whole
// output, and a status view that dropped them would send the reader back to a
// command that no longer exists.
func TestEnvStatus_CarriesTheReleaseHalf(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1", map[string]string{"ghcr.io/acme/api": sha("a")})
	store := testBindings(t, dir)

	var err error
	out := captureStdout(t, func() {
		err = runEnvStatusRelease(context.Background(), "prod", envStatusOptions{
			Bindings: store,
			Lister:   &stubLister{images: []cluster.WorkloadImage{runningImage("ghcr.io/acme/api", sha("a"))}},
			Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
		})
	})
	if err != nil {
		t.Fatalf("a matching env must exit 0: %v", err)
	}
	for _, want := range []string{"v1", "match"} {
		if !strings.Contains(out, want) {
			t.Errorf("the release half must report %q:\n%s", want, out)
		}
	}
}

// Verify's exit codes survive the merge, and the 1-vs-2 distinction with
// them: 1 is "we looked and it is wrong", 2 is "we could not look". A gate
// that reported a VPN drop and a drifted release with the same code gets
// switched off the first week it is wrong about one of them.
func TestEnvStatus_ReleaseHalfKeepsVerifyExitCodes(t *testing.T) {
	cases := []struct {
		name    string
		running []cluster.WorkloadImage
		listErr error
		want    int
	}{
		{"match", []cluster.WorkloadImage{runningImage("ghcr.io/acme/api", sha("a"))}, nil, exitOK},
		{"drift", []cluster.WorkloadImage{runningImage("ghcr.io/acme/api", sha("9"))}, nil, exitWrong},
		{"missing", nil, nil, exitWrong},
		{"unreachable", nil, errStubUnreachable, exitUndetermined},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeBinding(t, dir, "prod", "v1", map[string]string{"ghcr.io/acme/api": sha("a")})
			var err error
			captureStdout(t, func() {
				err = runEnvStatusRelease(context.Background(), "prod", envStatusOptions{
					Bindings: testBindings(t, dir),
					Lister:   &stubLister{images: tc.running, err: tc.listErr},
					Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
				})
			})
			if got := exitCodeForError(err); got != tc.want {
				t.Fatalf("%s: exit %d, want %d (%v)", tc.name, got, tc.want, err)
			}
		})
	}
}

// A HOSTED env is verified through the control plane's observer (F7's
// GetRollout read), never through kubectl — forge cannot read a hosted
// cluster. A STALE observation is UNREACHABLE → exit 2, never 0: "we cannot
// see it" is not permission.
func TestEnvStatus_HostedReleaseHalfReadsTheObserver(t *testing.T) {
	pin := sha("1")
	_, store := hostedPromoteFixture(t, "v1")
	cur, _, _ := store.Current(context.Background(), "prod")

	cases := []struct {
		name  string
		phase string
		seen  string
		want  int
	}{
		{"observer saw the pin", wireRolloutPhaseSucceeded, pin, exitOK},
		{"observer saw another digest", wireRolloutPhaseProgressing, sha("9"), exitWrong},
		{"stale observation", wireRolloutPhaseUnknown, pin, exitUndetermined},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var askedFor string
			read := func(_ context.Context, _ string, promotionID string) (wireRollout, error) {
				askedFor = promotionID
				return wireRollout{Workloads: []wireWorkloadRollout{{
					Name: "api", Artifact: "api", PinnedDigest: pin, ObservedDigest: tc.seen,
					ObservedState: "DEPLOY_OBSERVED_STATE_READY", Phase: tc.phase,
				}}}, nil
			}
			t.Chdir(t.TempDir())
			var err error
			captureStdout(t, func() {
				err = runEnvStatusRelease(context.Background(), "prod", envStatusOptions{
					Bindings: store, HostedRollout: read,
					Resolver: stubResolver{}, // must NOT be consulted for a hosted env
				})
			})
			if got := exitCodeForError(err); got != tc.want {
				t.Fatalf("exit = %d, want %d (%v)", got, tc.want, err)
			}
			if askedFor != cur.ID {
				t.Errorf("the release half must read the CURRENT promotion %q's rollout, asked for %q", cur.ID, askedFor)
			}
		})
	}
}

// A stale file ledger makes the whole verdict UNDETERMINED (exit 2), above
// drift. A .forge/promotions/<env>.jsonl is committed to git, so a checkout
// that has not pulled compares the cluster against an OLDER promotion: a fine
// deploy reads as DRIFT and a deploy that never happened can read as MATCH.
// Neither is evidence about the release, so the answer is "could not
// determine" — #369's refusal, carried into the merged view.
func TestEnvStatus_StaleLedgerIsUndetermined(t *testing.T) {
	for _, state := range []ledgerFreshness{ledgerBehind, ledgerDiverged} {
		t.Run(state.String(), func(t *testing.T) {
			err := staleLedgerError("prod", &ledgerFreshnessReport{
				State: state, Ref: "origin/main", Detail: "2 entries behind",
			})
			if got := exitCodeForError(err); got != exitUndetermined {
				t.Fatalf("a %s ledger must be exit %d, got %d (%v)", state, exitUndetermined, got, err)
			}
		})
	}
	// AHEAD is noted, not failed: a release recorded here and not yet
	// merged is a normal state during a release.
	if err := staleLedgerError("prod", &ledgerFreshnessReport{State: ledgerAhead, Ref: "origin/main"}); err != nil {
		t.Errorf("an AHEAD ledger must not fail the read: %v", err)
	}
}

// ─── --json: one F0 envelope ────────────────────────────────────────────────

// --json emits ONE document carrying the F0 envelope, and ok/exit_code are
// computed from the SAME error the text path returns. A pipeline reading
// ok:true from a document whose process exited 1 has no way to discover which
// half is lying, which is the bug the envelope exists to prevent.
func TestEnvStatusJSON_CarriesTheF0Envelope(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1", map[string]string{"ghcr.io/acme/api": sha("a")})

	run := func(running []cluster.WorkloadImage) (envStatusDocument, string) {
		var err error
		out := captureStdout(t, func() {
			err = runEnvStatusRelease(context.Background(), "prod", envStatusOptions{
				JSON:     true,
				Bindings: testBindings(t, dir),
				Lister:   &stubLister{images: running},
				Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
			})
		})
		var doc envStatusDocument
		if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
			t.Fatalf("--json must be parseable: %v\n%s", jerr, out)
		}
		// The envelope must agree with the error the command returned.
		if doc.OK != (err == nil) || doc.ExitCode != exitCodeForError(err) {
			t.Errorf("envelope {ok:%v exit_code:%d} disagrees with err %v (code %d)",
				doc.OK, doc.ExitCode, err, exitCodeForError(err))
		}
		return doc, out
	}

	clean, cleanRaw := run([]cluster.WorkloadImage{runningImage("ghcr.io/acme/api", sha("a"))})
	if !clean.OK || clean.ExitCode != exitOK {
		t.Errorf("a matching env = %+v, want ok/0", clean.jsonEnvelope)
	}
	if clean.Release != "v1" || !clean.Bound {
		t.Errorf("the document must name the bound release, got bound=%v release=%q", clean.Bound, clean.Release)
	}
	// The release fields are FLAT, because `forge gate record --from`
	// recognises this document by top-level `bound` + `images`. Nesting
	// them would make every recorded deploy-verification gate fall through
	// to the generic reading and silently lose the unbound-is-SKIPPED
	// distinction. Asserted on the raw bytes, which is what gate record
	// actually parses.
	if !strings.Contains(cleanRaw, `"bound"`) || !strings.Contains(cleanRaw, `"images"`) {
		t.Errorf("`forge gate record` recognises this document by top-level bound+images:\n%s", cleanRaw)
	}

	drifted, _ := run([]cluster.WorkloadImage{runningImage("ghcr.io/acme/api", sha("9"))})
	if drifted.OK || drifted.ExitCode != exitWrong {
		t.Errorf("a drifted env = %+v, want not-ok/1", drifted.jsonEnvelope)
	}
	if drifted.Error == "" {
		t.Error("a non-OK envelope must carry the reason")
	}
}

// STDOUT CARRIES EXACTLY ONE DOCUMENT. The default view has two halves, and
// the obvious implementation — let each half print its own JSON — produces a
// stream no `jq` invocation can read. Worse, the failure looks like malformed
// JSON rather than like two commands sharing an output, so the reader debugs
// the wrong thing.
//
// Asserted by DECODING THE WHOLE STREAM: a json.Decoder that finds a second
// top-level value is the only check that actually catches this, since
// Unmarshal on the concatenation would stop happily after the first.
func TestEnvStatusJSON_EmitsExactlyOneDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"),
		[]byte("name: demo\nmodule_path: github.com/example/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declareEnvDir(t, dir, "dev")
	writeBinding(t, dir, "dev", "v1", map[string]string{"ghcr.io/acme/api": sha("a")})
	t.Chdir(dir)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t,
		`{"output":{"workloads":[{"name":"api","kind":"service","image":"ghcr.io/acme/api",`+
			`"runtime":{"type":"host"},"spec":{"kind":"service","env":[{"name":"API_PORT","value":"59998"}]}}]}}`))

	out := captureStdout(t, func() {
		_ = runEnvStatus(context.Background(), "dev", envStatusOptions{
			JSON:     true,
			Bindings: testBindings(t, dir),
			Lister:   &stubLister{images: []cluster.WorkloadImage{runningImage("ghcr.io/acme/api", sha("a"))}},
			Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
		})
	})

	dec := json.NewDecoder(strings.NewReader(out))
	var first envStatusDocument
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("the stream must open with one parseable document: %v\n%s", err, out)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == nil {
		t.Fatalf("stdout carries MORE than one JSON document — no jq invocation can read this:\n%s", out)
	}
	// And the one document carries BOTH halves: the release fields flat,
	// the runtime half nested.
	if !first.Bound || first.Release != "v1" {
		t.Errorf("the document lost the release half: bound=%v release=%q", first.Bound, first.Release)
	}
	if first.Runtime == nil {
		t.Fatalf("the document lost the runtime half:\n%s", out)
	}
	if first.Runtime.Env != "dev" {
		t.Errorf("runtime.env = %q, want dev", first.Runtime.Env)
	}
	// The runtime facts the pre-merge `env status --json` tests pinned,
	// one level deeper: a non-nil workloads document (an empty list and
	// "we could not reach the cluster" are different facts) and the
	// runtime checks.
	if first.Runtime.Workloads == nil {
		t.Errorf("runtime.workloads serialised as null — the cluster's state would again be reachable only by parsing prose")
	}
	if len(first.Runtime.Checks) == 0 {
		t.Error("runtime.checks is empty — the checks that moved here from `forge doctor` are gone")
	}
}

// ─── The all-envs view: the old `env topology` ──────────────────────────────

// With no env, status reads every declared env's ledger. Ledger-only by
// default: every image state is `not_verified`, which means UNKNOWN — nothing
// was compared against any cluster, and a consumer rendering that as `match`
// shows a green screen over environments nobody looked at.
func TestEnvStatus_AllEnvsIsLedgerOnlyByDefault(t *testing.T) {
	dir := t.TempDir()
	declareEnvDir(t, dir, "prod")
	writeBinding(t, dir, "prod", "v1", map[string]string{"api": dg("a")})

	report, code, _ := runTopologyJSON(t, nil, envTopologyOptions{ProjectDir: dir})
	if code != exitOK {
		t.Fatalf("the ledger-only all-envs read must exit 0, got %d", code)
	}
	if report.Verified {
		t.Error("verified must be false without --verify")
	}
	if len(report.Environments) != 1 || report.Environments[0].Env != "prod" {
		t.Fatalf("environments = %+v, want [prod]", report.Environments)
	}
}

// The additive F7 fields survive: promotion_id, from_env and gates_summary,
// which is what lets a reader badge a row without walking its gates.
func TestEnvStatus_AllEnvsCarriesF7Fields(t *testing.T) {
	dir := t.TempDir()
	store := testBindings(t, dir)
	got, err := store.Append(context.Background(), release.Promotion{
		Env: "prod", Release: "v1", Kind: release.KindPromote, FromEnv: "staging",
		Resolved: map[string]string{"api": sha("a")},
		Gates:    []release.Gate{{Name: "lint", Status: release.GateStatusPassed}},
	}, appendGuard{})
	if err != nil {
		t.Fatal(err)
	}
	ledgers := func(context.Context, string) (envLedger, error) {
		return envLedger{Bindings: store, Releases: testReleases(t, dir)}, nil
	}
	row := buildTopologyEnvRow(context.Background(), dir, "prod", false, map[string]release.Release{}, nil,
		envTopologyOptions{Ledgers: ledgers})
	if row.PromotionID != got.ID || row.FromEnv != "staging" {
		t.Errorf("promotion_id/from_env = %q/%q, want %q/staging", row.PromotionID, row.FromEnv, got.ID)
	}
	if row.GatesSummary == nil || row.GatesSummary.Passed != 1 {
		t.Errorf("gates_summary = %+v", row.GatesSummary)
	}
	raw, _ := json.Marshal(row)
	for _, key := range []string{`"promotion_id"`, `"from_env"`, `"gates_summary"`, `"release"`, `"bound"`, `"images"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("all-envs row JSON lacks %s:\n%s", key, raw)
		}
	}
}

// A hosted env's row carries the control-plane-computed rollout phase — the
// answer the old `env rollout` printed, in the all-envs view.
func TestEnvStatus_AllEnvsCarriesHostedRolloutPhase(t *testing.T) {
	_, store := hostedPromoteFixture(t, "v1")
	ledgers := func(context.Context, string) (envLedger, error) {
		return envLedger{Bindings: store, Releases: store, Hosted: true}, nil
	}
	rollouts := func(context.Context, string, string) (wireRollout, error) {
		return wireRollout{Phase: wireRolloutPhaseStabilizing}, nil
	}
	row := buildTopologyEnvRow(context.Background(), t.TempDir(), "prod", false, map[string]release.Release{}, nil,
		envTopologyOptions{Ledgers: ledgers, Rollouts: rollouts})
	if row.RolloutPhase != "stabilizing" || row.PromotionID == "" {
		t.Fatalf("rollout_phase/promotion_id = %q/%q", row.RolloutPhase, row.PromotionID)
	}
}

// ─── The runtime half keeps its contract ────────────────────────────────────

// The runtime half REPORTS; it never fails the command. "The app is down" is
// a state status must be able to print, and a status command that exited
// non-zero for it would be unusable as the one read view.
//
// The release half owns the exit code, and these are different questions: an
// env can be perfectly bound and have nothing running locally.
func TestEnvStatus_RuntimeHalfNeverFailsTheCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: demo\nmodule_path: github.com/example/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declareEnvDir(t, dir, "dev")
	t.Chdir(dir)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t,
		`{"output":{"workloads":[{"name":"api","kind":"service","image":"api",`+
			`"runtime":{"type":"host"},"spec":{"kind":"service","env":[{"name":"API_PORT","value":"59999"}]}}]}}`))

	// Nothing is listening on 59999, so every runtime probe is down.
	var err error
	captureStdout(t, func() { err = runUpServices(context.Background(), "dev", false, "", false) })
	if err != nil {
		t.Fatalf("a down stack is a STATE, not a failure: %v", err)
	}
}

// The --signal arms survive on the merged command: `forge doctor` redirects
// here for the runtime question, and that redirect must not dangle.
func TestEnvStatus_CarriesTheRuntimeSignals(t *testing.T) {
	cmd := newEnvStatusCmd()
	flag := cmd.Flags().Lookup("signal")
	if flag == nil {
		t.Fatal("`forge env status` has no --signal flag, but `forge doctor` redirects to it")
	}
	for _, sig := range []string{"app", "metrics", "traces", "logs", "profiles"} {
		if !strings.Contains(flag.Usage, sig) {
			t.Errorf("`forge env status --signal` help does not name %q:\n  %s", sig, flag.Usage)
		}
	}
}

// ─── Help text ──────────────────────────────────────────────────────────────

// The help names every mode and every exit code a pipeline branches on. This
// is the only place a reader learns that the six verbs became one, so a help
// text that omitted a mode would leave the absorbed behaviour undiscoverable.
func TestEnvStatus_HelpNamesEveryModeAndExitCode(t *testing.T) {
	long := newEnvStatusCmd().Long
	for _, want := range []string{"--wait", "--history", "--json"} {
		if !strings.Contains(long, want) {
			t.Errorf("the help does not name %s", want)
		}
	}
	// The wait outcomes, which are the codes CI branches on directly:
	// superseded, queued and timed out. Timed out is 8, not 5 — 5 has been
	// the deploy's plan confirmation since O-13 (exitcodes.go), and a help
	// that still said 5 sent a pipeline to branch on a code the wait never
	// returns.
	for _, want := range []string{"  6  ", "  7  ", "  8  "} {
		if !strings.Contains(long, want) {
			t.Errorf("the help does not name exit code %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(long, "  5  --wait only") {
		t.Error("the help still names 5 as the wait's timeout; it is 8")
	}
	// And it must not point at a command that no longer exists.
	for _, dead := range []string{"forge env verify", "forge env wait", "forge env rollout",
		"forge env topology", "forge env history"} {
		if strings.Contains(long, dead) {
			t.Errorf("the help still names the deleted `%s`", dead)
		}
	}
}

// runningImage is one workload running one image, in the shape the cluster
// lister reports. A helper rather than a literal because every case here
// states "this image, at this digest" and nothing else about the workload.
func runningImage(image, digest string) cluster.WorkloadImage {
	return cluster.WorkloadImage{Kind: "Deployment", Name: "api", Container: "api", Image: image + "@" + digest}
}

var errStubUnreachable = errors.New("stub: cluster unreachable")
