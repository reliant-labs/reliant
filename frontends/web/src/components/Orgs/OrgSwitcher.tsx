// Copyright (c) 2025 Reliant Labs

import { Building2, Check, ChevronsUpDown } from "lucide-react";
import { useRef } from "react";

import DropdownMenu, { type MenuGroup } from "@/components/forge-ui/dropdown_menu";
import { useActiveOrg, useSetDefaultOrg } from "@/hooks/useOrgs";
import { cn } from "@/lib/utils";
import { toast } from "@/lib/toast-manager";
import type { MyOrg } from "@/services/controlPlane/orgs";

/**
 * Which org the app is acting in, and a way to change it.
 *
 * A person with ONE org gets no chrome at all: a switcher with a single
 * option is a control that does nothing, and most people never have a second
 * org. It appears the moment ListMyOrganizations returns two.
 *
 * Switching changes what THIS app shows and acts on (ActiveOrgBinding resets
 * the org-scoped caches). "Make default" is the separate, server-side
 * preference that the CLI and other devices follow, so it is its own action
 * and says so.
 */
export function OrgSwitcher({ variant = "sidebar" }: { variant?: "sidebar" | "list" }) {
  const { orgs, active, canSwitch, select } = useActiveOrg();
  if (!canSwitch || !active) return null;
  return variant === "list" ? (
    <OrgList orgs={orgs} activeId={active.id} onSelect={select} />
  ) : (
    <SidebarOrgSwitcher orgs={orgs} active={active} onSelect={select} />
  );
}

/** The org's role, in words. */
function roleLabel(org: MyOrg): string {
  return org.isOwner ? "Owner" : org.role.charAt(0).toUpperCase() + org.role.slice(1);
}

function SidebarOrgSwitcher({ orgs, active, onSelect }: { orgs: MyOrg[]; active: MyOrg; onSelect: (id: string) => void }) {
  const setDefault = useSetDefaultOrg();

  const groups: MenuGroup[] = [
    {
      label: "Organization",
      selection: "single",
      items: orgs.map((o) => ({
        label: o.name,
        checked: o.id === active.id,
        hint: o.isDefault ? "Default" : undefined,
        testId: `org-option-${o.slug || o.id}`,
        onClick: () => onSelect(o.id),
      })),
    },
  ];
  if (!active.isDefault) {
    groups.push({
      items: [
        {
          label: `Make ${active.name} my default`,
          testId: "org-make-default",
          onClick: () =>
            setDefault.mutate(active.id, {
              onError: () => toast.error("Could not change your default organization"),
            }),
        },
      ],
    });
  }

  return (
    <DropdownMenu
      groups={groups}
      align="left"
      label="Switch organization"
      density="compact"
      menuClassName="min-w-[220px] max-w-[280px]"
      triggerClassName="w-full"
      trigger={(props) => (
        <button
          {...props}
          type="button"
          data-testid="org-switcher"
          className={cn(
            "flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-sm transition-colors",
            "text-foreground/85 hover:bg-muted/50 hover:text-foreground",
            "focus:outline-none focus:ring-2 focus:ring-ring/40"
          )}
        >
          <Building2 className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <span className="min-w-0 flex-1 truncate" data-testid="org-switcher-name">
            {active.name}
          </span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        </button>
      )}
    />
  );
}

/** The phone and the approve pages: every org as a row, 44px targets. */
function OrgList({ orgs, activeId, onSelect }: { orgs: MyOrg[]; activeId: string; onSelect: (id: string) => void }) {
  return (
    <div role="radiogroup" aria-label="Organization" data-testid="org-list">
      {orgs.map((o) => (
        <OrgRow key={o.id} org={o} selected={o.id === activeId} onSelect={() => onSelect(o.id)} />
      ))}
    </div>
  );
}

export function OrgRow({ org, selected, onSelect }: { org: MyOrg; selected: boolean; onSelect: () => void }) {
  const ref = useRef<HTMLButtonElement>(null);
  return (
    <button
      type="button"
      ref={ref}
      role="radio"
      tabIndex={selected ? 0 : -1}
      aria-checked={selected}
      onKeyDown={(event) => {
        if (!["ArrowDown", "ArrowRight", "ArrowUp", "ArrowLeft", "Home", "End"].includes(event.key)) return;
        const rows = Array.from(ref.current?.closest('[role="radiogroup"]')?.querySelectorAll<HTMLButtonElement>('[role="radio"]') ?? []);
        if (!rows.length) return;
        event.preventDefault();
        const index = rows.indexOf(event.currentTarget);
        const next = event.key === "Home" ? 0 : event.key === "End" ? rows.length - 1
          : (index + (event.key === "ArrowDown" || event.key === "ArrowRight" ? 1 : -1) + rows.length) % rows.length;
        rows[next]?.focus();
        rows[next]?.click();
      }}
      onClick={onSelect}
      data-testid={`org-row-${org.slug || org.id}`}
      className="flex min-h-[44px] w-full items-center gap-3 px-4 py-2 text-left active:bg-foreground/5"
    >
      <Building2 className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm text-foreground">{org.name}</span>
        <span className="block truncate text-xs text-muted-foreground">
          {roleLabel(org)}
          {org.isDefault && " · Default"}
        </span>
      </span>
      <Check className={cn("h-4 w-4 shrink-0 text-primary", !selected && "invisible")} aria-hidden="true" />
    </button>
  );
}
