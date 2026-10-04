// Copyright (c) 2025 Reliant Labs

/**
 * Chrome for the /automations pages. The shell itself is the shared
 * AreaShell; this fixes the area's path and words.
 */

import type { ReactNode } from "react";

import { AreaShell } from "../Layout/AreaShell";

export function AutomationsShell({ children }: { children: ReactNode }) {
  return (
    <AreaShell areaPath="/automations" areaLabel="All automations" areaNoun="automations">
      {children}
    </AreaShell>
  );
}
