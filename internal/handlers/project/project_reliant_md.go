// Copyright (c) 2025 Reliant Labs
package project

import _ "embed"

// defaultProjectReliantMD is the reliant.md a new project is scaffolded with:
// empty sections for the user's own guidelines, plus code-navigation and
// skill pointers. It is written to disk once and then belongs to the user.
//
// It deliberately does NOT carry the "What Reliant can do" routing guide
// (workflows, integrations, forge). A file written once goes stale, and older
// projects never got the guide at all, so the harness renders it live into
// every turn instead (workflow/runtime/activities/handlers/reliant_capabilities.go),
// and drops the stale copy older scaffolded files still carry.
//
//go:embed templates/project_reliant.md
var defaultProjectReliantMD string
