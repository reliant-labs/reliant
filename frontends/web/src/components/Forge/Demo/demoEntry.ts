import { consolePath } from "@/components/Forge/Console/paths";
import { forgeDemoPath } from "@/lib/paths";
// Copyright (c) 2025 Reliant Labs
/** Bare entry leads with the plain-language home. Explicit views/tours/sections keep their links. */
export function demoEntryHref(searchStr: string): string {
  const search = new URLSearchParams(searchStr);
  if (search.has("tour") || search.has("view") || search.has("trace") || search.has("issue")) return `${consolePath("shop", { kind: "overview" })}${searchStr}`;
  if (!search.has("section")) search.set("section", "home");
  return `${forgeDemoPath()}?${search.toString()}`;
}
