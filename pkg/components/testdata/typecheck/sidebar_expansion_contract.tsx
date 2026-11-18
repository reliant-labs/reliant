import React, { useState } from "react";
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
const badChange: SidebarNavItem = {
  label: "Bad",
  // @ts-expect-error Controlled open state is boolean, not a state string.
  open: "expanded",
  // @ts-expect-error onOpenChange reports the next open state as a boolean.
  onOpenChange: (next: string) => next,
};
void badChange;

export function Contract() {
  const [open, setOpen] = useState(true);
  const controlled: SidebarNavItem = {
    label: "Alerts",
    href: "/alerts",
    open,
    onOpenChange: setOpen,
    children: [{ label: "Rules", href: "/alerts/rules" }],
  };
  return (
    <SidebarLayout brand="Forge" navItems={[item, controlled]}>
      Page
    </SidebarLayout>
  );
}
