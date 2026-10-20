// Copyright (c) 2025 Reliant Labs
package builtin

import "embed"

//go:embed *.yaml
var BuiltinWorkflowsFS embed.FS

//go:embed presets/*.yaml
var BuiltinPresetsFS embed.FS

// BuiltinScenarioDirsFS embeds every builtin scenario: the per-workflow
// scenario directories (<workflow-name>/scenarios/*.yaml, one scenario per
// file), the layout a project's .reliant/workflows uses too
// (workflowref.ScenarioDir). `reliant workflow scenario run --include-builtins`
// and `go test` both read them from here (workflowref.BuiltinLayout), so the
// CLI runs exactly the scenarios the tests do.
//
//go:embed */scenarios/*.yaml
var BuiltinScenarioDirsFS embed.FS
