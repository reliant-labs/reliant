// Copyright (c) 2025 Reliant Labs
/** Only bare entry leads with the build story. Explicit views/tours keep their links. */
export function demoEntryHref(searchStr: string): string {
  const search = new URLSearchParams(searchStr);
  if (search.has("tour") || search.has("view") || search.has("trace") || search.has("issue")) return `/forge/p/shop${searchStr}`;
  if (!search.has("section")) search.set("section", "built");
  return `/forge/demo?${search.toString()}`;
}
