// Copyright (c) 2025 Reliant Labs
package drivererrors

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ErrContextOverflow matches a provider refusing a request because the prompt
// is larger than the model's context window ("prompt is too long",
// context_length_exceeded, "input token count ... exceeds the maximum").
//
// Resending the same request fails the same way, so it is never retried as
// is. It is recoverable all the same: a smaller request fits, which is what
// call_llm does with it (trim harder, then send again) before it gives up.
//
// It lives here, in a leaf package, for the reason ErrStreamEndedEarly does:
// the drivers mark the error (drivers.markContextOverflow, every stream goes
// through it) and the workflow runtime's classifiers recognise it by identity,
// and neither may import the other.
var ErrContextOverflow = errors.New("llm request exceeds the model's context window")

// ContextOverflowError is a provider error recognised as a context-window
// refusal. Its text is the provider's, unchanged: everything that already
// reads that text keeps reading it. Actual and Limit are the prompt's size and
// the window, in the provider's tokens, when the refusal states them; 0 when
// it does not.
type ContextOverflowError struct {
	Err    error
	Actual int
	Limit  int
}

func (e *ContextOverflowError) Error() string { return e.Err.Error() }

// Unwrap keeps the provider's own error (its SDK type, its status) reachable.
func (e *ContextOverflowError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrContextOverflow) hold.
func (e *ContextOverflowError) Is(target error) bool { return target == ErrContextOverflow }

// ProviderFailure is what is known about a failed provider request: its HTTP
// status (0 when unknown, as for an error that arrived mid-stream), the
// machine-readable codes the provider attached (OpenAI's error.code and
// error.type, Anthropic's error.type, Gemini's status), and its text.
type ProviderFailure struct {
	Status  int
	Codes   []string
	Message string
}

// overflowCodes are the structured codes that name a context-window refusal.
// A code decides before any wording does.
var overflowCodes = map[string]struct{}{
	// OpenAI chat + Responses, Codex (in a response.failed event), OpenRouter
	// passing OpenAI through, vLLM's OpenAI server.
	"context_length_exceeded": {},
	// Anthropic's stop_reason vocabulary, which some gateways reuse as an
	// error code (opencode provider-error.ts:233-242).
	"model_context_window_exceeded": {},
	// Anthropic HTTP 413 for a request body over its size cap; trimming the
	// request is what fixes it (opencode provider-error.ts:233-242).
	"request_too_large": {},
	// GitHub Copilot: "prompt token count of N exceeds the limit of M".
	"model_max_prompt_tokens_exceeded": {},
	// llama.cpp server: "the request exceeds the available context size".
	"exceed_context_size_error": {},
}

// overflowPatterns are the refusal wordings, for the errors that carry no code
// (or whose code a proxy dropped). Taken from opencode's classifier
// (packages/ai/src/provider-error.ts:16-56) and hermes-agent's
// (agent/error_classifier.py:318-331), narrowed to wording that can only mean
// the prompt outgrew the window. Matched case-insensitively.
var overflowPatterns = []*regexp.Regexp{
	// Anthropic (API, Claude Code, Vertex/Bedrock Claude):
	// "prompt is too long: 210000 tokens > 200000 maximum".
	regexp.MustCompile(`prompt is too long`),
	// Anthropic, when input plus max_tokens is past the window:
	// "input length and `max_tokens` exceed context limit: 188240 + 21333 > 200000".
	regexp.MustCompile("input length and `?max_tokens`? exceed context limit"),
	// Anthropic via Bedrock: "input is too long for requested model".
	regexp.MustCompile(`input is too long for (?:the )?(?:requested|selected) model`),
	// OpenAI Responses / Codex: "Your input exceeds the context window of this model".
	regexp.MustCompile(`exceeds the context window`),
	// OpenAI chat, OpenRouter, vLLM: "This model's maximum context length is
	// 128000 tokens. However, your messages resulted in 130000 tokens."
	regexp.MustCompile(`maximum context length is \d+ tokens`),
	regexp.MustCompile(`exceeds (?:the )?(?:model'?s )?maximum context length`),
	regexp.MustCompile(`context[_ ]length[_ ]exceeded`),
	regexp.MustCompile(`model_context_window_exceeded`),
	// Gemini, Vertex Gemini, Antigravity: "The input token count (1200000)
	// exceeds the maximum number of tokens allowed (1048576)."
	regexp.MustCompile(`input token count.*exceeds the maximum`),
	// GitHub Copilot: "prompt token count of 140000 exceeds the limit of 128000".
	regexp.MustCompile(`prompt token count of \d+ exceeds the limit of \d+`),
	// llama.cpp: "the request exceeds the available context size, try increasing it".
	regexp.MustCompile(`exceeds the available context size`),
	// LM Studio / llama.cpp: "... is greater than the context length".
	regexp.MustCompile(`greater than the context length`),
	// Ollama: "input length exceeds the context length".
	regexp.MustCompile(`input length.*exceeds.*context length`),
	// vLLM: "The decoder prompt (length 40000) is longer than the maximum model length of 32768".
	regexp.MustCompile(`longer than the maximum model length`),
	// Together / Fireworks / DeepInfra: "Input length 131393 exceeds the
	// maximum allowed input length of 131040 tokens."
	regexp.MustCompile(`exceeds (?:the )?maximum allowed input length`),
	// LiteLLM's ContextWindowExceededError, which some gateways forward.
	regexp.MustCompile(`context window exceeded`),
}

