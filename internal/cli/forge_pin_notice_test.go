package cli

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// noticeStore gives the pin-notice throttle a stamp directory of the test's
// own and a clock it moves (main_test.go's default keeps no stamps, so every
// notice prints).
func noticeStore(t *testing.T) (advance func(time.Duration)) {
	t.Helper()
	prevDir, prevNow := pinNoticeDir, pinNoticeNow
	t.Cleanup(func() { pinNoticeDir, pinNoticeNow = prevDir, prevNow })
	dir := t.TempDir()
	pinNoticeDir = func() (string, error) { return dir, nil }
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.Local)
	pinNoticeNow = func() time.Time { return now }
	t.Setenv(pinNoticeEnv, "")
	return func(d time.Duration) { now = now.Add(d) }
}

// Pause the first claimant before it reads or writes the stamp. The second
// invocation must not also claim the notice, even with no stamp on disk yet.
func TestPinNoticeDue_ConcurrentClaim(t *testing.T) {
	advance := noticeStore(t)
	for _, day := range []string{"missing stamp", "previous day's stamp"} {
		t.Run(day, func(t *testing.T) {
			now := pinNoticeNow()
			previousNow := pinNoticeNow
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			pinNoticeNow = func() time.Time {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				return now
			}
			result := make(chan bool, 1)
			go func() { result <- pinNoticeDue("handoff", "project", "pin", "binary", false) }()
			<-entered
			second := pinNoticeDue("handoff", "project", "pin", "binary", false)
			close(release)
			first := <-result
			pinNoticeNow = previousNow
			if !first || second {
				t.Errorf("concurrent claims: first=%v second=%v, want true/false", first, second)
			}
			if pinNoticeDue("handoff", "project", "pin", "binary", false) {
				t.Error("notice remained due after the claimant wrote the stamp")
			}
		})
		advance(24 * time.Hour)
	}
}

const handOffNoticeLead = "forge: running forge_version " + skewTestPin + ", which forge.yaml pins ("

// Friction item #49 (Ranger build): the hand-off notice printed before every
// command. It is news once a day per (project, pin, this forge); every run is
// still handed over. A new day, another forge or another project prints it
// again, and FORGE_PIN_NOTICE=always prints it every time.
func TestHandOffNotice_PrintsOnceADayPerProjectPinAndForge(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, _ := armPin(t, pinCached)
	advance := noticeStore(t)

	run := func(what string, wantNotice bool) {
		t.Helper()
		*h = handoff{}
		handed, out, err := handOffForTest(t, "skill", "load", "forge")
		if err != nil || !handed || h.path == "" {
			t.Fatalf("%s: handed %v, err %v — every run is handed over, printed or not", what, handed, err)
		}
		if got := strings.Contains(out, handOffNoticeLead); got != wantNotice {
			t.Errorf("%s: notice printed %v, want %v:\n%q", what, got, wantNotice, out)
		}
		if !wantNotice && out != "" {
			t.Errorf("%s: printed %q, want nothing", what, out)
		}
	}
	run("first run today", true)
	run("second run today", false)
	advance(24 * time.Hour)
	run("first run the next day", true)
	run("second run the next day", false)

	prevVersion, prevCommit := buildinfo.Version(), buildinfo.GitCommit()
	buildinfo.Set("v0.1.44-0.20261001000000-aaaaaaaaaaaa", "", "")
	run("another forge", true)
	buildinfo.Set(prevVersion, "", prevCommit)

	t.Setenv(pinNoticeEnv, "always")
	run(pinNoticeEnv+"=always", true)
	run(pinNoticeEnv+"=always, again", true)
	t.Setenv(pinNoticeEnv, "")

	// Another project with the same pin is another notice.
	project, _ := os.Getwd()
	other := t.TempDir()
	writeForgeYAML(t, other, "name: other\nmodule_path: github.com/example/other\nforge_version: "+skewTestPin+"\n")
	t.Chdir(other)
	h, _ = armPin(t, pinCached)
	run("another project", true)
	t.Chdir(project)
	h, _ = armPin(t, pinCached)
	run("back in the first project, the same day", false)
}

