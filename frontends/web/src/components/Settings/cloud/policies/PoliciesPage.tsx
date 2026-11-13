// Copyright (c) 2025 Reliant Labs

/**
 * Settings › Policies: the active org's admin policies
 * (control-plane docs/design/org-policies.md §6), beside Members.
 *
 *   Policies  one card per rule: its mode (audit or enforced), what it does
 *             in plain words, and, for an admin or owner, its editor
 *   History   who changed which rule, from what, to what (every member)
 *
 * Members read the rules that bind them; admins and owners edit them. Editing
 * is a draft of the whole document: nothing applies until the admin reviews
 * the preview and saves, and the save carries the version they started from,
 * so a teammate's newer save is never overwritten unseen
 * (PolicyReviewDialog).
 *
 * Keyed by org: a switch remounts the page, so a draft can never be saved
 * into the next org.
 */

import { useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import Tabs from "@/components/forge-ui/tabs";
import type { OrgPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { useActiveOrg } from "@/hooks/useOrgs";
import { formatRelativeTime } from "@/lib/relativeTime";
import { toast } from "@/lib/toast-manager";
import {
  canEditPolicy,
  orgPolicyKeys,
  policyErrorMessage,
  type PolicyRecord,
  type PolicyViolation,
} from "@/services/controlPlane/orgPolicy";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { SettingsPageHeader } from "../../SettingsPageHeader";
import { memberLabel } from "../members/MemberRoster";
import { useMembers } from "../members/useMembers";
import { ApprovalsCard } from "./cards/ApprovalsCard";
import { AutomationsCard } from "./cards/AutomationsCard";
import { IntegrationsCard } from "./cards/IntegrationsCard";
import { MachinesCard } from "./cards/MachinesCard";
import { McpCard } from "./cards/McpCard";
import { ModelsCard } from "./cards/ModelsCard";
import { ProvidersCard } from "./cards/ProvidersCard";
import { SpendCard } from "./cards/SpendCard";
import { listWords } from "./PolicyCard";
import { PolicyHistory } from "./PolicyHistory";
import { PolicyReviewDialog } from "./PolicyReviewDialog";
import { activeSections, changedSections, copyPolicy, emptyPolicy, SECTION_KEYS, SECTION_TITLES, type SectionKey } from "./policyDocument";
import { useOrgPolicy } from "./usePolicies";

const TITLE = "Policies";

export function PoliciesPage() {
  const { active, isLoading } = useActiveOrg();
  if (!active) {
    return (
      <div className="space-y-6">
        <SettingsPageHeader title={TITLE} description="The limits your organization sets on models, spend, integrations and machines." />
        <p className="text-sm text-muted-foreground" role="status">
          {isLoading ? "Loading your organization…" : "You're not in an organization yet."}
        </p>
      </div>
    );
  }
  return <OrgPoliciesPage key={active.id} org={active} />;
}

/** The section a violation's field path names: "spend.member_overrides[u1]" is spend's. */
function sectionOf(v: PolicyViolation): string {
  return v.field.split(/[.[]/)[0] ?? "";
}

function versionLine(record: PolicyRecord): string {
  if (record.version === 0) return "No policies yet";
  const by = record.updatedByEmail ? ` by ${record.updatedByEmail}` : "";
  const when = record.updatedAt ? ` ${formatRelativeTime(record.updatedAt.toISOString())}` : "";
  return `Version ${record.version} · saved${by}${when}`;
}

type TabId = "policies" | "history";

function OrgPoliciesPage({ org }: { org: MyOrg }) {
  const editable = canEditPolicy(org.role);
  const qc = useQueryClient();
  const query = useOrgPolicy(org.id);
  const members = useMembers(org.id);
  const roster = useMemo(() => members.data?.pages.flatMap((p) => p.members) ?? [], [members.data]);
  const nameOf = (userId: string) => {
    const m = roster.find((r) => r.userId === userId);
    return m ? memberLabel(m) : "a former member";
  };

  const [tab, setTab] = useState<TabId>("policies");
  // The version being edited, and the draft of it. Cards edit the draft only.
  const [base, setBase] = useState<PolicyRecord | null>(null);
  const [draft, setDraft] = useState<OrgPolicy>(emptyPolicy);
  const [overlapping, setOverlapping] = useState<SectionKey[]>([]);
  const [violations, setViolations] = useState<PolicyViolation[]>([]);
  const [reviewing, setReviewing] = useState(false);
  // Remounts the cards when the draft is replaced wholesale, so no input keeps stale text.
  const [resetKey, setResetKey] = useState(0);

  const dirtySections = base ? changedSections(base.policy, draft) : [];
  const dirty = dirtySections.length > 0;

  const load = (record: PolicyRecord) => {
    setBase(record);
    setDraft(copyPolicy(record.policy));
    setOverlapping([]);
    setViolations([]);
    setResetKey((k) => k + 1);
  };

  // Follow the server's version while nothing is being edited. A newer save
  // that arrives during an edit is the review's to reconcile, never this.
  const loaded = query.data;
  useEffect(() => {
    if (!loaded) return;
    if (base !== null && (dirty || loaded.version === base.version)) return;
    setBase(loaded);
    setDraft(copyPolicy(loaded.policy));
    setOverlapping([]);
    setViolations([]);
    setResetKey((k) => k + 1);
  }, [loaded, base, dirty]);

  const setSection = <K extends SectionKey>(key: K, value: OrgPolicy[K]) => {
    setDraft((d) => ({ ...d, [key]: value }) as OrgPolicy);
    setViolations((vs) => vs.filter((v) => sectionOf(v) !== key));
  };

  const cardProps = <K extends SectionKey>(key: K) => ({
    value: draft[key],
    onChange: (next: OrgPolicy[K]) => setSection(key, next),
    editable,
    org,
    changed: dirtySections.includes(key),
    violations: violations.filter((v) => sectionOf(v) === key),
  });

  if (query.isPending || !base) {
    if (query.isError) {
      return (
        <div className="space-y-6">
          <SettingsPageHeader title={TITLE} description={`The limits ${org.name} sets on models, spend, integrations and machines.`} />
          <div className="flex items-center gap-3 rounded-lg border border-border bg-card px-4 py-3" role="alert">
            <p className="flex-1 text-sm text-muted-foreground">{policyErrorMessage(query.error, "Could not load this organization's policies.")}</p>
            <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
              Try again
            </Button>
          </div>
        </div>
      );
    }
    return (
      <div className="space-y-6">
        <SettingsPageHeader title={TITLE} description={`The limits ${org.name} sets on models, spend, integrations and machines.`} />
        <p className="text-sm text-muted-foreground" role="status">
          Loading policies…
        </p>
      </div>
    );
  }

  // A member sees the rules that exist; an editor sees every rule, to turn one on.
  const shown = new Set<SectionKey>(editable ? SECTION_KEYS : activeSections(draft));

  return (
    <div className="forge-ui space-y-6" data-testid="policies-page">
      <SettingsPageHeader
        title={TITLE}
        description={
          editable
            ? `The limits on how ${org.name} spends its money and reaches its integrations and machines. They bind everyone, admins and owners included.`
            : `The limits ${org.name}'s admins set on models, spend, integrations and machines. They apply to you too.`
        }
        meta={
          <span className="text-xs text-muted-foreground" data-testid="policies-version">
            {versionLine(base)}
          </span>
        }
      />

      <Tabs
        tabs={[
          { id: "policies", label: "Policies" },
          { id: "history", label: "History" },
        ]}
        activeTab={tab}
        onChange={(id) => setTab(id as TabId)}
        ariaLabel="Policies sections"
      />

      {tab === "policies" && (
        <div className="space-y-4">
          {editable && (
            <p className="text-sm text-muted-foreground">
              Each rule starts in audit mode: it counts what it would block and blocks nothing until you choose Enforce.
            </p>
          )}
          {shown.size === 0 && (
            <div className="rounded-lg border border-border bg-card px-4 py-6 text-center" data-testid="policies-empty">
              <p className="text-sm font-medium text-foreground">{org.name} has no policies</p>
              <p className="mt-1 text-sm text-muted-foreground">Nothing limits which models, accounts, integrations or tools you use here beyond your plan and balance.</p>
            </div>
          )}
          <div key={resetKey} className="space-y-4">
          {shown.has("models") && <ModelsCard {...cardProps("models")} />}
          {shown.has("providers") && <ProvidersCard {...cardProps("providers")} />}
          {shown.has("automations") && <AutomationsCard {...cardProps("automations")} providers={draft.providers} />}
          {shown.has("spend") && <SpendCard {...cardProps("spend")} members={roster} />}
          {shown.has("integrations") && <IntegrationsCard {...cardProps("integrations")} />}
          {shown.has("mcp") && <McpCard {...cardProps("mcp")} />}
          {shown.has("approvals") && <ApprovalsCard {...cardProps("approvals")} />}
          {shown.has("machines") && <MachinesCard {...cardProps("machines")} />}
          </div>

          {editable && dirty && (
            <div
              className="sticky bottom-0 flex flex-wrap items-center gap-3 rounded-lg border border-border bg-popover px-4 py-3 shadow-lg"
              data-testid="policies-savebar"
            >
              <p className="min-w-0 flex-1 text-sm text-foreground">Unsaved changes to {listWords(dirtySections.map((k) => SECTION_TITLES[k]))}.</p>
              <Button variant="ghost" size="sm" onClick={() => load(base)}>
                Discard
              </Button>
              <Button variant="primary" size="sm" onClick={() => setReviewing(true)} data-testid="policies-review">
                Review and save
              </Button>
            </div>
          )}
        </div>
      )}

      {tab === "history" && <PolicyHistory orgId={org.id} nameOf={nameOf} />}

      {editable && (
        <PolicyReviewDialog
          open={reviewing}
          orgId={org.id}
          base={base}
          draft={draft}
          overlapping={overlapping}
          nameOf={nameOf}
          onClose={() => setReviewing(false)}
          onViolations={setViolations}
          onSaved={(record, changed) => {
            qc.setQueryData(orgPolicyKeys.policy(org.id), record);
            void qc.invalidateQueries({ queryKey: orgPolicyKeys.history(org.id) });
            load(record);
            setReviewing(false);
            toast.success(changed ? `Policies saved as version ${record.version}.` : "Nothing changed: these were the saved policies.");
          }}
          onRebased={(latest, policy, both) => {
            qc.setQueryData(orgPolicyKeys.policy(org.id), latest);
            setBase(latest);
            setDraft(policy);
            setOverlapping(both);
            setResetKey((k) => k + 1);
          }}
          onDiscardToLatest={(latest) => {
            qc.setQueryData(orgPolicyKeys.policy(org.id), latest);
            load(latest);
            setReviewing(false);
          }}
        />
      )}
    </div>
  );
}
