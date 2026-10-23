interface PubSubMatrixProps {
  /** Column headers — typically consumer / subscriber service names. */
  consumers: Array<{ id: string; label: string }>;
  /** Rows — typically topics or event types. */
  topics: Array<{ id: string; label: string; sub?: string }>;
  /**
   * Subscription cells. Each entry marks one (topic, consumer) cell as subscribed.
   * Cells not listed render empty.
   */
  subscriptions: Array<{ topicId: string; consumerId: string }>;
  /** Title rendered above the matrix. Optional. */
  title?: string;
}

/**
 * Pub/sub subscription matrix — topics as rows × consumers as columns, cells
 * marking who subscribes to what. Complements `bus_bar` (which shows the bus
 * structure) by making routing rules explicit and scannable.
 *
 * Pure presentational. No coordinate math — it's a table.
 */
export default function PubSubMatrix({
  consumers,
  topics,
  subscriptions,
  title,
}: PubSubMatrixProps) {
  const subSet = new Set(subscriptions.map((s) => `${s.topicId}::${s.consumerId}`));

  return (
    <div className="w-full overflow-x-auto">
      {title && <h3 className="mb-3 text-sm font-semibold text-gray-900">{title}</h3>}
      <table className="min-w-full border-collapse text-sm">
        <thead>
          <tr>
            <th className="sticky left-0 z-10 border-b border-gray-200 bg-white px-3 py-2 text-left text-xs font-semibold uppercase tracking-wider text-gray-500">
              Topic
            </th>
            {consumers.map((c) => (
              <th
                key={c.id}
                className="border-b border-l border-gray-200 px-3 py-2 text-center text-xs font-semibold text-gray-700"
                scope="col"
              >
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {topics.map((t) => (
            <tr key={t.id} className="hover:bg-gray-50">
              <th
                scope="row"
                className="sticky left-0 z-10 border-b border-gray-100 bg-white px-3 py-2 text-left align-top"
              >
                <div className="font-medium text-gray-900">{t.label}</div>
                {t.sub && <div className="text-xs text-gray-500">{t.sub}</div>}
              </th>
              {consumers.map((c) => {
                const subscribed = subSet.has(`${t.id}::${c.id}`);
                return (
                  <td
                    key={c.id}
                    className="border-b border-l border-gray-100 px-3 py-2 text-center"
                    aria-label={
                      subscribed
                        ? `${c.label} subscribes to ${t.label}`
                        : `${c.label} does not subscribe to ${t.label}`
                    }
                  >
                    {subscribed ? (
                      <span
                        aria-hidden="true"
                        className="inline-flex h-5 w-5 items-center justify-center rounded-full bg-emerald-100 text-xs font-semibold text-emerald-700"
                      >
                        ✓
                      </span>
                    ) : (
                      <span aria-hidden="true" className="text-gray-300">
                        ·
                      </span>
                    )}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