// The notice leads with what runs: the command did run, under the forge the
// project pins. It used to read as a refusal ("this forge is X, but the
// project wants Y").
func TestHandOffNotice_LeadsWithWhatRuns(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	armPin(t, pinCached)
	noticeStore(t)
	_, out, err := handOffForTest(t, "skill", "load", "forge")
	if err != nil {
		t.Fatal(err)
	}
	want := handOffNoticeLead + "the cached build — this forge is " + skewTestBinary + "; --allow-version-skew, or FORGE_ALLOW_VERSION_SKEW=1, runs it instead)\n"
	if out != want {
		t.Errorf("notice:\n got %q\nwant %q", out, want)
	}
}

// --quiet promises only what failed, so a handed-off `lint --quiet` (or
// `lint -q`, the shorthand lint defines) prints no notice — through the
// entrypoint and through the root's own check. A flag this forge lacks is
// read in its long spelling only: -q means quiet only where this forge knows
// it does.
func TestHandOffNotice_QuietPrintsNothing(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, _ := armPin(t, pinCached)
	noticeStore(t)
	t.Setenv(pinNoticeEnv, "always") // only --quiet can be what silences it

	for _, args := range [][]string{
		{"lint", "--quiet"},
		{"lint", "-q", "--scope", "pkg/x"},
		{"lint", "--quiet=true"},
		{"a-command-from-the-future", "--quiet"},
		{"generate", "--quiet"}, // a flag the pinned forge may have added
	} {
		*h = handoff{}
		handed, out, err := handOffForTest(t, args...)
		if err != nil || !handed || h.path == "" {
			t.Errorf("forge %v: handed %v, err %v", args, handed, err)
		}
		if out != "" {
			t.Errorf("forge %v printed %q under --quiet", args, out)
		}
		if strings.Join(h.args, " ") != strings.Join(args, " ") {
			t.Errorf("forge %v: handed over as %v", args, h.args)
		}
	}
	for _, args := range [][]string{{"lint"}, {"lint", "--quiet=false"}, {"generate", "-q"}, {"skill", "load", "forge", "--", "--quiet"}} {
		if _, out, _ := handOffForTest(t, args...); !strings.Contains(out, handOffNoticeLead) {
			t.Errorf("forge %v is not quiet, and printed no notice: %q", args, out)
		}
	}

	*h = handoff{}
	if out, err := runCheck(t, "lint", "--quiet"); err != nil || h.path == "" || out != "" {
		t.Errorf("the root check, lint --quiet: err %v, handed %q, printed %q", err, h.path, out)
	}
}

// A refusal is not a notice: --quiet never hides that nothing ran.
func TestHandOffNotice_QuietStillReportsAFailure(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	armPin(t, pinUnobtainable)
	noticeStore(t)
	handed, _, err := handOffForTest(t, "lint", "--quiet")
	if handed || err == nil || !strings.Contains(err.Error(), "Nothing ran") {
		t.Errorf("handed %v, err %v: want the failure", handed, err)
	}
}

// The --allow-version-skew one-liner confirms a choice the caller made, like
// the hand-off notice: once a day, never under --quiet. The full ⚠️ warning —
// a forge nobody chose runs the command — prints on every run, --quiet
// included.
func TestPinSkewNotice_OverrideIsThrottledTheWarningIsNot(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	advance := noticeStore(t)
	const override = "running it anyway (--allow-version-skew / FORGE_ALLOW_VERSION_SKEW)"

	t.Setenv(allowVersionSkewEnv, "1")
	if out, err := runCheck(t, "lint", "--quiet"); err != nil || out != "" {
		t.Errorf("override, lint --quiet: err %v, printed %q", err, out)
	}
	if out, err := runCheck(t, "lint"); err != nil || !strings.Contains(out, override) {
		t.Errorf("override, first run today: err %v, printed %q", err, out)
	}
	if out, err := runCheck(t, "lint"); err != nil || out != "" {
		t.Errorf("override, second run today: err %v, printed %q", err, out)
	}
	advance(24 * time.Hour)
	if out, err := runCheck(t, "lint"); err != nil || !strings.Contains(out, override) {
		t.Errorf("override, the next day: err %v, printed %q", err, out)
	}

	t.Setenv(allowVersionSkewEnv, "")
	for _, args := range [][]string{{"lint"}, {"lint"}, {"lint", "--quiet"}} {
		if out, err := runCheck(t, args...); err != nil || !strings.Contains(out, "⚠️  this forge is "+skewTestBinary) {
			t.Errorf("forge %v with no override, under skew: err %v, printed %q; want the full warning every run", args, err, out)
		}
	}
}
