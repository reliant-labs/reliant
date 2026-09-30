// Copyright (c) 2025 Reliant Labs

/**
 * The records the tenant publishes at their DNS provider.
 *
 * THIS IS THE WHOLE SCREEN, not a detail panel under a status line. Until
 * these are pasted into a registrar nothing converges, and Reliant cannot do
 * it on the tenant's behalf. So the table is the primary content of a
 * domain's detail view, and it is shown in EVERY state including `live` —
 * removing the records fails the next check, so a tenant auditing their zone
 * needs to see what must stay.
 *
 * ── RENDERED, NEVER DERIVED ─────────────────────────────────────────────────
 *
 * Every row comes from the server's `required_records`. This component knows
 * nothing about apexes, CNAMEs or the ingress address, and adding that
 * knowledge would be a bug waiting for the first time the platform changes
 * one — the tenant would paste a confidently-rendered record that no longer
 * verifies.
 *
 * ── COPYING IS THE INTERACTION ──────────────────────────────────────────────
 *
 * The values are long, exact, and retyped into a different application. A
 * mistyped TXT token fails verification with an error that says nothing about
 * the typo. So each value has its own copy button, the text is monospaced and
 * selectable, and nothing is truncated with an ellipsis that would survive a
 * manual copy as literal dots.
 */

import { useCallback, useState } from "react";
import { AlertTriangle, Check, CircleDashed, Copy } from "lucide-react";

import { cn } from "@/lib/utils";
import type { DnsRecordCheck, DomainDnsRecord } from "@/services/forge/domains";

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);

  const onCopy = useCallback(() => {
    // Clipboard access can be refused (an insecure origin, a denied
    // permission). The value stays selectable either way, so a failure means
    // the button does not confirm — never that the record is unavailable.
    void navigator.clipboard
      ?.writeText(value)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => setCopied(false));
  }, [value]);

  return (
    <button
      type="button"
      onClick={onCopy}
      aria-label={copied ? `${label} copied` : `Copy ${label}`}
      className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted-foreground transition hover:bg-muted hover:text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-success" aria-hidden="true" />
      ) : (
        <Copy className="h-3.5 w-3.5" aria-hidden="true" />
      )}
    </button>
  );
}

/**
 * Why this particular record exists, one line each.
 *
 * Keyed by TYPE rather than by name, because the type is what the server
 * sends and what the tenant's DNS form asks for. The TXT explanation is the
 * one that earns its place: tenants routinely publish the address record,
 * skip the token, and then cannot understand why a domain that plainly
 * resolves to Reliant will not verify.
 */
function purposeOf(type: string): string {
  switch (type.toUpperCase()) {
    case "A":
      return "Points the domain at Reliant's load balancer. An apex domain needs an address record because it cannot hold a CNAME.";
    case "AAAA":
      return "Points the domain at Reliant's load balancer over IPv6.";
    case "CNAME":
      return "Points the subdomain at Reliant's ingress name, which stays stable as the addresses behind it change.";
    case "TXT":
      return "Proves you own this domain. Routing alone is not proof — a domain someone else once pointed at us keeps resolving here — so the token is required, and it is re-checked on every pass.";
    default:
      return "";
  }
}

/**
 * One record's verification status.
 *
 * THE THIRD STATE IS THE WHOLE CARE HERE. `unchecked` — the verifier has
 * not reached this record yet, which is every record on a brand-new domain
 * — must not read as a failure. It gets the dashed, unfilled treatment the
 * rest of this console uses for "nothing was measured" (see
 * stateVocabulary.ts), which is the one axis that survives greyscale and
 * colour-vision deficiency, so it can never be mistaken for a red cross.
 *
 * The text label is not decoration either: an icon alone would leave a
 * screen-reader user with no status at all, and the three glyphs are
 * meaningless without it.
 */
function RecordStatus({ check }: { check?: DnsRecordCheck }) {
  const styles: Record<DnsRecordCheck, { icon: typeof Check; className: string; label: string }> = {
    ok: { icon: Check, className: "text-success", label: "Found" },
    failed: { icon: AlertTriangle, className: "text-destructive", label: "Not found" },
    unchecked: { icon: CircleDashed, className: "text-muted-foreground", label: "Not checked yet" },
  };
  // ABSENT IS `unchecked`, the same reading the wire conversion gives a
  // record that carries no verdict. This table is also handed records by
  // callers that predate the column, and the alternative to a default is
  // destructuring undefined — a blank screen for the whole domain detail
  // because one cell had nothing to say.
  //
  // Normalized ONCE, here, so the styling and the test id cannot disagree:
  // reading `check` again below would emit `dns-record-check-undefined`
  // beside an "unchecked" glyph.
  const state: DnsRecordCheck = check ?? "unchecked";
  const { icon: Icon, className, label } = styles[state];
  return (
    <span
      className={cn("inline-flex items-center gap-1 whitespace-nowrap", className)}
      data-testid={`dns-record-check-${state}`}
    >
      <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      {label}
    </span>
  );
}

export function DnsRecordsTable({ records }: { records: DomainDnsRecord[] }) {
  if (records.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        The control plane did not return any DNS records for this domain. That is expected for a
        domain Reliant owns, which needs no setup at your provider.
      </p>
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-border/60 bg-background">
      <table className="w-full text-left text-xs" data-testid="dns-records-table">
        <caption className="sr-only">DNS records to publish for this domain</caption>
        <thead>
          <tr className="border-b border-border/60 text-2xs uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-3 py-2 font-medium">
              Type
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Name
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Value
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Status
            </th>
          </tr>
        </thead>
        <tbody>
          {records.map((record, index) => {
            const purpose = purposeOf(record.type);
            return (
              <tr
                key={`${record.type}-${record.name}-${index}`}
                className={cn(index > 0 && "border-t border-border/60")}
                data-testid={`dns-record-${record.type.toLowerCase()}`}
              >
                <td className="px-3 py-2 align-top font-mono font-medium text-foreground">
                  {record.type}
                </td>
                <td className="px-3 py-2 align-top">
                  <div className="flex items-start gap-1">
                    {/* break-all, not truncate: an ellipsis would be copied
                        as literal dots by anyone selecting the text. */}
                    <span className="break-all font-mono text-foreground">{record.name}</span>
                    <CopyButton value={record.name} label={`${record.type} record name`} />
                  </div>
                </td>
                <td className="px-3 py-2 align-top">
                  <div className="flex items-start gap-1">
                    <span className="break-all font-mono text-foreground">{record.value}</span>
                    <CopyButton value={record.value} label={`${record.type} record value`} />
                  </div>
                  {purpose && <p className="mt-1 max-w-prose text-2xs text-muted-foreground">{purpose}</p>}
                </td>
                <td className="px-3 py-2 align-top">
                  <RecordStatus check={record.check} />
                  {/* The checker's own sentence — what it saw versus what
                      it wanted. Shown only on a failure, where it is the
                      thing that turns a cross into an action. */}
                  {record.detail && (
                    <p
                      className="mt-1 max-w-prose text-2xs text-destructive"
                      data-testid={`dns-record-detail-${record.type.toLowerCase()}`}
                    >
                      {record.detail}
                    </p>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
