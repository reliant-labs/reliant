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
// (scenarios/<workflow-name>/*.yaml). These are the same scenario files the
// `reliant workflow scenario run` CLI discovers on disk; embedding them lets
// `go test` run every scenario so they cannot silently rot.
//
//go:embed scenarios/*/*.yaml
var BuiltinScenarioDirsFS embed.FS
