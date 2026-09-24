# Template / CEL validation gaps under the strict runtime

Status: research, 2026-09-23. No production code was changed; probe tests were deleted.

## Why this matters now

The runtime is strict (see `specs/workflow-history-size.md`). A `save_message`, node config, inject,
or declared output that fails to resolve **fails the step**. Before, some of these failures were
logged and skipped. So anything `StaticAnalysis` lets through is now a run-time failure, often
after expensive LLM work has already happened.

Out of scope, because they are already decided and in progress elsewhere: `save_message.condition`
becoming raw CEL (DirectCelBool), and rejecting `{{ }}` in raw-CEL fields.

## Method and evidence

1. **Dev logs.** `control-plane/.forge/logs/dev/reliant-temporal-worker.log` (193 MB) only goes
   back to the restart at 2026-09-22 23:35. Every hit for `CEL evaluation error|no such key|no such
   overload|undeclared reference` in it is an LLM payload echoing text, not a real failure. The
   API server log has none. So the current log contains **zero live CEL failures**.
2. **Reliant DB history** (5434/`reliant`, SELECT only). `message_content_blocks` keeps the
   agent transcripts that diagnosed real failures over the last ~6 weeks. The distinct
   real-world failure shapes found there:

   | # | Runtime error (verbatim) | Where | Would validation catch it today? |
   |---|---|---|---|
   | F1 | `no such key: scrape_website` | pitch-deck inject; the router skipped the node | **Warning only** (node_order.go) |
   | F2 | `failed to evaluate output "plan_summary": ... no such key: planning` | one-ring `outputs:`; the router skipped `planning` | **Warning only** |
   | F3 | `no such key: guidance` | auditing-agent `execute_audit.save_message`; optional response-tool field | **No** |
   | F4 | `no such key: 1 (type: wrapError, retryable: true): workflow.thread.inject.content` | parallel-compete `nodes.implementations._results["1"]` | **No** (`_results` is dyn) |
   | F5 | `execute_tools.compaction_threshold: ... no such key: compaction_threshold` | a proto field that is declared but unset (`EmitUnpopulated: false`) | **No** (the field exists in the type) |
   | F6 | `failed to evaluate output "has_feedback": no such key: ask_question` | loop outputs, node absent this iteration | No. Fixed in the runtime by `substituteTypedZero` |
   | F7 | `no such overload: size` | `size(x)` where x is null or absent | **No** |
   | F8 | `iter.item.num` → `no such key: num` | runtime bug: iter was not populated in loop bodies | n/a (runtime fix) |

   Every F-row except F8 is an **absent or null value at run time on a path the type system says
   exists**. There were no real-world failures from typos or undeclared names: the validator
   already catches those well (see the probes below).
3. **Corpus.** I ran the real CLI (`go build ./cmd/reliant` then `reliant workflow validate --json`)
   over 23 builtins, 12 example workflows and all 8 `workflow_drafts` rows:
   - Builtins and DB drafts: **0 errors, 105 warnings**. That breaks down as 55 "output expression
     has dynamic type (dyn)", 30 "node X has a condition and may be skipped", and 20 "not
     guaranteed to have executed before". Two examples fail for unrelated reasons (stale demos).
   - Four drafts have `is_valid=0` with `validation_errors=NULL`. They now validate with warnings
     only. `is_valid` is computed once at save time and never recomputed, so it goes stale.
   - Two user drafts (`my-pitch-deck-*`) carry **10 "not guaranteed to have executed" warnings
     each**. That is the exact F1 shape which previously failed in production. Under the strict
     runtime these are latent failures that saved without complaint.
4. **Probes.** I wrote 26 minimal YAMLs, ran each through the CLI validator, and evaluated the same
   expressions with `wfcel.EvaluateTemplate` / `EvaluateBool` against the runtime contexts
   (`PostActivityContext`, `NodeResolutionContext`, `LoopEvalContext`). This was a throwaway test in
   `internal/workflow/cel/`, since deleted. Results are quoted in each gap below.

### What validation already does well (no action)
- Undeclared input: `CEL compilation error: undefined field 'nope' (did you mean 'opt'?)`.
- Typo on a typed node output: `undefined field 'respnse_text' (did you mean 'response_text'?)`.
- Typo on an inline loop output (`nodes.l.doen`) and on a ref child output (`nodes.w.not_an_output`).
- Response-tool field and tool typos read via `nodes.<id>.response_data.*`.
- `string + int` and `int * double` produce `found no matching overload` at compile time. When the
  operand types are known, the type checker works.

