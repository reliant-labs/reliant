---
name: ci/golangci
description: The golangci-lint rules a scaffolded project's `.golangci.yml` gates on, and what gofmt and go vet do not check — goimports' three import groups, funlen's 80 statements / 150 lines, gocognit, nestif, interfacebloat, revive's limits, gocritic's tags — with the fix for each and where to change a threshold. Use when `forge lint` reports goimports, funlen, gocognit, nestif or a revive limit, or before writing a long function.
---

# The golangci-lint rules your project gates on

`forge lint` runs golangci-lint with the project's own `.golangci.yml`, and CI
runs the same file. forge scaffolds it once; after that it is yours. Raise a
threshold there for the whole project, or suppress one function with a reason:
`//nolint:funlen // a table of 40 cases; splitting it hides the table`. Prefer
the reasoned `//nolint` to a path glob, which also hides every future finding
under that path.

gofmt and `go vet` accept code that this gate rejects. The rows below are the
usual surprises.

## Import grouping (goimports)

`goimports` runs with `local-prefixes: <your module>`. It wants **three
groups, separated by blank lines**: the standard library, then third-party
modules, then your module's packages. forge's libraries
(`github.com/reliant-labs/forge/pkg/...`) are third-party here, because they
are not your module:

```go
import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"example.com/myapp/internal/db"
)
```

gofmt accepts the forge import inside your module's group, and goimports does
not. `forge lint --fix` regroups them for you. Formatting is the one class of
finding it rewrites.

## Size and complexity

| Linter | Fails a function or type at | Fix |
|---|---|---|
| `funlen` | more than 80 statements or 150 lines | extract the steps into named helpers |
| `gocognit` | cognitive complexity over 80 | split the branches into functions; flatten early returns |
| `nestif` | an `if` nest of complexity 8 or more | return early; move the inner block into a function |
| `interfacebloat` | an interface with more than 12 methods | declare the narrow interface the consumer uses |
| revive `max-control-nesting` | control flow nested deeper than 5 | return early; extract the inner loop |
| revive `argument-limit` | more than 8 parameters | pass a struct (a `Deps` or an options value) |
| revive `function-result-limit` | more than 4 results | return a struct |
| revive `max-public-structs` | more than 25 exported structs in a package | split the package |

Test files (`_test.go`) are exempt from funlen, gocognit, errcheck and
gocritic: a test's length is its table.

## The rest of the set

`govet` (all analyzers except fieldalignment and shadow), `errcheck`,
`staticcheck` (all checks minus the style-only ST/QF ones the file lists),
`unused`, `ineffassign`, `noctx`, `misspell`, and `gocritic` with the
`diagnostic` and `performance` tags (its `style` tag is off; `hugeParam`,
`rangeValCopy`, `ifElseChain` and `singleCaseSwitch` are disabled). revive also
runs its naming and error-style rules (`var-naming`, `error-strings`,
`error-return`, `context-as-argument`, …). Generated files (`gen/`,
`*_gen.go`, `*.pb.go`, `*.connect.go`) are excluded. The file explains each
choice in a comment beside it.
