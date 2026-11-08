import React from "react";
import SidebarLayout, { type SidebarNavItem } from "./sidebar_layout";

const item: SidebarNavItem = {
  id: "observability",
  label: "Observability",
  href: "/obs",
  defaultExpanded: true,
  children: [
    { label: "Logs", href: "/obs/logs", active: true },
    { id: "saved", label: "Saved", defaultExpanded: false, children: [] },
  ],
};
// @ts-expect-error Initial expansion is boolean, not a controlled state string.
const invalid: SidebarNavItem = { label: "Bad", defaultExpanded: "open" };
void invalid;

export function Contract() {
  return (
    <SidebarLayout brand="Forge" navItems={[item]}>
      Page
    </SidebarLayout>
  );
}
