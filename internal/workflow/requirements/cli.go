// Copyright (c) 2026 Reliant Labs
package requirements

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/reliant-labs/reliant/internal/vendorcli"
)

// cliProblems reads the vendor CLI verdicts in the probe's output (lines
// vendorcli.StatusScript printed) for the CLIs the run requires signed in.
// machine is the machine's name for `reliant machine login --machine`.
//
// Every state but signed in is a problem, unknown included: a check that
// cannot tell is not a sign-in the run can count on. Its fix names the
// status command, which says more, and the way to stop requiring it.
func cliProblems(ids []string, out, machine string) []Problem {
	if len(ids) == 0 {
		return nil
	}
	details := vendorcli.ParseStatusDetail(out)
	on, flag, where := machinePhrases(machine)
	var problems []Problem
	for _, id := range ids {
		v, ok := vendorcli.Lookup(id)
		if !ok {
			problems = append(problems, Problem{Kind: "cli", Name: id, Machine: machine,
				Problem: "this release of Reliant does not know this vendor CLI",
				Fix:     "drop it from requires.cli_login (" + vendorcli.UnknownVendorError(id).Error() + ")"})
			continue
		}
		d := details[id]
		fixLogin := fmt.Sprintf("run `reliant machine login %s%s`%s", id, flag, where)
		switch d.State {
		case vendorcli.StateSignedIn:
		case vendorcli.StateSignedOut:
			problems = append(problems, Problem{Kind: "cli", Name: id, Machine: machine,
				Problem: fmt.Sprintf("%s isn't signed in %s", v.Name, on), Fix: fixLogin})
		case vendorcli.StateMissing:
			problems = append(problems, Problem{Kind: "cli", Name: id, Machine: machine,
				Problem: fmt.Sprintf("%s (%s) isn't installed %s", v.Name, v.Binary, on), Fix: fixLogin + " (it installs the CLI)"})
		case vendorcli.StateExpired:
			problems = append(problems, Problem{Kind: "cli", Name: id, Machine: machine,
				Problem: fmt.Sprintf("%s's sign-in %s has expired", v.Name, on), Fix: fixLogin})
		default:
			problems = append(problems, Problem{Kind: "cli", Name: id, Machine: machine,
				Problem: fmt.Sprintf("could not tell whether %s is signed in %s: its status check timed out or answered something unexpected", v.Name, on),
				Fix:     fmt.Sprintf("run `reliant machine login --status %s%s`%s to see why, then `reliant machine login %s%s` to sign it in again", id, flag, where, id, flag)})
		}
		// OffPath is diagnostic only: cli_login requires installation and
		// sign-in, not bare-name lookup. Older daemons can run the CLI by
		// its full path even without ~/.local/bin on their children's PATH.
	}
	return problems
}

// safeArg is a machine name the fix can print bare in a shell command.
var safeArg = regexp.MustCompile(`^[A-Za-z0-9._@:-]+$`)

// machinePhrases is how the copy names the machine: "on machine X" in a
// sentence, the --machine flag (with a leading space) in a command, and,
// when the machine's name is not known, where to run the command instead.
func machinePhrases(machine string) (on, flag, where string) {
	if machine == "" {
		return "on the run's machine", "", " on that machine"
	}
	arg := machine
	if !safeArg.MatchString(arg) {
		arg = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return "on machine " + machine, " --machine " + arg, ""
}
