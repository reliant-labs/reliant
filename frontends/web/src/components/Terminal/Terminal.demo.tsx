// Copyright (c) 2025 Reliant Labs

/**
 * components/Terminal/Terminal.tsx in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). A terminal is a shell on the user's machine:
 * the real component opens a WebSocket to the reliant api-server with the
 * signed-in user's token (Supabase or the forge login broker), which the
 * daemon relays to a pty. The demo runs in the page on sample data, with no
 * machine, no sign-in and no request off the page's origin, so the terminal
 * panel's tabs keep working and each tab says why it has no shell.
 */

import { SquareTerminal } from "lucide-react";

import { cn } from "../../lib/utils";
import type * as Real from "./Terminal";

export const Terminal: typeof Real.Terminal = ({ className }) => (
  <div className={cn("flex h-full w-full items-center justify-center bg-background p-6 text-center", className)}>
    <div className="flex max-w-sm flex-col items-center gap-2">
      <SquareTerminal className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
      <p className="text-ui font-medium text-foreground">The terminal opens a shell on your machine</p>
      <p className="text-ui text-muted-foreground">This demo runs in your browser on sample data, so there is no machine to open one on.</p>
    </div>
  </div>
);