// overflowExclusions are wordings that look like an overflow and are not one.
// They are checked FIRST: a throttle or an out-of-memory server that happens to
// mention tokens or a limit must never be read as a prompt to shrink, because
// shrinking cannot fix it and the retries it costs hit a provider that is
// already refusing (hermes-agent #52261, the reason its classifier checks
// memory ceilings before overflow; opencode provider-error.ts:61-70).
var overflowExclusions = []*regexp.Regexp{
	regexp.MustCompile(`rate[ _-]?limit`),
	regexp.MustCompile(`too many requests`),
	regexp.MustCompile(`throttl`),
	// OpenAI's TPM 429: "Request too large for gpt-4o ... on tokens per min (TPM)".
	regexp.MustCompile(`tokens per (?:min|minute|day)`),
	// Bedrock: "Too many tokens, please wait before trying again."
	regexp.MustCompile(`please wait`),
	// Gemini / Vertex 429.
	regexp.MustCompile(`resource[ _-]?exhausted`),
	regexp.MustCompile(`overloaded`),
	// A max_tokens above the model's OUTPUT cap is the request's own setting,
	// not the conversation outgrowing the window; trimming cannot fix it.
	regexp.MustCompile(`max[_ ]tokens must be less than`),
	regexp.MustCompile(`maximum allowed number of output tokens`),
	// Local servers that refuse for lack of memory (hermes-agent #52261).
	regexp.MustCompile(`out of memory`),
	regexp.MustCompile(`insufficient memory`),
	regexp.MustCompile(`memory (?:limit|guard|ceiling)`),
}

// IsContextOverflow reports whether the failure is a context-window refusal.
//
// The order is the point:
//  1. A 429 is never an overflow, whatever its body says, and neither is a
//     5xx: only a 4xx, or an error with no status (one delivered mid-stream),
//     can be. A throttle body can read like an overflow ("too many tokens").
//  2. A structured code decides next.
//  3. Throttle and out-of-memory wordings exclude before any overflow wording
//     is tried.
//  4. Only then the overflow wordings.
func (f ProviderFailure) IsContextOverflow() bool {
	if f.Status != 0 && (f.Status < 400 || f.Status >= 500 || f.Status == 429) {
		return false
	}
	for _, code := range f.Codes {
		if _, ok := overflowCodes[strings.ToLower(strings.TrimSpace(code))]; ok {
			return true
		}
	}
	text := strings.ToLower(f.Message)
	if text == "" {
		return false
	}
	for _, re := range overflowExclusions {
		if re.MatchString(text) {
			return false
		}
	}
	for _, re := range overflowPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// MarkContextOverflow returns err wrapped as a *ContextOverflowError when the
// failure describes a context-window refusal, and err unchanged otherwise
// (including when it is already marked).
func MarkContextOverflow(err error, f ProviderFailure) error {
	if err == nil || errors.Is(err, ErrContextOverflow) || !f.IsContextOverflow() {
		return err
	}
	actual, limit := OverflowTokenCounts(err.Error())
	return &ContextOverflowError{Err: err, Actual: actual, Limit: limit}
}

// IsContextOverflow reports whether err is a context-window refusal: by
// identity when a driver marked it, and otherwise by its text, with the status
// read from the text where the driver printed one. The text path exists for
// errors that never passed a driver's marking (an injected test driver, an
// error a layer re-created from a string); production drivers mark theirs.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrContextOverflow) {
		return true
	}
	text := err.Error()
	return ProviderFailure{Status: StatusFromText(text), Message: text}.IsContextOverflow()
}

