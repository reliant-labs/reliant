// Copyright (c) 2025 Reliant Labs

/**
 * The preview list: teammates who see a chosen version under every mode but
 * off, and are never measured. A teammate is named by a label (for people
 * reading this list) and their unit id (what the app evaluates with: their
 * user id for unit user). The id is hashed by the control plane and never
 * stored, so a saved entry shows its label and hash only.
 */

import { useId, useState } from "react";
import { X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import { NO_REFETCH, useChangeWrites } from "@/hooks/changes-queries";
import { setChangePreviewRequest } from "@/services/controlPlane/changes/changesRequests";
import type { Change, ChangePreviewInput } from "@/services/controlPlane/changes/changesTypes";

import { FIELD } from "../controlStyles";
import { useObsReadOnly } from "../Observability/obsReadOnly";
import type { ChangeWrite } from "./WhoGetsIt";

interface Row extends ChangePreviewInput {
  /** Stable for React: the hash of a saved entry, a counter for a new one. */
  rowKey: string;
}

const rowsOf = (change: Change): Row[] => change.preview.map((e) => ({ rowKey: `h:${e.unitHash}`, unitHash: e.unitHash, variant: e.variant, label: e.label }));

export function PreviewEditor({ change, busy, onWrite }: { change: Change; busy: string | null; onWrite: ChangeWrite }) {
  const writes = useChangeWrites();
  const id = useId();
  const lock = useObsReadOnly();
  const variants = change.declaration?.variants.map((v) => v.name) ?? [];
  const [rows, setRows] = useState<Row[]>(() => rowsOf(change));
  const [seen, setSeen] = useState(`${change.id}@${change.revision}`);
  const [label, setLabel] = useState("");
  const [unitId, setUnitId] = useState("");
  const [variant, setVariant] = useState(variants.find((v) => v !== change.declaration?.defaultVariant) ?? variants[0] ?? "");
  const [activate, setActivate] = useState(change.mode === "off");
  const [counter, setCounter] = useState(0);
  if (seen !== `${change.id}@${change.revision}`) {
    setSeen(`${change.id}@${change.revision}`);
    setRows(rowsOf(change));
  }

  const dirty = JSON.stringify(rows.map(({ rowKey: _k, ...r }) => r)) !== JSON.stringify(rowsOf(change).map(({ rowKey: _k, ...r }) => r));
  const add = () => {
    if (!unitId.trim() || !variant) return;
    setRows((current) => [...current, { rowKey: `n:${counter}`, unitId: unitId.trim(), variant, label: label.trim() || unitId.trim() }]);
    setCounter((n) => n + 1);
    setLabel("");
    setUnitId("");
  };
  const save = () =>
    onWrite("Save preview list", () =>
      writes.setChangePreview(
        setChangePreviewRequest(
          change.id,
          change.revision,
          rows.map(({ rowKey: _k, ...r }) => r),
          activate && change.mode === "off"
        ),
        NO_REFETCH
      )
    );

  return (
    <Card padding="md" aria-labelledby={`${id}-title`} className="space-y-3">
      <div>
        <h2 id={`${id}-title`} className="text-section text-ink">
          Preview
        </h2>
        <p className="text-meta text-ink-muted">Teammates who see a version before anyone else. They're never counted in results, and turning the change off turns it off for them too.</p>
      </div>

      {rows.length > 0 ? (
        <ul className="divide-y divide-border-subtle" aria-label="Preview list">
          {rows.map((row) => (
            <li key={row.rowKey} className="flex items-center gap-3 py-1.5 text-ui">
              <span className="min-w-0 flex-1 truncate text-ink">{row.label || row.unitHash}</span>
              <span className="font-mono text-meta text-ink-muted">{row.unitHash ? `#${row.unitHash}` : "new"}</span>
              <span className="font-mono text-ink">{row.variant}</span>
              <Button variant="ghost" size="xs" icon={<X />} aria-label={`Remove ${row.label || row.unitHash}`} onClick={() => setRows((current) => current.filter((r) => r.rowKey !== row.rowKey))} />
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-ink-muted">No one previews this change.</p>
      )}

      {variants.length > 0 && (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            add();
          }}
        >
          <input value={label} onChange={(event) => setLabel(event.target.value)} placeholder="Name or email" aria-label="Label" className={`${FIELD} w-44`} />
          <input value={unitId} onChange={(event) => setUnitId(event.target.value)} placeholder={`${change.unit} id`} aria-label="Unit id" className={`${FIELD} w-44 font-mono`} data-testid="preview-unit-id" />
          <select value={variant} onChange={(event) => setVariant(event.target.value)} aria-label="Version" className={FIELD}>
            {variants.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
          <Button type="submit" variant="outline" size="sm" disabled={!unitId.trim()}>
            Add
          </Button>
        </form>
      )}

      {(dirty || (change.mode === "off" && rows.length > 0)) && (
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="outline" size="sm" onClick={() => void save()} disabled={busy !== null || lock.readOnly} title={lock.readOnly ? lock.reason : undefined} loading={busy === "Save preview list"} data-testid="preview-save">
            Save preview list
          </Button>
          {change.mode === "off" && (
            <label className="flex items-center gap-2 text-sm text-ink">
              <input type="checkbox" checked={activate} onChange={(event) => setActivate(event.target.checked)} />
              Turn preview on now
            </label>
          )}
        </div>
      )}
    </Card>
  );
}
