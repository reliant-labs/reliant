// Copyright (c) 2025 Reliant Labs
package builtin

import "embed"

//go:embed *.yaml
var BuiltinWorkflowsFS embed.FS

//go:embed presets/*.yaml
var BuiltinPresetsFS embed.FS

//go:embed testdata/*.yaml
var BuiltinScenariosFS embed.FS
