package cli

// What a forge failure means and the command to run next: the one table the
// MCP explain_error tool (mcp_deploy.go), deploy_claimable's failure results
// and `forge explain` (explain_cmd.go) all read, so the three cannot drift.
// The listable keys (error reasons, exit codes) are rows, not switch cases,
// so `forge explain` lists exactly what it can explain.

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
)

// forgeErrorExplanation is explain_error's answer, and `forge explain
// --json`'s document.
type forgeErrorExplanation struct {
	// Meaning is what happened, in a sentence, and whether anything changed.
	Meaning string `json:"meaning"`
	// Next are the exact commands to run, best first. Empty when the next
	// step is a person's.
	Next []string `json:"next,omitempty"`
	// Retry says whether re-running the same command can succeed without a
	// change: "now", "later" (after a wait), or "no".
	Retry string `json:"retry"`
}

func (e forgeErrorExplanation) text() string {
	var b strings.Builder
	b.WriteString(e.Meaning)
	for i, n := range e.Next {
		if i == 0 {
			fmt.Fprintf(&b, "\nrun: %s", n)
		} else {
			fmt.Fprintf(&b, "\nor:  %s", n)
		}
	}
	fmt.Fprintf(&b, "\nretry as is: %s", e.Retry)
	return b.String()
}

// explainCtx is what an explanation's next commands name.
type explainCtx struct {
	// env is the env the failing command acted on.
	env string
	// deploy is the claimable deploy of env; login signs in, then deploys.
	deploy, login string
}

func newExplainCtx(env string) explainCtx {
	return explainCtx{
		env:    env,
		deploy: fmt.Sprintf("%s env deploy %s --claimable", Name(), env),
		login:  loginThenDeploy(env),
	}
}

// reasonExplanation explains the error_reasons it lists.
type reasonExplanation struct {
	reasons []string
	explain func(c explainCtx) forgeErrorExplanation
}

// reasonExplanations is the error_reason table, in the order `forge explain`
// lists it.
var reasonExplanations = []reasonExplanation{
	{[]string{cloud.ReasonClaimableThrottled}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"Too many new anonymous deploys right now (from this network, this machine, or the whole platform). Nothing was created and nothing is wrong with the project. Wait until retry_at, then run the same command; or sign in, which this limit does not apply to.", []string{c.deploy, c.login}, "later"}
	}},
	{[]string{cloud.ReasonClaimableUnavailable}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The platform is not taking anonymous deploys right now (an operator switch or a dependency it needs). This is not the project. Retry later, or deploy to an account.", []string{c.deploy, c.login}, "later"}
	}},
	{[]string{reasonClaimableSignedIn}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"This machine is signed in to Reliant, so the anonymous path is refused: deploy to the account instead.", []string{fmt.Sprintf("%s env deploy %s", Name(), c.env)}, "no"}
	}},
	{[]string{reasonClaimableIneligible}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The env does not fit an unclaimed deploy (one static site, or one service with its migration job and one database). The error lists each item with its KCL fix; fix them and re-check with --plan-only, or deploy to an account.", []string{c.deploy + " --plan-only", c.login}, "no"}
	}},
	{[]string{cloud.ReasonClaimableClaimed, cloud.ReasonAccessTokenRevoked}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The deploy was claimed: someone opened its claim link and it belongs to their account now. Deploy again as that account.", []string{c.login}, "no"}
	}},
	{[]string{cloud.ReasonClaimableExpired}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The anonymous deploy's lifetime is over and it was deleted. Deploy again to get a new one, or to an account to keep it.", []string{c.deploy, c.login}, "now"}
	}},
	{[]string{cloud.ReasonClaimableTakenDown}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The platform took this deploy down (content or abuse policy). Do not retry anonymously; tell your user. They can deploy under their own account.", []string{c.login}, "no"}
	}},
	{[]string{cloud.ReasonClaimableInvalidPoW}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The proof-of-work challenge expired before it was solved (a slow or busy machine). Nothing was created; run it again.", []string{c.deploy}, "now"}
	}},
	{[]string{ReasonNoControlPlaneCredential}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"There is no Reliant credential on this machine. Deploy with no account (a claim link for your user), or sign in.", []string{c.deploy, c.login}, "no"}
	}},
	{[]string{cloud.ReasonAccessTokenExpired, cloud.ReasonAccessTokenUnknown, cloud.ReasonAccessTokenMalformed}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The stored Reliant login is no longer accepted. Sign in again.", []string{c.login}, "no"}
	}},
	{[]string{cloud.ReasonAccessTokenUnavailable}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The control plane could not check the credential right now. Nothing was changed; retry shortly.", []string{fmt.Sprintf("%s env status %s", Name(), c.env)}, "later"}
	}},
	{[]string{cloud.ReasonClientUpgradeRequired}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"This forge is older than the control plane accepts. Run the latest forge.", []string{fmt.Sprintf("npx -y @reliantlabs/forge@latest env deploy %s --claimable", c.env)}, "no"}
	}},
	{[]string{"scope_missing"}, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The credential lacks a permission this needs (error_scopes names it). Sign in as someone who has it, or ask an organization admin for the grant.", []string{c.login}, "no"}
	}},
	{[]string{ReasonDeliveryGrantsMissing}, func(explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The cluster's Flux apply identities lack permissions this release needs. A cluster admin must re-run the Flux bootstrap; nothing was applied.", nil, "no"}
	}},
}

