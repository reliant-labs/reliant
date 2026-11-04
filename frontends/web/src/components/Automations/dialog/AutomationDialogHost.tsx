// Copyright (c) 2025 Reliant Labs

/**
 * Mounts the automation dialog for openAutomationDialog. Rendered once, in the
 * root shell (routes.tsx), so any page, chat card or builder panel can open it
 * without owning any state. Keyed on the open count: every open starts from
 * the request, never from a half-edited previous session.
 */

import { AutomationDialog } from "./AutomationDialog";
import { useAutomationDialogStore } from "./openAutomationDialog";

export function AutomationDialogHost() {
  const request = useAutomationDialogStore((s) => s.request);
  const openCount = useAutomationDialogStore((s) => s.openCount);
  const close = useAutomationDialogStore((s) => s.close);
  if (!request) return null;
  return <AutomationDialog key={openCount} request={request} onClose={close} />;
}
