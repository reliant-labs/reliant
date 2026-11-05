import { describe, expect, it } from "vitest";
import { ABUSE_EMAIL, SECURITY_EMAIL, renderSecurityTxt } from "./vite-plugin-security-txt";

describe("renderSecurityTxt", () => {
  const now = new Date("2026-11-02T21:30:00Z");
  const body = renderSecurityTxt(now);

  it("lists the security contact, then the abuse contact", () => {
    const sec = body.indexOf(`Contact: mailto:${SECURITY_EMAIL}\n`);
    const abuse = body.indexOf(`Contact: mailto:${ABUSE_EMAIL}\n`);
    expect(sec).toBeGreaterThanOrEqual(0);
    expect(abuse).toBeGreaterThan(sec);
    expect(ABUSE_EMAIL).toBe("abuse@reliantlabs.io");
  });

  it("expires after the build and within a year (RFC 9116)", () => {
    const line = body.split("\n").find((l) => l.startsWith("Expires: "));
    const expires = new Date(line!.slice("Expires: ".length));
    expect(expires.getTime()).toBeGreaterThan(now.getTime());
    expect(expires.getTime()).toBeLessThan(now.getTime() + 365 * 86_400_000);
    expect(line).toMatch(/^Expires: \d{4}-\d{2}-\d{2}T00:00:00Z$/);
  });

  it("names its canonical URL", () => {
    expect(body).toContain("Canonical: https://app.reliantlabs.io/.well-known/security.txt\n");
  });
});
