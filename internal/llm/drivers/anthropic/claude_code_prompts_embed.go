// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"unicode/utf16"
)

// Byte-exact Claude Code system-prompt blocks, extracted verbatim from real
// claude-cli traffic. These are embedded (not retyped) because they contain
// backticks, which Go raw-string literals cannot hold. Do NOT edit the .txt files
// by hand — they must stay byte-identical to the captured prompts so the spoof is
// not flagged and prompt-cache keys match Claude Code's.
//
// Three CLI releases are represented: 2.1.204 (most models), 2.1.261 (fable-5.1)
// and 2.1.280 (opus-5.5). A model must never mix blocks across releases — see
// claudeCodeProfile below. The one exception is fable-5.2, which sends 2.1.280's
// blocks under 2.1.321's metadata because the API refuses it below 2.1.290 and
// no 2.1.290+ prompt capture exists (profileFable52).

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

//go:embed ccprompts/agent_opus55.txt
var ccAgentOpus55 string // agent block, opus-5.5 variant (2.1.280)

//go:embed ccprompts/output_opus55.txt
var ccOutputOpus55 string // output block, opus-5.5 variant (2.1.280)

// apiModelFable51 and apiModelOpus55 are the api_model strings for Claude Fable
// 5.1 and Claude Opus 5.5. The model catalog entries (ids claude-5.1-fable and
// claude-5.5-opus) are owned elsewhere; this driver keys off the api_model it
// sends on the wire.
const (
	apiModelFable51 = "claude-fable-5-1"
	apiModelFable52 = "claude-fable-5-2"
	apiModelOpus55  = "claude-opus-5-5"
)

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
	// plus a 3-hex suffix, captured verbatim from one request. Empty means the
	// profile has no capture to take it from: the suffix is then derived per
	// conversation the way the CLI derives it (see claudeCodeFingerprint).
	billingVersion string

	// stainlessVersion is X-Stainless-Package-Version, the version of the JS
	// SDK that release of the CLI bundles.
	stainlessVersion string

	// billingPromptID selects the billing header's trailing segment. 2.1.261
	// and 2.1.280 carry `cc_prompt_id=<uuid>;`; 2.1.204 carried `cc_prev_req=`
	// instead (which we omit — see claudeCodeBillingHeader).
	billingPromptID bool

	// billingTurnOrigin appends `cc_turn_origin=human;`, which only 2.1.280
	// sends. Real 2.1.280 traffic also emits cc_turn_origin on non-human turns,
	// but Reliant has no equivalent distinction to map onto it, and every
	// request we send originates from a user turn.
	billingTurnOrigin bool

	// thinkingDisplay, when true, adds `display:"updates"` to an adaptive
	// thinking config. 2.1.261 and 2.1.280 both send it; 2.1.204 sends a bare
	// {"type":"adaptive"}. This is a per-release fact, not a per-model one, so
	// it belongs here rather than in an api_model switch.
	thinkingDisplay bool

	// dispatchID and requestClass are the `anthropic-dispatch-id` and
	// `x-claude-code-request-class` headers. Only 2.1.280 sends them; an empty
	// value means the header is omitted entirely.
	dispatchID, requestClass string
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
		thinkingDisplay:   true,
	}

	// profileOpus55 is the 2.1.280 fingerprint, captured from
	// .dev/claude/opus-5.5.{curl,json}. Like 2.1.204 it sends NO
	// reporting-outcomes block, so it is back to a 4-block system array — the
	// block moved into the release's mid-conversation system turn, which
	// Reliant does not send. It is the only profile carrying cc_turn_origin, a
	// dispatch id and a request class.
	profileOpus55 = claudeCodeProfile{
		agent:             ccAgentOpus55,
		output:            ccOutputOpus55,
		cliVersion:        "2.1.280",
		billingVersion:    "2.1.280.790",
		stainlessVersion:  "0.112.1",
		billingPromptID:   true,
		billingTurnOrigin: true,
		thinkingDisplay:   true,
		dispatchID:        "v2d",
		requestClass:      "main",
	}

	// profileFable52 is a VERSION-FLOOR COMPATIBILITY OVERRIDE, not a capture.
	// The API refuses claude-fable-5-2 from any claude-cli below 2.1.290, so the
	// 2.1.280 fingerprint fable-5.2 used to ride is rejected outright. Its only
	// capture (.dev/claude/fable-5.2.*) is a claude-desktop 2.1.321 thread
	// continuation with no identity/agent/output blocks and no tools, so no
	// complete 2.1.290+ fingerprint exists to copy.
	//
	// So the release-identifying metadata is 2.1.321's, read from the published
	// @anthropic-ai/claude-code-linux-x64@2.1.321 binary (VERSION "2.1.321", its
	// bundled SDK's X-Stainless-Package-Version "0.128.0"), and the prompt bodies
	// stay the newest captured ones, 2.1.280's. That one mix is deliberate and
	// confined to this profile: every header and the billing cc_version agree on
	// 2.1.321, and no prompt text is invented. There is no captured 2.1.321
	// billing suffix, so billingVersion is empty and the suffix is derived with
	// 2.1.321's own algorithm. The beta set, dispatch id, request class and
	// billing segments are 2.1.280's; the 2.1.321 binary still carries each of
	// them. Replace this with a real profile once a claude-cli capture of a fresh
	// fable-5.2 conversation exists.
	profileFable52 = claudeCodeProfile{
		agent:             ccAgentOpus55,
		output:            ccOutputOpus55,
		cliVersion:        "2.1.321",
		stainlessVersion:  "0.128.0",
		billingPromptID:   true,
		billingTurnOrigin: true,
		thinkingDisplay:   true,
		dispatchID:        "v2d",
		requestClass:      "main",
	}
)

