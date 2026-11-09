// Copyright (c) 2025 Reliant Labs

/**
 * WHICH DATABASE: an environment has as many as it declares (each
 * forge.ManagedDatabase and forge.ExternalDatabase), and two is the normal
 * case. A segment, because there are few and every one should be visible:
 * one the control plane cannot reach is still shown, disabled, with the
 * reason under the segment, because the environment declares it and a
 * missing name would read as "the browser lost it".
 */

import type { ReactNode } from "react";
import { Database, Lock, ShieldCheck, ShieldOff } from "lucide-react";

import Tag from "@/components/forge-ui/tag";
import type { BrowsableDb, DataAccessPolicyName } from "@/services/controlPlane/observability/appDatabaseApi";
import { cn } from "@/lib/utils";

import { SEGMENTED, segmentTone } from "../controlStyles";

const POLICY_TAG: Record<DataAccessPolicyName, { label: string; title: string; tone: "neutral" | "danger"; icon: ReactNode }> = {
  open: { label: "open", title: "Anyone with the db:query grant reads without giving a reason", tone: "neutral", icon: <Database className="h-3 w-3" /> },
  audited: {
    label: "audited",
    title: "Every read is recorded with the reader and their reason",
    tone: "neutral",
    icon: <ShieldCheck className="h-3 w-3" />,
  },
  deny: { label: "no access", title: "The environment's data access policy refuses every read", tone: "danger", icon: <ShieldOff className="h-3 w-3" /> },
  unknown: { label: "no policy", title: "No data access policy is recorded, so every read is refused", tone: "danger", icon: <ShieldOff className="h-3 w-3" /> },
};

export function PolicyTag({ policy }: { policy: DataAccessPolicyName }) {
  const spec = POLICY_TAG[policy];
  return (
    <span data-testid="data-policy" data-policy={policy}>
      <Tag tone={spec.tone} icon={spec.icon} title={spec.title}>
        {spec.label}
      </Tag>
    </span>
  );
}

const KIND_LABEL: Record<BrowsableDb["kind"], string> = { managed: "managed", external: "external", unknown: "" };

export function DatabaseSelector({
  databases,
  selected,
  onSelect,
}: {
  databases: BrowsableDb[];
  selected: string | null;
  onSelect: (name: string) => void;
}) {
  const unreachable = databases.filter((db) => !db.reachable);
  return (
    <div className="min-w-0 space-y-1.5" data-testid="data-databases">
      <div role="radiogroup" aria-label="Database" className={cn(SEGMENTED, "max-w-full flex-wrap")}>
        {databases.map((db) => {
          const on = db.name === selected;
          return (
            <button
              key={db.name}
              type="button"
              role="radio"
              aria-checked={on}
              disabled={!db.reachable}
              title={db.reachable ? `${db.name} (${KIND_LABEL[db.kind] || "database"})` : `${db.name}: ${db.unreachableReason || "unreachable"}`}
              onClick={() => onSelect(db.name)}
              data-testid={`data-db-${db.name}`}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-sm transition-colors",
                db.reachable ? segmentTone(on) : "cursor-not-allowed text-ink-subtle"
              )}
            >
              {db.reachable ? <Database className="h-3.5 w-3.5" aria-hidden="true" /> : <Lock className="h-3.5 w-3.5" aria-hidden="true" />}
              <span className="font-mono">{db.name}</span>
              {db.kind === "external" && <span className="text-2xs text-ink-muted">external</span>}
            </button>
          );
        })}
      </div>
      {unreachable.length > 0 && (
        <ul className="space-y-0.5 text-xs text-ink-muted" data-testid="data-db-unreachable">
          {unreachable.map((db) => (
            <li key={db.name}>
              <span className="font-mono text-ink">{db.name}</span> can&apos;t be browsed: {db.unreachableReason || "the control plane cannot reach it"}.
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
