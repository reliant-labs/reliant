// Copyright (c) 2025 Reliant Labs

/**
 * Spend limits: LLM spend through the Reliant gateway, per UTC calendar day
 * or month (§4.7): an organization limit, a limit per member, and exceptions
 * for named members. Admins and owners are bound too; an exception is how an
 * admin exempts someone (D4). A hosted app's calls count toward the
 * organization limit only when the admin says so (D6).
 *
 * The member's own usage against these limits comes with the gateway's
 * counters (GetMyPolicyUsage); until this deployment has them the card says
 * so instead of guessing.
 */

import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Trash2 } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import {
  MemberSpendCapSchema,
  OrgPolicyMode,
  SpendAppliesTo,
  SpendCapSchema,
  SpendPeriod,
  SpendPolicySchema,
  type MemberSpendCap,
  type SpendCap,
  type SpendPolicy,
} from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import type { OrgMember } from "@/services/controlPlane/members";

import { memberLabel } from "../../members/MemberRoster";
import { PolicyCardShell, selectClass, type PolicyCardProps } from "../PolicyCard";
import { formatUsdNanos, parseUsd, PERIOD_LABELS, usdInputValue } from "../policyDocument";

const DEFAULT_CAP = () => create(SpendCapSchema, { usdNanos: 100_000_000_000n, period: SpendPeriod.MONTH });

/** "$50 a month". */
export function capWords(cap: SpendCap): string {
  return `${formatUsdNanos(cap.usdNanos)} a ${PERIOD_LABELS[cap.period]}`;
}

export function spendSummary(value: SpendPolicy): string {
  const parts: string[] = [];
  if (value.orgCap) parts.push(`the organization spends at most ${capWords(value.orgCap)}`);
  if (value.memberDefaultCap) parts.push(`each member at most ${capWords(value.memberDefaultCap)}`);
  const n = Object.keys(value.memberOverrides).length;
  if (n > 0) parts.push(`${n} ${n === 1 ? "member has" : "members have"} their own limit`);
  if (parts.length === 0) return "No limit is set yet, so this rule limits nothing.";
  const sentence = parts.join("; ");
  return `On Reliant credits, ${sentence}. A call over a limit is refused and the chat pauses until the limit is raised or the period ends.`;
}

/** An amount in dollars, kept as typed until it is a valid one. */
function UsdInput({ nanos, onChange, label }: { nanos: bigint; onChange: (nanos: bigint) => void; label: string }) {
  const [text, setText] = useState(usdInputValue(nanos));
  const invalid = parseUsd(text) === null;
  return (
    <span className="inline-flex items-center gap-1 text-sm text-foreground">
      $
      <span className="w-28">
        <Input
          aria-label={label}
          inputMode="decimal"
          inputSize="sm"
          value={text}
          invalid={invalid}
          onChange={(e) => {
            setText(e.target.value);
            const parsed = parseUsd(e.target.value);
            if (parsed !== null) onChange(parsed);
          }}
        />
      </span>
    </span>
  );
}

function CapEditor({ cap, onChange, label }: { cap: SpendCap; onChange: (cap: SpendCap) => void; label: string }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <UsdInput nanos={cap.usdNanos} label={`${label} amount`} onChange={(usdNanos) => onChange({ ...cap, usdNanos })} />
      <select
        aria-label={`${label} period`}
        className={selectClass}
        value={cap.period === SpendPeriod.DAY ? "day" : "month"}
        onChange={(e) => onChange({ ...cap, period: e.target.value === "day" ? SpendPeriod.DAY : SpendPeriod.MONTH })}
      >
        <option value="day">a day</option>
        <option value="month">a month</option>
      </select>
    </span>
  );
}

/** Your own spend against the limits: not available until the gateway counts it per member (G2). */
export function MyPolicyUsage() {
  return (
    <p className="text-sm text-muted-foreground" data-testid="policy-my-usage">
      Your usage against these limits: not available yet.
    </p>
  );
}

function overrideWords(o: MemberSpendCap): string {
  if (o.cap.case === "unlimited") return "No limit";
  if (o.cap.case === "limit") return capWords(o.cap.value);
  return "The default limit";
}

