// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/skills/catalog"
)

// The Antigravity managed system prompt.
//
// The captured systemInstruction is one text blob of ten tag sections, in this
// fixed order:
//
//	identity, user_information, skills, subagents, messaging,
//	conversation_transcript, artifacts, slash_commands, guidelines,
//	communication_style
//
// Six of those are byte-for-byte identical across requests and live in
// agyprompts/*.txt (see prompts_embed.go). The rest interleave static prose with
// values that change per request — the user's OS, their open workspaces, the app
// data directory, the conversation id, and three "Available X:" lists. Those are
// rendered here from RELIANT's own state.
//
// Rendering them is not cosmetic. The capture's literal values name one
// developer's home directory and one Antigravity conversation id; shipping them
// hardcoded would tell the model to read files that do not exist on the user's
// machine, and would leak the capturing machine's workspace layout into every
// request. prompts_test.go fails if either string reaches rendered output.
//
// Where Reliant has no equivalent of a section's dynamic content, the WHOLE
// section is omitted rather than shipped with the capture's placeholder. A
// <skills> block whose preamble promises an "Available skills:" list and then
// lists none is worse than no block: it describes a capability the request does
// not actually carry.

// Workspace is one open project: the absolute path the agent works in, and the
// short "org/repo" label the capture calls a CorpusName.
type Workspace struct {
	// URI is the absolute path on the machine that will execute tool calls.
	URI string
	// CorpusName is the short label. WorkspaceForPath derives it from the path.
	CorpusName string
}

// WorkspaceForPath builds a Workspace from a working directory, deriving the
// CorpusName the way the capture's value was derived: the last two path
// segments, which for a normal checkout is "<org>/<repo>". A single-segment path
// yields just that segment.
func WorkspaceForPath(dir string) Workspace {
	clean := filepath.Clean(dir)
	corpus := filepath.Base(clean)
	if parent := filepath.Base(filepath.Dir(clean)); parent != "." && parent != string(filepath.Separator) && parent != "" {
		corpus = parent + "/" + corpus
	}
	return Workspace{URI: clean, CorpusName: corpus}
}

// SkillRef is one entry of the "Available skills:" list. Reliant's skill catalog
// (internal/skills/catalog) supplies these; SkillRefsForProject does the
// conversion.
type SkillRef struct {
	Name string
	// DefinitionPath is the absolute path to the skill's SKILL.md. The prompt
	// instructs the model to read this exact path, so a relative or synthetic
	// path here produces a tool call that cannot succeed.
	DefinitionPath string
	Description    string
}

// SubagentRef is one entry of the "Available subagents:" list.
type SubagentRef struct {
	Name        string
	Description string
}

// SlashCommandRef is one entry of the "Available slash commands" list.
type SlashCommandRef struct {
	// Name is written without the leading slash; the renderer adds it.
	Name        string
	Description string
}

// SystemPromptParams carries everything the managed prompt interpolates. Every
// field is optional: a zero value omits or defaults its section, so a caller
// that knows only the working directory still renders a coherent prompt.
type SystemPromptParams struct {
	// OSVersion is the value for "The USER's OS version is X." Empty defaults
	// to this process's GOOS, spelled the way the capture spells it.
	OSVersion string

	// Workspaces are the projects the agent may write to. Empty omits the
	// workspace mapping lines (but keeps the rest of user_information).
	Workspaces []Workspace

	// AppDataDir backs both "App Data Directory:" and the artifact directory
	// path. Empty defaults to Reliant's own app data dir.
	AppDataDir string

	// ConversationID identifies this conversation. Reliant's chat/session id
	// goes here. Empty omits the "Conversation ID:" line and, because the
	// artifact directory is <appDataDir>/brain/<conversation-id>, omits the
	// artifact directory path line too.
	ConversationID string

	// Skills, Subagents and SlashCommands each render one "Available X:" list.
	// An empty slice omits that section entirely.
	Skills        []SkillRef
	Subagents     []SubagentRef
	SlashCommands []SlashCommandRef

	// CallerPrompts are Reliant's OWN system prompts. They are appended AFTER
	// the managed base, never interleaved with it — the same ordering
	// callerSystemBlocks gives the Claude Code driver. Antigravity's wire
	// format carries a single systemInstruction text part, so they are joined
	// onto the end of the blob rather than sent as separate blocks.
	CallerPrompts []string
}

