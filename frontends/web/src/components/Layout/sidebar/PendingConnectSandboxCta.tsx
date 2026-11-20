// Copyright (c) 2026 Reliant Labs

/**
 * PLACEHOLDER for `components/EmptyState/ConnectSandboxCta.tsx`, which the
 * U6d CONNECTORS branch builds (u6d-briefing.md, "Path contract"). The Chats
 * area of the Agents nav shows it while no agent sandbox can run a chat.
 *
 * TODO(U6d CONNECTORS): the orchestrator's merge replaces the one import of
 * this file (Layout/Sidebar.tsx) with the real component and deletes it. Its
 * props are the contract this placeholder assumes: `onNavigate` (the shell's
 * navigate) and an optional `setup` (onboarding first, or connect a sandbox).
 */

import type React from "react";
import { Cloud, Laptop } from "lucide-react";

import { sandboxesPath } from "@/lib/paths";

import { chatSetupPath } from "../../OnboardingFlow/onboardingGate";

export interface ConnectSandboxCtaProps {
  onNavigate: (href: string) => void;
  /** Onboarding is not done yet: both options go through it. */
  setup?: "onboard" | "connect-machine";
}

export function ConnectSandboxCta({ onNavigate, setup = "connect-machine" }: ConnectSandboxCtaProps) {
  const href = setup === "onboard" ? chatSetupPath(setup) : sandboxesPath();
  const open = (event: React.MouseEvent<HTMLAnchorElement>) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
    event.preventDefault();
    onNavigate(href);
  };
  const option = (Icon: typeof Laptop, label: string, detail: string, testId: string) => (
    <a
      href={href}
      onClick={open}
      data-testid={testId}
      className="no-color flex items-start gap-2 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-foreground/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <span className="min-w-0">
        <span className="block text-ui font-medium text-foreground">{label}</span>
        <span className="block text-meta text-muted-foreground">{detail}</span>
      </span>
    </a>
  );
  return (
    <section aria-label="Connect an agent sandbox" data-testid="connect-sandbox-cta" className="mt-1 rounded-xl border border-border px-1 py-2">
      <p className="px-2 text-ui font-medium text-foreground">Connect an agent sandbox to start chats</p>
      <div className="mt-1 space-y-0.5">
        {option(Laptop, "Use your own machine", "Run reliant daemon start and pair it", "connect-sandbox-own")}
        {option(Cloud, "Provision a Reliant sandbox", "A cloud sandbox, ready in a minute", "connect-sandbox-cloud")}
      </div>
    </section>
  );
}
