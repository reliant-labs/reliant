// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	_ "embed"
)

// Byte-exact Antigravity system-prompt blocks, extracted verbatim from real
// antigravity/cli 1.2.5 traffic (a 14,011-byte systemInstruction). They are
// embedded rather than retyped as Go literals for two reasons: the prompt text
// contains backticks, which Go raw-string literals cannot hold, and mechanical
// extraction is the only way to guarantee the bytes match.
//
// Do NOT hand-edit these .txt files. The static blocks must stay byte-identical
// to the capture for the same reason ccprompts/ must — fingerprint coherence. A
// request's User-Agent, its client version and the bytes of its prompt blocks
// all have to come from ONE real release; a prompt assembled from edited or
// mixed-release text is a combination no real client emits, which is precisely
// the anomaly the managed prompt exists to avoid.
//
// The capture was split so that every run of bytes that is IDENTICAL across
// requests lives in a .txt here, and every value that VARIES per request is
// rendered by BuildSystemInstruction. The split is verified in prompts_test.go
// by reassembling the capture's own values and diffing against the original.

//go:embed agyprompts/identity.txt
var agyIdentity string // <identity>…</identity>, fully static

//go:embed agyprompts/user_information_workspaces.txt
var agyUserInfoWorkspacesIntro string // the "[URI] -> [CorpusName]" explainer line

//go:embed agyprompts/user_information_trailer.txt
var agyUserInfoTrailer string // the "Code relating to the user's requests…" line

//go:embed agyprompts/skills_preamble.txt
var agySkillsPreamble string // <skills> through the "Available skills:" header

//go:embed agyprompts/subagents_preamble.txt
var agySubagentsPreamble string // <subagents> through the "Available subagents:" header

//go:embed agyprompts/subagents_trailer.txt
var agySubagentsTrailer string // the no-polling reminder + </subagents>

//go:embed agyprompts/messaging.txt
var agyMessaging string // <messaging>…</messaging>, fully static

//go:embed agyprompts/conversation_transcript.txt
var agyConversationTranscript string // <conversation_transcript>…</…>, fully static

//go:embed agyprompts/artifacts_body.txt
var agyArtifactsBody string // <artifacts> through the scratch-files section

//go:embed agyprompts/slash_commands_preamble.txt
var agySlashCommandsPreamble string // <slash_commands> through the "Available slash commands…" header

//go:embed agyprompts/guidelines.txt
var agyGuidelines string // <guidelines>…</guidelines>, fully static

//go:embed agyprompts/communication_style.txt
var agyCommunicationStyle string // <communication_style>…</…>, fully static; NO trailing newline