// osVersionFor spells a GOOS the way the capture spells it. The capture was
// taken on darwin and reads "mac", not "darwin".
func osVersionFor(goos string) string {
	if goos == "darwin" {
		return "mac"
	}
	return goos
}

// BuildSystemInstruction renders the complete systemInstruction text: the
// managed Antigravity base prompt with every dynamic field interpolated, then
// Reliant's own caller prompts appended.
//
// This is the driver's single entry point for the system prompt.
func BuildSystemInstruction(params SystemPromptParams) string {
	osVersion := params.OSVersion
	if osVersion == "" {
		osVersion = osVersionFor(runtime.GOOS)
	}
	appDataDir := params.AppDataDir
	if appDataDir == "" {
		appDataDir = config.GetAppDataDir()
	}

	var b strings.Builder

	b.WriteString(agyIdentity)

	// user_information — every line after the preamble is machine-specific.
	b.WriteString("<user_information>\n")
	b.WriteString("The USER's OS version is " + osVersion + ".\n")
	if len(params.Workspaces) > 0 {
		b.WriteString(agyUserInfoWorkspacesIntro)
		for _, ws := range params.Workspaces {
			b.WriteString(ws.URI + " -> " + ws.CorpusName + "\n")
		}
		b.WriteString(agyUserInfoTrailer)
	}
	b.WriteString("App Data Directory: " + appDataDir + "\n")
	if params.ConversationID != "" {
		b.WriteString("Conversation ID: " + params.ConversationID + "\n")
	}
	b.WriteString("</user_information>\n")

	// skills — Reliant's own catalog, at the absolute SKILL.md paths that exist
	// on THIS machine.
	if len(params.Skills) > 0 {
		b.WriteString(agySkillsPreamble)
		for _, s := range params.Skills {
			b.WriteString("- " + s.Name + " (" + s.DefinitionPath + "): " + s.Description + "\n")
		}
		b.WriteString("\n\n</skills>\n")
	}

	// subagents — Reliant's spawn presets.
	if len(params.Subagents) > 0 {
		b.WriteString(agySubagentsPreamble)
		for _, s := range params.Subagents {
			b.WriteString("- " + s.Name + ": " + s.Description + "\n")
		}
		b.WriteString(agySubagentsTrailer)
	}

	b.WriteString(agyMessaging)
	b.WriteString(agyConversationTranscript)

	b.WriteString(agyArtifactsBody)
	if params.ConversationID != "" {
		b.WriteString("Artifact Directory Path: " +
			filepath.Join(appDataDir, "brain", params.ConversationID) + "\n")
	}
	b.WriteString("\n</artifacts>\n")

	// slash_commands — omitted by default; see SlashCommandRef and the note in
	// the driver on why Reliant supplies none.
	if len(params.SlashCommands) > 0 {
		b.WriteString(agySlashCommandsPreamble)
		for _, c := range params.SlashCommands {
			b.WriteString("- /" + c.Name + ": " + c.Description + "\n")
		}
		b.WriteString("\n\n</slash_commands>\n")
	}

	b.WriteString(agyGuidelines)
	b.WriteString(agyCommunicationStyle)

	// Reliant's own prompts land after the managed base, in caller order.
	for _, p := range params.CallerPrompts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		b.WriteString("\n\n")
		b.WriteString(p)
	}

	return b.String()
}

// SkillRefsForProject converts Reliant's skill catalog for projectPath into the
// "Available skills:" list.
//
// It reads the same catalog the skill tool resolves against, so a skill the
// prompt advertises is a skill the agent can actually load — the failure mode a
// parallel reimplementation would reintroduce. Definitions whose Path is not
// absolute are dropped: the prompt tells the model to view_file that exact
// string, and forge's synthetic skill paths are not openable files.
func SkillRefsForProject(projectPath string) []SkillRef {
	if projectPath == "" {
		return nil
	}
	snapshot := catalog.Discover(catalog.DiscoverInput{ProjectPath: projectPath})
	refs := make([]SkillRef, 0, len(snapshot.Definitions))
	for _, def := range snapshot.Definitions {
		if !filepath.IsAbs(def.Path) {
			continue
		}
		name := def.SkillPath
		if name == "" {
			name = def.Name
		}
		if name == "" {
			continue
		}
		refs = append(refs, SkillRef{
			Name:           name,
			DefinitionPath: def.Path,
			Description:    def.Description,
		})
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}
