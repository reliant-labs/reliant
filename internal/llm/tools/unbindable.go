// Copyright (c) 2025 Reliant Labs
package tools

// Binding a parameter fixes it for every call the agent makes. For most
// parameters that is exactly the point. For a few it is a bug that looks like
// a feature, and this is where we say so.
//
// The test is not "is this parameter dangerous" — it is "does a SINGLE value
// still make sense on the thousandth call in an agentic loop". A parameter
// that names a distinct thing per call (an output path, a process id, a target
// file) fails that test: binding it does not configure the tool, it makes
// every call collide with the last one.
//
// This is a POLICY list, and it is the one hand-maintained thing in this
// mechanism. It cannot silently rot: the generator checks every entry against
// the tool's real reflected schema and FAILS if a name here is not a parameter
// of that tool, so renaming or deleting a parameter breaks the build here
// rather than leaving a rule that quietly matches nothing.
//
// Default is BINDABLE. A parameter has to be argued onto this list, which
// keeps the list short and the reasons specific.
var unbindableParams = map[string]map[string]string{
	"generate_image": {
		"save_to": "every generated image would overwrite the previous one at the same path",
	},
	"save_attachment": {
		"attachment_id": "names the one attachment to save, which is chosen per call and cannot be known in advance",
		"save_to":       "every save would target the same file, so each call destroys the last one's output",
	},
	"write": {
		"file_path": "every write would target the same file, so each call destroys the last one's output",
	},
	"edit": {
		"file_path": "an edit is defined by the file it edits; fixing it makes the tool able to edit exactly one file",
	},
	"shell_output": {
		"process_id": "identifies one live process, which is chosen per call and cannot be known in advance",
	},
	"shell_wait": {
		"process_id": "identifies one live process, which is chosen per call and cannot be known in advance",
	},
	"shell_kill": {
		"process_id": "identifies one live process, which is chosen per call and cannot be known in advance",
	},
	"spawn_send": {
		"agent_id": "identifies one running sub-agent, which is chosen per call",
	},
	"spawn_status": {
		"agent_id": "identifies one running sub-agent, which is chosen per call",
	},
}

// UnbindableParams returns the parameters of toolName that may not be bound,
// mapped to the reason why. Exported for the catalog generator, which is the
// only caller: everything downstream reads the generated catalog instead, so
// the workflow layer never has to import this package.
func UnbindableParams(toolName string) map[string]string {
	return unbindableParams[toolName]
}

// UnbindablePolicyTools returns every tool name this policy mentions, so the
// generator can verify that each one is a real registered tool.
func UnbindablePolicyTools() []string {
	names := make([]string, 0, len(unbindableParams))
	for name := range unbindableParams {
		names = append(names, name)
	}
	return names
}
