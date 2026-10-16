/** Sparse time separators; invalid/local optimistic timestamps do not advance the clock. */
export function mobileMessageTimeLabels(messages: Array<{ id: string; createdAt?: string }>): Map<string, string> {
  const labels = new Map<string, string>();
  let previous: Date | undefined;
  for (const message of messages) {
    if (!message.createdAt) continue;
    const date = new Date(message.createdAt);
    if (!Number.isFinite(date.getTime())) continue;
    const dayChanged = !previous || date.toDateString() !== previous.toDateString();
    if (dayChanged) {
      labels.set(message.id, date.toLocaleDateString(undefined, { month: "short", day: "numeric" }));
    }
    previous = date;
  }
  return labels;
}
