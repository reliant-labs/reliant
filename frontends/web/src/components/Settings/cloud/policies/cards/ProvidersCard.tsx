// Copyright (c) 2025 Reliant Labs

/**
 * AI providers: which drivers, and which kinds of account by who pays, may
 * serve the org's chats (§4.2; org-provider-accounts.md §9). An empty list
 * admits everything of its kind, so each list is "Any" or "Only these", and
 * "Only these" keeps at least one ticked.
 */

import { create } from "@bufbuild/protobuf";

import { OrgPolicyMode, ProvidersPolicySchema, type ProviderSource, type ProvidersPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { CheckList, listWords, PolicyCardShell, TagList, selectClass, type PolicyCardProps } from "../PolicyCard";
import { driverLabel, PROVIDER_DRIVERS, PROVIDER_SOURCES, sourceLabel, toggled } from "../policyDocument";

export function providersSummary(value: ProvidersPolicy, orgName: string): string {
  const drivers = value.allowed.length === 0 ? "any provider" : listWords(value.allowed.map(driverLabel));
  const sources = value.allowedSources.length === 0 ? "" : ` paid through ${listWords(value.allowedSources.map((s) => sourceLabel(s, orgName)))}`;
  if (value.allowed.length === 0 && value.allowedSources.length === 0) return "Chats may use any provider and any kind of account.";
  return `Chats may run on ${drivers}${sources}. Anything else is skipped when a chat picks an account, failover included.`;
}

/** "Any" vs "Only these", for a list where empty means everything. */
function ScopeSelect({ label, any, only, onChange }: { label: string; any: boolean; only: string; onChange: (any: boolean) => void }) {
  return (
    <label className="flex flex-wrap items-center gap-2 text-sm text-foreground">
      {label}
      <select aria-label={label} className={selectClass} value={any ? "any" : "only"} onChange={(e) => onChange(e.target.value === "any")}>
        <option value="any">Any</option>
        <option value="only">{only}</option>
      </select>
    </label>
  );
}

export function ProvidersCard({ value, onChange, editable, org, changed, violations }: PolicyCardProps<ProvidersPolicy>) {
  const allDrivers = PROVIDER_DRIVERS.map((d) => d.id).sort();

  return (
    <PolicyCardShell
      section="providers"
      enabled={value !== undefined}
      mode={value?.mode}
      editable={editable}
      changed={changed}
      violations={violations}
      offSummary="Chats may use any provider and any kind of account."
      summary={value ? providersSummary(value, org.name) : null}
      onEnable={(on) => onChange(on ? create(ProvidersPolicySchema, { mode: OrgPolicyMode.AUDIT }) : undefined)}
      onModeChange={(mode) => value && onChange({ ...value, mode })}
    >
      {value &&
        (editable ? (
          <>
            <ScopeSelect
              label="Providers"
              any={value.allowed.length === 0}
              only="Only these providers"
              onChange={(any) => onChange({ ...value, allowed: any ? [] : allDrivers })}
            />
            {value.allowed.length > 0 && (
              <CheckList
                legend="Allowed providers"
                options={PROVIDER_DRIVERS.map((d) => ({ id: d.id, label: d.label }))}
                checked={new Set(value.allowed)}
                locked={value.allowed.length === 1 ? new Set(value.allowed) : undefined}
                onToggle={(id, on) => onChange({ ...value, allowed: toggled(value.allowed, id, on) })}
              />
            )}
            <ScopeSelect
              label="Accounts"
              any={value.allowedSources.length === 0}
              only="Only these kinds of account"
              onChange={(any) => onChange({ ...value, allowedSources: any ? [] : [...PROVIDER_SOURCES].sort((a, b) => a - b) })}
            />
            {value.allowedSources.length > 0 && (
              <CheckList
                legend="Allowed kinds of account"
                options={PROVIDER_SOURCES.map((s) => ({ id: String(s), label: sourceLabel(s, org.name) }))}
                checked={new Set(value.allowedSources.map(String))}
                locked={value.allowedSources.length === 1 ? new Set(value.allowedSources.map(String)) : undefined}
                onToggle={(id, on) =>
                  onChange({ ...value, allowedSources: toggled(value.allowedSources, Number(id) as ProviderSource, on) })
                }
              />
            )}
          </>
        ) : (
          <>
            <TagList items={value.allowed.map(driverLabel)} empty="Any provider." testId="policy-providers-allowed" />
            <TagList items={value.allowedSources.map((s) => sourceLabel(s, org.name))} empty="Any kind of account." testId="policy-providers-sources" />
          </>
        ))}
    </PolicyCardShell>
  );
}
