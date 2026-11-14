// Copyright (c) 2025 Reliant Labs

/**
 * Automations: the kinds of account runs with nobody present (trigger runs
 * and their agents) may spend through (org-provider-accounts.md §11.6). Only
 * sources the AI providers rule admits for chats are offered: an automation
 * may use only what a chat may, and the server refuses anything else.
 */

import { create } from "@bufbuild/protobuf";

import {
  AutomationsPolicySchema,
  OrgPolicyMode,
  ProviderSource,
  type AutomationsPolicy,
  type ProvidersPolicy,
} from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { CheckList, listWords, PolicyCardShell, TagList, type PolicyCardProps } from "../PolicyCard";
import { PROVIDER_SOURCES, sourceLabel, toggled } from "../policyDocument";

/** The sources the org pays for: what an unattended run defaults to (§11). */
const ORG_PAID: readonly ProviderSource[] = [ProviderSource.ORG_ACCOUNTS, ProviderSource.ORG_SEATS, ProviderSource.RELIANT];

/** The sources the providers rule lets chats use: all when it names none. */
export function admittedSources(providers: ProvidersPolicy | undefined): ProviderSource[] {
  const attended = providers?.allowedSources ?? [];
  return attended.length > 0 ? [...attended] : [...PROVIDER_SOURCES];
}

export function automationsSummary(value: AutomationsPolicy, orgName: string): string {
  if (value.allowedSources.length === 0) return "Automations may use whatever chats may use.";
  return `Automations may use: ${listWords(value.allowedSources.map((s) => sourceLabel(s, orgName)))}. An automation with no such account fails to launch and says why.`;
}

export function AutomationsCard({
  value,
  onChange,
  editable,
  org,
  changed,
  violations,
  providers,
}: PolicyCardProps<AutomationsPolicy> & { providers: ProvidersPolicy | undefined }) {
  const admitted = admittedSources(providers);
  // A stored source the providers rule no longer admits stays listed, so it can be removed.
  const options = [...new Set([...admitted, ...(value?.allowedSources ?? [])])].sort((a, b) => a - b);
  const checked = new Set((value?.allowedSources ?? []).map(String));

  return (
    <PolicyCardShell
      section="automations"
      enabled={value !== undefined}
      mode={value?.mode}
      editable={editable}
      changed={changed}
      violations={violations}
      offSummary="Automations may use whatever chats may use."
      summary={value ? automationsSummary(value, org.name) : null}
      onEnable={(on) => {
        const orgPaid = admitted.filter((s) => ORG_PAID.includes(s));
        onChange(on ? create(AutomationsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowedSources: (orgPaid.length > 0 ? orgPaid : admitted).sort((a, b) => a - b) }) : undefined);
      }}
      onModeChange={(mode) => value && onChange({ ...value, mode })}
    >
      {value &&
        (editable ? (
          <CheckList
            legend="Automations may use"
            options={options.map((s) => ({
              id: String(s),
              label: sourceLabel(s, org.name),
              hint: admitted.includes(s) ? undefined : "Not allowed for chats: remove it, or allow it under AI providers.",
            }))}
            checked={checked}
            locked={checked.size === 1 ? checked : undefined}
            onToggle={(id, on) => onChange({ ...value, allowedSources: toggled(value.allowedSources, Number(id) as ProviderSource, on) })}
          />
        ) : (
          <TagList items={value.allowedSources.map((s) => sourceLabel(s, org.name))} empty="Whatever chats may use." testId="policy-automations-sources" />
        ))}
    </PolicyCardShell>
  );
}
