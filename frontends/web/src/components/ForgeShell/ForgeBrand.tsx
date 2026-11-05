// Copyright (c) 2025 Reliant Labs

/**
 * The forge shell's lockup: "Forge" with "by Reliant" under it. Used by the
 * shell's top bar, the console's brand row and the auth cards on the forge
 * host (AuthLayout.AuthHeader).
 */

import { Anvil } from "lucide-react";

import { cn } from "@/lib/utils";

export function ForgeMark({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn("grid shrink-0 place-items-center rounded-md bg-primary text-primary-foreground", className ?? "h-7 w-7")}
    >
      <Anvil className="h-[60%] w-[60%]" strokeWidth={2.2} />
    </span>
  );
}

export function ForgeBrand({ size = "sm", className }: { size?: "sm" | "lg"; className?: string }) {
  const large = size === "lg";
  return (
    <span className={cn("flex min-w-0 items-center gap-2", className)} data-testid="forge-brand">
      <ForgeMark className={large ? "h-9 w-9" : "h-7 w-7"} />
      <span className="flex min-w-0 flex-col leading-none">
        <span className={cn("font-semibold tracking-tight text-foreground", large ? "text-3xl" : "text-sm")}>Forge</span>
        <span className={cn("text-muted-foreground", large ? "mt-1 text-sm" : "mt-0.5 text-2xs")}>by Reliant</span>
      </span>
    </span>
  );
}
