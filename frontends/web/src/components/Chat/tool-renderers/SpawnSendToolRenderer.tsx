/**
 * Renderer for spawn_send: one agent's message to another.
 *
 * The tool row already reads as the message's one-line subject (see
 * formatSpawnSendParams), so this, the expanded body, is the message itself,
 * rendered as markdown rather than as a JSON blob of the arguments, followed
 * by the tool's receipt. A refused send shows the refusal instead of the
 * receipt.
 */

import { memo } from "react";

import type { ToolContentProps } from "./types";
import { MarkdownRenderer } from "../MarkdownRenderer";
import { formatErrorMessage } from "../../../lib/utils";
import { ToolErrorText } from "./ToolErrorText";

function SpawnSendToolRendererComponent({ ctx }: ToolContentProps) {
  const { input, result } = ctx;
  const message =
    typeof input === "object" && typeof input.message === "string" ? input.message.trim() : "";

  if (!message && !result?.content) return null;

  return (
    <div className="tool-content-spawn-send min-w-0" data-testid="spawn-send-body">
      {message && (
        <div className="min-w-0 px-2 py-1.5 text-sm">
          <MarkdownRenderer content={message} isUser={false} className="text-sm w-full" />
        </div>
      )}
      {result?.content &&
        (result.is_error ? (
          <ToolErrorText className="border-t border-border/30">{formatErrorMessage(result.content)}</ToolErrorText>
        ) : (
          <div className="border-t border-border/30 px-2 py-1.5 text-xs text-muted-foreground">{result.content}</div>
        ))}
    </div>
  );
}

export const SpawnSendToolRenderer = memo(SpawnSendToolRendererComponent);
