// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"os"
	"strings"
	"testing"
)

// Values from the capture (.dev/agy/3.8-flash.curl). They appear here ONLY as
// the inputs that reproduce the original bytes, and as the strings that must
// never appear in output rendered from anyone else's params.
const (
	captureConversationID = "807de90e-b4c3-46d7-bdc8-9934ceb16962"
	captureAppDataDir     = "/Users/capture-user/.gemini/antigravity-cli"
	captureWorkspaceURI   = "/Users/capture-user/src/reliant-labs/reliant"
	captureCorpusName     = "reliant-labs/reliant"
)

// captureParams are the exact dynamic values the captured request carried.
func captureParams() SystemPromptParams {
	return SystemPromptParams{
		OSVersion:      "mac",
		Workspaces:     []Workspace{{URI: captureWorkspaceURI, CorpusName: captureCorpusName}},
		AppDataDir:     captureAppDataDir,
		ConversationID: captureConversationID,
		Skills: []SkillRef{
			{
				Name:           "agy-customizations",
				DefinitionPath: captureAppDataDir + "/builtin/skills/agy-customizations/SKILL.md",
				Description:    "Comprehensive guide and reference for the Antigravity Customization System. Use to explain how customizations work, their loading priority, discovery mechanisms, and to guide the creation of skills, rules, plugins, hooks, and MCP servers.",
			},
			{
				Name:           "antigravity-guide",
				DefinitionPath: captureAppDataDir + "/builtin/skills/antigravity_guide/SKILL.md",
				Description:    "Provides a comprehensive guide, quick reference, and sitemap for Google Antigravity (AGY), including the Antigravity CLI (agy), Antigravity 2.0, Antigravity IDE, Python SDK, slash commands, keybindings, and customizations (skills, rules, MCP, sidecars). Activate this skill when the user asks questions about how to use, configure, or customize Antigravity, AGY, the agy CLI, the Antigravity IDE, or Antigravity 2.0.",
			},
		},
		Subagents: []SubagentRef{
			{Name: "self", Description: "Subagent that inherits the parent agent's full configuration including tools, system prompt, and model. Use this when you need to run a task in a separate conversation context but with the same capabilities as the current agent."},
			{Name: "research", Description: "Research subagent with read-only tools for exploring the codebase, searching the web, and reading files. Delegate to this agent when you need to run a task in a separate conversation context but with the same capabilities as the current agent, when a research task requires many search and file-reading steps that would clutter your context, or when you need a broad survey of the codebase or documentation. Prefer doing research yourself for quick, targeted lookups."},
		},
		SlashCommands: []SlashCommandRef{
			{Name: "goal", Description: "Recommend this when the user wants to run a long-running task (e.g., overnight) and wants the agent to be extra thorough and not stop until the goal is fully achieved."},
			{Name: "schedule", Description: "Recommend this when the user wants to run an instruction on a recurring schedule or set a one-time timer."},
			{Name: "browser", Description: "Recommend this when the user's task involves web browsing, searching the web, or interacting with web applications."},
			{Name: "plan", Description: "Recommend this when the task is complex and requires careful step-by-step planning before execution."},
			{Name: "grill-me", Description: "Recommend this when the user wants to align on a plan through an interactive interview to resolve design decisions."},
			{Name: "learn", Description: "Recommend this when the user has corrected the agent or solved a complex setup and wants the agent to persist this behavior for future tasks."},
		},
	}
}

// TestPromptReproducesCapture is the load-bearing test: fed the capture's own
// dynamic values, the builder must reproduce the captured systemInstruction
// byte for byte. It proves the static blocks were not edited AND that the
// static/dynamic split cuts in the right places — a renderer that dropped a
// blank line or reordered a section would still "interpolate correctly" and
// still be a different fingerprint on the wire.
func TestPromptReproducesCapture(t *testing.T) {
	const goldenPath = "testdata/capture_system_instruction.txt"
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	got := BuildSystemInstruction(captureParams())
	if got != string(want) {
		t.Errorf("rendered prompt differs from capture: got %d bytes, want %d bytes\n%s",
			len(got), len(want), firstDiff(got, string(want)))
	}
}

