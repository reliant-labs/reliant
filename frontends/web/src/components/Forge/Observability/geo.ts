// Copyright (c) 2025 Reliant Labs

/**
 * Where a browser was, as the console shows it. The platform derives the
 * location at ingest from the address the browser connected from (DB-IP City
 * Lite, CC BY 4.0) and drops the address; the page never reports it, so it
 * cannot claim to be elsewhere. It is still IP geolocation: a VPN or a carrier
 * NAT moves it, which is why it is shown as "near", never as fact.
 */

/** The fields every geolocated record carries; empty strings when unknown. */
export interface GeoFields {
  country: string;
  region: string;
  city: string;
}

/** The attribution DB-IP's licence requires wherever its data is shown. */
export const GEO_ATTRIBUTION = "IP geolocation by DB-IP (db-ip.com), CC BY 4.0";

const regionNames = (() => {
  try {
    return new Intl.DisplayNames(undefined, { type: "region" });
  } catch {
    return null;
  }
})();

/** The country's name in the reader's language, else its code. */
export function countryName(code: string): string {
  if (!code) return "";
  try {
    return regionNames?.of(code.toUpperCase()) ?? code;
  } catch {
    return code;
  }
}

/** The flag emoji of an ISO 3166-1 alpha-2 code ("DE" → 🇩🇪); "" for anything else. */
export function countryFlag(code: string): string {
  if (!/^[A-Za-z]{2}$/.test(code)) return "";
  return String.fromCodePoint(...[...code.toUpperCase()].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
}

/**
 * Whether a value is a country code a reader would rather see named: two
 * letters the reader's locale knows as a region. "Unattributed", "" and an
 * unknown code are not.
 */
export function isCountryCode(code: string): boolean {
  return /^[A-Za-z]{2}$/.test(code) && countryName(code) !== code && countryName(code) !== code.toUpperCase();
}

/** "Berlin, Germany" — the most specific place known, with its country. Empty when unknown. */
export function placeLabel(geo: GeoFields): string {
  const country = countryName(geo.country);
  const local = geo.city || geo.region;
  if (local && country) return `${local}, ${country}`;
  return local || country;
}

/** The full place for a tooltip: city, region, country (each once). */
export function placeTitle(geo: GeoFields): string {
  const parts = [geo.city, geo.region !== geo.city ? geo.region : "", countryName(geo.country)].filter(Boolean);
  return parts.length ? `${parts.join(", ")} (approximate, from the browser's IP address). ${GEO_ATTRIBUTION}` : "";
}
