package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/doctor"
)

// doctor's `--signal` help must name exactly the values doctor.RunFiltered
// accepts — no more, no fewer.
//
// Four checks used to guard themselves with `signal != "" && signal !=
// "ingress"|"tools"|"external-builds"`, naming filters that DO NOT EXIST:
// RunFiltered rejects any value outside its own set before a check is
// reached, so those halves could never be true. Their doc comments described
// the phantom surface as if it worked, which is how a reader learns a filter
// that does not.
//
// After the doctor/env-status split the accepted set is just `deploy`: the
// telemetry signals are ENVIRONMENT-runtime questions and moved to
// `forge env status <env> --signal`.
func TestDoctorSignalHelpMatchesWhatRunFilteredAccepts(t *testing.T) {
	cmd := newDoctorCmd()
	flag := cmd.Flags().Lookup("signal")
	if flag == nil {
		t.Fatal("doctor has no --signal flag")
	}
	help := flag.Usage

	if !strings.Contains(help, "deploy") {
		t.Errorf("--signal help does not name %q, which RunFiltered accepts:\n  %s", "deploy", help)
	}
	// Values that were advertised in code but never accepted.
	for _, phantom := range []string{"ingress", "tools", "external-builds"} {
		if strings.Contains(help, phantom) {
			t.Errorf("--signal help names %q, which RunFiltered REJECTS — advertising a filter "+
				"that errors out is worse than omitting it:\n  %s", phantom, help)
		}
	}
	// The moved signals must be pointed AT their new home, not silently
	// dropped: a user who typed `forge doctor --signal traces` yesterday
	// needs the help to say where traces went.
	if !strings.Contains(help, "env status") {
		t.Errorf("--signal help does not point at `forge env status <env> --signal`, "+
			"where the runtime signals moved:\n  %s", help)
	}
}

// The phantom signals must be rejected in fact, not just undocumented.
func TestDoctorRunFilteredRejectsThePhantomSignals(t *testing.T) {
	d := doctor.New(doctor.Deps{})
	for _, phantom := range []string{"ingress", "tools", "external-builds", "bogus"} {
		if _, err := d.RunFiltered(t.Context(), "p", t.TempDir(), phantom); err == nil {
			t.Errorf("RunFiltered(%q) returned no error — a check guarding itself on this "+
				"signal would be unreachable code advertising a working filter", phantom)
		}
	}
}

// The env-runtime signals are no longer doctor's. Rejecting them silently
// (as "unknown signal") would leave a user who typed `forge doctor --signal
// traces` with nowhere to go, so the error must NAME the new command.
func TestDoctorRunFilteredRedirectsTheRuntimeSignals(t *testing.T) {
	d := doctor.New(doctor.Deps{})
	for _, moved := range []string{"metrics", "traces", "logs", "profiles"} {
		_, err := d.RunFiltered(t.Context(), "p", t.TempDir(), moved)
		if err == nil {
			t.Fatalf("RunFiltered(%q) still runs in doctor — it is a runtime question "+
				"and doctor has no resolved port to ask it against", moved)
		}
		if !strings.Contains(err.Error(), "forge env status") {
			t.Errorf("RunFiltered(%q) error does not name `forge env status`: %v", moved, err)
		}
	}
}

// `forge doctor bogus` used to run every check and exit 0 — the runnable-parent
// form of the exit-0-on-a-typo wart. Doctor takes no positionals.
func TestDoctorRejectsStrayPositionals(t *testing.T) {
	cmd := newDoctorCmd()
	if cmd.Args == nil {
		t.Fatal("doctor has no Args validator — a stray positional would run the full suite and exit 0")
	}
	if err := cmd.Args(cmd, []string{"bogus"}); err == nil {
		t.Error("doctor accepted a positional argument; a typo must fail rather than be swallowed")
	}
	if err := cmd.Args(cmd, nil); err != nil {
		t.Errorf("doctor rejected the no-argument form: %v", err)
	}
}

// THE SPLIT, pinned at the command surface.
//
// `forge doctor --env string (default "dev")` was the tell that doctor was
// answering two questions: run it in a project and it silently reported on
// an environment you never named. Everything doctor now checks spans EVERY
// declared environment, so there is nothing for --env to scope.
func TestDoctorTakesNoEnvFlag(t *testing.T) {
	if f := newDoctorCmd().Flags().Lookup("env"); f != nil {
		t.Errorf("doctor still has --env (default %q) — it answers project questions, "+
			"which span every environment; `forge env status <env>` owns the per-env runtime question",
			f.DefValue)
	}
}

// The runtime question has to be ASKABLE somewhere, and `forge env status`
// is where it went. Pin the flag so the redirect above cannot dangle.
func TestEnvStatusCarriesTheRuntimeSignals(t *testing.T) {
	cmd := newEnvStatusCmd()
	flag := cmd.Flags().Lookup("signal")
	if flag == nil {
		t.Fatal("`forge env status` has no --signal flag, but `forge doctor` now redirects to it")
	}
	for _, sig := range []string{"app", "metrics", "traces", "logs", "profiles"} {
		if !strings.Contains(flag.Usage, sig) {
			t.Errorf("`forge env status --signal` help does not name %q:\n  %s", sig, flag.Usage)
		}
	}
}
