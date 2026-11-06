// Copyright (c) 2025 Reliant Labs

/**
 * The CDN edge's certificate-authorization CNAME is a CNAME like the routing
 * one, so a description keyed by type alone told tenants it "points the
 * subdomain at the ingress name". It does not: it lets Google issue and renew
 * the TLS certificate, and it must stay.
 */

import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";

import type { DomainDnsRecord } from "@/services/forge/domains";

import { DnsRecordsTable, isCertAuthorizationRecord } from "../DnsRecordsTable";

const ROUTING: DomainDnsRecord = {
  type: "CNAME",
  name: "www.hounders.club",
  value: "ingress.reliantlabs.dev",
  check: "unchecked",
  detail: "",
};
const OWNERSHIP: DomainDnsRecord = {
  type: "TXT",
  name: "_reliant-challenge.www.hounders.club",
  value: "reliant-verify-1122334455667788",
  check: "unchecked",
  detail: "",
};
const CERT_AUTH: DomainDnsRecord = {
  type: "CNAME",
  name: "_acme-challenge_abc.www.hounders.club",
  value: "abc.1.authorize.certificatemanager.goog",
  check: "unchecked",
  detail: "",
};

const ROUTING_COPY = "stays stable as the addresses behind it change";
const CERT_COPY = "Lets Google issue the TLS certificate for this host at Reliant's CDN edge. Keep it: renewals need it.";

function rows() {
  render(<DnsRecordsTable records={[ROUTING, OWNERSHIP, CERT_AUTH]} />);
  const table = screen.getByTestId("dns-records-table");
  return within(table).getAllByTestId(/^dns-record-(cname|txt)$/);
}

describe("per-record descriptions for a host served through the CDN edge", () => {
  it("describes the routing CNAME as pointing at the ingress name", () => {
    const [routing] = rows();
    expect(routing.textContent).toContain(ROUTING_COPY);
    expect(routing.textContent).not.toContain("TLS certificate");
  });

  it("describes the ownership TXT as proof of ownership", () => {
    const [, txt] = rows();
    expect(txt.textContent).toContain("Proves you own this domain");
    expect(txt.textContent).not.toContain("TLS certificate");
  });

  it("describes the cert-authorization CNAME as certificate issuance, not routing", () => {
    const [, , auth] = rows();
    expect(auth.textContent).toContain(CERT_COPY);
    expect(auth.textContent).not.toContain(ROUTING_COPY);
  });
});

describe("isCertAuthorizationRecord", () => {
  it("recognises the record by its name prefix and by its target", () => {
    expect(isCertAuthorizationRecord(CERT_AUTH)).toBe(true);
    expect(isCertAuthorizationRecord({ ...CERT_AUTH, name: "x.hounders.club" })).toBe(true);
    expect(isCertAuthorizationRecord({ ...CERT_AUTH, value: "ingress.reliantlabs.dev" })).toBe(true);
    expect(isCertAuthorizationRecord({ ...CERT_AUTH, value: "abc.1.authorize.certificatemanager.goog." })).toBe(true);
  });

  it("leaves the routing CNAME and the TXT alone", () => {
    expect(isCertAuthorizationRecord(ROUTING)).toBe(false);
    expect(isCertAuthorizationRecord(OWNERSHIP)).toBe(false);
  });
});
