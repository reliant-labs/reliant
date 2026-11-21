// File: internal/cli/lint/lint_format_standard.go
//
// What the format pre-pass formats TO: the project's own gate, read from its
// golangci-lint config — never forge's scaffold default in its place.
//
// The pre-pass exists so mechanical formatting never surfaces as a gate. That
// argument holds only while it applies the formatting the gate checks. It
// used to apply goimports with the go.mod module as the local prefix
// unconditionally: the setting the scaffolded .golangci.yml declares. reliant,
// adopted in place, declares goimports with no local-prefixes, and its
// pre-commit hook runs `go fmt`. So `forge lint --scope internal/grpc`
// regrouped the imports of six files there that reliant's gate, its hook and
// its author all accepted, and the hook and the pre-pass disagreed on every
// later edit.
//
// The rule (checksums.ProjectGoFormat, shared with the codemods and
// narrow-deps so every step that writes Go agrees on it):
//
//   - gofmt is the floor. It is Go's own canonical formatting (`go fmt`,
//     every editor); no gate rejects it, so it is always applied.
//   - Import GROUPING follows the gate. goimports enabled (v2
//     formatters.enable, or v1 linters.enable) → goimports' grouping, with
//     exactly the local-prefixes the config declares (none declared, none
//     applied). gci enabled → gci owns grouping and golangci-lint --fix
//     applies it, so the pre-pass leaves grouping alone. Neither enabled, no
//     config, or a config that does not parse → no regrouping.
//
// A scaffolded project declares goimports with its module as the prefix, so
// nothing changes there: the pre-pass still matches the gate byte for byte.

package lint

import "github.com/reliant-labs/forge/internal/checksums"

// goFormatStandard is the formatting the pre-pass applies to a project's Go.
type goFormatStandard = checksums.GoFormatStandard

// projectGoFormatStandard reads the standard from the golangci-lint config a
// run started in root loads.
func projectGoFormatStandard(root string) goFormatStandard {
	return checksums.ProjectGoFormat(root)
}
