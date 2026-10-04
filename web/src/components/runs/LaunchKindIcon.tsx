// Copyright (c) 2025 Reliant Labs

import { Bot, CalendarClock, MessageSquare, Zap } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * The launch kind's icon. Decorative: the short label or tooltip beside it
 * carries the meaning. Its own module so the sidebar's origin glyph can use it
 * without importing the Runs list.
 */
export function LaunchKindIcon({ kind, className }: { kind: string; className?: string }) {
  const Icon =
    kind === "schedule" ? CalendarClock : kind === "agent.start_run" ? Bot : kind === "chat.start" ? MessageSquare : Zap;
  return <Icon className={cn("h-3.5 w-3.5 shrink-0", className)} aria-hidden="true" />;
}
