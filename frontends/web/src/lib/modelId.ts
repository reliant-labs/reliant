/**
 * App-wide model ids are "<model>@<driver>" ("gpt-5.6-sol@codex"); a bare
 * "<model>" (no driver) is also a valid pin. Split on the LAST "@" so a model
 * id can never be mistaken for a driver.
 */
export function splitModelId(id: string): { modelId: string; driverId?: string } {
  const at = id.lastIndexOf("@");
  if (at <= 0 || at === id.length - 1) return { modelId: id };
  return { modelId: id.slice(0, at), driverId: id.slice(at + 1) };
}

/**
 * The model a composer pin names, among the models the user can run now.
 *
 * Exact id first. Failing that, the same model on another connected provider:
 * a send moves a pin whose provider is not connected onto exactly that model
 * (launch.substituteModelInputs), so it is what the next message runs on.
 *
 * The match used to compare each model's BARE id against the pin's FULL id —
 * `m.id.split('@')[0] === "gpt-5.6-sol@codex"` — which can never hold for a
 * driver-qualified pin, so once Codex was disconnected the composer fell back
 * to printing the raw id.
 */
export function findPinnedModel<M extends { id: string }>(
  pinnedId: string,
  models: readonly M[],
): M | undefined {
  const exact = models.find((m) => m.id === pinnedId);
  if (exact) return exact;
  const { modelId } = splitModelId(pinnedId);
  return models.find((m) => splitModelId(m.id).modelId === modelId);
}
