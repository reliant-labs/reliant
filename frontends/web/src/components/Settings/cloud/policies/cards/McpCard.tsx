// Copyright (c) 2025 Reliant Labs

/**
 * Tools (MCP): which MCP servers' tools the org's agents may see and call
 * (§4.5). Servers are configured on each machine, so there is no catalog to
 * pick from: an admin names them, as in `mcp__<server>__<tool>`. What it does
 * not do is said plainly: an older machine still starts a server it was
 * configured with; only its tools are kept away from the org's agents.
 */

import { useState } from "react";
import { create } from "@bufbuild/protobuf";

import Badge from "@/components/forge-ui/badge";
import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import { McpPolicySchema, OrgPolicyMode, type McpPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { listWords, PolicyCardShell, TagList, type PolicyCardProps } from "../PolicyCard";
import { toggled } from "../policyDocument";

/** The <server> of mcp__<server>__<tool> (control-plane's rule): no "__", no spaces. */
export function isMcpServerName(name: string): boolean {
  return /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name) && !name.includes("__");
}

export function mcpSummary(value: McpPolicy): string {
  if (value.allowAll) return "Agents may use the tools of every MCP server.";
  if (value.allowedServers.length === 0) return "Agents may use no MCP server's tools.";
  return `Agents may use the tools of ${listWords(value.allowedServers)} only. Other servers' tools are hidden from them and refused if called.`;
}

export function McpCard({ value, onChange, editable, changed, violations }: PolicyCardProps<McpPolicy>) {
  const [name, setName] = useState("");
  const trimmed = name.trim();
  const valid = trimmed !== "" && isMcpServerName(trimmed);

  const add = () => {
    if (!value || !valid) return;
    onChange({ ...value, allowedServers: toggled(value.allowedServers, trimmed, true) });
    setName("");
  };

  return (
    <PolicyCardShell
      section="mcp"
      enabled={value !== undefined}
      mode={value?.mode}
      editable={editable}
      changed={changed}
      violations={violations}
      offSummary="Agents may use the tools of every MCP server on the machine they run on."
      summary={value ? mcpSummary(value) : null}
      onEnable={(on) => onChange(on ? create(McpPolicySchema, { mode: OrgPolicyMode.AUDIT, allowAll: false, allowedServers: [] }) : undefined)}
      onModeChange={(mode) => value && onChange({ ...value, mode })}
    >
      {value && (
        <>
          {editable ? (
            <>
              <ToggleSwitch
                checked={value.allowAll}
                onChange={(allowAll) => onChange({ ...value, allowAll, allowedServers: allowAll ? [] : value.allowedServers })}
                label="Allow every MCP server"
                size="sm"
              />
              {!value.allowAll && (
                <div className="space-y-2">
                  {value.allowedServers.length === 0 ? (
                    <p className="text-sm text-muted-foreground">No server is allowed yet.</p>
                  ) : (
                    <div className="flex flex-wrap gap-1.5" data-testid="policy-mcp-servers">
                      {value.allowedServers.map((s) => (
                        <Badge key={s} label={s} size="sm" removable onRemove={() => onChange({ ...value, allowedServers: toggled(value.allowedServers, s, false) })} />
                      ))}
                    </div>
                  )}
                  <form
                    className="flex flex-wrap items-center gap-2"
                    onSubmit={(e) => {
                      e.preventDefault();
                      add();
                    }}
                  >
                    <div className="w-56">
                      <Input aria-label="MCP server name" inputSize="sm" placeholder="e.g. linear" value={name} invalid={trimmed !== "" && !valid} onChange={(e) => setName(e.target.value)} />
                    </div>
                    <Button type="submit" variant="outline" size="sm" disabled={!valid}>
                      Allow server
                    </Button>
                  </form>
                </div>
              )}
            </>
          ) : (
            !value.allowAll && <TagList items={value.allowedServers} empty="No MCP servers." testId="policy-mcp-allowed" />
          )}
          <p className="text-xs text-muted-foreground">
            A machine on an older version still starts the servers it is configured with; its tools are kept from this organization's agents either way.
          </p>
        </>
      )}
    </PolicyCardShell>
  );
}