## Gaps, ranked

Score = frequency × severity ÷ effort. Frequency and severity are each 1–3 (evidence-weighted);
effort is S=1, M=2, L=3.

| Rank | Gap | Freq | Sev | Effort | Score |
|---|---|---|---|---|---|
| 1 | G1 Unguarded refs to maybe-skipped nodes are warnings | 3 | 3 | S | 9 |
| 2 | G2 Per-site namespace env mismatch (while / config / outputs) | 2 | 3 | S | 6 |
| 3 | G3 Optional response-tool fields read without a guard | 2 | 3 | M | 4.5 |
| 4 | G4 `output.response_data.*` in save_message is not schema-checked | 2 | 2 | S | 4 |
| 5 | G5 `iter` used outside any loop | 1 | 3 | S | 3 |
| 6 | G6 `size()` / concat / arithmetic on nullable values | 2 | 2 | M | 2 |
| 7 | G7 Optional inputs without a default read bare | 1 | 2 | S | 2 |
| 8 | G8 `_results[...]` / dyn loop aggregates | 1 | 3 | M | 1.5 |
| 9 | G9 Structured-agent `response` schema not typed | 1 | 2 | M | 1 |
| 10 | G10 Unset proto message/wrapper fields (`EmitUnpopulated: false`) | 1 | 2 | L | 0.7 |
| 11 | G11 CEL-looking literals in template fields | 1 | 2 | M | 1 |
| — | G12 Entry-point gaps (agent tools, stale is_valid) | — | — | S | process |

---

### G1. Unguarded refs to maybe-skipped nodes are only warnings (F1, F2)

Repro:
```yaml
entry: [a]
nodes:
  - id: a
    type: save_message
    args: {role: user, content: hi}
  - id: b
    type: call_llm
    condition: "false"
    args: {model: claude-4-sonnet}
edges: [{from: a, to: b}]
outputs:
  t: "{{nodes.b.response_text}}"
```
- **Validation today:** `W: probe.outputs.t: node 'b' has a condition and may be skipped ...
  consider using optional chaining`. Router or parallel skips give `W: ... references
  'nodes.scrape_website', but node 'scrape_website' is not guaranteed to have executed before ...`.
  `LoadWorkflowActivity` only rejects on `HasErrors()`, so the workflow runs.
- **Runtime:** `CEL evaluation error: no such key: <node>`. For a skipped `run`/conditional node
  the key exists (skip output), but the field may not. For router/parallel skips the key is absent.
  In declared outputs, `substituteTypedZero` rescues only bare `{{nodes.x.f}}` paths with a schema
  zero (`runtime/loop_output_schema.go:190`). `{{nodes.b.response_text + '!'}}` still fails.
- **Proposed check:** promote the node-order "not guaranteed to have executed" finding to
  **error** in all evaluation sites except bare declared-output paths that `substituteTypedZero`
  will rescue. Keep the rescue rule in one shared predicate so validation and runtime agree.
  For conditional nodes, keep a warning when the skip output supplies the field, and raise an
  error when it does not.
- **False-positive risk:** medium-low. The AST detector already honours `has()`, `?.` and ternary
  guards (I verified that `has(nodes.review) && ... ? ... : 'pass'` produces no finding). Corpus
  impact: 20 findings, all in two user pitch-deck drafts (a real latent bug, F1 shape) plus
  get-it-right, migrate, one-ring and deploy-product-driver conditional cases (30). Each of those
  needs to be triaged against the skip-output rule before flipping. Expect a handful of builtin
  edits.
- **Effort:** S. This is a severity flip plus a shared "rescued by typed zero" predicate.

### G2. The validation env is a superset of every runtime env

`ValidateCELWithCompilation` builds **one** env for all node fields, node/edge conditions and
outputs. It contains `inputs, workflow, nodes, iter, outputs, output` and always includes the custom
functions (`validation/cel.go:1613`, `cel_env.go:94`). The runtime builds a narrower env per site
(`cel/types.go`):

