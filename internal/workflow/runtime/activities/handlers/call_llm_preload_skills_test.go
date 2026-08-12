// Copyright (c) 2025 Reliant Labs

package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/llm"
	reliantdriver "github.com/reliant-labs/reliant/internal/llm/drivers/reliant"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// catalog mirrors how forge-namespaced skills land in projectCfg.Skills:
// SkillPath is the hierarchical path ("forge/db") and Name is the leaf name.
func preloadTestCatalog() []cfgpkg.StoredSkill {
	return []cfgpkg.StoredSkill{
		{Name: "getting-started", SkillPath: "forge/getting-started", Body: "GS body"},
		{Name: "db", SkillPath: "forge/db", Body: "DB body"},
		{Name: "proto", SkillPath: "forge/proto", Body: "PROTO body"},
		{Name: "empty", SkillPath: "forge/empty", Body: "   "},
	}
}

func TestLoadSkillForInjection_ExactFoldAndBody(t *testing.T) {
	skills := preloadTestCatalog()

	// Exact match returns the resolved leaf name and the skill body.
	name, body, ok := tools.LoadSkillForInjection(skills, "forge/db")
	require.True(t, ok)
	require.Equal(t, "db", name)
	require.Contains(t, body, "DB body")

	// Case-insensitive / whitespace-tolerant match — the injection resolver must
	// agree with the runtime skill tool, otherwise a preloaded skill silently
	// no-ops while the agent can still load it by hand.
	_, _, ok = tools.LoadSkillForInjection(skills, "Forge/DB")
	require.True(t, ok)
	_, _, ok = tools.LoadSkillForInjection(skills, "  forge/proto  ")
	require.True(t, ok)

	// Empty-body, unknown, and empty paths do not resolve.
	_, _, ok = tools.LoadSkillForInjection(skills, "forge/empty")
	require.False(t, ok)
	_, _, ok = tools.LoadSkillForInjection(skills, "forge/does-not-exist")
	require.False(t, ok)
	_, _, ok = tools.LoadSkillForInjection(skills, "")
	require.False(t, ok)
}

// The seed must never be attributable to the model. A fabricated
// assistant(tool_call skill) is indistinguishable from one the model issued, so
// a unit whose brief named skills it never "called" concluded it had erred and
// burned its whole life re-loading them (run b7aa4056: "I loaded the wrong skill
// by mistake."). This pins the shape: no assistant turn, no tool_call, no
// tool_result — one user turn that says the harness did it.
func TestBuildSeededSkillMessages_NeverAttributedToTheModel(t *testing.T) {
	cfg := &cfgpkg.Config{Skills: preloadTestCatalog()}

	msgs, injected, oversized, _ := buildSeededSkillMessages(cfg, []string{"forge/getting-started", "forge/db"})
	require.Empty(t, oversized)
	require.Equal(t, []string{"getting-started", "db"}, injected)
	require.Len(t, msgs, 1, "the whole preload is one turn")

	require.Equal(t, message.User, msgs[0].Role,
		"a preloaded skill seeded as an assistant turn reads to the model as its own action")
	for _, part := range msgs[0].Parts {
		switch part.(type) {
		case message.ToolCall:
			t.Fatal("seed carries a ToolCall: the model cannot tell a fabricated call from one it made")
		case message.ToolResult:
			t.Fatal("seed carries a ToolResult: a tool_result implies a call the model did not make")
		}
	}

	text := msgs[0].Content().Text
	// Attribution, and the specific wrong conclusion it must pre-empt.
	require.Contains(t, text, "The Reliant harness loaded")
	require.Contains(t, text, "You did NOT call the skill tool")
	require.Contains(t, text, "ALREADY\nSATISFIED")

	// Both bodies are present, each named, in the requested order.
	require.Contains(t, text, `<skill name="getting-started" path="forge/getting-started">`)
	require.Contains(t, text, "GS body")
	require.Contains(t, text, `<skill name="db" path="forge/db">`)
	require.Contains(t, text, "DB body")
	require.Less(t, strings.Index(text, "GS body"), strings.Index(text, "DB body"))

	// Byte-stable across turns: the Anthropic prompt cache dedups the seed only
	// if the same request produces the same bytes.
	again, _, _, _ := buildSeededSkillMessages(cfg, []string{"forge/getting-started", "forge/db"})
	require.Equal(t, text, again[0].Content().Text)
}

