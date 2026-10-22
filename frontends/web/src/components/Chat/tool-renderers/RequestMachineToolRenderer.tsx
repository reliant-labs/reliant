// Copyright (c) 2025 Reliant Labs

/**
 * request_machine (research/NO_MACHINE_CHATS.md §3): the model in a chat with
 * no machine says the task needs the user's computer. The card shows its
 * reason and the one action that helps, "Connect a machine", which opens the
 * machine choice and calls SetChatDaemon.
 *
 * Rendered inline in the transcript (ChatMessage), not inside a collapsed tool
 * row: it is a question to the user, not a record of work. Once the chat is on
 * a machine the card says so and the button goes away.
 */

import { memo, useState } from "react";
import { CheckCircle, Monitor } from "lucide-react";
import { useChat } from "@/hooks/chat-queries";
import { Button } from "../../ui/Button";
import { ConnectMachineDialog } from "../ConnectMachineDialog";
import type { ToolContentProps } from "./types";

/** The model's reason, from the call's input. */
export function requestMachineReason(input: Record<string, unknown> | string | undefined): string {
  if (!input || typeof input === "string") return "";
  const reason = input.reason;
  return typeof reason === "string" ? reason.trim() : "";
}

interface RequestMachineCardProps {
  chatId?: string;
  reason: string;
}

function RequestMachineCardComponent({ chatId, reason }: RequestMachineCardProps) {
  const { data: chat } = useChat(chatId);
  const [open, setOpen] = useState(false);
  // Until the chat has loaded, assume it still needs one: the card exists
  // because the run had no machine when the model asked.
  const connected = chat ? !chat.noMachine : false;

  return (
    <div
      data-testid="request-machine-card"
      className="my-1.5 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <div className="flex items-start gap-2.5">
        {connected ? (
          <CheckCircle className="mt-0.5 h-4 w-4 flex-shrink-0 text-success-ink" aria-hidden="true" />
        ) : (
          <Monitor className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-foreground">
            {connected ? "Machine connected" : "This needs a machine"}
          </p>
          {reason && <p className="mt-0.5 text-sm text-muted-foreground break-words">{reason}</p>}
          {connected && (
            <p className="mt-0.5 text-xs text-muted-foreground">Send a message to continue on it.</p>
          )}
        </div>
        {!connected && chatId && (
          <Button size="sm" onClick={() => setOpen(true)} data-testid="request-machine-connect">
            Connect a machine
          </Button>
        )}
      </div>
      {chatId && <ConnectMachineDialog chatId={chatId} open={open} onClose={() => setOpen(false)} />}
    </div>
  );
}

export const RequestMachineCard = memo(RequestMachineCardComponent);

/** The tool-content router's entry, for surfaces that render tool content. */
function RequestMachineToolRendererComponent({ ctx }: ToolContentProps) {
  return <RequestMachineCard chatId={ctx.chatId} reason={requestMachineReason(ctx.input)} />;
}

export const RequestMachineToolRenderer = memo(RequestMachineToolRendererComponent);
