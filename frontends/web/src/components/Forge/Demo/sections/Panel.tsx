// Copyright (c) 2026 Reliant Labs

import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/** A plain card that does not link, for the demo's own pages. */
export function Panel({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("rounded-2xl border border-border bg-surface p-5", className)}>{children}</div>;
}
