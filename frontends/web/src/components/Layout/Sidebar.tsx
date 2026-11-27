// Copyright (c) 2026 Reliant Labs

/**
 * The app's nav (routes.tsx's AppNav, the shell's `nav` seam): the chat
 * shell's nav column (ChatShellNav.tsx) with the app's config
 * (navConfig/hybrid.ts) and what only the app has: Help and the desktop
 * app's update button in the footer, and the Set up chats card while no
 * agent sandbox can run a chat (OnboardingFlow/onboardingGatePolicy.ts).
 *
 * The public demo draws the same column with its own config
 * (Forge/Demo/DemoNav.tsx); this file is where the app's pieces stay, so the
 * demo's bundle never imports them.
 */

import { useCallback, useMemo } from "react";

import { ConnectSandboxCta } from "../EmptyState/ConnectSandboxCta";
import { chatSetupNeeded, chatSetupPath } from "../OnboardingFlow/onboardingGatePolicy";
import { useChatSetup } from "../OnboardingFlow/useChatSetup";
import { ChatShellNav, type ChatSetupGate, type ChatShellNavProps } from "./ChatShellNav";
import { HelpMenu } from "./sidebar/HelpMenu";
import type { NavSlots } from "./sidebar/NavRenderer";
import { hybridNav } from "./sidebar/navConfig";
import { SidebarUpdateButton } from "./SidebarUpdateButton";

interface SidebarProps extends Omit<ChatShellNavProps, "config" | "hostSlots" | "chatSetup" | "find" | "scope"> {
  /** Open the journey picker (`?tour=pick:menu`) on the page on screen: Help › Take a tour, the Setup Guide's tour item. */
  onOpenTours?: () => void;
}

export function Sidebar({ onOpenTours, ...props }: SidebarProps) {
  const { onNavigate } = props;
  const navigate = useCallback((href: string) => onNavigate?.(href), [onNavigate]);
  const hostSlots = useMemo<NavSlots>(
    () => ({
      "update-button": () => <SidebarUpdateButton />,
      // Help, beside Settings.
      help: () => <HelpMenu onNavigate={navigate} onOpenTours={onOpenTours} />,
    }),
    [navigate, onOpenTours],
  );
  // Whether chats can run yet (OnboardingFlow/onboardingGatePolicy.ts, ChatSetup).
  // Until they can, the Chats section is the Set up chats card, and New chat
  // goes where the card does.
  const setup = useChatSetup();
  const chatSetup: ChatSetupGate | null = chatSetupNeeded(setup)
    ? { href: chatSetupPath(setup), card: <ConnectSandboxCta variant="nav" /> }
    : null;
  return <ChatShellNav {...props} config={hybridNav} hostSlots={hostSlots} chatSetup={chatSetup} />;
}
