// Copyright (c) 2025 Reliant Labs

import { Building2, Check, ChevronDown, ChevronsUpDown, Plus } from "lucide-react";
import { useRef, useState, type ReactNode } from "react";

import DropdownMenu, { type MenuGroup } from "@/components/forge-ui/dropdown_menu";
import type { PopoverTriggerProps } from "@/components/forge-ui/popover";
import Tooltip from "@/components/forge-ui/tooltip";
import { useActiveOrg, useSetDefaultOrg } from "@/hooks/useOrgs";
import { cn } from "@/lib/utils";
import { toast } from "@/lib/toast-manager";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { SIDEBAR_ORG_TRIGGER, SidebarOrgLabel } from "../Layout/sidebar/primitives";
import { CreateOrganizationDialog } from "./CreateOrganizationDialog";
import { OrgPlanBadge } from "./OrgPlanBadge";
import { useOrgPlanLabel } from "./orgPlan";

/**
 * Which org the app is acting in, a way to change it, and a way to make one.
 *
 * The sidebar menu shows for everyone with an org, one included: it names the
 * org the app acts in and is the entry point to "Create organization", which a
 * person with one org needs most (control-plane docs/design/orgs-and-members.md
 * §2.12). The phone and approve-page LIST stays a choice between orgs, so it
 * still needs two.
 *
 * Switching changes what THIS app shows and acts on (ActiveOrgBinding resets
 * the org-scoped caches). "Make default" is the separate, server-side
 * preference that the CLI and other devices follow, so it is its own action
 * and says so.
 *
 * `sidebar-top` is the same menu as the sidebar's top row (restyle §11.3):
 * the brand mark and the org's name, the whole row the trigger.
 *
 * `sidebar-header` is the one shell's top row (U6, owner; Vercel's team row):
 * the org's avatar, its name (in full in a tooltip), its plan badge, and one
 * ⇕, the whole row the trigger. The project switcher is its own row below.
 */
export function OrgSwitcher({
  variant = "sidebar",
  fallback = null,
  onSwitched,
}: {
  /** `sidebar-switcher`: the project shell's `[Org ▾] / [Project ▾]` row, the org half sized to its name. */
  variant?: "sidebar" | "sidebar-top" | "sidebar-switcher" | "sidebar-header" | "list";
  /** What to render with no org (no control plane, or a person in none). */
  fallback?: ReactNode;
  /** Runs after a pick moved the tab to another org (the phone's drawer leaves the old org's chat). */
  onSwitched?: (orgId: string) => void;
}) {
  const { orgs, active, canSwitch, select } = useActiveOrg();
  if (!active) return fallback;
  if (variant === "list") return canSwitch ? <OrgList orgs={orgs} activeId={active.id} onSelect={select} /> : null;
  return (
    <SidebarOrgSwitcher
      orgs={orgs}
      active={active}
      onSelect={(orgId) => {
        select(orgId);
        if (orgId !== active.id) onSwitched?.(orgId);
      }}
      top={variant === "sidebar-top" || variant === "sidebar-switcher"}
      compact={variant === "sidebar-switcher"}
      header={variant === "sidebar-header"}
    />
  );
}

/** The org's role, in words. */
function roleLabel(org: MyOrg): string {
  return org.isOwner ? "Owner" : org.role.charAt(0).toUpperCase() + org.role.slice(1);
}

function SidebarOrgSwitcher({
  orgs,
  active,
  onSelect,
  top,
  compact = false,
  header = false,
}: {
  orgs: MyOrg[];
  active: MyOrg;
  onSelect: (id: string) => void;
  top: boolean;
  compact?: boolean;
  header?: boolean;
}) {
  const setDefault = useSetDefaultOrg();
  const [creating, setCreating] = useState(false);

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
            setDefault.mutate({ orgId: active.id }, {
              onError: () => toast.error("Could not change your default organization"),
            }),
        },
      ],
    });
  }
  groups.push({
    items: [
      {
        label: "Create organization",
        icon: <Plus className="h-4 w-4" aria-hidden="true" />,
        testId: "org-create",
        onClick: () => setCreating(true),
      },
    ],
  });

  return (
    <>
      <DropdownMenu
        groups={groups}
        align="left"
        label="Switch organization"
        density="compact"
        menuClassName="min-w-[220px] max-w-[280px]"
        triggerClassName="w-full"
        trigger={(props) =>
          header ? (
            <OrgHeaderTrigger {...props} org={active} />
          ) : top ? (
            <button {...props} type="button" data-testid="org-switcher" className={cn(SIDEBAR_ORG_TRIGGER, compact && "w-auto max-w-full flex-none gap-1.5 px-1.5")}>
              {compact ? (
                // Sized to the name (a flex-1 name would shrink-wrap to nothing beside the project);
                // no mark: the row is tight, and the project half carries one.
                <>
                  <span className="min-w-0 truncate font-semibold" data-testid="org-switcher-name">
                    {active.name}
                  </span>
                  <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                </>
              ) : (
                <SidebarOrgLabel name={active.name} menu nameTestId="org-switcher-name" />
              )}
            </button>
          ) : (
            <button
              {...props}
              type="button"
              data-testid="org-switcher"
              className={cn(
                "flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-sm transition-colors",
                "text-foreground/85 hover:bg-muted/50 hover:text-foreground",
                "focus:outline-none focus:ring-2 focus:ring-ring"
              )}
            >
              <Building2 className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="min-w-0 flex-1 truncate" data-testid="org-switcher-name">
                {active.name}
              </span>
              <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            </button>
          )
        }
      />
      <CreateOrganizationDialog open={creating} onClose={() => setCreating(false)} />
    </>
  );
}

/** The org's avatar: its initial, round (a person or team mark, where a project's is square). */
export function OrgAvatar({ name, className }: { name: string; className?: string }) {
  const initial = Array.from(name.trim())[0]?.toUpperCase() ?? "?";
  return (
    <span
      aria-hidden="true"
      data-testid="org-avatar"
      className={cn("flex size-6 shrink-0 items-center justify-center rounded-full bg-foreground/10 text-meta font-semibold leading-none text-foreground", className)}
    >
      {initial}
    </span>
  );
}

/** The one shell's top row: avatar, name, plan, ⇕. The whole row opens the org menu. */
function OrgHeaderTrigger({ org, ...props }: PopoverTriggerProps & { org: MyOrg }) {
  const plan = useOrgPlanLabel(org.id);
  return (
    <button
      {...props}
      type="button"
      data-testid="org-switcher"
      data-variant="header"
      aria-label={`Organization: ${org.name}${plan ? `, ${plan} plan` : ""}. Switch organization`}
      className={cn(SIDEBAR_ORG_TRIGGER, "h-9 w-full gap-2 px-1.5")}
    >
      <OrgAvatar name={org.name} />
      <Tooltip content={org.name} side="bottom" focusable={false} triggerClassName="flex min-w-0">
        <span className="min-w-0 truncate font-semibold" data-testid="org-switcher-name">
          {org.name}
        </span>
      </Tooltip>
      {plan && <OrgPlanBadge label={plan} />}
      <ChevronsUpDown className="ml-auto size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
    </button>
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
      <Check className={cn("h-4 w-4 shrink-0 text-primary-ink", !selected && "invisible")} aria-hidden="true" />
    </button>
  );
}
