// Copyright (c) 2025 Reliant Labs

/**
 * Integrations: which integrations the org's runs may use (§4.3). It governs
 * USE in this org's runs, never connecting: a person's own GitHub connection
 * stays theirs and usable in their other orgs. Ids are the catalog's
 * (ListIntegrations). An empty list without "allow every integration" admits
 * none, which is a valid rule.
 */

import { create } from "@bufbuild/protobuf";

import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import { IntegrationsPolicySchema, OrgPolicyMode, type IntegrationsPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { useIntegrationNames } from "@/hooks/connection-queries";

import { CheckList, listWords, PolicyCardShell, TagList, type PolicyCardProps } from "../PolicyCard";
import { toggled } from "../policyDocument";

export function integrationsSummary(value: IntegrationsPolicy, orgName: string, nameOf: (id: string) => string): string {
  if (value.allowAll) return `${orgName}'s runs may use every integration.`;
  if (value.allowed.length === 0) return `${orgName}'s runs may use no integration. Connecting one is still allowed; using it in this organization is not.`;
  return `${orgName}'s runs may use only ${listWords(value.allowed.map(nameOf))}. A step using another one stops and says why; connecting one is still allowed.`;
}

export function IntegrationsCard({ value, onChange, editable, org, changed, violations }: PolicyCardProps<IntegrationsPolicy>) {
  const names = useIntegrationNames();
  const nameOf = (id: string) => names.get(id) ?? id;
  const ids = [...new Set([...names.keys(), ...(value?.allowed ?? [])])].sort();

  return (
    <PolicyCardShell
      section="integrations"
      enabled={value !== undefined}
      mode={value?.mode}
      editable={editable}
      changed={changed}
      violations={violations}
      offSummary={`${org.name}'s runs may use every integration.`}
      summary={value ? integrationsSummary(value, org.name, nameOf) : null}
      onEnable={(on) => onChange(on ? create(IntegrationsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowAll: false, allowed: [...names.keys()].sort() }) : undefined)}
      onModeChange={(mode) => value && onChange({ ...value, mode })}
    >
      {value &&
        (editable ? (
          <>
            <ToggleSwitch
              checked={value.allowAll}
              onChange={(allowAll) => onChange({ ...value, allowAll, allowed: allowAll ? [] : ids })}
              label="Allow every integration"
              size="sm"
            />
            {!value.allowAll && (
              <CheckList
                legend="Allowed integrations"
                options={ids.map((id) => ({ id, label: nameOf(id) }))}
                checked={new Set(value.allowed)}
                onToggle={(id, on) => onChange({ ...value, allowed: toggled(value.allowed, id, on) })}
              />
            )}
          </>
        ) : (
          !value.allowAll && <TagList items={value.allowed.map(nameOf)} empty="No integrations." testId="policy-integrations-allowed" />
        ))}
    </PolicyCardShell>
  );
}
