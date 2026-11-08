# Model serialization analysis

`task lint:models -- ./internal/handlers/scenario/...` runs standard vet and
Reliant's `modelserialization` rule over the named packages (default: `./...`).
It is also part of `task lint`, the existing affected-package merge vet phase
and the release gate's existing whole-module vet phase. No new test lane is
added. `scripts/vet.sh` builds `tools/modelvet`, a standard `unitchecker` vet
tool; Go's vet driver supplies complete per-package type information, includes
test variants, rejects invalid input and caches analysis. The rule never loads
packages itself, stubs imports or ignores type errors.

Forge's built-in rules use the same `go/analysis` framework but its registry is
internal, not a public app-rule extension point. This app-specific rule therefore
uses the standard vet driver alongside (not instead of) pinned Forge lint.

## Bounded guarantee

The rule reports statically identifiable `internal/db/core.StepExecution`,
`ChatStepExecution` and `WorkflowScenario` payloads at resolved calls into gob,
XML, YAML or protojson codecs. Exact package identity matters; another package's
`StepExecution` is not a match. It follows aliases (including `internal/db`),
pointers, arrays, slices, map keys/values, exported/embedded fields (with cycle
protection), and explicit interface conversions/composite elements directly at
the call. Both encoder and decoder method calls are covered. Fields tagged
`json:"-"` still count: that tag does not hide them from other codecs.

JSON is deliberately **allowed**. `internal/db/core/legacy_json.go` implements
the legacy `sql.Null*` JSON shape; `legacy_json_test.go` pins compatibility. This
rule changes neither the models nor their public serialization.

This is **not** proof of absence of arbitrary dynamic serialization. It does
not track interface variables, dynamic contents assembled earlier, reflection,
serializer function variables, application wrapper calls or interprocedural
flows. For example, `var value any = model; yaml.Marshal(value)` is outside the
rule's guarantee; `yaml.Marshal(any(model))` is within it. It is conservative
about exported fields, not a full implementation of each codec's tags/custom
marshaler precedence. Review is still needed for those cases. Typed fixtures
explicitly include both diagnosed examples and these documented non-goals.

## Tests

`internal/analysis/modelserialization` uses `analysistest` on tiny, completely
typed fixture packages, not on the repository. Its fixture codec API has real
Go declarations checked normally by the analysis driver (not an importer stub).
Expected diagnostics assert the file and line, and unexpected diagnostics fail
positive cases. The rule's `RunDespiteErrors` stays false.

The fixture test invokes the Go loader and is marked `testkit.Slow`, so the
short tier skips it. Run it explicitly without `-short` when changing the rule:

```
go test -race -count=3 -cpu=1,2,4 ./internal/analysis/modelserialization/
```

The expensive, fail-open repository-scanning architecture prototype from #2346
is removed. The rule does not fail merely because a package contains zero
serializer calls: most packages should have none. Positive/negative typed
fixtures, rather than a repository-wide call count, establish that it runs.
