// Copyright (c) 2025 Reliant Labs
package runtime

import "go.temporal.io/sdk/workflow"

// presetsNeedNoPathChangeID gates loading a node's presets in a run that has
// no project path (workflow.GetVersion).
//
// Preset loading used to refuse such a run with a TerminalError ("project
// path not set, cannot load presets"). The refusal guarded nothing:
// LoadPresetParams never reads project_path — it resolves a preset from the
// stored project config or the builtins — so the only effect was to fail
// every spawn of a run whose launch had no checkout to record. A chat started
// on a worktree that is still being created is exactly that run: the
// worktree row has no path until the daemon reports where it landed, the
// launcher records no project_path, and on 2026-10-10 all seven agents a
// planning chat spawned failed before doing any work (chat 0028ced4).
//
// Consulted only when the path is empty, which is the one case the two
// shapes differ in: the old one returns the TerminalError and schedules
// nothing, the new one schedules LoadPresetParams. A run that failed its
// spawns before this shipped has no marker, so it replays the failure it
// recorded.
const presetsNeedNoPathChangeID = "presets-load-without-project-path"

// presetsRefuseUnsetPath reports whether preset loading must still refuse a
// run with no project path: true only when the path is empty AND the history
// predates presetsNeedNoPathChangeID.
func presetsRefuseUnsetPath(ctx workflow.Context, projectPath string) bool {
	if projectPath != "" {
		return false
	}
	return workflow.GetVersion(ctx, presetsNeedNoPathChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion
}
