import type { ReactNode } from "react";

/** Native disclosure: tap, click, Enter or Space; never hover-only help. */
export function SetupHelp({ children, label = "More help" }: { children: ReactNode; label?: string }) {
  return (
    <details className="text-xs text-muted-foreground">
      <summary className="min-h-[44px] cursor-pointer content-center rounded-md px-1 font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
        {label}
      </summary>
      <div className="space-y-3 pb-2 leading-relaxed">{children}</div>
    </details>
  );
}
