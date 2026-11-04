// Copyright (c) 2025 Reliant Labs

/** The pivot link every page uses: a router link in the console's small button style. */

import type { ReactNode } from "react";

import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";

import { BUTTON_SMALL } from "../Dashboards/parts";

export function ExploreLink({ href, children, testId, className }: { href: string; children: ReactNode; testId: string; className?: string }) {
  return (
    <Link href={href} className={cn(BUTTON_SMALL, className)} data-testid={testId}>
      {children}
    </Link>
  );
}
