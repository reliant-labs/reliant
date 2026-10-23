package cli

// The confirmation gate: the plan is shown, and nothing is written until
// somebody says yes (owner decision O-13, doc §8.6).
//
// WHY THE REVIEW MOVED IN FRONT OF THE WRITE. With the promotion converger
// on, writing a promotion IS the deploy — the converger picks it up within
// minutes and rolls the stack. That was verified in prod during the v1.7.13
// release: everything had rolled roughly three minutes after `promote`,
// BEFORE the operator's client-side `kubectl diff` finished. The review
// happened, and it happened after the change it was reviewing.
//
// The alternative was splitting promote from a separate imperative apply so
// there would be a gap to review in. That was rejected: it re-introduces the
// two-step, drift-prone path the whole deploy-source-of-truth design exists
// to remove, and "the record is the deploy" is the property that makes the
// ledger trustworthy. What was wrong was never that the write deploys; it was
// that nothing stood between the operator and the write. So: plan, then
// approve, then write.
//
// NO TTY IS NOT CONSENT. A non-interactive caller with no flag is REFUSED
// (exit 5, plan_unconfirmed) rather than defaulted to yes. That breaks
// existing non-interactive callers on purpose (§13 F-18): the point of O-13
// is that nothing deploys without a human having seen the plan, and a default
// of "no TTY means yes" would reproduce the v1.7.13 situation in exactly the
// place it is most likely to recur — automation. Breaking loudly at the
// upgrade is cheap; a silent pre-approval is not.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/pkg/release"
)

// deployConfirm carries the approval inputs into runPromote.
//
// The zero value REQUIRES confirmation, which is the safe default: a caller
// that forgets to thread a flag gets the gate, not a bypass.
type deployConfirm struct {
	// Yes is --yes: "I read the plan, proceed."
	Yes bool
	// Approve is --approve <digest>: "I read THIS plan, proceed." It is
	// consent as well as binding — the documented two-stage pipeline runs
	// stage two with --approve and no --yes, from CI, with no terminal. The
	// binding half (refuse a plan that is not this digest, exit 3) runs
	// earlier, in gateDeployOnPlan; this half is only "may it proceed".
	Approve string
	// PlanOnly is --plan-only: print the plan (and the version a no-version
	// deploy cut) and stop, exit 0, writing no promotion.
	PlanOnly bool
	// AutoVersion is the version a no-version deploy cut, for the
	// --plan-only line and the JSON document. Empty for a versioned deploy.
	AutoVersion string

	// Interactive is whether forge may prompt. PRODUCTION ALWAYS SETS IT,
	// from cliutil.StdinIsTTY at the command boundary
	// (newDeployConfirm) — it is not a seam with a nil fallback.
	//
	// The difference matters. A fallback that read the real terminal
	// whenever the field was unset would mean production never wrote it,
	// so every test that stated "no TTY" would be exercising a shape
	// production cannot produce, and the only code reading the field would
	// be reading a test's value. Resolving it once, where the process's
	// actual stdin is known, keeps one answer for the whole command.
	Interactive bool
	// prompt is the one seam: a test must be able to answer the question
	// without a pty. nil = the real terminal prompt.
	prompt func(question string) (bool, error)
	// out is where the plan and the prompt are rendered. nil = stdout (or
	// stderr under --json, which progressWriter decides).
	out io.Writer
}

// newDeployConfirm is the PRODUCTION constructor: it resolves whether forge
// may prompt from the process's own stdin, once, at the command boundary.
//
// Every `forge env deploy` path goes through it, which is what keeps
// Interactive a field production writes rather than one only tests set.
func newDeployConfirm(p promoteCmdFlags, autoVersion string) *deployConfirm {
	return &deployConfirm{
		Yes:         p.yes,
		Approve:     p.approve,
		PlanOnly:    p.planOnly,
		AutoVersion: autoVersion,
		Interactive: cliutil.StdinIsTTY(),
	}
}

