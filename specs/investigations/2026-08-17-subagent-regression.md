# Sub-agent regression — diagnosis

Task `b0f1227d-f568-48d7-9377-91bf49598009`. Diagnosis only; no behavior changes
landed. All measurements from the live DB (port 5434, `reliant`,
`threads.origin='spawn'`).

## Headline

**There is no code-level regression in the spawn path.** The three symptoms
reported (agents "spend 30+ min orienting", "do no work", "don't follow
directions") decompose into one chronic problem and one measurement artifact:

1. **Chronic, not new:** sub-agents average ~1.1 tool calls per assistant turn.
   That has been true every day back to 2026-07-29. The batching guidance has
   never been effective; it did not newly stop working.
2. **The "never did any work" spike on Aug 16–17 is an artifact of task mix plus
   parent-initiated kills.** Of 21 zero-edit spawn threads on Aug 16–17, 15 were
   research/review tasks that were never supposed to edit ("Forensics:",
   "Blast radius:", "Review uncommitted:", "Cancellation architecture: full
   map"), and 6 were explicitly terminated mid-flight by the parent.
3. **Per-turn latency did not regress.** It improved.

The user's own hypothesis — *"I think it did that because they were taking too
long perhaps?"* — is correct, and it is the actual causal chain. The parent
killed slow children; the kills produced the zero-edit rows; the zero-edit rows
were read as "sub-agents no longer do work."

## Evidence

### The zero-edit threads were killed by the parent, not by code

The three long Aug-17 "Item B" threads (114–126 messages, 22–24 min, zero edits)
each end with a parent-injected user turn. Verbatim, from
`message_content_blocks`:

- `76317b05` ord=116: "Status check — you've been running ~20min and I see no
  edits on disk yet … Reply briefly with:"
- `19d38796` ord=112: "STOP — I'm taking task 1 (the mailbox drain hardening)
  myself, since nothing is on disk after 20min … Do NOT make any further edits."
- `c2b699d7` ord=120: "STOP — I'm taking this task over myself. Nothing is on
  disk after 20min … Do NOT make any further edits. Reply with a short status
  only:"

Their final assistant turns are compliant answers to that question, not
self-directed stops — e.g. `c2b699d7` ord=121: "**No edits made.** I only read
files (spec, `step_executor.go`, `stream_finalized.go`, `call_llm.go` …).
Nothing changed on disk."

So `edits=0` on these rows records **the parent's instruction being obeyed**. A
metric that counts mutating tool calls will score a correctly-obeyed "do not
edit" as total failure.

### Cross-tab, Aug 16–17 (n=26)

| | interrupted | not interrupted |
|---|---|---|
| zero-edit | 6 | 15 (research/review by title) |
| edited | 0 | 5 |

Every thread given an implementation task and left alone edited. The five
"Fix C / Fix D / Fix E / Fix #2 / orphaned-stream" threads produced 10–32 edits
each.

### Time-to-first-edit did NOT regress

Restricting to implementation-titled spawn threads:

| day | impl threads | avg ordinal of first edit | avg min to first edit |
|---|---|---|---|
| 08-10 | 4 | 49 | 13.8 |
| 08-11 | 9 | 39 | 15.0 |
| 08-12 | 13 | 48 | 9.6 |
| 08-13 | 5 | 37 | 5.6 |
| 08-14 | 4 | 32 | 5.8 |
| 08-15 | 2 | 91 | 21.9 |
| 08-16 | 1 | 17 | 4.7 |
| **08-17** | **8** | **33** | **3.7** |

Aug 17 reaches its first edit *faster than any prior day* — 3.7 min vs a 5.6–15.0
baseline. The "30+ minutes before doing anything" impression is not visible in
per-thread data.

### Per-turn latency did not regress

Median/mean seconds between consecutive messages, spawn threads:

| day | steps | avg gap | median gap |
|---|---|---|---|
| 08-11 | 18360 | 9.9s | 2.7s |
| 08-14 | 12392 | 11.3s | 2.8s |
| 08-16 | 891 | 8.8s | 1.8s |
| 08-17 | 1098 | 8.6s | 2.1s |

### Where the 30 minutes actually goes: the re-spawn ladder

Four sequential threads attacked the same "Item B" work, each killed for being
slow, each starting from zero context:

```
15:18  233f749c  Item B: feasibility of stable assistant-msg id   54 msgs  0 edits
15:35  76317b05  Item B: stable call_llm identity, retire the w   126 msgs  0 edits
16:00  19d38796  Harden the mailbox drain, then finish B          114 msgs  0 edits
16:33  c2b699d7  B-part-1: stable key + detached partial persist  122 msgs  0 edits
```

75 minutes of wall clock, 416 messages, zero edits — and **each child re-derived
the same orientation from scratch**, because a spawn starts with no history. The
parent's 20-minute patience window is shorter than the time this task needs at
1.07 calls/turn, so the loop cannot converge: every child is killed during
orientation, and the next child repeats it. That is the user's perceived
"30+ minutes of orienting with no work," and it is real — it is just an
*aggregate across serially-killed siblings*, not one slow agent.

### Batching is chronic and correlates with model, not prompt

Per-thread averages, Aug 17: the three highest-batching threads are 1.88–2.20
calls/turn; the eight lowest are 1.00–1.07, and those eight are the
claude-5-sonnet implementation threads. The month-long norm is 1.07–1.28. The
one outlier day (Aug 15, 2.33/turn, 276 turns with 3+ calls) was predominantly
`gpt-5.5`. Same prompt, same skills, same repo — 2× the batching. This points at
**model-dependent instruction adherence**, not a missing-guidance bug.

## What I ruled out

- **Tools missing from spawned agents.** `edit` (29) and `write` (5) appear on
  Aug 17, `edit` (12) on Aug 16. The registry is fine.
- **Model downgrade to a cheap default.** Slow threads ran `claude-5-sonnet` and
  `claude-5-opus`; `claude-4.5-haiku` appears on only 2 of 14 Aug-17 threads,
  and the haiku thread (`2bc9b302`, 92 msgs, 2 min) was fast.
- **Premature thread completion by code.** `threads.status` for Aug 16–17 is 3
  (10) / 2 (3) / 5 (1) — the same distribution as the Aug 10–14 baseline. No new
  terminal-status spike. `918a230d`'s history reduction collapses *Temporal
  history events*, not LLM message history.
- **Skills/memory silently dropped for spawned threads.** Not the cause of the
  reported symptoms, so I did not pursue it to a verdict. See the caveat below —
  it remains the one open question worth a follow-up.

## One real, separate finding: skills and memory ride in *history*, not the system prompt

`getSystemPrompts` (`internal/workflow/runtime/activities/handlers/call_llm.go:1503`)
returns only the base prompt, working-directory block, multi-repo hint, a skills
*announcement*, and any `system_prompt` override. Memory and preloaded skill
bodies are instead prepended as **history messages** (`call_llm.go:750-771` for
memory, `:796-838` for seeded skills), and history then passes through
`prepareHistoryForLLM` → `TrimMessagesToFitContextWindow`
(`llm_request.go:233`). System prompts are passed to the trimmer only as a size
input; the *messages* are what get dropped.

So in a long-running thread near the context limit, the batching guidance —
which lives in memory and in the `general-agent` skill body, both delivered as
messages — is trimmable, while the base prompt is not. That is a genuine
structural fragility and a plausible contributor to the chronic 1.1 calls/turn
in long threads. It is **not** the Aug-16/17 regression, and the observed threads
(75–182 messages) are likely nowhere near the trim threshold. Worth a test.

## Recommended fixes, ranked

**1. Fix the parent's supervision loop, not the child. (High confidence, zero blast radius.)**
The defect is in how work was delegated, not in the spawn machinery. Concretely:
judge a child on progress reported, not on `git diff`; a research/spec task has
no on-disk footprint at 20 minutes and never will. When a child is killed, hand
its findings to the successor instead of re-spawning cold — the four Item-B
threads each paid full orientation cost for work the previous one had already
done. If a task needs edits within N minutes, say so in the brief.

**2. Move the batching guidance into the non-trimmable system prompt. (Medium-high confidence, small blast radius.)**
One-line-ish change in `getSystemPrompts`: emit a short batching directive
alongside the working-directory block. Cheap, and it removes the
trim-vulnerability described above. Pair it with a test asserting the assembled
system prompt contains the directive for a spawned thread.

**3. Treat batching as model-conditional. (Medium confidence, needs data.)**
The Aug-15 `gpt-5.5` result suggests prompt wording is not the lever. Before
rewriting guidance again, A/B one model against another on an identical brief.
Rewording a prompt that four model families already ignore is the lowest-yield
option available, despite being the most tempting.

**4. Do NOT "fix" the zero-edit metric by making agents edit sooner. (Stated to prevent it.)**
Three of the six killed threads had correctly stopped at a genuine gating
question — `19d38796`'s brief even says "A previous agent investigated and
correctly STOPPED at a gating correctness question." Optimizing for early edits
would destroy that judgment.

## Caveats

- Aug 16–17 is n=26 threads against baselines of n=77–103. Day-over-day rates on
  that base are noisy; the task-mix explanation is well-supported by titles and
  transcripts, but the "71% → 92% never edited" figures should not be treated as
  a stable trend.
- Thread classification into "implementation" vs "research" is by title regex,
  which is a heuristic.
- I did not reach a verdict on whether `params.skills: [general-agent]` from
  `presets/general.yaml` actually resolves into a spawned thread's context. The
  spawn path routes through `mergePresetParams`
  (`internal/workflow/runtime/inline_workflow_executor.go:304`) and
  `args.GetSkills()` in `call_llm.go:796`; `skill` tool calls appear 13× on
  Aug 15 and 63× on Aug 13 but 0× on Aug 16–17, which is suggestive but is also
  consistent with the task mix. Open.
