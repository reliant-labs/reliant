// Copyright (c) 2025 Reliant Labs
package builtin

import "embed"

//go:embed *.yaml
var BuiltinWorkflowsFS embed.FS

//go:embed presets/*.yaml
var BuiltinPresetsFS embed.FS

//go:embed testdata/*.yaml
var BuiltinScenariosFS embed.FS

// BuiltinScenarioDirsFS embeds the per-workflow scenario directories
// (<workflow-name>/scenarios/*.yaml, one scenario per file) — the layout a
// project's .reliant/workflows uses too (workflowref.ScenarioDir), so
// `reliant workflow scenario run --dir internal/workflow/builtin` discovers the
// same files. Embedding them lets `go test` run every scenario so they cannot
// silently rot.
//
//go:embed */scenarios/*.yaml
var BuiltinScenarioDirsFS embed.FS
