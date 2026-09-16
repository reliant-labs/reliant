# Context providers: who decides what the model sees

**Status:** proposal, for discussion. Supersedes the `context:`-switch sketch in
this file's first draft — see "Why not a switch" below.

Backed by `INJECTION_POINTS.md` (inventory of all 29 injection points) and
`WEB_CURATION_DEPS.md` (what reliant-web actually depends on), plus direct
verification of the claims marked below.

---

## The enabling fact

**Turning injection off breaks nothing structurally in reliant-web.**

Every always-on injection is prompt-side only: assembled in memory, handed to
the driver, never persisted and never streamed. The UI builds its transcript
exclusively from persisted `messages` rows, so it *cannot see* the injected
material. The code says so at `call_llm.go:1126-1148` — "These are NOT saved to
DB — re-injected each turn".

What degrades is agent behavior quality, not UI correctness. That is exactly the
trade an API caller is asking to make, and it means this is a design question,
not a migration.

---

## Why not a switch

My first draft proposed a `context:` block enumerating what to include —
`persona: reliant`, `memory: [project]`, and so on. That is better than a
boolean, and it is still the wrong shape, for one reason:

**every entry is a feature I decided to offer.** A caller who wants their own
persona, their own memory convention, their own per-request environment block
gets it only if I anticipated it and added a key. An OEM building on the engine
gets a fixed menu. The extensibility stops at my imagination, which is the same
failure as the permission ladder — a small set of named bundles, none of which
is what a given caller needs.

The inversion: **the engine supplies nothing implicitly. Context is
*contributed*, by named providers, and today's curated behavior is just the set
of providers reliant-coding happens to register.** Anyone can add one. Nothing
about ours is privileged.

---

## The model

A **context provider** contributes to the request. It has a name, and it
returns content:

```yaml
# The coding product
context:
  - reliant/persona            # built-in, ships with the coding product
  - reliant/workspace          # working directory + this project's repos
  - reliant/memory             # reliant.md / AGENTS.md / per-repo
  - mcp://my-server/house-style   # ANY MCP prompt
  - caller                     # whatever the API caller supplied on this request

# Some other product, on the same engine
context:
  - mcp://their-server/their-persona
  - mcp://their-server/per-run-state
  - caller
```

`context: []` is a raw run: the model sees `system_prompt`, `messages`, the
thread, and tool descriptions. Nothing else.

The second example matters only in that **we cannot say what it is**. It names
no provider we shipped, contains no concept from our domain, and needed no
engine change. If describing the design requires naming the products it
supports, the design is a menu.

Three properties make this extensible rather than a menu:

**1. Ours are not special.** `reliant/persona` is a provider registered by the
coding product, in exactly the form a third party would register one. If it
cannot be expressed as a provider, the provider interface is wrong and we fix
that rather than special-casing ourselves. This is the parity test from
`ENGINE_SPLIT_PLAN.md` applied to context.

**2. MCP is the extension mechanism, and we already have it.** Verified: our MCP
client fully implements `ListPrompts` / `GetPrompt` / `ListResources` /
`ReadResource` (`internal/mcp/client.go:334-492`) — and **nothing calls them.**
`rg` across `internal/llm/` and `internal/workflow/` for those symbols returns
zero hits. We built the standard mechanism for "a third party supplies context
to a model" and then wired only the tools half.

That is the answer to "extensible for others": a user who wants their own house
style writes an MCP server exposing a prompt, and names it. No engine change, no
feature request, no key I had to think of. It also composes with what already
exists — MCP servers are already per-project configured and already surface
tools.

**3. The caller is a provider too.** `SendMessageRequest.messages` is already
`repeated InputMessage`, and `InputMessage.role` already accepts
`MESSAGE_ROLE_SYSTEM` (`chat.proto:375-379`, handled at `chat_send.go:111`). A
caller can already contribute system-level content per request. The `caller`
provider just names that as a first-class position in the ordering rather than
an accident of where the handler happens to write it.

### Ordering and precedence

