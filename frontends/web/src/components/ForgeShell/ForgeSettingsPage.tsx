// Copyright (c) 2025 Reliant Labs

/**
 * /settings and /settings/$section in the forge shell: the account and
 * organization settings a forge user needs, and nothing of Reliant's (no AI,
 * MCP, machines, editor or chat settings).
 *
 *   profile        the account: email, linked sign-ins, sign-out, deletion
 *   organization   which org you act in
 *   members        who is in the org, invitations, and who holds which permission
 *   tokens         API tokens for the forge CLI and CI
 *   billing        plans, wallet, usage consent and caps (Reliant's section)
 *
 * The paths match Reliant's where both have a section (`/settings/billing`),
 * so the console's links into billing land here unchanged. A Reliant-only
 * slug (`/settings/ai`) falls back to Profile rather than a dead end.
 */

import { lazy, Suspense, type ReactNode } from "react";
import { useParams } from "@tanstack/react-router";

import { AccountSettings } from "@/components/Settings/AccountSettings";
import { MembersPage } from "@/components/Settings/cloud/members/MembersPage";
import { SettingsPageHeader } from "@/components/Settings/SettingsPageHeader";
import { OrgSwitcher } from "@/components/Orgs/OrgSwitcher";
import { useActiveOrg } from "@/hooks/useOrgs";
import { cn } from "@/lib/utils";

import { ForgeTokensSection } from "./ForgeTokensSection";

const BillingSection = lazy(() =>
  import("@/components/Settings/cloud/billing").then((m) => ({ default: m.BillingSection }))
);

export const FORGE_SETTINGS_SECTIONS = [
  { id: "profile", label: "Profile" },
  { id: "organization", label: "Organization" },
  { id: "members", label: "Members" },
  { id: "tokens", label: "API tokens" },
  { id: "billing", label: "Billing" },
] as const;

export type ForgeSettingsSection = (typeof FORGE_SETTINGS_SECTIONS)[number]["id"];

/** Reliant's slugs that mean one of these sections. */
const ALIASES: Record<string, ForgeSettingsSection> = {
  account: "profile",
  "member-permissions": "members",
};

/** The section a URL slug opens. */
export function forgeSettingsSectionOf(slug: string | undefined): ForgeSettingsSection {
  if (!slug) return "profile";
  const lower = slug.toLowerCase();
  if (FORGE_SETTINGS_SECTIONS.some((s) => s.id === lower)) return lower as ForgeSettingsSection;
  return ALIASES[lower] ?? "profile";
}

export function ForgeSettingsPage() {
  const { section: slug } = useParams({ strict: false }) as { section?: string };
  const section = forgeSettingsSectionOf(slug);

  return (
    <div className="flex min-w-0 flex-col gap-6 md:flex-row" data-testid="forge-settings-page" data-section={section}>
      <nav aria-label="Settings" className="flex shrink-0 gap-1 overflow-x-auto md:w-44 md:flex-col">
        {FORGE_SETTINGS_SECTIONS.map((entry) => (
          <a
            key={entry.id}
            href={`/settings/${entry.id}`}
            aria-current={entry.id === section ? "page" : undefined}
            className={cn(
              "no-color whitespace-nowrap rounded-md px-3 py-1.5 text-sm transition-colors",
              entry.id === section ? "bg-ink/5 font-medium text-ink" : "text-ink-muted hover:bg-ink/5 hover:text-ink"
            )}
          >
            {entry.label}
          </a>
        ))}
      </nav>
      <div className="min-w-0 max-w-4xl flex-1">{renderSection(section)}</div>
    </div>
  );
}

function ActiveOrgCard() {
  const { active, canSwitch, isLoading } = useActiveOrg();
  return (
    <div className="space-y-3 rounded-lg border border-border bg-card p-4" data-testid="forge-settings-org">
      <p className="text-sm">
        {isLoading ? "Loading…" : active ? (
          <>
            Acting in <span className="font-medium">{active.name || active.id}</span>
          </>
        ) : (
          "No organization is active."
        )}
      </p>
      {canSwitch && <OrgSwitcher variant="list" />}
    </div>
  );
}

function renderSection(section: ForgeSettingsSection): ReactNode {
  switch (section) {
    case "profile":
      return <AccountSettings />;
    case "organization":
      return (
        <div className="space-y-4">
          <SettingsPageHeader
            title="Organization"
            description="The organization you act in. Apps, sites, usage and billing are all the active organization's."
          />
          <ActiveOrgCard />
        </div>
      );
    case "members":
      return <MembersPage />;
    case "tokens":
      return <ForgeTokensSection />;
    case "billing":
      return (
        <Suspense fallback={<div className="rounded-lg border border-border bg-card px-4 py-3 text-sm text-muted-foreground">Loading…</div>}>
          <BillingSection />
        </Suspense>
      );
  }
}