export function SpendCard({ value, onChange, editable, changed, violations, members }: PolicyCardProps<SpendPolicy> & { members: OrgMember[] }) {
  const [adding, setAdding] = useState("");
  const byId = new Map(members.map((m) => [m.userId, memberLabel(m)]));
  const nameOf = (id: string) => byId.get(id) ?? "A former member";
  const overrides = Object.entries(value?.memberOverrides ?? {}).sort(([a], [b]) => nameOf(a).localeCompare(nameOf(b)));

  const setOverride = (userId: string, next: MemberSpendCap | undefined) => {
    if (!value) return;
    const memberOverrides = { ...value.memberOverrides };
    if (next) memberOverrides[userId] = next;
    else delete memberOverrides[userId];
    onChange({ ...value, memberOverrides });
  };

  const hasCap = !!value && (!!value.orgCap || !!value.memberDefaultCap || overrides.length > 0);

  return (
    <PolicyCardShell
      section="spend"
      enabled={value !== undefined}
      mode={value?.mode}
      editable={editable}
      changed={changed}
      violations={violations}
      offSummary="No spend limit beyond the organization's balance."
      summary={value ? spendSummary(value) : null}
      onEnable={(on) =>
        onChange(on ? create(SpendPolicySchema, { mode: OrgPolicyMode.AUDIT, appliesTo: SpendAppliesTo.MEMBERS, memberDefaultCap: DEFAULT_CAP() }) : undefined)
      }
      onModeChange={(mode) => value && onChange({ ...value, mode })}
    >
      {value && editable && (
        <>
          <div className="space-y-2">
            <ToggleSwitch
              checked={!!value.orgCap}
              onChange={(on) => onChange({ ...value, orgCap: on ? DEFAULT_CAP() : undefined })}
              label="Limit the organization's total"
              size="sm"
            />
            {value.orgCap && <CapEditor cap={value.orgCap} label="Organization limit" onChange={(orgCap) => onChange({ ...value, orgCap })} />}
          </div>
          <div className="space-y-2">
            <ToggleSwitch
              checked={!!value.memberDefaultCap}
              onChange={(on) => onChange({ ...value, memberDefaultCap: on ? DEFAULT_CAP() : undefined })}
              label="Limit each member"
              description="Admins and owners included. Give someone an exception below to change theirs."
              size="sm"
            />
            {value.memberDefaultCap && (
              <CapEditor cap={value.memberDefaultCap} label="Limit per member" onChange={(memberDefaultCap) => onChange({ ...value, memberDefaultCap })} />
            )}
          </div>

          <fieldset className="space-y-2">
            <legend className="mb-1 text-xs font-medium text-muted-foreground">Exceptions</legend>
            {overrides.length === 0 && <p className="text-sm text-muted-foreground">No member has their own limit.</p>}
            <ul className="space-y-2">
              {overrides.map(([userId, o]) => (
                <li key={userId} className="flex flex-wrap items-center gap-2 text-sm text-foreground" data-testid={`policy-override-${userId}`}>
                  <span className="min-w-0 flex-1 truncate">{nameOf(userId)}</span>
                  <select
                    aria-label={`Limit for ${nameOf(userId)}`}
                    className={selectClass}
                    value={o.cap.case === "unlimited" ? "unlimited" : "limit"}
                    onChange={(e) =>
                      setOverride(
                        userId,
                        e.target.value === "unlimited"
                          ? create(MemberSpendCapSchema, { cap: { case: "unlimited", value: true } })
                          : create(MemberSpendCapSchema, { cap: { case: "limit", value: DEFAULT_CAP() } }),
                      )
                    }
                  >
                    <option value="limit">Their own limit</option>
                    <option value="unlimited">No limit</option>
                  </select>
                  {o.cap.case === "limit" && (
                    <CapEditor
                      cap={o.cap.value}
                      label={`Limit for ${nameOf(userId)}`}
                      onChange={(cap) => setOverride(userId, create(MemberSpendCapSchema, { cap: { case: "limit", value: cap } }))}
                    />
                  )}
                  <Button
                    variant="ghost"
                    size="sm"
                    icon={<Trash2 className="h-4 w-4" aria-hidden="true" />}
                    aria-label={`Remove the exception for ${nameOf(userId)}`}
                    onClick={() => setOverride(userId, undefined)}
                  />
                </li>
              ))}
            </ul>
            <div className="flex flex-wrap items-center gap-2">
              <select aria-label="Member to give an exception" className={selectClass} value={adding} onChange={(e) => setAdding(e.target.value)}>
                <option value="">Choose a member…</option>
                {members
                  .filter((m) => !(m.userId in value.memberOverrides))
                  .map((m) => (
                    <option key={m.userId} value={m.userId}>
                      {memberLabel(m)}
                    </option>
                  ))}
              </select>
              <Button
                variant="outline"
                size="sm"
                disabled={!adding}
                onClick={() => {
                  setOverride(adding, create(MemberSpendCapSchema, { cap: { case: "unlimited", value: true } }));
                  setAdding("");
                }}
              >
                Add exception
              </Button>
            </div>
          </fieldset>

          <ToggleSwitch
            checked={value.appliesTo === SpendAppliesTo.MEMBERS_AND_APPS}
            onChange={(on) => onChange({ ...value, appliesTo: on ? SpendAppliesTo.MEMBERS_AND_APPS : SpendAppliesTo.MEMBERS })}
            label="Count hosted apps toward the organization limit"
            description="Off: an app you deploy is never throttled by this limit. Its calls still count toward the balance."
            size="sm"
          />
        </>
      )}
      {value && !editable && overrides.length > 0 && (
        <ul className="space-y-1 text-sm text-foreground" data-testid="policy-spend-overrides">
          {overrides.map(([userId, o]) => (
            <li key={userId}>
              {nameOf(userId)}: {overrideWords(o)}
            </li>
          ))}
        </ul>
      )}
      {hasCap && <MyPolicyUsage />}
    </PolicyCardShell>
  );
}