// exitCodeExplanation explains one exit code (exitcodes.go).
type exitCodeExplanation struct {
	code    int
	explain func(c explainCtx) forgeErrorExplanation
}

// exitCodeExplanations is the exit-code table, in code order.
var exitCodeExplanations = []exitCodeExplanation{
	{exitOK, func(explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"That is success: forge exited 0.", nil, "no"}
	}},
	{exitWrong, func(explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"forge looked and something is wrong: a check failed or the input is invalid. The error says what; fix it, then run the same command.", nil, "no"}
	}},
	{exitUndetermined, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"forge could not tell: the control plane was unreachable or refused the credential. Nothing is known to have failed; check again.", []string{fmt.Sprintf("%s env status %s", Name(), c.env)}, "later"}
	}},
	{exitConflict, func(explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"Someone else changed this environment since forge read it. Stop and ask your user; retrying would overwrite their change.", nil, "no"}
	}},
	{exitRefused, func(explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The control plane declined the write (a rollout in flight, a pinned env, a limit). Nothing was lost; wait and run the same command again.", nil, "later"}
	}},
	{exitPlanUnconfirmed, func(explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The deploy plan was printed and nobody approved it, so nothing was deployed. Re-run with --yes once the plan is right.", nil, "no"}
	}},
	{exitSuperseded, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"A newer deploy replaced the one being waited on. Nothing failed; check what is live now.", []string{fmt.Sprintf("%s env status %s", Name(), c.env)}, "no"}
	}},
	{exitQueued, func(explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The deploy was accepted and is held on a person's action (billing). Give the printed action URL to your user and stop: it proceeds on its own when they act.", nil, "no"}
	}},
	{exitTimedOut, func(c explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"The rollout was still progressing when the wait ran out. Do not deploy again; check it.", []string{fmt.Sprintf("%s obs check %s --wait", Name(), c.env), fmt.Sprintf("%s env status %s", Name(), c.env)}, "later"}
	}},
	{exitNeedsHuman, func(explainCtx) forgeErrorExplanation {
		return forgeErrorExplanation{"Nothing more happens until a person acts; re-running changes nothing. The report names each item and its fix.", nil, "no"}
	}},
}

// explainByText answers the few error texts forge prints without an
// error_reason.
func explainByText(text string, c explainCtx) (forgeErrorExplanation, bool) {
	switch t := strings.ToLower(text); {
	case strings.Contains(t, "forge.yaml not found"), strings.Contains(t, "no forge.yaml"):
		return forgeErrorExplanation{"This directory is not a forge project. deploy_claimable adopts a Vite or Next.js app, or an app with its own Dockerfile, in place; otherwise start a site.", []string{fmt.Sprintf("%s project new --in-place --kind site", Name()), fmt.Sprintf("%s project new --in-place --kind app", Name()), fmt.Sprintf("%s project new <name> --kind site", Name())}, "no"}, true
	case strings.Contains(t, "declares no hosted control plane"):
		return forgeErrorExplanation{"The env does not deploy to Reliant hosting: its KCL Bundle has no control_plane. Add `control_plane = forge.ControlPlane {}` to deploy/kcl/" + c.env + "/main.k, then deploy.", []string{c.deploy}, "no"}, true
	case strings.Contains(t, "executable file not found"), strings.Contains(t, "command not found"):
		return forgeErrorExplanation{"A tool the build needs is not installed (the error names it). Install it, then run the same command.", []string{c.deploy}, "no"}, true
	}
	return forgeErrorExplanation{}, false
}

// explainForgeErrorMatch maps a forge failure to its meaning and the next
// command. The error_reason decides when there is one (it is the stable
// field); then a few error texts forge prints without one; then the exit
// code's table (exitcodes.go). exitCode < 0 means unknown. ok is false when
// none of the three is known to this forge; the explanation is then the
// generic one.
func explainForgeErrorMatch(exitCode int, reason, text, env string) (ex forgeErrorExplanation, ok bool) {
	c := newExplainCtx(env)
	if reason != "" {
		for _, r := range reasonExplanations {
			for _, known := range r.reasons {
				if known == reason {
					return r.explain(c), true
				}
			}
		}
	}
	if ex, ok := explainByText(text, c); ok {
		return ex, true
	}
	for _, e := range exitCodeExplanations {
		if e.code == exitCode {
			return e.explain(c), true
		}
	}
	return forgeErrorExplanation{"forge failed; the error text is the explanation. If it names a command, run that.", nil, "no"}, false
}

// explainForgeError is explainForgeErrorMatch's answer, generic when nothing
// matched: what the MCP tool and deploy_claimable's failures return.
func explainForgeError(exitCode int, reason, text, env string) forgeErrorExplanation {
	ex, _ := explainForgeErrorMatch(exitCode, reason, text, env)
	return ex
}

// knownErrorReasons lists every error_reason the table explains, in table
// order.
func knownErrorReasons() []string {
	var out []string
	for _, r := range reasonExplanations {
		out = append(out, r.reasons...)
	}
	return out
}

// knownExitCodes lists every exit code the table explains, in code order.
func knownExitCodes() []int {
	out := make([]int, 0, len(exitCodeExplanations))
	for _, e := range exitCodeExplanations {
		out = append(out, e.code)
	}
	return out
}