func TestBuildSeededSkillMessages_SkipsEmptyUnknownAndDedupes(t *testing.T) {
	cfg := &cfgpkg.Config{Skills: preloadTestCatalog()}

	// An empty body and an unknown path both fail to resolve, so neither is
	// injected: there is no skill body in the seed at all. The seed is still
	// emitted, carrying only the notice — the model is told what was requested
	// for it and did not arrive. Saying nothing there is what let a scaffold
	// phase choose its proto surface and author its migrations without `db`:
	// the node ran, the answer looked like any other, and the only trace was a
	// count in an INFO line.
	msgs, injected, _, missing := buildSeededSkillMessages(cfg, []string{"forge/empty", "forge/missing"})
	require.Empty(t, injected)
	require.Equal(t, []string{"forge/empty", "forge/missing"}, missing)
	require.Len(t, msgs, 1, "nothing resolved, but the notice is still worth a turn")
	text := msgs[0].Content().Text
	require.NotContains(t, text, "<skill ", "nothing resolved, so no skill body is delivered")
	require.Contains(t, text, "forge/empty")
	require.Contains(t, text, "forge/missing")

	// Two paths that resolve to the same skill inject one body, once — and a
	// deduped alias is not a miss, so nothing is reported unavailable.
	msgs, injected, _, missing = buildSeededSkillMessages(cfg, []string{"forge/db", "Forge/DB"})
	require.Equal(t, []string{"db"}, injected)
	require.Empty(t, missing)
	require.Len(t, msgs, 1)
	require.Equal(t, 1, strings.Count(msgs[0].Content().Text, "DB body"))

	// Cold catalog and empty request both short-circuit to no seed, but they
	// are different facts: only the cold catalog owes the caller a miss, and it
	// owes one for every path that was asked for.
	msgs, injected, _, missing = buildSeededSkillMessages(nil, []string{"forge/db"})
	require.Empty(t, msgs)
	require.Empty(t, injected)
	require.Equal(t, []string{"forge/db"}, missing)

	msgs, _, _, missing = buildSeededSkillMessages(cfg, nil)
	require.Empty(t, msgs)
	require.Empty(t, missing)
}

// An oversize skill must arrive at the model the same way whether the agent
// loaded it by hand or the node preloaded it. Before the cap, the hand-load path
// ran through the ToolWrapper's output limiter while the seed path bypassed it
// entirely — so the same skill reached the model as two different documents, and
// the preloaded copy carried the full file into the cached prefix of every turn.
func TestBuildSeededSkillMessages_OversizeSkillIsCappedAndMarked(t *testing.T) {
	body := "SKILL HEAD\n" + strings.Repeat("guidance line that fills the budget\n", 800) + "SKILL TAIL\n"
	require.Greater(t, len(body), tools.MaxSkillBodySize, "fixture must exceed the budget to exercise the cap")

	catalog := []cfgpkg.StoredSkill{{Name: "db", SkillPath: "forge/db", Description: "d", Body: body}}
	cfg := &cfgpkg.Config{Skills: catalog}

	msgs, injected, oversized, _ := buildSeededSkillMessages(cfg, []string{"forge/db"})
	require.Equal(t, []string{"db"}, injected)
	require.Equal(t, []string{"db"}, oversized, "an oversize preload must be reported, not absorbed")
	require.Len(t, msgs, 1)

	text := msgs[0].Content().Text
	require.Contains(t, text, "SKILL HEAD")
	require.NotContains(t, text, "SKILL TAIL")
	// A dropped tail must be announced AND made recoverable. The announcement
	// alone was not enough: it used to point at a SKILL.md the model cannot
	// open, so the tail of a preloaded skill was unreachable in practice.
	require.Contains(t, text, "BYTES REMAIN", "a dropped tail must be announced in the delivered text")
	require.Contains(t, text, `skill(action="load"`,
		"the announcement must name the call that fetches the remainder")

	// The hand-load path (skill tool through its ToolWrapper) must produce the
	// exact same bytes — that equality is the whole point of the shared cap, and
	// it is what survives changing the seed's envelope. Only the framing around
	// the body differs between the two delivery paths.
	handLoaded, err := tools.NewSkillTool(catalog).Run(
		&rctx.ToolContext{Context: context.Background()},
		tools.ToolCall{ID: "c1", Name: tools.ToolSkill, Input: `{"action":"load","path":"forge/db"}`},
	)
	require.NoError(t, err)
	require.LessOrEqual(t, len(handLoaded.Content), tools.MaxSkillBodySize)
	require.Contains(t, text, handLoaded.Content,
		"preloaded and hand-loaded skill bodies must be byte-identical")
}

