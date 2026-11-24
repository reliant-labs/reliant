package buildinfo

// agent.go — "which coding agent is running this command?", answered from the
// environment the agent's shell tool injects. Two consumers read it: `forge
// project new` picks the files to scaffold for that agent
// (generator.DetectHarness), and every control-plane call names it in the
// X-Forge-Client header (cloud.ClientHeaderValue), so the control plane can
// tell which agent drove a deploy. One table serves both, so the two can never
// disagree about what a marker means.

// The agents DetectAgent recognises. The names match generator.Harness for the
// agents forge scaffolds for; gemini is detected but has no scaffold.
const (
	AgentReliant = "reliant"
	AgentClaude  = "claude"
	AgentCodex   = "codex"
	AgentCursor  = "cursor"
	AgentGemini  = "gemini"
)

// Detection markers: an environment variable each agent harness sets in the
// shell commands it runs. Only markers confirmed against the harness's own
// docs or source are listed; copilot and windsurf set none we could confirm,
// so they are never auto-detected.
//
//   - reliant: RELIANT_AGENT_SHELL=1, set on every agent shell by the
//     reliant daemon (internal/daemonpolicy.AgentShellEnv).
//   - claude:  CLAUDECODE=1 (code.claude.com/docs/en/env-vars).
//   - codex:   CODEX_THREAD_ID / CODEX_SESSION_ID, injected into every shell
//     command by codex-rs (core/src/exec_env.rs); not in OpenAI's public docs.
//   - cursor:  CURSOR_AGENT (cursor.com/docs/agent/tools/terminal).
//   - gemini:  GEMINI_CLI=1, set by gemini-cli's run_shell_command tool
//     (docs/tools/shell.md).
//
// Reliant is checked first: its daemon passes its own environment through, so
// a daemon started from a Claude Code or Cursor terminal still carries those
// markers, and the innermost agent is the one running the command.
func agentMarkers() []struct {
	agent string
	vars  []string
} {
	return []struct {
		agent string
		vars  []string
	}{
		{AgentReliant, []string{"RELIANT_AGENT_SHELL"}},
		{AgentClaude, []string{"CLAUDECODE"}},
		{AgentCodex, []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID"}},
		{AgentCursor, []string{"CURSOR_AGENT"}},
		{AgentGemini, []string{"GEMINI_CLI"}},
	}
}

// DetectAgent reports which coding agent is running this process, from the
// markers above, and the variable that identified it. It returns ("", "") when
// none is set. Only the variable's NAME is ever reported: values such as a
// codex thread id are per-session identifiers and never leave the process.
// getenv is os.Getenv in production.
func DetectAgent(getenv func(string) string) (agent, marker string) {
	for _, m := range agentMarkers() {
		for _, v := range m.vars {
			if val := getenv(v); val != "" && val != "0" {
				return m.agent, v
			}
		}
	}
	return "", ""
}
