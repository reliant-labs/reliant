// Copyright (c) 2026 Reliant Labs

/**
 * "Set up chats": what the nav's Chats section and the top of Projects show a
 * person who cannot run a chat yet (U6, the daemonless home). One line on why,
 * and one link to the step that fixes it (chatSetupPath): the wizard for a
 * person who has not onboarded, Settings › Machines for one who has.
 *
 * Quiet on purpose: one border, 14px text, an outline button. The nav's one
 * filled button is New chat, and this card is not an alarm.
 */

import type React from "react";
import { Laptop } from "lucide-react";

import { buttonClasses } from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";

import { chatSetupPath } from "./onboardingGate";

export const SET_UP_CHATS_COPY = "Chats run your agents on a machine";

export function SetUpChatsCard({
  setup,
  variant,
  onNavigate,
  className,
}: {
  setup: "onboard" | "connect-machine";
  /** `nav`: compact, in the side nav's Chats section. `page`: a page's lead card. */
  variant: "nav" | "page";
  /** Opens a path in the app; a modified click (new tab) is left to the browser. */
  onNavigate: (href: string) => void;
  className?: string;
}) {
  const href = chatSetupPath(setup);
  const onClick = (event: React.MouseEvent<HTMLAnchorElement>) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
    event.preventDefault();
    onNavigate(href);
  };
  const link = (
    <a
      href={href}
      onClick={onClick}
      data-testid="set-up-chats-link"
      className={cn("no-color shrink-0", buttonClasses({ variant: "outline", size: "sm" }))}
    >
      Set up
    </a>
  );

  if (variant === "nav") {
    return (
      <section
        aria-label="Set up chats"
        data-testid="set-up-chats-card"
        className={cn("mx-2 mt-3 rounded-xl border border-border px-3 py-2.5", className)}
      >
        <p className="text-ui font-medium text-foreground">Set up chats</p>
        <p className="mt-0.5 text-ui text-muted-foreground">{SET_UP_CHATS_COPY}.</p>
        <div className="mt-2">{link}</div>
      </section>
    );
  }

  return (
    <section
      aria-label="Set up chats"
      data-testid="set-up-chats-card"
      className={cn("flex items-center gap-3 rounded-xl border border-border px-4 py-3", className)}
    >
      <Laptop className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="text-ui font-medium text-foreground">Set up chats</p>
        <p className="text-ui text-muted-foreground">
          {SET_UP_CHATS_COPY}. Connect one to start chatting; your apps work without it.
        </p>
      </div>
      {link}
    </section>
  );
}
