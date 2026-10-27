# Private review: output-only expressions

## Scope and owner decision

Base: frozen union `90ea0a30b143b802a6068b1249b3ab41176e8dd4`.
The owner approved **only top-level workflow output string expressions**, not a
complete classification of runtime CEL and not general load-time templating.
This is private reviewpublisheradapter logic; no public API, loader flag change,
workflow activation, SQL schema change, or harness change is included.

## Why the old refusal was too broad

The production loader's `HasTemplates` means source contains `{{`, including
runtime output expressions. `ResolveWorkflowTemplates` deliberately skips the
root `outputs` subtree; `EvaluateDeclaredOutputs` evaluates it after graph nodes
settle. Thus `review_verdict: '{{ nodes.review.response_text }}'` is an immutable
approved expression whose result may differ between heads without changing the
policy revision, graph, raw YAML or digest. It is not permission for an activity
caller to assert a verdict.

The original refusal remains correct for templates that can substitute the
executed graph at load time: a pinned placeholder JSON alone is not the effective
execution graph. Merely deleting `HasTemplates` or the raw delimiter check would
reopen that gap.

## Boundary proof

`validateReviewOutputExpressions` is unexported and shared by the server-owned
post-graph hook and every observed ActivityLoadWorkflow result in history.

1. Bound raw YAML and JSON to 1 MiB each before parsing. Existing history limits,
   inline `json/plain` payload-only decoding, and claim-check refusal remain.
2. Accept exactly one YAML document with a mapping root. Walk the YAML AST before
   the workflow parser: reject duplicate/non-string keys, aliases, anchors,
   merge keys, custom tags, unsupported nodes, and excessive depth/node count.
   Snake/camel aliases colliding after underscore removal and case folding are
   conservatively refused within every ordinary mapping, so map reordering
   cannot choose a different interpretation. This may reject otherwise usable
   similarly named user keys; private reviews intentionally fail closed.
3. Permit `{{` only in immediate string values of the root `outputs` mapping.
   Keys and all other scalar values are static. Nested/inline outputs, node args,
   dynamic dependency refs, trigger inputs and on_failure expressions are NOT
   covered by this allowance. Comments are not executable YAML nodes.
4. Preserve HasTemplates semantics. A true flag requires the structural proof;
   a false flag with any raw delimiter is denied. A true flag without a delimiter
   in normalized YAML is permitted because the real loader detects templates
   before re-marshaling source (comments may disappear).
5. Parse the accepted YAML with the actual workflow YAML parser. Strictly decode
   WorkflowJSON as a protobuf Workflow (unknown/duplicate JSON fields refused)
   and require protobuf equality. Do not use the runtime's legacy JSON fallback
   or a generic float64 JSON projection for this correspondence proof.
6. History still pins exact raw YAML and canonical snapshot JSON/digest. Validate
   BOTH the policy's expected JSON and the actual result JSON against the YAML;
   this also prevents generic JSON-number canonicalization from masking differing
   protobuf values. Each loaded dependency passes the same proof; all snapshot
   graphs must have been observed. Unexecuted/unloaded dependencies fail closed.

The hook validates root structure but cannot certify dependencies by itself;
CompletionActivity's exact-history verification certifies every observed load
before recording pending evidence. No completion success is granted by the hook.
The fixed private activity identity, actual execution/assignment enrollment,
verified CAN ancestry, policy type/digest, completed logical execution, completed
WorkflowStatus success, and matching final Outputs.review_verdict remain required.
No original SQL execution identity is updated here.

## Tests and fixture corrections

- `TestReviewOutputExpressionStructure`: allow output-only CEL and literals;
  refuse false metadata, load-time substitutions, runtime CEL outside this narrow
  allowance, non-string/nested outputs, duplicate keys/field aliases, aliases,
  merges, custom tags, malformed/multidocument/overdeep YAML, changed graph/output
  expression, duplicate/unknown/oversized protobuf JSON.
- `TestPrivateReviewHookOutputExpressionBoundary`: actual private hook accepts
  changes/approve, schedules fixed completion identity once, and refuses invalid
  verdict/metadata/graph/template before assignment resolution or completion.
  This is a Temporal test-environment unit test, not a real-server proof.
- `TestHistoryAcceptsPinnedOutputExpressionForDifferentVerdicts`: same snapshot
  and digest accept changes and approve as different recorded run results.
  Raw histories are controlled fixtures, not a claim to have run a model.
- `TestHistoryOutputExpressionDoesNotRelaxPins`: altered YAML/JSON/digest,
  unloaded/dynamic dependency, placeholder/effective graph mismatch, invalid
  verdict, failed sibling, missing status, final-output mismatch cannot succeed.
- Existing template/YAML substitution, missing claim-check, payload-size,
  pagination and terminal-evidence negative tests remain intact.
- Old synthetic completion fixtures had YAML `nodes: []` but JSON containing a
  node. Their JSON now matches the empty-node YAML; no negative assertion was
  weakened. Real-server fixture files are not executed as part of this change.

## Validation and handoff

Scoped `go test -short ./internal/reviewpublisheradapter` and
`go build ./internal/reviewpublisheradapter` pass. The four new focused test
functions also pass three consecutive runs (`go test -short ... -run '<four names>'
-count=3`). `git diff --check` passes. No whole-repo gate or release is run.

The automated `prove_test` tool refused the isolated checkout as outside its
bound workspace before running either command. No before/after regression proof
is claimed; the new positive history test exercises the exact former refusal.

Agent389 owns harness fixture updates separately (faithful HasTemplates values).
The parent must independently review/integrate this commit into the frozen union
and run real harness proofs. These unit tests are not a whole-SQL/LLM/CP/Temporal
attestation, and no production activation is implied.
