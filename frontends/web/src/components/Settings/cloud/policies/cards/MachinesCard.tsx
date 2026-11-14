// Copyright (c) 2025 Reliant Labs

/**
 * Machine credentials: run every command of the org's runs with
 * `credentials: none` (§4.6). Honest about the gap (D11): on a machine running
 * an older version only the environment half applies; hiding credential
 * files needs a newer one.
 */

import { create } from "@bufbuild/protobuf";

import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import { MachinesPolicySchema, OrgPolicyMode, type MachinesPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { PolicyCardShell, type PolicyCardProps } from "../PolicyCard";

export function machinesSummary(value: MachinesPolicy, orgName: string): string {
  return value.forceCredentialsNone
    ? `Every command ${orgName}'s runs send to a machine runs without credentials: tokens, the git credential helper, the SSH agent and cloud variables are removed, whatever a step or agent asked for.`
    : "Commands run with whatever credentials each step or agent asks for.";
}

export function MachinesCard({ value, onChange, editable, org, changed, violations }: PolicyCardProps<MachinesPolicy>) {
  return (
    <PolicyCardShell
      section="machines"
      enabled={value !== undefined}
      mode={value?.mode}
      editable={editable}
      changed={changed}
      violations={violations}
      offSummary="Commands run with whatever credentials each step or agent asks for."
      summary={value ? machinesSummary(value, org.name) : null}
      onEnable={(on) => onChange(on ? create(MachinesPolicySchema, { mode: OrgPolicyMode.AUDIT, forceCredentialsNone: true }) : undefined)}
      onModeChange={(mode) => value && onChange({ ...value, mode })}
    >
      {value && (
        <>
          {editable && (
            <ToggleSwitch
              checked={value.forceCredentialsNone}
              onChange={(forceCredentialsNone) => onChange({ ...value, forceCredentialsNone })}
              label="Run the organization's commands without credentials"
              size="sm"
            />
          )}
          <p className="text-xs text-muted-foreground">
            A machine on an older version gets the environment half only: credential files such as ~/.git-credentials and ~/.ssh stay readable until it updates. People typing in a terminal are not affected.
          </p>
        </>
      )}
    </PolicyCardShell>
  );
}
