// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { countryName, GEO_ATTRIBUTION, placeLabel, placeTitle } from "../geo";

describe("browser location", () => {
  it("names the most specific place with its country", () => {
    expect(placeLabel({ country: "DE", region: "Land Berlin", city: "Berlin" })).toMatch(/^Berlin, (Germany|DE)$/);
    expect(placeLabel({ country: "US", region: "California", city: "" })).toMatch(/^California, (United States|US)$/);
    expect(placeLabel({ country: "JP", region: "", city: "" })).toMatch(/^(Japan|JP)$/);
  });

  it("says nothing when the place is unknown", () => {
    expect(placeLabel({ country: "", region: "", city: "" })).toBe("");
    expect(placeTitle({ country: "", region: "", city: "" })).toBe("");
  });

  it("calls it approximate and credits DB-IP, as its licence requires", () => {
    const title = placeTitle({ country: "DE", region: "Land Berlin", city: "Berlin" });
    expect(title).toContain("approximate");
    expect(title).toContain(GEO_ATTRIBUTION);
    expect(title).toContain("Land Berlin");
  });

  it("falls back to the code for a country it cannot name", () => {
    expect(countryName("")).toBe("");
    expect(countryName("ZZ")).toBeTruthy();
  });
});
