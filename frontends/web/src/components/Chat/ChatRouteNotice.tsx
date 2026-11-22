// Copyright (c) 2025 Reliant Labs

import { MessageSquareOff, TriangleAlert } from "lucide-react";
import { useRouter } from "@tanstack/react-router";

import Button from "@/components/forge-ui/button";
import { openNewChat } from "@/store/openChat";

/**
 * What a chat's URL shows when it opens no chat (lib/chatRouteResolve.ts), in
 * the chat shell's main pane, with the sidebar beside it:
 *
 *   missing  no such chat, or not one the person may read: the server answers
 *            the same for both, and so does this page. Never a silent hop to
 *            the home page, which would leave a broken link looking like a
 *            working one.
 *   error    the chat could not be read (offline, a server error): a Retry.
 */
export function ChatRouteNotice({ kind }: { kind: "missing" | "error" }) {
  const router = useRouter();
  const missing = kind === "missing";
  const Icon = missing ? MessageSquareOff : TriangleAlert;
  return (
    <div className="flex h-full min-h-0 items-center justify-center p-6" data-testid={missing ? "chat-not-found" : "chat-load-error"}>
      <div className="max-w-sm space-y-4 text-center">
        <Icon className="mx-auto h-8 w-8 text-muted-foreground" aria-hidden="true" />
        <div className="space-y-1">
          <h1 className="text-base font-semibold text-foreground">{missing ? "This chat isn't available" : "This chat couldn't be loaded"}</h1>
          <p className="text-sm text-muted-foreground">
            {missing
              ? "It may have been deleted, or it isn't shared with you."
              : "Check your connection, then try again."}
          </p>
        </div>
        {missing ? (
          <Button variant="primary" onClick={() => void openNewChat()} data-testid="chat-not-found-new">
            Start a new chat
          </Button>
        ) : (
          <Button variant="primary" onClick={() => void router.invalidate()} data-testid="chat-load-retry">
            Try again
          </Button>
        )}
      </div>
    </div>
  );
}