// deployConfirmOutcome is what the gate decided.
type deployConfirmOutcome struct {
	// Confirmed is whether the promotion may be written. It is the field
	// --json reports, so a consumer never has to infer consent from an
	// exit code plus a message.
	Confirmed bool
	// NextStep is the command that approves THIS plan, when the gate
	// computed one (--plan-only). Empty otherwise, and the caller then
	// leaves the plan's own next_step alone.
	//
	// It is returned rather than written onto the plan so the string a
	// consumer reads in --json is the SAME string printed in text mode —
	// building it twice is how the two drift, and a pipeline that pasted a
	// next_step which did not match the rendered line would have no way to
	// tell which one was right.
	NextStep string
	// Err is non-nil when the command must stop. It carries the exit code:
	// 5 for an unconfirmed non-interactive caller, 0 (nil) for --plan-only
	// and for a declined prompt.
	Err error
}

// renderForConfirmation prints the plan a reviewer is about to approve.
//
// It is the SAME renderer `--plan` uses, deliberately: the thing being
// approved must be the thing a preview would have shown, or the approval is
// of a different document than the one the operator read. Under --json it
// goes to stderr, so stdout still carries exactly one document.
func (p promotePlan) renderForConfirmation(jsonMode bool) {
	renderPromotePlanText(progressWriter(jsonMode), p)
}

// confirmDeployPlan is the gate. It is called AFTER the plan is computed and
// BEFORE anything is written, and it is the only thing between them.
//
// The order of the branches is the policy:
//
//  1. --plan-only stops here, exit 0. It is the first stage of a two-stage
//     pipeline, and it must write nothing even when --yes is also present.
//  2. --yes proceeds. "I read the plan."
//  3. --approve <digest> proceeds when it names this plan. "I read THIS plan."
//  4. A TTY prompts, defaulting to NO. A bare Enter must not deploy.
//  5. Anything else refuses with exit 5, printing the plan and the exact
//     flag to add.
//
// jsonMode is --json: it decides only WHERE the gate's human text goes.
// Under --json stdout belongs to the document, so the plan line and the
// prompt go to stderr — the same split renderForConfirmation uses. Without
// it, --plan-only --json printed "Approve it with: …" above the document and
// made stdout unparseable, which is the one thing --json promises it is not.
func confirmDeployPlan(env string, plan promotePlan, c deployConfirm, jsonMode bool) deployConfirmOutcome {
	out := c.out
	if out == nil {
		out = progressWriter(jsonMode)
	}

	if c.PlanOnly {
		if c.AutoVersion != "" {
			fmt.Fprintf(out, "\n--plan-only: release %s was cut and pushed; NO promotion was written.\n", c.AutoVersion)
		} else {
			fmt.Fprintf(out, "\n--plan-only: NO promotion was written.\n")
		}
		next, why := approveCommand(env, planOnlyVersion(plan, c), plan.DeployPlan)
		fmt.Fprintf(out, "  Approve it with: %s\n", next)
		if why != "" {
			fmt.Fprintf(out, "  %s\n", why)
		}
		return deployConfirmOutcome{Confirmed: false, NextStep: next}
	}

	if c.Yes {
		return deployConfirmOutcome{Confirmed: true}
	}

	// --approve <digest> is consent for EXACTLY that plan. gateDeployOnPlan
	// has already refused a mismatch (plan_stale) and a digest with no plan
	// to judge it against; the equality is re-checked here so this branch can
	// never admit a plan nobody named, whatever order a future caller runs
	// the two gates in. Without this branch the next_step --plan-only prints
	// (`forge env deploy prod vX --approve <digest>`) refused with exit 5
	// "plan_unconfirmed" — the documented pipeline could not complete.
	if c.Approve != "" && plan.DeployPlan != nil && plan.DeployPlan.Digest == c.Approve {
		return deployConfirmOutcome{Confirmed: true}
	}

	if c.Interactive {
		ask := c.prompt
		if ask == nil {
			ask = promptYesNo
		}
		ok, err := ask(fmt.Sprintf("Deploy %s to env %q?", deployConfirmSubject(plan, c), env))
		if err != nil {
			return deployConfirmOutcome{Err: err}
		}
		if !ok {
			fmt.Fprintln(out, "Cancelled — nothing was written.")
			return deployConfirmOutcome{Confirmed: false}
		}
		return deployConfirmOutcome{Confirmed: true}
	}

	return deployConfirmOutcome{Err: errPlanUnconfirmed(env, plan, c)}
}

// planOnlyVersion is the version --plan-only's approve command must name: the
// one a versionless deploy already CUT, else the plan's target.
//
// The auto version wins because that release exists and its images are
// pushed, so approving it must not send the caller back to the versionless
// form and pay for the build twice.
func planOnlyVersion(plan promotePlan, c deployConfirm) string {
	if c.AutoVersion != "" {
		return c.AutoVersion
	}
	return plan.Target.Release
}