// claudeCodeFingerprintSalt is the constant claude-cli mixes into the billing
// suffix (2.1.321 bundle: `var aO="59cf53e54c78"`).
const claudeCodeFingerprintSalt = "59cf53e54c78"

// claudeCodeFingerprint is claude-cli 2.1.321's billing suffix, ported from its
// bundle (`function td(e,n)`): sha256 of the salt, the UTF-16 code units at
// indices 4, 7 and 20 of the first user message's text ("0" past the end), and
// the cli version, as hex, first three characters. JavaScript indexes strings by
// UTF-16 code unit and Node encodes a lone surrogate as U+FFFD, so this does too.
func claudeCodeFingerprint(firstUserText, cliVersion string) string {
	units := utf16.Encode([]rune(firstUserText))
	var picked []uint16
	for _, i := range [...]int{4, 7, 20} {
		if i >= len(units) {
			picked = append(picked, '0')
		} else {
			picked = append(picked, units[i])
		}
	}
	// Decode after concatenation: selected surrogate halves can form a pair
	// even when they came from separate characters in the original message.
	sum := sha256.Sum256([]byte(claudeCodeFingerprintSalt + string(utf16.Decode(picked)) + cliVersion))
	return hex.EncodeToString(sum[:])[:3]
}

// ccVersion is the billing header's cc_version for a conversation whose first
// user message reads firstUserText: the captured literal when the profile has
// one, else the cli version plus the derived suffix.
func (p claudeCodeProfile) ccVersion(firstUserText string) string {
	if p.billingVersion != "" {
		return p.billingVersion
	}
	return p.cliVersion + "." + claudeCodeFingerprint(firstUserText, p.cliVersion)
}

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
	apiModelOpus55:  profileOpus55,
	// fable-5.2 needs claude-cli >= 2.1.290; see profileFable52.
	apiModelFable52: profileFable52,
}

// claudeCodeProfileFor returns the fingerprint for the given api_model.
func claudeCodeProfileFor(apiModel string) claudeCodeProfile {
	if p, ok := claudeCodeProfiles[apiModel]; ok {
		return p
	}
	return profileVerbose204
}
