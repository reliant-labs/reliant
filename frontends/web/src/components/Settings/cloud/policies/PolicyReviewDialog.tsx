// Copyright (c) 2025 Reliant Labs

/**
 * Review before save: PreviewOrgPolicy on the draft, then UpdateOrgPolicy
 * carrying the version the editor read.
 *
 * The preview says what changes, what validation refuses (each refusal is
 * also shown on its card) and what an admin should know first. It also
 * returns the stored version: when that is not the version the editor read,
 * someone saved in between, and saving would fail `policy_version_conflict`.
 * Either way the dialog offers the same two ways out:
 *
 *   Reapply my changes   the sections this editor changed are put onto the
 *                        newer version, every other section stays theirs,
 *                        and the preview runs again against it
 *   Discard mine         load the newer version as it is
 *
 * Nothing is overwritten silently: a section both people changed is named
 * before the save.
 */

import { useEffect, useRef, useState } from "react";

import AlertBanner from "@/components/forge-ui/alert_banner";
import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import type { OrgPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { formatRelativeTime } from "@/lib/relativeTime";
import {
  getOrgPolicy,
  isPolicyVersionConflict,
  policyErrorMessage,
  previewOrgPolicy,
  updateOrgPolicy,
  type PolicyPreview,
  type PolicyRecord,
  type PolicyViolation,
} from "@/services/controlPlane/orgPolicy";

import { describeHistoryField, formatHistoryValue, rebasePolicy, SECTION_TITLES, type SectionKey } from "./policyDocument";
import { listWords } from "./PolicyCard";

interface Props {
  open: boolean;
  orgId: string;
  base: PolicyRecord;
  draft: OrgPolicy;
  /** Sections a reapply put over someone else's newer change. */
  overlapping: SectionKey[];
  nameOf: (userId: string) => string;
  onClose: () => void;
  /** The last preview's refusals, for the cards. */
  onViolations: (violations: PolicyViolation[]) => void;
  onSaved: (record: PolicyRecord, changed: boolean) => void;
  /** Reapply: `latest` is the new base, `policy` the draft on top of it. */
  onRebased: (latest: PolicyRecord, policy: OrgPolicy, overlapping: SectionKey[]) => void;
  /** Discard mine: load `latest` as it is. */
  onDiscardToLatest: (latest: PolicyRecord) => void;
}

function who(record: PolicyRecord): string {
  return record.updatedByEmail || "Someone";
}

export function PolicyReviewDialog({ open, orgId, base, draft, overlapping, nameOf, onClose, onViolations, onSaved, onRebased, onDiscardToLatest }: Props) {
  const [preview, setPreview] = useState<PolicyPreview | null>(null);
  const [latest, setLatest] = useState<PolicyRecord | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const seq = useRef(0);

  // Preview whenever the dialog opens or what it would save changes (a reapply).
  useEffect(() => {
    if (!open) return;
    const mine = ++seq.current;
    setPreview(null);
    setLatest(null);
    setError(null);
    void (async () => {
      try {
        const p = await previewOrgPolicy(orgId, draft);
        if (mine !== seq.current) return;
        setPreview(p);
        onViolations(p.violations);
        if (p.currentVersion !== base.version) {
          const l = await getOrgPolicy(orgId);
          if (mine === seq.current) setLatest(l);
        }
      } catch (err) {
        if (mine === seq.current) setError(policyErrorMessage(err, "Could not check these policies. Try again."));
      }
    })();
    // onViolations is a setter from the page; the preview depends on what is saved.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, orgId, draft, base.version]);

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const { record, changed } = await updateOrgPolicy(orgId, draft, base.version);
      onSaved(record, changed);
    } catch (err) {
      if (isPolicyVersionConflict(err)) {
        try {
          setLatest(await getOrgPolicy(orgId));
        } catch (readErr) {
          setError(policyErrorMessage(readErr, "Someone saved a newer version, and it could not be loaded. Try again."));
        }
      } else {
        setError(policyErrorMessage(err, "Could not save these policies. Try again."));
      }
    } finally {
      setSaving(false);
    }
  };

  const conflict = latest !== null && latest.version !== base.version;
  const canSave = !!preview && preview.valid && !conflict && preview.changes.length > 0 && !saving;

  const reapply = () => {
    if (!latest) return;
    const { policy, overlapping: both } = rebasePolicy(base.policy, draft, latest.policy);
    onRebased(latest, policy, both);
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title="Review policy changes"
      description="Check what changes before it applies to everyone in the organization."
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose}>
            Keep editing
          </Button>
          <Button variant="primary" loading={saving} disabled={!canSave} onClick={() => void save()} data-testid="policy-save">
            Save policies
          </Button>
        </div>
      }
    >
      <div className="space-y-4" data-testid="policy-review">
        {conflict && latest && (
          <div className="space-y-2" data-testid="policy-conflict">
            <AlertBanner
              variant="warning"
              title={`${who(latest)} saved version ${latest.version}${latest.updatedAt ? ` ${formatRelativeTime(latest.updatedAt.toISOString())}` : ""}`}
              message="You started from an older version. Saving yours as it is would undo their change, so it isn't allowed."
            />
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" onClick={reapply} data-testid="policy-reapply">
                Reapply my changes to version {latest.version}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => onDiscardToLatest(latest)} data-testid="policy-discard-to-latest">
                Discard mine and load theirs
              </Button>
            </div>
          </div>
        )}

        {overlapping.length > 0 && !conflict && (
          <AlertBanner
            variant="info"
            message={`Your changes are now on top of the newer version. In ${listWords(overlapping.map((k) => SECTION_TITLES[k]))}, yours replace theirs.`}
          />
        )}

        {error && (
          <p role="alert" className="text-sm text-destructive-ink" data-testid="policy-review-error">
            {error}
          </p>
        )}

        {!preview && !error && (
          <p className="text-sm text-muted-foreground" role="status">
            Checking your changes…
          </p>
        )}

        {preview && preview.violations.length > 0 && (
          <section aria-labelledby="policy-review-violations">
            <h4 id="policy-review-violations" className="text-sm font-semibold text-foreground">
              Fix these first
            </h4>
            <ul className="mt-1 list-disc space-y-1 pl-5 text-sm text-destructive-ink" data-testid="policy-review-violations">
              {preview.violations.map((v, i) => (
                <li key={`${v.field}-${i}`}>{v.message}</li>
              ))}
            </ul>
          </section>
        )}

        {preview && preview.warnings.length > 0 && (
          <section aria-labelledby="policy-review-warnings">
            <h4 id="policy-review-warnings" className="text-sm font-semibold text-foreground">
              Before you save
            </h4>
            <ul className="mt-1 list-disc space-y-1 pl-5 text-sm text-muted-foreground" data-testid="policy-review-warnings">
              {preview.warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          </section>
        )}

        {preview && (
          <section aria-labelledby="policy-review-changes">
            <h4 id="policy-review-changes" className="text-sm font-semibold text-foreground">
              What changes
            </h4>
            {preview.changes.length === 0 ? (
              <p className="mt-1 text-sm text-muted-foreground">Nothing: these are the policies already saved.</p>
            ) : (
              <ul className="mt-1 divide-y divide-border rounded-md border border-border/60 bg-background" data-testid="policy-review-changes">
                {preview.changes.map((c) => {
                  const section = c.resource.split(".")[1] ?? "";
                  return (
                    <li key={`${c.resource}-${c.field}`} className="px-3 py-2 text-sm">
                      <p className="font-medium text-foreground">{describeHistoryField(section, c.field, nameOf)}</p>
                      <p className="text-muted-foreground">
                        {formatHistoryValue(c.oldValue)} → <span className="text-foreground">{formatHistoryValue(c.newValue)}</span>
                      </p>
                    </li>
                  );
                })}
              </ul>
            )}
          </section>
        )}
      </div>
    </Modal>
  );
}
