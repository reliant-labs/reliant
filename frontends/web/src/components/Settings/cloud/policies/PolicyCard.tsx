// Copyright (c) 2025 Reliant Labs

/**
 * The shell every policy card shares: its title, its mode badge, the
 * plain-language statement of what it does, where it is applied, and, for an
 * admin, the switch that adds or removes the rule and its mode. A card fills
 * in only its own value.
 *
 * Every rule starts in AUDIT: turning a section on never blocks anything until
 * an admin chooses Enforce.
 */

import { Children, type ReactNode } from "react";

import Badge from "@/components/forge-ui/badge";
import Tag from "@/components/forge-ui/tag";
import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import { OrgPolicyMode } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import type { PolicyViolation } from "@/services/controlPlane/orgPolicy";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { isEnforced, SECTION_ENFORCED_BY, SECTION_TITLES, type SectionKey } from "./policyDocument";

/** What every card receives: its section of the draft, and how to change it. */
export interface PolicyCardProps<T> {
  value: T | undefined;
  onChange: (next: T | undefined) => void;
  editable: boolean;
  org: MyOrg;
  /** The draft's section differs from the saved one. */
  changed: boolean;
  /** The last preview's refusals for this section. */
  violations: PolicyViolation[];
}

export const selectClass =
  "rounded-md border border-border bg-background px-2 py-1 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring disabled:opacity-60";

/** "Enforced" or "Audit only": the one thing an admin must see at a glance. */
export function ModeBadge({ mode }: { mode: OrgPolicyMode | undefined }) {
  return isEnforced(mode) ? <Badge label="Enforced" size="sm" /> : <Badge label="Audit only" variant="info" size="sm" />;
}

interface ShellProps {
  section: SectionKey;
  /** The section is set in the draft. */
  enabled: boolean;
  mode: OrgPolicyMode | undefined;
  editable: boolean;
  changed: boolean;
  violations: PolicyViolation[];
  /** Adds the section (with its defaults, in audit) or removes it. */
  onEnable: (on: boolean) => void;
  onModeChange: (mode: OrgPolicyMode) => void;
  /** What the rule does, in a sentence or two. */
  summary: ReactNode;
  /** The editor or the read-only value. Shown only while the section is on. */
  children?: ReactNode;
  /** What the card says while the section is off. */
  offSummary: string;
}

export function PolicyCardShell({ section, enabled, mode, editable, changed, violations, onEnable, onModeChange, summary, children, offSummary }: ShellProps) {
  const title = SECTION_TITLES[section];
  const headingId = `policy-${section}-heading`;
  return (
    <section className="rounded-lg border border-border bg-card" aria-labelledby={headingId} data-testid={`policy-card-${section}`}>
      <header className="flex flex-wrap items-start gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0 flex-1">
          <h3 id={headingId} className="flex flex-wrap items-center gap-2 text-sm font-semibold text-foreground">
            {title}
            {enabled ? <ModeBadge mode={mode} /> : <span className="text-xs font-normal text-muted-foreground">Off</span>}
            {changed && <Tag tone="warning">Unsaved</Tag>}
          </h3>
          <p className="mt-1 text-sm text-muted-foreground" data-testid={`policy-summary-${section}`}>
            {enabled ? summary : offSummary}
          </p>
          {enabled && !isEnforced(mode) && (
            <p className="mt-1 text-xs text-muted-foreground">Audit mode: nothing is blocked yet. What this rule would block is counted.</p>
          )}
        </div>
        {editable && (
          <div className="flex items-center gap-3">
            {enabled && (
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                Mode
                <select
                  aria-label={`${title} mode`}
                  className={selectClass}
                  value={isEnforced(mode) ? "enforce" : "audit"}
                  onChange={(e) => onModeChange(e.target.value === "enforce" ? OrgPolicyMode.ENFORCE : OrgPolicyMode.AUDIT)}
                >
                  <option value="audit">Audit only</option>
                  <option value="enforce">Enforce</option>
                </select>
              </label>
            )}
            <ToggleSwitch checked={enabled} onChange={onEnable} ariaLabel={`${title} policy`} size="sm" />
          </div>
        )}
      </header>

      {violations.length > 0 && (
        <ul role="alert" className="space-y-1 border-b border-border px-4 py-3 text-sm text-destructive-ink" data-testid={`policy-violations-${section}`}>
          {violations.map((v, i) => (
            <li key={`${v.field}-${i}`}>{v.message}</li>
          ))}
        </ul>
      )}

      {enabled && Children.toArray(children).length > 0 && <div className="space-y-4 px-4 py-3">{children}</div>}

      <p className="border-t border-border px-4 py-2 text-xs text-muted-foreground">{SECTION_ENFORCED_BY[section]}</p>
    </section>
  );
}

/** A labelled group of checkboxes, one per option. `locked` options cannot be unticked (the last one of a list that may not be empty). */
export function CheckList({
  legend,
  options,
  checked,
  onToggle,
  locked,
}: {
  legend: string;
  options: ReadonlyArray<{ id: string; label: string; hint?: string }>;
  checked: ReadonlySet<string>;
  onToggle: (id: string, on: boolean) => void;
  locked?: ReadonlySet<string>;
}) {
  return (
    <fieldset className="space-y-1.5">
      <legend className="mb-1 text-xs font-medium text-muted-foreground">{legend}</legend>
      <div className="grid gap-1.5 sm:grid-cols-2">
        {options.map((o) => (
          <label key={o.id} className="flex items-start gap-2 text-sm text-foreground">
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 accent-primary"
              checked={checked.has(o.id)}
              disabled={locked?.has(o.id)}
              onChange={(e) => onToggle(o.id, e.target.checked)}
            />
            <span className="min-w-0">
              {o.label}
              {o.hint && <span className="block text-xs text-muted-foreground">{o.hint}</span>}
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/** A read-only list of values as tags; `empty` when there are none. */
export function TagList({ items, empty, testId }: { items: string[]; empty: string; testId?: string }) {
  if (items.length === 0) return <p className="text-sm text-muted-foreground" data-testid={testId}>{empty}</p>;
  return (
    <div className="flex flex-wrap gap-1.5" data-testid={testId}>
      {items.map((i) => (
        <Tag key={i}>{i}</Tag>
      ))}
    </div>
  );
}

/** "a, b and c". */
export function listWords(items: string[]): string {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}