Providers are a **list, and order is the precedence**. No merge strategy, no
priority integers — the thing that bites everyone who builds a config system.
If two providers say contradictory things, the later one is later in the prompt,
which is a rule you can reason about without reading the engine.

### What providers cannot do

The one hard rule, learned from the skill-suggestion bug already fixed in #257:
**a provider contributes its own content; it never edits the caller's.** The
suggester used to append into the user's message, which made the user's words
not be the user's words. A provider returns blocks; it does not get a mutable
handle on the request.

---

## What is NOT a provider

Only one category stays out:

**Declared by the author** — `args.system_prompt`, `args.messages`,
`ApplyDefaults` filling a workflow's own declared defaults. These are the
contract. An API caller gets them too.

That is the whole list. An earlier draft of this document had a second category
— "describes the executor" — holding the shell's per-OS description, the
`load_tool` deferred-tool list, and the multi-repo `repo` hint, on the grounds
that removing them makes the agent *wrong* rather than freer.

**It does not survive, and the way it failed is the lesson.** It was defended by
pointing at a use case: for a coding agent, these things are true, so they must
always be supplied. But a category justified by a use case only holds for that
use case. Name any product without a git checkout and the multi-repo hint stops
describing the executor and starts describing a world the caller does not live
in; the working-directory block asserts "the root of the project you are working
on" with no referent.

The general form: **any rule of the shape "this must always be on, because
<situation>" is wrong, whatever the situation.** It encodes an assumption the
engine has no standing to make. The fix is not to enumerate more situations —
that is the menu again, one entry longer. It is to have no category that needs a
situation to justify it.

The current code half-knows this. The working-directory block is guarded
(`if workingDir != ""`, `call_llm.go:2154`), so it degrades when there is no
project. The persona sentence above it is **unconditional**: every agent on this
engine is told it is "a world class Software Engineer" that should be "careful
with destructive commands… such as git checkout, git stash". Nothing is wrong
with that sentence. It is wrong that the engine asserts it rather than a
provider contributing it.

### The rule that actually holds

**A tool describes itself. Everything else is a provider.**

The shell's per-OS description is **already** the shell tool's own
`Description()` (`shell.go:292` → `shellDescription(s.platform)`), not a
system-prompt injection — so it needs no change and no exception. An agent
holding the shell gets it, an agent without the shell never sees it, and the
engine decides nothing. Same for the `load_tool` deferred list, which is
`load_tool`'s own description.

The multi-repo hint is the interesting one, because **it is already redundant.**
Every tool taking a `repo` param already documents it in its JSON schema:

> `Multi-repo only. Which repo the path is relative to: 'root' for the project
> root, or a repo name (e.g. 'api', 'web'). Omit in single-repo projects…`

— on `write`, `edit`, `shell`, `find_replace`, `move_code`, `save_attachment`,
and more. The prompt block adds only the *enumeration of this project's repos*,
which is project context, not tool documentation. It is a provider
(`reliant/workspace`), present when a caller lists it and absent otherwise.

So the boundary is not "curation vs. mechanism". It is **self-description vs.
contribution**:

| | Where it lives | Who decides |
|---|---|---|
| What a tool is and how to call it | the tool's own description/schema | the tool |
| What the caller declared | `system_prompt`, `messages`, args | the workflow author |
| Everything else | a named provider | the caller's provider list |

This also removes the last "trust me, this one has to stay on" from the design.
Nothing is unconditional except the tools the caller asked for and the arguments
they wrote.

### Checking the design without naming a use case

Use cases are infinite, so a design validated against a list of them is only
ever validated against that list. These three questions are answerable by
reading the code, and none requires knowing what anyone is building:

1. **Can the engine name anything it always supplies?** Every such thing is an
   assumption the engine is making on the caller's behalf. The answer should be
   "only what the caller declared."
2. **Is our provider registered through the same interface a stranger's is?** If
   `reliant/persona` has a privileged path — loaded earlier, exempt from
   ordering, unable to be dropped — then ours is a special case and everyone
   else's is a plugin.