func TestInsertSeededMessagesAfterFirstUserTurn(t *testing.T) {
	seed := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "seed"}}},
	}

	// Memory (System) prefix + user turn: seed lands right after the brief that
	// named the skills.
	history := []message.Message{
		{Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "memory"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
	}
	out := insertSeededMessagesAfterFirstUserTurn(history, seed)
	require.Len(t, out, 4)
	require.Equal(t, message.System, out[0].Role)
	require.Equal(t, "hello", out[1].Content().Text)
	require.Equal(t, "seed", out[2].Content().Text)
	require.Equal(t, "hi", out[3].Content().Text)

	// Agent role counts as the leading user turn (it normalizes to user).
	agentHistory := []message.Message{
		{Role: message.Agent, Parts: []message.ContentPart{message.TextContent{Text: "spawn prompt"}}},
	}
	out = insertSeededMessagesAfterFirstUserTurn(agentHistory, seed)
	require.Len(t, out, 2)
	require.Equal(t, message.Agent, out[0].Role)
	require.Equal(t, "seed", out[1].Content().Text)

	// No user turn to anchor to: the seed is dropped.
	noUser := []message.Message{
		{Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "sys"}}},
	}
	out = insertSeededMessagesAfterFirstUserTurn(noUser, seed)
	require.Len(t, out, 1)
	require.Equal(t, message.System, out[0].Role)
}

// The seed must survive ConvertMessages on the provider it is actually used
// with.
//
// This test used to also pin the inverse as a counter-example: a System-role
// message was SILENTLY DROPPED by the OpenAI-compatible family, which had no
// message.System case at all. That was a bug, not a contract — it also ate
// compaction summaries, branch notes, and the agent mailbox envelope, on that
// family only. Every converter now handles System history, so the assertion
// below is that it ARRIVES.
//
// The seed itself stays a user turn regardless: a fabricated turn attributed
// to the agent reads differently to the model than a system note, and that
// reasoning (see preloadedSkillsPreamble) never depended on the driver bug.
func TestSeededSkillTurnSurvivesTheOpenAICompatibleDriver(t *testing.T) {
	cfg := &cfgpkg.Config{Skills: preloadTestCatalog()}
	msgs, injected, _, _ := buildSeededSkillMessages(cfg, []string{"forge/db"})
	require.Equal(t, []string{"db"}, injected)

	client := reliantdriver.NewClient(llm.DriverOptions{})
	converted := client.ConvertMessages(nil, msgs)
	require.Len(t, converted, 1, "the seeded turn must reach the provider, not be dropped by role")

	delivered := client.ConvertMessages(nil, []message.Message{{
		Role:  message.System,
		Parts: []message.ContentPart{message.TextContent{Text: "memory"}},
	}})
	require.Len(t, delivered, 1, "System-role history must reach the OpenAI-compatible driver family")
}

