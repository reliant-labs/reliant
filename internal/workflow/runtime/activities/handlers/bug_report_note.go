// Copyright (c) 2025 Reliant Labs
package handlers

import "github.com/reliant-labs/reliant/internal/llm/tools"

// bugReportSystemNote is the one sentence an agent needs to know report_bug
// exists when it was not handed the tool.
const bugReportSystemNote = "If you hit a defect in Reliant itself or in forge — not in the user's code — report it to engineering with " +
	tools.ToolReportBug + " (load it with " + tools.ToolLoadTool + " first); one call is enough."

// bugReportNote is bugReportSystemNote when the turn may load report_bug but
// was not offered it — the narrow presets spawned sub-agents run, which see the
// tool only as a name in load_tool's list. An agent holding the tool reads its
// description instead, and one that cannot reach it is told nothing.
func bugReportNote(caps *tools.Capabilities) string {
	if caps == nil || caps.Offers(tools.ToolReportBug) || !caps.CanLoad(tools.ToolReportBug) {
		return ""
	}
	return bugReportSystemNote
}
