// Copyright (c) 2025 Reliant Labs

/**
 * THE PER-RECORD STATUS COLUMN — three states, and the third is the one
 * that matters.
 *
 * A domain that will not verify is almost always a domain whose address
 * record is perfect and whose TXT record was never published: it visibly
 * resolves, so the tenant concludes they are finished and stops. The
 * aggregate state cannot say that. This column can, and these tests pin
 * the two ways it could get it wrong:
 *
 *   1. marking an UNCHECKED record as failed — a red cross on a record
 *      that is very likely correct, which sends someone to "fix"
 *      something that was never broken;
 *   2. attaching a failure to the wrong row.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

import {
  DOMAIN_STATE_EXPLANATIONS,
  dnsRecordCheckOf,
  type DomainDnsRecord,
  type ForgeDomain,
} from "@/services/forge/domains";

import { DnsRecordsTable } from "../DnsRecordsTable";
import { DomainDetail } from "../DomainDetail";

function record(overrides: Partial<DomainDnsRecord> = {}): DomainDnsRecord {
  return {
    type: "A",
    name: "hounders.club",
    value: "34.63.203.181",
    check: "unchecked",
    detail: "",
    ...overrides,
  };
}

/** The common stall: address record right, ownership token never published. */
const TXT_MISSING: DomainDnsRecord[] = [
  record({ check: "ok" }),
  record({
    type: "TXT",
    name: "_reliant-challenge.hounders.club",
    value: "reliant-verify-8f3a91c2e7b04d56",
    check: "failed",
    detail: "no TXT record found at _reliant-challenge.hounders.club",
  }),
];

function rowFor(type: string) {
  return screen.getByTestId(`dns-record-${type.toLowerCase()}`);
}

describe("classifying the wire's (resolved, detail) pair", () => {
  it("resolved is ok", () => {
    expect(dnsRecordCheckOf(true, "")).toBe("ok");
  });

  it("not resolved WITH a detail is a real failure", () => {
    expect(dnsRecordCheckOf(false, "no TXT record found")).toBe("failed");
  });

  it("not resolved with NO detail is unchecked, never failed", () => {
    // The distinction the whole feature turns on: a record the verifier
    // has not reached is not a record that is wrong.
    expect(dnsRecordCheckOf(false, "")).toBe("unchecked");
    expect(dnsRecordCheckOf(false, "   ")).toBe("unchecked");
  });

  it("reads resolved as the conclusion even if a stray detail came with it", () => {
    expect(dnsRecordCheckOf(true, "leftover")).toBe("ok");
  });
});

describe("the status column", () => {
  it("marks the good record found and the missing one not found", () => {
    render(<DnsRecordsTable records={TXT_MISSING} />);

    expect(within(rowFor("A")).getByTestId("dns-record-check-ok")).toHaveTextContent("Found");
    expect(within(rowFor("TXT")).getByTestId("dns-record-check-failed")).toHaveTextContent(
      "Not found"
    );
  });

  it("puts the reason on the failing row, and nowhere else", () => {
    render(<DnsRecordsTable records={TXT_MISSING} />);

    expect(screen.getByTestId("dns-record-detail-txt")).toHaveTextContent(
      "no TXT record found at _reliant-challenge.hounders.club"
    );
    // The A record is fine. A detail here would send the tenant to edit it.
    expect(screen.queryByTestId("dns-record-detail-a")).not.toBeInTheDocument();
  });

  it("renders an unchecked record as unchecked, NOT as a failure", () => {
    // Every record on a domain added seconds ago looks like this.
    render(<DnsRecordsTable records={[record(), record({ type: "TXT", name: "_c.hounders.club" })]} />);

    expect(screen.getAllByTestId("dns-record-check-unchecked")).toHaveLength(2);
    expect(screen.queryByTestId("dns-record-check-failed")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dns-record-check-ok")).not.toBeInTheDocument();
    // And no explanation, because there is nothing to explain yet.
    expect(screen.queryByTestId("dns-record-detail-a")).not.toBeInTheDocument();
  });

  it("shows the address mismatch verbatim — what it is, and what it should be", () => {
    render(
      <DnsRecordsTable
        records={[
          record({
            check: "failed",
            detail: "resolves to 203.0.113.7, expected 34.63.203.181",
          }),
        ]}
      />
    );
    const detail = screen.getByTestId("dns-record-detail-a");
    expect(detail).toHaveTextContent("203.0.113.7");
    expect(detail).toHaveTextContent("34.63.203.181");
  });

  it("marks every record found once the domain is live", () => {
    render(
      <DnsRecordsTable
        records={[
          record({ check: "ok" }),
          record({ type: "TXT", name: "_reliant-challenge.hounders.club", check: "ok" }),
        ]}
      />
    );
    expect(screen.getAllByTestId("dns-record-check-ok")).toHaveLength(2);
    expect(screen.queryByTestId("dns-record-check-failed")).not.toBeInTheDocument();
  });

  it("carries a text label, not an icon alone", () => {
    // An icon-only status is nothing at all to a screen reader, and the
    // three glyphs are meaningless without their words.
    render(<DnsRecordsTable records={TXT_MISSING} />);
    for (const label of ["Found", "Not found"]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
  });
});

/**
 * The per-record column ADDS a layer; it does not replace the one above it.
 *
 * These answer different questions and a tenant needs both. The aggregate
 * explanation says whose turn it is and what happens next ("we check
 * automatically", "nothing to do"); the rows say which record is holding it
 * up. Dropping the explanation once rows existed would be an easy and
 * plausible simplification, and it would leave someone looking at two red
 * crosses with no idea whether the platform is still checking.
 */
describe("the aggregate explanation survives alongside the rows", () => {
  const PENDING: ForgeDomain = {
    id: "dom-1",
    hostname: "hounders.club",
    state: "pending-dns",
    origin: "external",
    requiredRecords: TXT_MISSING,
    lastError: "publish TXT _reliant-challenge.hounders.club",
    binding: null,
  };

  it("renders the state sentence, the next step, and the per-row verdicts together", () => {
    render(
      <DomainDetail
        domain={PENDING}
        envNameFor={(id) => id}
        onVerify={vi.fn()}
        onRebind={vi.fn()}
        onUnbind={vi.fn()}
        onRemove={vi.fn()}
        isVerifying={false}
        actionError={null}
      />
    );

    // The aggregate layer, unchanged.
    expect(screen.getByText(DOMAIN_STATE_EXPLANATIONS["pending-dns"])).toBeInTheDocument();
    expect(screen.getByTestId("domain-next-step")).toBeInTheDocument();
    expect(screen.getByTestId("domain-last-error")).toBeInTheDocument();

    // And the new one, naming the record that is actually wrong.
    expect(within(rowFor("A")).getByTestId("dns-record-check-ok")).toBeInTheDocument();
    expect(within(rowFor("TXT")).getByTestId("dns-record-check-failed")).toBeInTheDocument();
    expect(screen.getByTestId("dns-record-detail-txt")).toBeInTheDocument();
  });
});
