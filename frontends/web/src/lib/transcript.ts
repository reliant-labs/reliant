/**
 * Conversation transcript formatting.
 *
 * Turns a chat's messages into Markdown suitable for pasting into an issue, a
 * PR description, or another chat. Only user and assistant prose is included:
 * tool calls and their results are the model's working, and pasting hundreds of
 * lines of them is almost never what someone means by "copy the conversation".
 */

import { ContentBlockType, MessageRole } from "../gen/reliant/v1/chat_pb";
import type { Message } from "../api/client";

/** Extract the plain-text prose from a message, ignoring tool traffic. */
export function messageText(message: Message): string {
  return (message.contentBlocks || [])
    .filter((block) => block.type === ContentBlockType.TEXT)
    .map((block) => block.content || "")
    .join("")
    .trim();
}

function roleLabel(role: MessageRole): string | null {
  switch (role) {
    case MessageRole.USER:
      return "You";
    case MessageRole.ASSISTANT:
      return "Assistant";
    default:
      // Tool and system messages are omitted from the transcript.
      return null;
  }
}

export interface TranscriptOptions {
  /** Included as a heading when present. */
  title?: string;
}

/**
 * Format messages as Markdown.
 *
 * Returns an empty string when there is nothing quotable, so callers can
 * distinguish "no transcript" from "a transcript of blank turns".
 */
export function formatTranscript(
  messages: Message[],
  options: TranscriptOptions = {},
): string {
  const sections: string[] = [];

  for (const message of messages) {
    const label = roleLabel(message.role);
    if (!label) continue;

    const text = messageText(message);
    if (!text) continue;

    sections.push(`### ${label}\n\n${text}`);
  }

  if (sections.length === 0) return "";

  const heading = options.title ? `# ${options.title}\n\n` : "";
  return `${heading}${sections.join("\n\n")}\n`;
}
