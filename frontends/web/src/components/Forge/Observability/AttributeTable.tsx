// Copyright (c) 2025 Reliant Labs

/** A sorted key/value list of OTel attributes, as an inset under a log line or span. */
export function AttributeTable({ title, attributes }: { title: string; attributes: Record<string, string> }) {
  const entries = Object.entries(attributes).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return null;
  return (
    <section aria-label={title}>
      <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-ink-muted">{title}</h4>
      <dl className="grid grid-cols-[minmax(8rem,max-content)_minmax(0,1fr)] gap-x-4 gap-y-0.5 rounded-md border border-border/60 bg-surface-sunken px-3 py-2 font-mono text-xs">
        {entries.map(([key, value]) => (
          <div key={key} className="contents">
            <dt className="truncate text-ink-muted" title={key}>
              {key}
            </dt>
            <dd className="break-all text-ink">{value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