| Site | Runtime namespaces | Custom fns |
|---|---|---|
| node config / inject (`NodeResolutionContext`) | inputs, nodes, iter, workflow | yes |
| edge / node condition (`EdgeEvalContext`) | inputs, workflow, nodes, iter, outputs | yes |
| loop `while` (`LoopEvalContext`, via `evaluateRaw`) | iter, outputs, inputs, nodes | yes (evaluateRaw hard-codes `true`; `LoopWhileCELEnvConfig`'s `false` is used only by a test) |
| save_message (`PostActivityContext`) | output, inputs, workflow, iter | yes (validation already uses this exact env) |

Repros (all validate clean today):
```yaml
- id: a                      # config referencing output/outputs
  type: save_message
  args: {role: user, content: "{{outputs.foo}} {{output.bar}}"}
---
- id: a                      # while referencing workflow
  type: loop
  while: "workflow.id != '' && iter.iteration < 3"
```
- **Runtime:** `CEL compilation error: ERROR: <input>:1:1: undeclared reference to 'outputs'`
  (and the same for `output`, and for `workflow` in `while`). The failure is a hard, deterministic
  compile error at run time.
- **Proposed check:** build one validation env per site, derived from the runtime context's
  `Namespaces()`, so there is a single source of truth. Export the per-site namespace lists from
  `wfcel` and have both sides consume them, as was just done for save_message. Also validate
  `while` against the LoopEval env. Currently it gets only `validateLoopWhileCondition` heuristics
  plus node ordering, and **is never compiled**.
- **False-positive risk:** very low. These are exact runtime compile failures. Corpus: 0 findings
  expected (spot-checked). `outputs.*` in workflow `outputs:` needs care: those evaluate in
  `NodeResolutionContext` (`engine.go:369`), so `outputs` there is also a bug.
- **Effort:** S.

### G3. Optional response-tool fields read without a guard (F3, the known auditing-agent bug)

Repro:
```yaml
- id: a
  type: call_llm
  args:
    response_tool:
      name: audit
      schema: {type: object, properties: {approved: {type: boolean}, guidance: {type: string}}, required: [approved]}
- id: b
  type: execute_tools
  args: {tool_calls: "{{nodes.a.tool_calls}}"}
  save_message: {role: user, content: "FEEDBACK: {{output.response_data.audit.guidance}}"}
- id: c
  type: save_message
  args: {role: user, content: "{{nodes.b.response_data.audit.guidance}}"}
```
- **Validation today:** no findings.
- **Runtime:** `CEL evaluation error: no such key: guidance`. If the tool was not called (the
  response_data is missing the tool key), you get `no such key: audit`.
- **Proposed check:** extend `validateResponseDataAccessWithContext` from "field exists in
  `properties`" to "field is in `required`, or the access is guarded". Reuse the guard-aware AST
  walk from `detectConditionalNodeAccess`: `has(x.f)`, `x.?f`, `'f' in x`, ternary/`&&`
  short-circuit dominance. Apply the same rule to the `<tool>` segment, because a response tool
  may not be called at all. Being condition→content guard-aware (the save_message condition
  dominates content) is a nice-to-have. Once the condition is raw CEL, pass the condition's
  proven `has()` set into content validation.
- **False-positive risk:** low-medium. The schema is authoritative about `required`. Loosely
  authored schemas that omit `required` would flag every access, so start at **warning** for
  schemas with no `required` array and **error** when `required` exists and excludes the field.
  Corpus: exactly 1 true positive (auditing-agent:193, `guidance`). Line 231
  `approved == true` is required, so it is not flagged.
- **Effort:** M. This needs a JSON-schema `required` plumb plus the guard walker.

### G4. `output.response_data.<tool>.<field>` in a node's own save_message is not schema-checked

The repro is G3 with a typo: `{{output.response_data.audit.guidanc}}` on `b.save_message`.
- **Validation today:** no findings. The same typo via `nodes.b.response_data.audit.guidanc`
  gives `E: response tool 'audit' has no field 'guidanc'`.
- **Cause:** `validateResponseDataAccessFromExpr` matches only `nodes\.(id)\.(path)`
  (`cel.go:784`). The `output.` form in save_message is never mapped to the current node.
- **Runtime:** `no such key: guidanc`.
- **Proposed check:** in save_message validation, rewrite `output.` → `nodes.<currentNode>.`
  before calling `validateResponseDataAccessFromExpr`, or teach it a current-node alias. This
  also lets G3 apply to save_message, which is where F3 occurred.
- **False-positive risk:** none beyond what the `nodes.` form already has.
- **Effort:** S. It is a prerequisite for G3 covering F3.

### G5. `iter` referenced outside any loop

Repro: a top-level node with `content: "{{iter.item.name}}"`.
- **Validation today:** no findings (`iter` is declared DynType everywhere).
- **Runtime:** `CEL evaluation error: no such key: item`. `EnsureNamespaceDefaults` supplies only
  `{iteration:0,index:0}`.
- **Proposed check:** when validating a node that is not inside a loop body (the validator already
  knows loop nesting; `IterItemFields` is set only for loop bodies), declare `iter` as the typed
  `{iteration, index}` object. Access to `item`/`key` then fails compilation with a clear message.
- **False-positive risk:** very low.
- **Effort:** S.

### G6. `size()`, concatenation and arithmetic over nullable/absent values (F7)

Repro: `{{size(nodes.a.response_data.x.items)}}` or `{{'x' + nodes.a.s}}` where the value is null.
- **Validation today:** no findings. The operands are dyn, so any overload type-checks.
- **Runtime:** `CEL evaluation error: no such overload: size` / `no such overload`.
- **Proposed check:** a narrow nullability lint. Flag `size(e)`, `e + ...` and `e.startsWith` where
  `e` is a path into a known-optional region (non-required response-tool fields, proto
  message/wrapper fields, maybe-skipped nodes) and `e` is not dominated by a `has()`/`!= null`
  guard. This shares the G3 walker. The corpus uses the pattern
  `has(x) && x != null && size(x) > 0` heavily (33 `size(` calls in builtins), so authors already
  know to guard. The lint enforces it.
- **False-positive risk:** medium, because proto repeated fields are never null (they are empty
  lists). Restrict the lint to JSON-schema optional fields and message-typed fields, not repeated
  or scalar proto fields.
- **Effort:** M. It could alternatively be solved in the runtime by making `size(null)` return 0
  (a transcript on 2026-09-15 explored exactly this). Choose one; a runtime fix removes the
  class.

### G7. Optional input with no default read bare

Repro: `inputs: {opt: {type: string, required: false}}` and `content: "{{inputs.opt}}"`.
- **Validation today:** no findings.
- **Runtime:** `CEL evaluation error: no such key: opt` when the caller omits it. (The DB history
  also records `no such key` "when the scenario omits an optional input".)
- **Proposed check:** error when an input that is not required and has no default is accessed
  without a `has(inputs.x)`/`inputs.?x` guard. Alternatively, have the runtime bind typed zeros for
  every declared input, which removes the class; that is the better fix if cheap.
- **False-positive risk:** low. The builtins give nearly all inputs defaults.
- **Effort:** S.

### G8. `_results[...]` and other loop aggregates are dyn (F4)

Repro: `{{nodes.l._results[1].path}}` on a parallel loop.
- **Validation today:** no findings.
- **Runtime:** `CEL evaluation error: no such key: 1`. Keys are strings, and missing candidates
  fail the same way. F4 itself (`_results["1"]` with an unfinished candidate) failed the whole
  review inject.
- **Proposed check:** type `_results` as `map(string, <inline-outputs object>)`. The inline outputs
  are already inferred for `nodes.l.<out>`. An int key then fails to compile, and field typos
  inside are caught. The keys are data-dependent, so absence cannot be proved. Warn on literal-key
  indexing without `in`/`has` guard.
- **False-positive risk:** low for typing. The literal-key warning is noisy (parallel-compete uses
  it deliberately), so keep it as a warning.
- **Effort:** M.

### G9. `builtin://structured-agent` `response` is not typed from `response_schema`

Repro: a `workflow` node with `ref: builtin://structured-agent`, `args.response_schema` declaring
`verdict`, and a consumer reading `{{nodes.w.response.verdcit}}`.
- **Validation today:** no findings (validated with `--include-builtins`).
- **Runtime:** `no such key: verdcit`. Also, non-required fields have the same absence problem as
  G3.
- **Proposed check:** when a ref'd workflow's output is sourced from a schema-typed arg, project
  the arg's JSON schema onto the output field type, reusing the response-tool FieldInfo path. 101
  `.response.<field>` reads exist in builtins and drafts, so this is high coverage once built.
- **False-positive risk:** low.
- **Effort:** M (cross_workflow contract plumbing).

### G10. Unset proto fields are absent, not zero (F5)

Activity output is marshalled with `EmitUnpopulated: false`
(`runtime/activities/types/activity_input.go:27`, `model/resolved.go:188`). Message-typed and
`Cel*` wrapper fields (e.g. `CallLLMOutput.compaction_threshold`, `thinking`) are **absent** when
unset, yet the validator types them as present.
- **Runtime:** `no such key: compaction_threshold`.
- **Proposed check:** there are two options. (a) Annotate proto output fields as `always_present` /
  optional and apply the G3 guard rule to optional ones. (b) Better: make the runtime emit
  defaults for output messages, which removes the class. I recommend (b) as a runtime change,
  not a validation check.
- **Effort:** L as a validation check, S–M as a runtime change.

### G11. CEL-looking literals in template (CelString/CelList) fields

Repro: `tool_calls: "nodes.a.tool_calls"` (braces missing).
- **Validation today:** no findings.
- **Runtime:** it evaluates to the literal string `"nodes.a.tool_calls"`
  (`EvaluateTemplate` returns literals as-is). It then fails downstream with a type error, or
  silently does the wrong thing.
- **Proposed check:** for fields whose proto type is non-string (`CelList`, `CelInt`, `CelBool`, map
  args), a literal that is not `{{...}}` must parse as that type. For string fields, warn when the
  whole value matches `^(nodes|inputs|iter|output|outputs|workflow)\.[\w.]+$`.
- **False-positive risk:** low for non-string fields. The string-field heuristic is warning-only.
- **Effort:** M.

### G12. Entry points

| Path | Runs StaticAnalysis? | Blocks on errors? |
|---|---|---|
| `LoadWorkflowActivity` (run start) | yes, with loader and presets | **yes** (`HasErrors`) |
| `CreateChat` tree validation (`chat_workflow.go:384`) | yes | yes |
| UI save / validate RPCs (`grpc/services/workflow.go:504,1132,1377`) | yes (no skill resolver) | no. Saved with `is_valid=false` |
| Agent tools `create/update/edit workflow` (`llm/tools/workflow_editing.go:524`) | yes, but **nil loader**: no cross-workflow typing, so ref outputs become dyn | no ("save regardless"), and **warnings are dropped** from the tool response |
| CLI `workflow validate` | yes, with skill resolver | yes |

Findings:
- Agent-authored workflows are validated without a loader. That is weaker than run-start
  validation, so the agent gets "valid", then a later run-start error, or the check is too weak
  for G1/G9. **Pass the same loader.**
- Warnings never reach the agent. Under the strict runtime the G1 warnings are the most predictive
  signal we have. **Return warnings in the tool response**, or better, promote them per G1.
- `is_valid` is computed once and goes stale when the validator changes (4 drafts currently say
  invalid with null errors, and validate cleanly now). Recompute on read or at run start
  (run start already re-validates, so this is a UI-honesty issue only).
- 55 of 105 corpus warnings are "output expression has dynamic type (dyn)". They are noise that
  trains authors to ignore warnings. Once G1 promotes the real ones, demote or drop this one.

## Recommended first batch

All S-effort, very low false-positive risk, targeting observed failures:

1. **G2**: per-site validation envs derived from the runtime contexts' `Namespaces()`, and
   compile `while`. These are deterministic run-time compile errors that validation cannot see
   today.
2. **G4 + G3**: map `output.` → current node for response_data checks, then enforce `required`-or-
   guarded access. This closes the auditing-agent bug class (F3).
3. **G1**: promote "not guaranteed to have executed" to an error, except for bare declared-output
   paths that `substituteTypedZero` rescues. This covers the two historically most common
   production failures (F1, F2) and flags the two latent user pitch-deck drafts.
4. **G5**: type `iter` as `{iteration,index}` outside loop bodies.
5. **G12**: give the agent workflow tools the loader and return warnings.

Then weigh runtime-side removals over new lints for G6 (`size(null)` → 0), G7 (bind typed zeros for
declared inputs) and G10 (emit defaults for output messages). Each removes a whole class of failure
that validation can only approximate.