// firstDiff reports the byte offset and surrounding context of the first
// difference, which is far more useful than dumping two 14KB blobs.
func firstDiff(got, want string) string {
	n := min(len(got), len(want))
	for i := range n {
		if got[i] != want[i] {
			lo := max(i-80, 0)
			return "first difference at byte " + itoa(i) +
				"\n got: ..." + got[lo:min(i+80, len(got))] +
				"\nwant: ..." + want[lo:min(i+80, len(want))]
		}
	}
	return "common prefix identical; lengths differ"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestStaticBlocksPresentVerbatim pins each embedded static block into the
// rendered output. If someone retypes one of the .txt files, this fails.
func TestStaticBlocksPresentVerbatim(t *testing.T) {
	got := BuildSystemInstruction(captureParams())
	blocks := map[string]string{
		"identity":                agyIdentity,
		"messaging":               agyMessaging,
		"conversation_transcript": agyConversationTranscript,
		"artifacts_body":          agyArtifactsBody,
		"guidelines":              agyGuidelines,
		"communication_style":     agyCommunicationStyle,
		"skills_preamble":         agySkillsPreamble,
		"subagents_preamble":      agySubagentsPreamble,
		"subagents_trailer":       agySubagentsTrailer,
		"slash_commands_preamble": agySlashCommandsPreamble,
	}
	for name, block := range blocks {
		if block == "" {
			t.Errorf("embedded block %q is empty — is the //go:embed path right?", name)
			continue
		}
		if !strings.Contains(got, block) {
			t.Errorf("embedded block %q does not appear verbatim in rendered output", name)
		}
	}
}

// TestNoCaptureValuesLeakIntoOtherUsers is the test that fails if anyone
// hardcodes the capture's dynamic values instead of templating them. It renders
// with a DIFFERENT user's params and asserts none of the capturing machine's
// identifiers survive.
func TestNoCaptureValuesLeakIntoOtherUsers(t *testing.T) {
	got := BuildSystemInstruction(SystemPromptParams{
		OSVersion:      "linux",
		Workspaces:     []Workspace{{URI: "/home/ada/work/acme/widgets", CorpusName: "acme/widgets"}},
		AppDataDir:     "/home/ada/.local/share/reliant",
		ConversationID: "11111111-2222-3333-4444-555555555555",
		Skills: []SkillRef{
			{Name: "db", DefinitionPath: "/home/ada/work/acme/widgets/.reliant/skills/db/SKILL.md", Description: "Database work."},
		},
		Subagents:     []SubagentRef{{Name: "research", Description: "Read-only research subagent."}},
		SlashCommands: []SlashCommandRef{{Name: "plan", Description: "Plan first."}},
	})

	forbidden := map[string]string{
		captureConversationID: "capture conversation id",
		captureAppDataDir:     "capture app data dir",
		captureWorkspaceURI:   "capture workspace path",
		"/Users/capture-user": "capturing machine's home directory",
		"agy-customizations":  "capture skill name",
		// The LIST ENTRY, not a bare "/goal": the static slash_commands
		// preamble legitimately names /goal as an illustrative example ("You
		// can use the `/goal` command to…"), and that prose is captured bytes
		// we must not edit. What must never appear is the capture's rendered
		// list of commands Reliant does not have.
		"- /goal:": "capture slash command list entry",
	}
	for needle, what := range forbidden {
		if strings.Contains(got, needle) {
			t.Errorf("rendered prompt leaks %s (%q) — that value must be templated, not hardcoded", what, needle)
		}
	}

	// And the caller's own values must actually be there.
	for _, want := range []string{
		"The USER's OS version is linux.",
		"/home/ada/work/acme/widgets -> acme/widgets",
		"App Data Directory: /home/ada/.local/share/reliant",
		"Conversation ID: 11111111-2222-3333-4444-555555555555",
		"Artifact Directory Path: /home/ada/.local/share/reliant/brain/11111111-2222-3333-4444-555555555555",
		"- db (/home/ada/work/acme/widgets/.reliant/skills/db/SKILL.md): Database work.",
		"- research: Read-only research subagent.",
		"- /plan: Plan first.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered prompt is missing interpolated value %q", want)
		}
	}
}