// TestBuildSeededSkillMessages_ReportsUnresolvedPaths pins the difference
// between "delivered a subset" and "delivered what was asked for".
//
// Born red. A preload that silently drops skills is a guard that cannot fail:
// the node runs, the model answers, and the only trace is a count in an INFO
// line. Measured on one run — the first three LLM turns of a scaffold phase
// logged requested=6 injected=2 against a catalog of 37 entries, then 86, then
// 114. The catalog was still filling, and the turns that chose the proto
// surface and authored the migrations ran without `db` or `forge/proto`.
// Nothing failed and nothing said so.
func TestBuildSeededSkillMessages_ReportsUnresolvedPaths(t *testing.T) {
	cfg := &cfgpkg.Config{Skills: preloadTestCatalog()}

	_, injected, _, missing := buildSeededSkillMessages(cfg, []string{"forge/db", "forge/nope", "forge/also-nope"})
	require.Equal(t, []string{"db"}, injected)
	require.Equal(t, []string{"forge/nope", "forge/also-nope"}, missing,
		"every requested path that did not resolve must be reported so the caller can fail")

	// An empty body does not resolve, and must be reported like any other miss —
	// a skill that exists but says nothing is not guidance delivered.
	_, _, _, missing = buildSeededSkillMessages(cfg, []string{"forge/empty"})
	require.Equal(t, []string{"forge/empty"}, missing)

	// The cold-catalog case is the one that actually bit. An absent catalog must
	// report EVERY requested path as missing — "nothing was injected because the
	// catalog was empty" and "nothing was asked for" are different facts, and
	// the old code returned the same value for both.
	_, injected, _, missing = buildSeededSkillMessages(nil, []string{"forge/db", "forge/proto"})
	require.Empty(t, injected)
	require.Equal(t, []string{"forge/db", "forge/proto"}, missing)

	_, _, _, missing = buildSeededSkillMessages(&cfgpkg.Config{}, []string{"forge/db"})
	require.Equal(t, []string{"forge/db"}, missing, "an empty catalog is a cold catalog, not a satisfied request")

	// A duplicate path resolving to an already-seen skill is deduped, NOT
	// missing — otherwise the guard fires on correct input.
	_, _, _, missing = buildSeededSkillMessages(cfg, []string{"forge/db", "Forge/DB"})
	require.Empty(t, missing, "a deduped alias is not a missing skill")

	// Nothing requested, nothing missing.
	_, _, _, missing = buildSeededSkillMessages(cfg, nil)
	require.Empty(t, missing)
}

// TestBuildSeededSkillMessages_TellsTheModelWhatItDidNotGet pins the permanent
// half of the split: a skill that does not exist cannot be retried into
// existence, so the run proceeds — but the model must be told, in the seed it
// actually reads, rather than in a log line nobody sees.
func TestBuildSeededSkillMessages_TellsTheModelWhatItDidNotGet(t *testing.T) {
	cfg := &cfgpkg.Config{Skills: preloadTestCatalog()}

	msgs, injected, _, missing := buildSeededSkillMessages(cfg, []string{"forge/db", "forge/nope"})
	require.Equal(t, []string{"db"}, injected)
	require.Equal(t, []string{"forge/nope"}, missing)
	require.Len(t, msgs, 1)
	text := msgs[0].Parts[0].(message.TextContent).Text
	require.Contains(t, text, "DB body", "what resolved is still delivered")
	require.Contains(t, text, "forge/nope", "the model must be told which skill it did not get")
	require.Contains(t, text, "do not try to load them",
		"otherwise the model burns turns hunting a skill that does not exist")

	// The preamble two paragraphs up tells the model that any skill listed in
	// this turn is ALREADY SATISFIED. Unreconciled, that sentence reads onto
	// the names in the notice and re-creates the exact failure the notice
	// exists to prevent: a model concluding it holds guidance it never got.
	require.Contains(t, text, "ALREADY SATISFIED note does not",
		"the notice must cancel the preamble for the skills that never arrived")

	// Every requested skill missing, catalog warm: still a seed, carrying only
	// the notice. Without it the model silently believes it has guidance the
	// brief promised.
	msgs, injected, _, missing = buildSeededSkillMessages(cfg, []string{"forge/nope", "forge/also-nope"})
	require.Empty(t, injected)
	require.Len(t, missing, 2)
	require.Len(t, msgs, 1, "a notice-only seed is still a seed")
	require.Contains(t, msgs[0].Parts[0].(message.TextContent).Text, "forge/also-nope")

	// A cold catalog emits NO seed — the caller errors and retries, so a notice
	// the model will never read is not worth building. Both shapes of cold
	// count: an absent config, and the one that actually bit — a config
	// snapshot that arrived with an empty catalog because it was still filling.
	msgs, _, _, missing = buildSeededSkillMessages(nil, []string{"forge/db"})
	require.Empty(t, msgs)
	require.Equal(t, []string{"forge/db"}, missing)

	msgs, _, _, missing = buildSeededSkillMessages(&cfgpkg.Config{}, []string{"forge/db"})
	require.Empty(t, msgs, "an empty catalog is cold, and cold is retried rather than reported to the model")
	require.Equal(t, []string{"forge/db"}, missing)

	// Nothing missing: no notice.
	msgs, _, _, _ = buildSeededSkillMessages(cfg, []string{"forge/db"})
	require.NotContains(t, msgs[0].Parts[0].(message.TextContent).Text, "<unavailable>")
}