// statusPatterns find the HTTP status a driver printed into an error's text:
// the SDKs' `POST "url": 400 Bad Request`, the hand-rolled drivers' "status
// 400" / "(status 400)", and genai's "Error 400, Message: ...".
var statusPatterns = []*regexp.Regexp{
	regexp.MustCompile(`": (\d{3}) [A-Z]`),
	regexp.MustCompile(`(?i)\bstatus(?: code)?:? (\d{3})\b`),
	regexp.MustCompile(`\bError (\d{3}), Message:`),
	regexp.MustCompile(`\bHTTP (\d{3})\b`),
}

// StatusFromText returns the HTTP status printed in an error's text, or 0.
func StatusFromText(text string) int {
	for _, re := range statusPatterns {
		if m := re.FindStringSubmatch(text); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n
			}
		}
	}
	return 0
}

// overflowCountPatterns read the prompt size and the window from refusals that
// state both. Group names make the order a provider prints them in irrelevant.
// "output" is the reserved output budget Anthropic adds to the input; the
// usable window for the input is limit minus output.
var overflowCountPatterns = []*regexp.Regexp{
	// Anthropic: "prompt is too long: 1114231 tokens > 1000000 maximum"
	regexp.MustCompile(`(?i)prompt is too long: (?P<actual>\d+) tokens > (?P<limit>\d+) maximum`),
	// Anthropic: "input length and `max_tokens` exceed context limit: 188240 + 21333 > 200000"
	regexp.MustCompile("(?i)exceed context limit: (?P<actual>\\d+) \\+ (?P<output>\\d+) > (?P<limit>\\d+)"),
	// OpenAI chat / OpenRouter / vLLM: "maximum context length is 128000
	// tokens. However, your messages resulted in 130000 tokens." OpenRouter
	// says "you requested about 234543 tokens".
	regexp.MustCompile(`(?i)maximum context length is (?P<limit>\d+) tokens.*?(?:resulted in|requested(?: about)?) (?P<actual>\d+) tokens`),
	// Gemini: "The input token count (1200000) exceeds the maximum number of
	// tokens allowed (1048576)."
	regexp.MustCompile(`(?i)input token count \((?P<actual>\d+)\) exceeds the maximum number of tokens allowed \((?P<limit>\d+)\)`),
	// Copilot: "prompt token count of 140000 exceeds the limit of 128000"
	regexp.MustCompile(`(?i)prompt token count of (?P<actual>\d+) exceeds the limit of (?P<limit>\d+)`),
}

// OverflowTokenCounts returns the prompt size and the window a refusal states,
// in the provider's tokens, or 0, 0 when it states neither.
func OverflowTokenCounts(text string) (actual, limit int) {
	for _, re := range overflowCountPatterns {
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		a, aerr := strconv.Atoi(m[re.SubexpIndex("actual")])
		l, lerr := strconv.Atoi(m[re.SubexpIndex("limit")])
		if aerr != nil || lerr != nil {
			continue
		}
		if i := re.SubexpIndex("output"); i > 0 {
			if out, err := strconv.Atoi(m[i]); err == nil && out < l {
				l -= out
			}
		}
		return a, l
	}
	return 0, 0
}
