// Copyright (c) 2026 Reliant Labs

/**
 * "Connect an agent sandbox to start chats" (U6d, owner): what the Agents
 * area's Chats shows when no sandbox can run a chat. Two ways, side by side:
 *
 *   Use your own machine          pair a computer you run the CLI on (the Add
 *                                 sheet's "Your own computer": `reliant daemon
 *                                 start`, then approve its pairing code)
 *   Provision a Reliant sandbox   a cloud sandbox Reliant runs (the Add sheet's
 *                                 cloud sizes, with the plan behind them)
 *
 * Both open the one Add sandbox sheet (Machines/AddMachineSheet), on the
 * matching choice, so there is still a single place a sandbox is added. Then
 * the guide (lib/learnLinks.tsx "sandboxes").
 *
 * `variant`: "nav" is the narrow version for the nav column (stacked, small
 * type); "page" (default) is for a page body.
 */

import { useState } from "react";
import { Cloud, Laptop } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { LearnLink } from "@/lib/learnLinks";
import { cn } from "@/lib/utils";

import { AddMachineSheet, type ReadyMachine } from "../Machines/AddMachineSheet";

export const CONNECT_SANDBOX_TITLE = "Connect an agent sandbox to start chats";

export interface ConnectSandboxCtaProps {
  variant?: "page" | "nav";
  /** The sandbox the sheet added, once it is connected and can run a chat. */
  onReady?: (sandbox: ReadyMachine) => void;
  className?: string;
}

export function ConnectSandboxCta({ variant = "page", onReady, className }: ConnectSandboxCtaProps) {
  // The sheet stays mounted: it keeps watching a cloud sandbox it created
  // after it closes, and says when that sandbox is ready.
  const [kind, setKind] = useState<"self" | "cloud">("self");
  const [open, setOpen] = useState(false);
  const add = (next: "self" | "cloud") => {
    setKind(next);
    setOpen(true);
  };
  const nav = variant === "nav";
  return (
    <section
      aria-labelledby="connect-sandbox-title"
      className={cn(nav ? "space-y-2 rounded-lg border border-border p-3" : "space-y-3 rounded-xl border border-border bg-surface p-5", className)}
      data-testid="connect-sandbox-cta"
      data-variant={variant}
    >
      <div className="space-y-1">
        <h2 id="connect-sandbox-title" className={nav ? "text-ui font-medium text-ink" : "text-section text-ink"}>
          {CONNECT_SANDBOX_TITLE}
        </h2>
        <p className={cn("text-ink-muted", nav ? "text-meta" : "text-ui")}>
          A sandbox is where your agents run: their files, shell and tools. Use a computer of yours, or let Reliant run one for you.
        </p>
      </div>
      <div className={cn("flex gap-2", nav ? "flex-col" : "flex-wrap")}>
        <Button
          variant="primary"
          size={nav ? "sm" : "md"}
          leftIcon={<Laptop className="h-4 w-4" />}
          onClick={() => add("self")}
          title="Run the Reliant CLI on your computer (reliant daemon start) and approve its pairing code. Free."
          data-testid="connect-sandbox-own"
        >
          Use your own machine
        </Button>
        <Button
          variant="secondary"
          size={nav ? "sm" : "md"}
          leftIcon={<Cloud className="h-4 w-4" />}
          onClick={() => add("cloud")}
          title="A cloud sandbox Reliant runs for you, on a plan. It keeps working when your computer is off."
          data-testid="connect-sandbox-cloud"
        >
          Provision a Reliant sandbox
        </Button>
      </div>
      <LearnLink section="sandboxes" label="How agent sandboxes work" />
      <AddMachineSheet
        open={open}
        initialKind={kind}
        onClose={() => setOpen(false)}
        onReady={(sandbox) => {
          setOpen(false);
          onReady?.(sandbox);
        }}
      />
    </section>
  );
}
