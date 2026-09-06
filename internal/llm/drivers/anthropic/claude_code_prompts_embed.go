// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	_ "embed"
)

// Byte-exact Claude Code system-prompt blocks, extracted verbatim from real
// claude-cli traffic. These are embedded (not retyped) because they contain
// backticks, which Go raw-string literals cannot hold. Do NOT edit the .txt files
// by hand — they must stay byte-identical to the captured prompts so the spoof is
// not flagged and prompt-cache keys match Claude Code's.
//
// Two CLI releases are represented: 2.1.204 (every model except fable-5.1) and
// 2.1.261 (fable-5.1 only). A model must never mix blocks across releases — see
// claudeCodeProfile below.

//go:embed ccprompts/identity.txt
var ccIdentityPrompt string // identity block, identical across all models and releases

//go:embed ccprompts/agent_verbose.txt
var ccAgentVerbose string // agent block, verbose variant (haiku-4.5, sonnet-5, older models)

//go:embed ccprompts/output_verbose.txt
var ccOutputVerbose string // output block, verbose variant

//go:embed ccprompts/agent_lean.txt
var ccAgentLean string // agent block, lean variant (opus-4.8, opus-5, fable-5)

//go:embed ccprompts/output_lean.txt
var ccOutputLean string // output block, lean variant

//go:embed ccprompts/reporting_outcomes.txt
var ccReportingOutcomes string // "# Reporting outcomes" block, fable-5.1 only (2.1.261)

//go:embed ccprompts/agent_fable51.txt
var ccAgentFable51 string // agent block, fable-5.1 variant (2.1.261)

//go:embed ccprompts/output_fable51.txt
var ccOutputFable51 string // output block, fable-5.1 variant (2.1.261)

// apiModelFable51 is the api_model string for Claude Fable 5.1. The model catalog
// entry (id claude-5.1-fable) is owned elsewhere; this driver keys off the
// api_model it sends on the wire.
const apiModelFable51 = "claude-fable-5-1"

// claudeCodeProfile is the complete per-model request fingerprint: which prompt
// blocks the system array carries, and which claude-cli release the request must
// claim to be.
//
// FINGERPRINT COHERENCE IS THE POINT. A request's User-Agent, its
// X-Stainless-Package-Version, its billing-header cc_version and the bytes of its
// prompt blocks all come from one real claude-cli release. Mixing them (2.1.204
// prompts under a 2.1.261 User-Agent, say) produces a combination no real client
// ever emits, which is exactly the anomaly the spoof exists to avoid. Everything a
// release pins therefore lives together in this one struct rather than in
// scattered package constants.
type claudeCodeProfile struct {
	// agent and output are the two cached prompt blocks.
	agent, output string

	// reportingOutcomes, when non-empty, is an EXTRA uncached block inserted
	// between the identity block and the agent block. 2.1.261 sends it (making
	// the base system array 5 blocks); 2.1.204 does not (4 blocks).
	reportingOutcomes string

	// cliVersion is the User-Agent's claude-cli version, e.g. "2.1.204".
	cliVersion string

	// billingVersion is the billing header's cc_version. It is the cliVersion
	// plus a build suffix and is NOT derivable from it, so it is captured
	// verbatim.
	billingVersion string

	// stainlessVersion is X-Stainless-Package-Version, the version of the JS
	// SDK that release of the CLI bundles.
	stainlessVersion string

	// billingPromptID selects the billing header's trailing segment. 2.1.261
	// carries `cc_prompt_id=<uuid>;`; 2.1.204 carried `cc_prev_req=` instead
	// (which we omit — see claudeCodeBillingHeader).
	billingPromptID bool
}

// The 2.1.204 fingerprint: verbose and lean prompt variants differ only in their
// agent/output bodies. Confirmed by sha256 across captures: opus-4.8 and fable-5
// share the lean blocks, haiku-4.5 and sonnet-5 share the verbose blocks.
var (
	profileVerbose204 = claudeCodeProfile{
		agent:            ccAgentVerbose,
		output:           ccOutputVerbose,
		cliVersion:       "2.1.204",
		billingVersion:   "2.1.204.281",
		stainlessVersion: "0.94.0",
	}

	profileLean204 = claudeCodeProfile{
		agent:            ccAgentLean,
		output:           ccOutputLean,
		cliVersion:       "2.1.204",
		billingVersion:   "2.1.204.281",
		stainlessVersion: "0.94.0",
	}

	// profileFable51 is the 2.1.261 fingerprint, captured from
	// .dev/claude/fable-5.1.{curl,json}. It is the only profile with a
	// reportingOutcomes block, and the only one not on 2.1.204.
	profileFable51 = claudeCodeProfile{
		agent:             ccAgentFable51,
		output:            ccOutputFable51,
		reportingOutcomes: ccReportingOutcomes,
		cliVersion:        "2.1.261",
		billingVersion:    "2.1.261.a78",
		stainlessVersion:  "0.112.1",
		billingPromptID:   true,
	}
)

// claudeCodeProfiles maps api_model to its fingerprint. Models absent from the
// map fall back to the verbose 2.1.204 profile, which older opus/sonnet releases
// still accept.
var claudeCodeProfiles = map[string]claudeCodeProfile{
	"claude-opus-4-8": profileLean204,
	"claude-fable-5":  profileLean204,
	// opus-5 is opus-class and uses the lean blocks. A 2.1.219 capture confirms
	// the lean variant; we serve the embedded 2.1.204 lean blocks so the whole
	// spoof stays on one CLI fingerprint.
	"claude-opus-5": profileLean204,
	apiModelFable51: profileFable51,
}

// claudeCodeProfileFor returns the fingerprint for the given api_model.
func claudeCodeProfileFor(apiModel string) claudeCodeProfile {
	if p, ok := claudeCodeProfiles[apiModel]; ok {
		return p
	}
	return profileVerbose204
}
