/**
 * The navigation drawer's organization card: which org this tab acts in, and,
 * for a person in more than one, the way to change it.
 *
 * It is the phone's twin of the desktop sidebar's `OrgSwitcher` and drives the
 * same state: `useActiveOrg` reads the tab's org and `select` is
 * `chooseActiveOrg` (store/activeOrgStore), so a switch here is per tab, never
 * reaches another tab, and writes nothing to the server. `ActiveOrgBinding`,
 * mounted at the root for every surface, then drops the old org's caches and
 * moves the context project (components/Orgs/followActiveOrg.ts) exactly as it
 * does on desktop.
 *
 * What the phone adds is the navigation. Desktop clears the open chat from the
 * chat store; on `/m/*` the open chat is in the URL, so the switch also goes
 * to the mobile home, the chat list, which the binding has already pointed at
 * the new org. Nothing of the old org stays on screen.
 *
 * A person with one org sees its name and no picker: there is nothing to
 * choose between. Creating an org and "Make default" stay desktop actions.
 */

import { useId, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Building2, ChevronDown } from "lucide-react";
import { useActiveOrg } from "../../hooks/useOrgs";
import { cn } from "../../lib/utils";
import { OrgRow } from "../Orgs/OrgSwitcher";

interface MobileOrgSwitcherProps {
  /** Called after a switch, so the drawer can close even when the route did not change. */
  onSwitched?: () => void;
}

function OrgSummary({ name }: { name: string }) {
  return (
    <>
      <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary-ink">
        <Building2 className="h-4 w-4" aria-hidden="true" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-xs text-muted-foreground">Organization</span>
        <span
          className="block truncate text-sm font-medium text-foreground"
          data-testid="mobile-org-name"
        >
          {name}
        </span>
      </span>
    </>
  );
}

export function MobileOrgSwitcher({ onSwitched }: MobileOrgSwitcherProps) {
  const { orgs, active, canSwitch, select } = useActiveOrg();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const listId = useId();

  // Still loading, signed out of the control plane, or a machine credential:
  // there is no org to name.
  if (!active) return null;

  if (!canSwitch) {
    return (
      <div
        data-testid="mobile-org-switcher"
        className="flex min-h-[60px] w-full items-center gap-3 rounded-lg px-3 elevation-1"
      >
        <OrgSummary name={active.name} />
      </div>
    );
  }

  const choose = (orgId: string) => {
    setOpen(false);
    if (orgId === active.id) return;
    select(orgId);
    // The open chat, if any, is the old org's: go to the new org's chat list.
    void navigate({ to: "/m/chats" });
    onSwitched?.();
  };

  return (
    <div data-testid="mobile-org-switcher" className="rounded-lg elevation-1">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={listId}
        aria-label={`Organization: ${active.name}. Switch organization`}
        onClick={() => setOpen((o) => !o)}
        className="flex min-h-[60px] w-full items-center gap-3 rounded-lg px-3 text-left active:bg-foreground/5"
      >
        <OrgSummary name={active.name} />
        <ChevronDown
          className={cn(
            "h-4 w-4 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-180",
          )}
          aria-hidden="true"
        />
      </button>
      {open && (
        <div
          id={listId}
          role="radiogroup"
          aria-label="Organization"
          data-testid="mobile-org-list"
          className="border-t border-border py-1"
        >
          {orgs.map((o) => (
            <OrgRow
              key={o.id}
              org={o}
              selected={o.id === active.id}
              onSelect={() => choose(o.id)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