// TestPreloadSkillMissError_FailsOnlyTheTransientCause pins the split itself:
// which miss stops the call and which one it rides through. Both directions are
// load-bearing and each is a different bug if wrong.
//
// Failing an UNSYNCED project is the fix — the snapshot fills asynchronously,
// so the retry is warm and the node gets what it asked for. Failing a synced
// one instead spends the entire retry budget re-asking a question whose answer
// cannot change, and kills a run over a charter typo. Not failing an unsynced
// one is the original bug: a turn silently runs on none of the guidance that
// was declared for it.
func TestPreloadSkillMissError_FailsOnlyTheTransientCause(t *testing.T) {
	requested := []string{"forge/db", "forge/proto"}

	// No daemon push yet: transient, so error and let Temporal retry into a
	// snapshot that has landed.
	err := preloadSkillMissError(false, 0, requested, requested)
	require.Error(t, err)
	require.Contains(t, err.Error(), "forge/db",
		"the error must name what was not delivered, not just count it")
	require.Contains(t, err.Error(), "no daemon has pushed a config snapshot",
		"and must name the cause, so a reader knows a retry is the fix")

	// Synced, path not in the catalog: permanent. Retrying cannot conjure the
	// skill, so the call proceeds and the seed carries the notice instead.
	require.NoError(t, preloadSkillMissError(true, 37, requested, []string{"forge/nope"}))

	// Synced and genuinely EMPTY is permanent too, and is the case emptiness
	// alone could not tell from an unfilled snapshot. Retrying here is the
	// unwinnable loop: the daemon re-sends only on a content-hash CHANGE, so
	// no amount of waiting turns this catalog into a populated one.
	require.NoError(t, preloadSkillMissError(true, 0, requested, requested))

	// Everything resolved: nothing to decide, either way round.
	require.NoError(t, preloadSkillMissError(true, 37, requested, nil))
	require.NoError(t, preloadSkillMissError(false, 0, nil, nil))
}

