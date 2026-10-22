// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { validateTimezone } from "../timezone";

describe("validateTimezone", () => {
  it.each(["America/New_York", "Europe/Berlin", "UTC", "US/Eastern", "  Asia/Tokyo  ", ""])(
    "accepts %j",
    (zone) => {
      expect(validateTimezone(zone)).toBeNull();
    },
  );

  it.each(["Mars/Olympus", "America/Nowhere", "EST5EDT/x", "not a zone"])("rejects %j", (zone) => {
    expect(validateTimezone(zone)).toMatch(/is not a time zone/);
  });
});
