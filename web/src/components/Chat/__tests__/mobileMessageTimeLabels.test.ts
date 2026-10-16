import { expect, it } from "vitest";
import { mobileMessageTimeLabels } from "../mobileMessageTimeLabels";
it("uses quiet date separators without repeating times within the same day", () => {
  const labels=mobileMessageTimeLabels([
    {id:"a",createdAt:"2026-10-14T10:00:00"},
    {id:"b",createdAt:"2026-10-14T10:29:00"},
    {id:"c",createdAt:"2026-10-14T10:59:00"},
    {id:"d",createdAt:"2026-10-14T23:59:00"},
    {id:"e",createdAt:"2026-10-15T00:01:00"},
  ]);
  expect([...labels.keys()]).toEqual(["a","e"]);
  expect(labels.get("e")).toContain("15");
});
it("ignores missing and invalid timestamps", () => {
  expect([...mobileMessageTimeLabels([{id:"bad",createdAt:"invalid"},{id:"missing"},{id:"valid",createdAt:"2026-10-14T10:00:00"}]).keys()]).toEqual(["valid"]);
});
