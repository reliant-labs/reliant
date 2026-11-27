// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Calendar: the route's page (routes.tsx, lazy). A placeholder behind
 * the gate until the page itself lands in its own change.
 */

import EmptyState from "@/components/forge-ui/empty_state";

import { GtmShell } from "../shared/GtmShell";

export function CalendarPage() {
  return (
    <GtmShell page="calendar">
      <EmptyState title="Calendar" description="This page is on its way." />
    </GtmShell>
  );
}