// approveCommand is the EXACT command that approves the plan just printed. It
// returns the command and, when the command is the weaker --yes form, one
// sentence saying why it could not be the stronger one.
//
// WHY --approve AND NOT --yes. The two flags mean different things and the
// difference is the whole point of the two-stage pipeline: --yes approves
// whatever forge computes at the moment the second command runs, while
// --approve <digest> approves the change set the operator actually read. If
// Live moves in between — another deploy lands, a new bundle is applied, drift
// appears — the digest no longer matches and the deploy is refused (exit 3)
// instead of shipping a plan nobody saw. A --plan-only that handed back --yes
// was therefore telling the reader to discard the only guarantee the stage
// they just ran exists to provide.
//
// Stop-class findings are listed BY CODE in the same command, because
// --acknowledge-destructive cannot be written in advance: the code is not
// known until the plan is computed, which is exactly what this stage did.
//
// The --yes form survives for the one state where there is no digest to name:
// no plan could be computed at all (a never-built env, or a control plane that
// predates bundles — F-15). Nil is not "no changes", so the fallback says why
// it is the fallback rather than looking like the normal answer.
func approveCommand(env, version string, plan *release.Plan) (command, why string) {
	var b strings.Builder
	b.WriteString("forge env deploy " + env)
	if version != "" {
		b.WriteString(" " + version)
	}
	if plan == nil || plan.Digest == "" {
		b.WriteString(" --yes")
		return b.String(), "No plan digest to approve (none could be computed for this env), so this is the --yes form: it approves whatever forge computes when it runs."
	}
	b.WriteString(" --approve " + plan.Digest)
	if stops := plan.StopCodes(); len(stops) > 0 {
		b.WriteString(" --acknowledge-destructive " + strings.Join(stops, ","))
	}
	return b.String(), ""
}

// deployConfirmSubject names what is being deployed, for the prompt.
func deployConfirmSubject(plan promotePlan, c deployConfirm) string {
	if c.AutoVersion != "" {
		return "release " + c.AutoVersion
	}
	if plan.Target.Release != "" {
		return "release " + plan.Target.Release
	}
	return "this release"
}

// errPlanUnconfirmed is the no-TTY, no-flag refusal: exit 5,
// plan_unconfirmed.
//
// It names the EXACT command to re-run, including the version a no-version
// deploy already cut — because that release exists, was pushed, and must not
// be built a second time to approve it. A message that only said "pass --yes"
// would send a CI author back to the no-version form, which would cut
// another release for the same tree (reused, but the build would re-run).
func errPlanUnconfirmed(env string, plan promotePlan, c deployConfirm) error {
	version := c.AutoVersion
	if version == "" {
		version = plan.Target.Release
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the deploy plan was not confirmed (plan_unconfirmed), so NO promotion was written.\n")
	if c.AutoVersion != "" {
		fmt.Fprintf(&b, "  Release %s WAS cut and its images pushed — approving it needs no rebuild.\n", c.AutoVersion)
	}
	fmt.Fprintf(&b, "  There is no terminal to confirm on and no --yes was given, and forge never reads\n"+
		"  \"no TTY\" as consent: a promotion IS the deploy, so the converger would roll this out\n"+
		"  within minutes of the write.\n")
	fmt.Fprintf(&b, "  fix: re-run with --yes (the CI path, after reading the plan above):\n")
	if version != "" {
		fmt.Fprintf(&b, "    forge env deploy %s %s --yes\n", env, version)
	} else {
		fmt.Fprintf(&b, "    forge env deploy %s --yes\n", env)
	}
	fmt.Fprintf(&b, "  or compute the plan alone first: forge env deploy %s --plan-only", env)
	return exitCodeError{code: exitPlanUnconfirmed, msg: b.String()}
}

// promptYesNo asks on the terminal, DEFAULTING TO NO.
//
// A bare Enter must not deploy. The default is the conservative answer for
// the same reason the whole gate exists: the write is the deploy, and a
// reflexive keystroke should not be able to ship one.
func promptYesNo(question string) (bool, error) {
	fmt.Fprintf(os.Stderr, "\n%s [y/N] ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		// A closed stdin is not a yes. It is the non-interactive case
		// arriving late, and it must refuse like one.
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
