// Copyright (c) 2025 Reliant Labs

import { Github, Globe, Mail, MessageSquare, Plug, Slack, Webhook, type LucideIcon } from "lucide-react";
import { cn } from "../../../lib/utils";

/**
 * An integration's icon from the manifest's icon hint. Unknown hints fall
 * back to a plug rather than nothing, so a new integration is never an empty
 * square.
 */
const ICONS: Record<string, LucideIcon> = {
  github: Github,
  slack: Slack,
  globe: Globe,
  http: Globe,
  webhook: Webhook,
  mail: Mail,
  gmail: Mail,
  sms: MessageSquare,
  twilio: MessageSquare,
};

export function integrationIcon(hint: string | undefined): LucideIcon {
  return (hint && ICONS[hint.toLowerCase()]) || Plug;
}

/** The icon on a neutral tile: integration marks are not ours to tint. */
export function IntegrationIcon({ hint, className, size = "md" }: { hint?: string; className?: string; size?: "sm" | "md" }) {
  const Icon = integrationIcon(hint);
  return (
    <span
      aria-hidden
      className={cn(
        "flex flex-shrink-0 items-center justify-center rounded-lg border border-border/60 bg-background text-foreground",
        size === "sm" ? "h-6 w-6" : "h-8 w-8",
        className,
      )}
    >
      <Icon className={size === "sm" ? "h-3.5 w-3.5" : "h-4 w-4"} />
    </span>
  );
}