3. **Does adding a context source require an engine change?** If yes, the
   extension point is us, and the design has failed regardless of how many
   sources we ship.

A concrete scenario is useful for *finding* a violation — that is how the
"executor-describing" category fell — but it is never what establishes the rule.

---

## Migration: reliant-web changes nothing

The default provider list is today's behavior. A workflow with no `context:` key
resolves to the coding product's registered set — persona, memory, skills — so
every existing workflow and the whole web app are unchanged.

That is the reverse of the usual "add a flag, default off", and it is right
here: the curated behavior is load-bearing for the shipped product, and making
it explicit later is mechanical, whereas dropping it silently is a quality
regression nobody sees in a diff.

The gate for the plumbing PR: **a test asserting the assembled prompt is
byte-identical before and after.** If the default list reproduces today's prompt
exactly, the refactor is safe by construction.

---

## Sequencing

1. **Wire MCP prompts and resources into the agent.** Standalone value
   regardless of everything else, and it is the extension point — the client is
   already written, so this is consumption, not construction.
2. **Extract each hardcoded injection into a named provider**, with the default
   list preserving current output byte-for-byte. Mechanical, testable, no
   behavior change.
3. **Make the list declarable**, so `context: []` becomes possible and the raw
   API run exists.
4. **Register the caller as a provider**, giving per-request contribution a
   defined position rather than an implicit one.

Step 2 is the only one with real risk, and the byte-identical test retires it.

---

## Open questions

1. **What is a provider's signature?** Simplest that works: `(request context) →
   []block`. But some want to be conditional (skills suggest only when the
   request matches), which argues for the engine passing enough context to
   decide, and against providers seeing the whole request. I lean narrow:
   provider sees the user's text and project config, returns blocks or nothing.
2. **Per-turn or per-run?** Most current injections are recomputed every turn,
   which costs tokens and hurts prompt-cache stability. A provider could declare
   `stable: true` to be assembled once. Worth designing in, since retrofitting
   caching is painful.
3. **Can a provider fail?** An MCP prompt server that is down — hard-fail the
   run, or drop that provider and continue? I lean: declared providers hard-fail
   (you asked for it), discovered ones degrade.
4. **What is a provider addressed by?** `reliant/persona` and
   `mcp://server/prompt` are two namespaces in one list. Clean, or should MCP
   providers be registered under a local name first so the list is uniform? The
   uniform version is tidier; the direct version is one less indirection to
   explain.
5. **`project_path` is injected unconditionally** (`chat_workflow.go:168-170`)
   and spawned workflows read it. For a chatless, projectless run this is where
   context meets the run-container work in `ENGINE_SPLIT_PLAN.md`.

---

## Appendix: the Claude Code prompt capture

Unrelated to this design; recorded here because the inventory surfaced it.

On `sk-ant-oat-*` keys the driver prepends 4–5 system blocks from
`internal/llm/drivers/anthropic/ccprompts/*.txt` before everything the workflow
declares. The `output_*.txt` files contain a literal `gitStatus:` block holding
a **real captured working tree** — branch `fix/vite-base-absolute-deep-routes`,
real commit SHAs and subjects, ~30 real file paths. I verified the paths exist
in this repo, so it is genuine capture rather than placeholder; the `Environment`
block just above it *is* sanitized (`/path/to/project`), which makes the
un-sanitized `gitStatus` look like an oversight.

It ships on every request on that key path including compaction and title
generation, and is stale by construction.

**It cannot simply be deleted.** `claude_code_prompts_embed.go:8-12` states the
files must stay byte-identical "so the spoof is not flagged and prompt-cache
keys match Claude Code's", and the `claudeCodeProfile` struct exists to keep
User-Agent, SDK version, billing header and prompt bytes coherent. Editing the
bytes breaks the property the design depends on.

Options: leave it; re-capture on a clean checkout (my recommendation, if that
reproduces cache behavior); or drop the block and accept the fingerprint break.
Needs a human decision — I have not touched these files.