// TestPromptOmitsSectionsWithNoContent pins the "omit, don't placeholder"
// decision: a section whose list Reliant cannot populate must not ship its
// preamble promising a list.
func TestPromptOmitsSectionsWithNoContent(t *testing.T) {
	got := BuildSystemInstruction(SystemPromptParams{
		OSVersion:  "linux",
		AppDataDir: "/tmp/appdata",
	})
	for _, absent := range []string{
		"<skills>", "</skills>", "Available skills:",
		"<subagents>", "</subagents>", "Available subagents:",
		"<slash_commands>", "</slash_commands>",
		"Artifact Directory Path:",
		"[URI] -> [CorpusName]",
	} {
		if strings.Contains(got, absent) {
			t.Errorf("expected %q to be omitted when it has no content, but it was rendered", absent)
		}
	}
	// The always-present sections must still be there and in order.
	mustBeOrdered(t, got, []string{
		"<identity>", "<user_information>", "<messaging>",
		"<conversation_transcript>", "<artifacts>", "<guidelines>",
		"<communication_style>",
	})
}

// TestSectionOrderMatchesCapture pins the tag order, which the model's training
// distribution is sensitive to and which a refactor could silently permute.
func TestSectionOrderMatchesCapture(t *testing.T) {
	got := BuildSystemInstruction(captureParams())
	mustBeOrdered(t, got, []string{
		"<identity>", "<user_information>", "<skills>", "<subagents>",
		"<messaging>", "<conversation_transcript>", "<artifacts>",
		"<slash_commands>", "<guidelines>", "<communication_style>",
	})
}

func mustBeOrdered(t *testing.T, s string, tags []string) {
	t.Helper()
	prev := -1
	for _, tag := range tags {
		at := strings.Index(s, tag)
		if at < 0 {
			t.Fatalf("tag %q missing from rendered prompt", tag)
		}
		if at <= prev {
			t.Fatalf("tag %q is out of order (at %d, previous tag ended at %d)", tag, at, prev)
		}
		prev = at
	}
}

// TestCallerPromptsAppendAfterManagedBase pins the ordering contract Reliant
// relies on everywhere else: the managed base first, the caller's prompts after,
// never interleaved.
func TestCallerPromptsAppendAfterManagedBase(t *testing.T) {
	params := captureParams()
	params.CallerPrompts = []string{"RELIANT-CALLER-ONE", "   ", "RELIANT-CALLER-TWO"}
	got := BuildSystemInstruction(params)

	mustBeOrdered(t, got, []string{
		"<communication_style>", "</communication_style>",
		"RELIANT-CALLER-ONE", "RELIANT-CALLER-TWO",
	})
	// The managed base is unchanged by the append.
	base := BuildSystemInstruction(captureParams())
	if !strings.HasPrefix(got, base) {
		t.Error("caller prompts modified the managed base instead of appending after it")
	}
	if strings.Count(got, "   \n") > strings.Count(base, "   \n") {
		t.Error("blank caller prompt was appended; it should be skipped")
	}
}

// TestWorkspaceForPath pins CorpusName derivation.
func TestWorkspaceForPath(t *testing.T) {
	cases := map[string]Workspace{
		"/Users/capture-user/src/reliant-labs/reliant": {URI: "/Users/capture-user/src/reliant-labs/reliant", CorpusName: "reliant-labs/reliant"},
		"/home/ada/acme/widgets/":                      {URI: "/home/ada/acme/widgets", CorpusName: "acme/widgets"},
		"/solo":                                        {URI: "/solo", CorpusName: "solo"},
	}
	for in, want := range cases {
		if got := WorkspaceForPath(in); got != want {
			t.Errorf("WorkspaceForPath(%q) = %+v, want %+v", in, got, want)
		}
	}
}

// TestOSVersionSpelling pins the capture's spelling: darwin renders as "mac".
func TestOSVersionSpelling(t *testing.T) {
	if got := osVersionFor("darwin"); got != "mac" {
		t.Errorf("osVersionFor(darwin) = %q, want %q", got, "mac")
	}
	if got := osVersionFor("linux"); got != "linux" {
		t.Errorf("osVersionFor(linux) = %q, want %q", got, "linux")
	}
}

// TestSkillRefsForProjectUsesRealCatalog checks the catalog wiring: every ref
// carries an absolute SKILL.md path, because the prompt tells the model to
// view_file that exact string.
func TestSkillRefsForProjectUsesRealCatalog(t *testing.T) {
	if got := SkillRefsForProject(""); got != nil {
		t.Errorf("SkillRefsForProject(\"\") = %v, want nil", got)
	}
	for _, ref := range SkillRefsForProject(t.TempDir()) {
		if !strings.HasPrefix(ref.DefinitionPath, "/") {
			t.Errorf("skill %q has non-absolute definition path %q", ref.Name, ref.DefinitionPath)
		}
	}
}
