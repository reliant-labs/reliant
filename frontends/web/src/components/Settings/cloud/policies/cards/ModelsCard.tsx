// Copyright (c) 2025 Reliant Labs

/**
 * Models: which managed models the org's calls may use (§4.1). Ids are the
 * gateway's api_model ids, from ListAvailableModels, the same list the server
 * validates against. An empty allow-list would stop every chat, so the last
 * ticked model cannot be unticked.
 */

import { create } from "@bufbuild/protobuf";

import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import { ModelsPolicySchema, OrgPolicyMode, type ModelsPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { useAvailableModels } from "@/hooks/useReliantAIQueries";

import { CheckList, listWords, PolicyCardShell, TagList, type PolicyCardProps } from "../PolicyCard";
import { toggled } from "../policyDocument";

export function modelsSummary(value: ModelsPolicy): string {
  if (value.allowAll) return "Every model the organization's plan includes may be used.";
  const n = value.allowed.length;
  const which = n <= 3 ? listWords(value.allowed) : `${n} models`;
  return `Chats may use only ${which}. A chat on another model pauses at its next message until someone picks an allowed one.`;
}

export function ModelsCard({ value, onChange, editable, org, changed, violations }: PolicyCardProps<ModelsPolicy>) {
  // Read up front for an editor, so turning the rule on starts from every model ticked.
  const models = useAvailableModels(org.id, editable);
  // A stored id the gateway no longer lists stays visible, so it can be removed.
  const ids = [...new Set([...(models.data ?? []), ...(value?.allowed ?? [])])].sort();
  const checked = new Set(value?.allowed ?? []);
  const locked = checked.size === 1 ? checked : undefined;

  return (
    <PolicyCardShell
      section="models"
      enabled={value !== undefined}
      mode={value?.mode}
      editable={editable}
      changed={changed}
      violations={violations}
      offSummary="Any model the organization's plan includes may be used."
      summary={value ? modelsSummary(value) : null}
      onEnable={(on) =>
        onChange(on ? create(ModelsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowAll: false, allowed: models.data ? [...models.data].sort() : [] }) : undefined)
      }
      onModeChange={(mode) => value && onChange({ ...value, mode })}
    >
      {value &&
        (editable ? (
          <>
            <ToggleSwitch
              checked={value.allowAll}
              onChange={(allowAll) => onChange({ ...value, allowAll, allowed: allowAll ? [] : ids })}
              label="Allow every model"
              description="Admits every model the plan includes, now and as new ones are added."
              size="sm"
            />
            {!value.allowAll &&
              (models.isPending ? (
                <p className="text-sm text-muted-foreground" role="status">Loading models…</p>
              ) : (
                <CheckList
                  legend="Allowed models"
                  options={ids.map((id) => ({ id, label: id }))}
                  checked={checked}
                  locked={locked}
                  onToggle={(id, on) => onChange({ ...value, allowed: toggled(value.allowed, id, on) })}
                />
              ))}
          </>
        ) : (
          !value.allowAll && <TagList items={value.allowed} empty="No models are allowed." testId="policy-models-allowed" />
        ))}
    </PolicyCardShell>
  );
}
