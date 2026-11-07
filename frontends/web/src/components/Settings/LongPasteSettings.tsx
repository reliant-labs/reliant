import { useEffect, useState } from "react";
import Input from "../forge-ui/input";
import Button from "../forge-ui/button";
import ToggleSwitch from "../forge-ui/toggle_switch";
import { DEFAULT_LONG_PASTE_PREFERENCES, useLongPastePreferencesQuery, useUpdateLongPastePreferences, type LongPastePreferences } from "../../hooks/settings-queries";

export function LongPasteSettings() {
  const query = useLongPastePreferencesQuery();
  const preferences = query.data ?? DEFAULT_LONG_PASTE_PREFERENCES;
  const update = useUpdateLongPastePreferences();
  const [error, setError] = useState("");
  const save = async (patch: Partial<LongPastePreferences>) => {
    setError("");
    try {
      await update.mutateAsync(patch);
    } catch {
      setError("Could not save paste preferences. Please try again.");
    }
  };
  const disabled = !query.isSuccess || update.isPending;

  return (
    <section aria-labelledby="long-paste-heading" className="rounded-lg border border-border bg-card p-4 space-y-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h3 id="long-paste-heading" className="text-sm font-semibold">Long pastes</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            Turn long pasted text into a text attachment when either threshold is reached. You can put it back in the message before sending.
          </p>
        </div>
        <ToggleSwitch className="forge-ui" checked={preferences.enabled} disabled={disabled}
          ariaLabel="Automatically attach long pastes" onChange={(enabled) => void save({ enabled })} />
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <ThresholdInput label="Character threshold" id="long-paste-characters" value={preferences.characterThreshold}
          disabled={disabled || !preferences.enabled} onSave={(characterThreshold) => void save({ characterThreshold })} />
        <ThresholdInput label="Line threshold" id="long-paste-lines" value={preferences.lineThreshold}
          disabled={disabled || !preferences.enabled} onSave={(lineThreshold) => void save({ lineThreshold })} />
      </div>
      {query.isError && <div role="alert" className="space-y-2">
        <p className="text-sm text-destructive-ink">Could not load paste preferences.</p>
        <Button variant="outline" size="sm" disabled={query.isFetching} onClick={() => void query.refetch()}>Retry</Button>
      </div>}
      {error && <p role="alert" className="text-sm text-destructive-ink">{error}</p>}
    </section>
  );
}

function ThresholdInput({ label, id, value, disabled, onSave }: {
  label: string; id: string; value: number; disabled: boolean; onSave: (value: number) => void;
}) {
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  const parsed = Number(draft);
  const valid = draft.trim() !== "" && Number.isSafeInteger(parsed) && parsed > 0;
  return (
    <div className="space-y-2">
      <label htmlFor={id} className="text-sm font-medium">{label}</label>
      <div className="forge-ui">
        <Input id={id} type="number" min={1} step={1} value={draft} disabled={disabled} invalid={!valid}
          aria-describedby={!valid ? `${id}-error` : undefined}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={() => { if (valid && parsed !== value) onSave(parsed); }}
          onKeyDown={(event) => { if (event.key === "Enter") event.currentTarget.blur(); }} />
      </div>
      {!valid && <p id={`${id}-error`} role="alert" className="text-xs text-destructive-ink">Enter a positive whole number.</p>}
    </div>
  );
}
