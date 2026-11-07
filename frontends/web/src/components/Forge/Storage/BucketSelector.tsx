// Copyright (c) 2025 Reliant Labs

/**
 * WHICH BUCKET: an environment has as many as it declares (each forge.Bucket
 * in its recorded release), usually one to three. A segment, because there
 * are few and every one should be visible: one the control plane cannot reach
 * is still shown, disabled, with its reason under the segment, because the
 * environment declares it and a missing name would read as "the browser lost
 * it".
 *
 * The policy is the environment's, not a bucket's (data_access.storage), so
 * it is one tag beside the segment.
 */

import type { ReactNode } from "react";
import { Globe, HardDrive, Lock, ShieldCheck, ShieldOff } from "lucide-react";

import Tag from "@/components/forge-ui/tag";
import type { BucketRuntimeName, StorageBucket } from "@/services/controlPlane/storageBrowser/storageBrowserApi";
import { cn } from "@/lib/utils";

import type { AccessPolicyName } from "../AuditedAccess/accessSession-model";
import { SEGMENTED, segmentTone } from "../controlStyles";

const POLICY_TAG: Record<AccessPolicyName, { label: string; title: string; tone: "neutral" | "danger"; icon: ReactNode }> = {
  open: { label: "open", title: "Anyone with the storage:read grant browses without giving a reason", tone: "neutral", icon: <HardDrive className="h-3 w-3" /> },
  audited: {
    label: "audited",
    title: "Every read is recorded with the reader and their reason",
    tone: "neutral",
    icon: <ShieldCheck className="h-3 w-3" />,
  },
  deny: { label: "no access", title: "The environment's storage access policy refuses every read", tone: "danger", icon: <ShieldOff className="h-3 w-3" /> },
  unknown: { label: "no policy", title: "No storage access policy is recorded, so every read is refused", tone: "danger", icon: <ShieldOff className="h-3 w-3" /> },
};

export function StoragePolicyTag({ policy }: { policy: AccessPolicyName }) {
  const spec = POLICY_TAG[policy];
  return (
    <span data-testid="storage-policy" data-policy={policy}>
      <Tag tone={spec.tone} icon={spec.icon} title={spec.title}>
        {spec.label}
      </Tag>
    </span>
  );
}

const RUNTIME_LABEL: Record<BucketRuntimeName, string> = { hosted: "hosted", gcs: "GCS", s3: "S3", minio: "MinIO", unknown: "" };

export function BucketSelector({ buckets, selected, onSelect }: { buckets: StorageBucket[]; selected: string | null; onSelect: (name: string) => void }) {
  const unreachable = buckets.filter((bucket) => !bucket.reachable);
  return (
    <div className="min-w-0 space-y-1.5" data-testid="storage-buckets">
      <div role="radiogroup" aria-label="Bucket" className={cn(SEGMENTED, "max-w-full flex-wrap")}>
        {buckets.map((bucket) => {
          const on = bucket.name === selected;
          const runtime = RUNTIME_LABEL[bucket.runtime];
          return (
            <button
              key={bucket.name}
              type="button"
              role="radio"
              aria-checked={on}
              disabled={!bucket.reachable}
              title={
                bucket.reachable
                  ? `${bucket.name}${runtime ? ` (${runtime})` : ""}${bucket.publicRead ? ": every object is publicly readable" : ""}`
                  : `${bucket.name}: ${bucket.unreachableReason || "unreachable"}`
              }
              onClick={() => onSelect(bucket.name)}
              data-testid={`storage-bucket-${bucket.name}`}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-sm transition-colors",
                bucket.reachable ? segmentTone(on) : "cursor-not-allowed text-ink-subtle"
              )}
            >
              {bucket.reachable ? <HardDrive className="h-3.5 w-3.5" aria-hidden="true" /> : <Lock className="h-3.5 w-3.5" aria-hidden="true" />}
              <span className="font-mono">{bucket.name}</span>
              {runtime && <span className="text-2xs text-ink-muted">{runtime}</span>}
              {bucket.publicRead && <Globe className="h-3 w-3 text-ink-muted" aria-label="public" />}
            </button>
          );
        })}
      </div>
      {unreachable.length > 0 && (
        <ul className="space-y-0.5 text-xs text-ink-muted" data-testid="storage-bucket-unreachable">
          {unreachable.map((bucket) => (
            <li key={bucket.name}>
              <span className="font-mono text-ink">{bucket.name}</span> can&apos;t be browsed: {bucket.unreachableReason || "the control plane cannot reach it"}.
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
