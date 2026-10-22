// Copyright (c) 2025 Reliant Labs

/**
 * The collapsed composer on an automation run nobody has taken over yet
 * (WORKFLOW_UI.md §4.2, §6.3): "This run was started by a schedule. Reply to
 * take it over." It says, BEFORE the user types, that replying changes what
 * the run is: it becomes one of their chats and stays in the sidebar.
 *
 * Clicking Reply expands the normal composer; the adoption itself happens on
 * send (useAdoptOnSend), not on expand, so opening the composer and walking
 * away commits nothing.
 */

import { useCallback } from "react";
import { MessageSquareReply } from "lucide-react";

import { useAdoptRun } from "../../hooks/run-queries";
import { useTriggerName } from "../../hooks/trigger-queries";
import { launchKindDisplay } from "../../lib/runStatus";
import { isUnadoptedAutomation } from "../../lib/sidebarChatList";
import { logger } from "../../lib/logger";
import { LaunchKindIcon } from "../runs/LaunchKindIcon";

interface TakeOverChat {
  id: string;
  launchKind?: string;
  adoptedAt?: string;
  triggerId?: string;
}

export function TakeOverRunBar({ chat, onReply }: { chat: TakeOverChat; onReply: () => void }) {
  const triggerName = useTriggerName(chat.triggerId);
  const launch = launchKindDisplay(chat.launchKind, { triggerName });
  const origin = launch.kind === "agent.start_run" ? "an agent" : launch.kind === "schedule" ? "a schedule" : launch.shortLabel;

  return (
    <div
      className="flex flex-shrink-0 items-center gap-3 border-t border-border bg-card px-4 py-2.5 sm:px-6 lg:px-8"
      data-testid="take-over-run-bar"
    >
      <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border/60 bg-background text-muted-foreground">
        <LaunchKindIcon kind={launch.kind} />
      </span>
      <p className="min-w-0 flex-1 text-sm text-muted-foreground">
        <span className="text-foreground">{triggerName ? `Started by ${triggerName}.` : `This run was started by ${origin}.`}</span>{" "}
        Replying adds it to your chats.
      </p>
      <button
        type="button"
        onClick={onReply}
        className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        <MessageSquareReply className="h-4 w-4" aria-hidden="true" />
        Reply to take it over
      </button>
    </div>
  );
}

/**
 * Wrap a send so that, on an un-adopted automation chat, a successful send
 * also adopts it (§6.3, "Implicit"). Order matters: send first, so a send that
 * fails does not leave a chat adopted with nothing said. Adoption failing is
 * logged, not thrown: the message is already sent, and the server starts the
 * continuation attended whether or not the chat is adopted.
 */
export function useAdoptOnSend<Args extends unknown[]>(
  chat: TakeOverChat | undefined,
  send: (...args: Args) => Promise<void>,
): (...args: Args) => Promise<void> {
  const adopt = useAdoptRun();
  const shouldAdopt = !!chat && isUnadoptedAutomation(chat);
  const chatId = chat?.id;
  const { mutateAsync } = adopt;
  return useCallback(
    async (...args: Args) => {
      await send(...args);
      if (!shouldAdopt || !chatId) return;
      try {
        await mutateAsync(chatId);
      } catch (error) {
        logger.error("[TakeOverRunBar] Adopting after reply failed", { chatId, error });
      }
    },
    [send, shouldAdopt, chatId, mutateAsync],
  );
}
