// Copyright (c) 2025 Reliant Labs

/**
 * Approvals: a floor under the two approval controls that exist (§4.4). It
 * only ever makes approvals more frequent, and approvals stay the machine
 * owner's to decide.
 */

import { create } from "@bufbuild/protobuf";

import {
  ApprovalsPolicySchema,
  AutomationApprovalFloor,
  ChatModeFloor,
  OrgPolicyMode,
  type ApprovalsPolicy,
} from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { PolicyCardShell, selectClass, type PolicyCardProps } from "../PolicyCard";
import { AUTOMATION_FLOOR_LABELS, CHAT_MODE_FLOOR_LABELS } from "../policyDocument";

export function approvalsSummary(value: ApprovalsPolicy): string {
  const parts: string[] = [];
  if (value.automationFloor === AutomationApprovalFloor.ALWAYS) parts.push("every agent change to an automation needs its owner's approval");
  if (value.automationFloor === AutomationApprovalFloor.ON_CAPABILITY_CHANGE) parts.push("an agent change that widens what an automation can do needs its owner's approval");
  if (value.chatModeFloor === ChatModeFloor.NO_AUTO) parts.push("chats can't run in auto mode, so every tool call asks first (plan mode is unaffected)");
  if (parts.length === 0) return "No floor is set yet, so this rule changes nothing.";
  const s = parts.join("; ");
  return `${s.charAt(0).toUpperCase()}${s.slice(1)}. A stricter personal setting still applies.`;
}

const FLOORS = [AutomationApprovalFloor.UNSPECIFIED, AutomationApprovalFloor.ON_CAPABILITY_CHANGE, AutomationApprovalFloor.ALWAYS];
const MODES = [ChatModeFloor.ANY, ChatModeFloor.NO_AUTO];

export function ApprovalsCard({ value, onChange, editable, changed, violations }: PolicyCardProps<ApprovalsPolicy>) {
  return (
    <PolicyCardShell
      section="approvals"
      enabled={value !== undefined}
      mode={value?.mode}
      editable={editable}
      changed={changed}
      violations={violations}
      offSummary="Each person's own approval settings apply."
      summary={value ? approvalsSummary(value) : null}
      onEnable={(on) =>
        onChange(
          on
            ? create(ApprovalsPolicySchema, { mode: OrgPolicyMode.AUDIT, automationFloor: AutomationApprovalFloor.ON_CAPABILITY_CHANGE, chatModeFloor: ChatModeFloor.ANY })
            : undefined,
        )
      }
      onModeChange={(mode) => value && onChange({ ...value, mode })}
    >
      {value && editable && (
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="flex flex-col gap-1 text-xs font-medium text-muted-foreground">
            Automation changes need approval
            <select
              className={selectClass}
              value={value.automationFloor}
              onChange={(e) => onChange({ ...value, automationFloor: Number(e.target.value) as AutomationApprovalFloor })}
            >
              {FLOORS.map((f) => (
                <option key={f} value={f}>
                  {AUTOMATION_FLOOR_LABELS[f]}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-xs font-medium text-muted-foreground">
            Chat modes
            <select
              className={selectClass}
              value={value.chatModeFloor === ChatModeFloor.NO_AUTO ? ChatModeFloor.NO_AUTO : ChatModeFloor.ANY}
              onChange={(e) => onChange({ ...value, chatModeFloor: Number(e.target.value) as ChatModeFloor })}
            >
              {MODES.map((m) => (
                <option key={m} value={m}>
                  {CHAT_MODE_FLOOR_LABELS[m]}
                </option>
              ))}
            </select>
          </label>
        </div>
      )}
    </PolicyCardShell>
  );
}