// TestSkillSuggestionsNeverContradictThePreload pins the one place the harness
// could tell the model to load what it had just told it not to.
//
// The suggester scores the LAST USER MESSAGE and appends its result INTO that
// same message. On a forked fan-out thread there is exactly one user turn — the
// brief — and the preload seed is spliced in right after it; every later turn is
// an assistant turn or a tool result, and a tool result carries role Tool. So
// from turn one onward the seed IS the last user message, which makes it both
// the text the suggester scores and the text it mutates.
//
// Scored against skill BODIES the ranking is not a ranking: a skill's own name
// weighs triple in suggest.Suggest and every body repeats its own name, so the
// preloaded skills win their own contest. The model then reads, contiguously,
// "that instruction is ALREADY SATISFIED. Do not load it again" and "use the
// skill tool to load if needed" naming the very same skills. That is the exact
// stimulus behind the failure preloadedSkillsPreamble exists to prevent — a unit
// concluding it had loaded the wrong skill and spending its life re-loading.
//
// Suppressing the suggestion where the preload spoke is the whole fix, rather
// than subtracting the preloaded names from the result: subtraction removes the
// literal contradiction and leaves the ranking, which is still computed over
// harness prose the user never wrote. Skills the brief did not ask for, endorsed
// by the harness immediately below a block saying the loaded ones are already
// right, read as a correction just as loudly. A call that declares its skills has
// already answered the question the suggester asks.
//
// It also keeps the seed byte-stable. The seed is deliberately pinned to an early
// offset for prompt-cache reuse; appending a catalog-dependent payload to it
// invalidates that prefix on every turn, and does so most while the catalog is
// still filling.
//
// Both halves are derived from what the emitters produce — the seed text from
// buildSeededSkillMessages, the contradicting names from the injected list it
// returns — so a reworded preamble or a renamed skill cannot leave this guard
// asserting nothing.
func TestSkillSuggestionsNeverContradictThePreload(t *testing.T) {
	catalog := preloadTestCatalog()
	cfg := &cfgpkg.Config{Skills: catalog}
	requested := []string{"forge/db", "forge/proto"}

	seed, injected, _, _ := buildSeededSkillMessages(cfg, requested)
	require.Len(t, seed, 1)
	require.NotEmpty(t, injected, "the preload injected no skill bodies, so there is no "+
		"'already satisfied' claim for a suggestion to contradict and this guard would pass "+
		"against anything")

	// The fan-out shape: one user brief, the seed after it, then the model's own
	// turns. Nothing later carries role User, so the seed is what the suggester
	// reads and writes.
	forkedThread := func() []message.Message {
		return []message.Message{
			{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Implement the orders service."}}},
			seed[0],
			{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Looking at the protos."}}},
		}
	}
	require.Equal(t, seed[0].Content().Text, getLatestUserMessageText(forkedThread()),
		"the seed is no longer the last user message — the premise of this guard has moved, "+
			"and the suggestion path it protects now scores something else entirely")

	// The stimulus is real: with no preload signal, the suggester fires on the
	// seed and endorses the very skills the seed says are already loaded.
	control := forkedThread()
	require.Positive(t, injectSkillSuggestions(control, catalog, nil),
		"the suggester no longer fires on a preload seed, so this guard is testing nothing")
	contradicted := 0
	for _, name := range injected {
		if strings.Contains(control[1].Content().Text, "- "+name+":") {
			contradicted++
		}
	}
	require.Positive(t, contradicted,
		"scoring the seed suggested none of the skills the seed itself delivered (%v) — the "+
			"contradiction this guard pins no longer reproduces, so it cannot fail for the "+
			"right reason either", injected)

	// The fix: a call that preloaded skills makes no suggestion, and the seed is
	// returned untouched.
	guarded := forkedThread()
	require.Zero(t, injectSkillSuggestions(guarded, catalog, injected),
		"the harness suggested skills into a turn whose seed already told the model not to "+
			"load them")
	require.Equal(t, seed[0].Content().Text, guarded[1].Content().Text,
		"the preload seed was mutated: it is pinned to an early offset for prompt-cache reuse, "+
			"and a catalog-dependent suffix invalidates that prefix on every turn")
}

// TestSkillSuggestionsStillFireWithoutAPreload keeps the guard above from being
// a switch that turns the feature off. A call that preloaded nothing has a real
// user request as its last user turn, and the suggester is the only thing that
// points at a skill nobody declared.
func TestSkillSuggestionsStillFireWithoutAPreload(t *testing.T) {
	catalog := preloadTestCatalog()
	history := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "PROTO body — regenerate the proto surface"}}},
	}

	require.Positive(t, injectSkillSuggestions(history, catalog, nil),
		"a call with no preloaded skills must still get suggestions; the guard above is a "+
			"condition on the preload, not a removal of the feature")
	require.Contains(t, history[0].Content().Text, "Potentially relevant skills",
		"the reminder must land on the user message the suggester scored")
}
