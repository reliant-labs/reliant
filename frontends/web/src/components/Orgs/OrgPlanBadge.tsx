// Copyright (c) 2026 Reliant Labs

/** The org's plan beside its name in the sidebar's top row (Vercel's "Hobby"): a quiet outlined pill. */
export function OrgPlanBadge({ label }: { label: string }) {
  return (
    <span
      data-testid="org-plan-badge"
      className="inline-flex shrink-0 items-center rounded-full border border-border px-1.5 py-0.5 text-2xs font-medium leading-none text-muted-foreground"
    >
      {label}
    </span>
  );
}
