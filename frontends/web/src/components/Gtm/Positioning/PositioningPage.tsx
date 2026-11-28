// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Positioning & facts (`/gtm/positioning`; the plan's "Settings"): the
 * positioning every draft starts from (GetPositioning / UpdatePositioning),
 * and the facts copy may claim (ListClaims), each with its source. Claim
 * checks count only facts a person has verified; verifying is a person's act
 * and needs the evidence (UpsertClaim status VERIFIED with source_url and
 * source_kind; a comparison needs a primary third-party source).
 */

import { Code } from "@connectrpc/connect";
import { Pencil } from "lucide-react";
import { useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import Card, { CardBody, CardHeader } from "@/components/forge-ui/card";
import Combobox from "@/components/forge-ui/combobox";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import EmptyState from "@/components/forge-ui/empty_state";
import Input from "@/components/forge-ui/input";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import Textarea from "@/components/forge-ui/textarea";
import {
  GtmBusinessModel,
  GtmBusinessType,
  GtmClaimCategory,
  GtmClaimSourceKind,
  GtmClaimStatus,
  type GtmClaim,
  type GtmPositioning,
} from "@/gen/reliant/v1/gtm_pb";
import { useGetPositioning, useListClaims, useUpdatePositioning, useUpsertClaim } from "@/hooks/gtm-service-hooks_gen";

import { GtmShell, useGtmOrgId } from "../shared/GtmShell";
import { PersonActionButton, PersonActionDialog } from "../shared/PersonAction";
import { BUSINESS_MODELS, BUSINESS_TYPES, businessModelLabel, businessTypeLabel, ClaimStatusBadge, formatDay, OutboundLink } from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";

/** The most facts the table lists; the list's own page limit (ListClaimsRequest.page_size). */
const CLAIMS_PAGE = 500;

const SOURCE_KINDS: { value: GtmClaimSourceKind; label: string }[] = [
  { value: GtmClaimSourceKind.DOCS, label: "Docs" },
  { value: GtmClaimSourceKind.PRICING_CATALOG, label: "Pricing catalog" },
  { value: GtmClaimSourceKind.CODE, label: "Code" },
  { value: GtmClaimSourceKind.PRIMARY_THIRD_PARTY, label: "Primary third-party source" },
];
const sourceKindLabel = (kind: GtmClaimSourceKind) => SOURCE_KINDS.find((k) => k.value === kind)?.label;

type ClaimRow = GtmClaim & Record<string, unknown>;

export function PositioningPage() {
  return (
    <GtmShell page="positioning">
      <PositioningView />
    </GtmShell>
  );
}

export function PositioningView() {
  const orgId = useGtmOrgId();
  const positioningQ = useGetPositioning({ orgId }, { enabled: !!orgId });
  const claimsQ = useListClaims({ orgId, pageSize: CLAIMS_PAGE }, { enabled: !!orgId });
  const [verifying, setVerifying] = useState<GtmClaim | null>(null);
  const claims = useMemo(() => claimsQ.data?.claims ?? [], [claimsQ.data]);
  const verified = claims.filter((c) => c.status === GtmClaimStatus.VERIFIED).length;

  const columns: DataTableColumn<ClaimRow>[] = useMemo(
    () => [
      {
        key: "text",
        header: "Fact",
        stackTitle: true,
        render: (_, row) => (
          <span className="block whitespace-normal py-1">
            <span className="block text-ink">{row.text}</span>
            {row.notes && <span className="mt-0.5 block text-xs text-ink-muted">{row.notes}</span>}
          </span>
        ),
      },
      {
        key: "sourceUrl",
        header: "Source",
        width: "240px",
        render: (_, row) => (
          <span className="block whitespace-normal text-ink-muted">
            {row.sourceUrl ? (
              <OutboundLink url={row.sourceUrl} className="break-all underline underline-offset-2">
                {sourceKindLabel(row.sourceKind) ?? row.sourceUrl}
              </OutboundLink>
            ) : (
              "No source yet"
            )}
          </span>
        ),
      },
      {
        key: "status",
        header: "Verified by a person",
        width: "200px",
        render: (_, row) => (
          <span className="flex flex-col items-start gap-1">
            <ClaimStatusBadge status={row.status} />
            {row.status === GtmClaimStatus.VERIFIED && row.verifiedBy && (
              <span className="text-xs text-ink-muted">
                {row.verifiedBy}, {formatDay(row.verifiedAt)}
              </span>
            )}
          </span>
        ),
      },
      {
        key: "actions",
        header: "Actions",
        srHeader: true,
        interactive: true,
        width: "120px",
        render: (_, row) =>
          row.status === GtmClaimStatus.UNVERIFIED ? <PersonActionButton action="verify" size="xs" subject={row.text} onClick={() => setVerifying(row)} /> : null,
      },
    ],
    [],
  );

  return (
    <>
      <PageHeader title="Positioning & facts" meta={<span>What every draft starts from, and what copy may claim.</span>} className="" />
      {positioningQ.isPending ? (
        <SkeletonLoader variant="card" />
      ) : positioningQ.isError && positioningQ.error.code !== Code.NotFound ? (
        <EmptyState title="Couldn't load the positioning" description="Something went wrong reading it." actionLabel="Try again" onAction={() => void positioningQ.refetch()} />
      ) : (
        <PositioningCard key={positioningQ.data?.positioning?.revision ?? 0} positioning={positioningQ.data?.positioning} />
      )}

      <section aria-labelledby="gtm-facts" className="space-y-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 id="gtm-facts" className="text-base font-semibold text-ink">
            Facts
          </h2>
          {claimsQ.isSuccess && (
            <span className="text-sm text-ink-muted">
              {verified} of {claims.length} verified by a person. Claim checks count only verified facts.
            </span>
          )}
        </div>
        {claimsQ.isError ? (
          <EmptyState title="Couldn't load the facts" description="Something went wrong reading this org's facts." actionLabel="Try again" onAction={() => void claimsQ.refetch()} />
        ) : (
          <DataTable
            columns={columns}
            data={claims as ClaimRow[]}
            loading={claimsQ.isPending}
            getRowKey={(row) => row.id}
            layout="fixed"
            stacked
            ariaLabel="Facts"
            emptyMessage="No facts yet. The agent proposes them as it drafts; a person verifies each against its source."
          />
        )}
        {claimsQ.data?.nextPageToken ? (
          <p className="text-xs text-ink-muted" data-testid="gtm-facts-truncated">
            Showing the first {CLAIMS_PAGE} facts.
          </p>
        ) : null}
      </section>
      {verifying && <VerifyDialog key={verifying.id} claim={verifying} onClose={() => setVerifying(null)} />}
    </>
  );
}

const SELECT =
  "h-9 w-full rounded-md border border-border-strong bg-surface px-2.5 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:w-72";

/**
 * The positioning, read or edited: the statement, the audiences, and the
 * business profile (what kind of business, how it makes money), which the
 * strategy library's "Fits my business" reads. None yet is revision 0: the
 * first save creates it. A save sends the whole record.
 */
function PositioningCard({ positioning }: { positioning: GtmPositioning | undefined }) {
  const orgId = useGtmOrgId();
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(positioning?.positioningMd ?? "");
  const [audiences, setAudiences] = useState((positioning?.audiences ?? []).join(", "));
  const [businessType, setBusinessType] = useState<GtmBusinessType>(positioning?.businessType ?? GtmBusinessType.UNSPECIFIED);
  const [models, setModels] = useState<GtmBusinessModel[]>(positioning?.businessModels ?? []);
  const toggleModel = (m: GtmBusinessModel) => setModels((cur) => (cur.includes(m) ? cur.filter((x) => x !== m) : [...cur, m]));
  const onError = useGtmWriteError();
  const update = useUpdatePositioning({ onSuccess: () => setEditing(false), onError });
  const reset = () => {
    setText(positioning?.positioningMd ?? "");
    setAudiences((positioning?.audiences ?? []).join(", "));
    setBusinessType(positioning?.businessType ?? GtmBusinessType.UNSPECIFIED);
    setModels(positioning?.businessModels ?? []);
    setEditing(false);
  };
  const save = () =>
    update.mutate({
      orgId,
      expectedRevision: positioning?.revision ?? 0,
      positioningMd: text,
      audiences: audiences
        .split(",")
        .map((a) => a.trim())
        .filter(Boolean),
      businessType,
      // In the proto's order, each once.
      businessModels: BUSINESS_MODELS.map((m) => m.id).filter((m) => models.includes(m)),
    });
  const statement = positioning?.positioningMd.trim() ?? "";

  return (
    <Card>
      <CardHeader
        title="Positioning"
        description={positioning?.updatedBy ? `Last edited by ${positioning.updatedBy}, ${formatDay(positioning.updatedAt)}` : undefined}
        actions={
          editing ? (
            <div className="flex gap-2">
              <Button variant="secondary" size="sm" onClick={reset} disabled={update.isPending}>
                Cancel
              </Button>
              <Button variant="primary" size="sm" onClick={save} loading={update.isPending}>
                Save
              </Button>
            </div>
          ) : (
            <Button variant="ghost" size="sm" leftIcon={<Pencil />} onClick={() => setEditing(true)}>
              Edit
            </Button>
          )
        }
      />
      <CardBody>
        {editing ? (
          <div className="space-y-3">
            <Textarea aria-label="Positioning" value={text} onChange={(e) => setText(e.target.value)} rows={9} resize="vertical" />
            <label className="block text-sm font-medium text-ink" htmlFor="gtm-audiences">
              Audiences <span className="font-normal text-ink-muted">(comma-separated)</span>
            </label>
            <Input id="gtm-audiences" value={audiences} onChange={(e) => setAudiences(e.target.value)} />
            <label className="block space-y-1.5 text-sm" htmlFor="gtm-business-type">
              <span className="block font-medium text-ink">Business type</span>
              <select id="gtm-business-type" className={SELECT} value={businessType} onChange={(e) => setBusinessType(Number(e.target.value) as GtmBusinessType)}>
                <option value={GtmBusinessType.UNSPECIFIED}>Not set</option>
                {BUSINESS_TYPES.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.label}
                  </option>
                ))}
              </select>
            </label>
            <fieldset className="space-y-1.5 text-sm">
              <legend className="font-medium text-ink">Business models</legend>
              <div className="flex flex-wrap gap-x-4 gap-y-2">
                {BUSINESS_MODELS.map((m) => (
                  <label key={m.id} className="inline-flex items-center gap-2 text-ink">
                    <input type="checkbox" className="size-4 accent-ink" checked={models.includes(m.id)} onChange={() => toggleModel(m.id)} />
                    {m.label}
                  </label>
                ))}
              </div>
            </fieldset>
          </div>
        ) : (
          <>
            {statement ? (
              <div className="max-w-prose space-y-3 text-sm leading-relaxed text-ink" data-testid="gtm-positioning">
                {statement.split(/\n\s*\n/).map((p, i) => (
                  <p key={i} className="whitespace-pre-wrap">
                    {p}
                  </p>
                ))}
              </div>
            ) : (
              <p className="text-sm text-ink-muted">No positioning yet. Write what every draft should start from.</p>
            )}
            <BusinessProfile positioning={positioning} />
            {(positioning?.audiences.length ?? 0) > 0 && (
              <div className="mt-4 flex flex-wrap items-center gap-2">
                <span className="text-xs text-ink-muted">Audiences</span>
                {positioning!.audiences.map((a) => (
                  <Tag key={a}>{a}</Tag>
                ))}
              </div>
            )}
          </>
        )}
      </CardBody>
    </Card>
  );
}

/** Verify a fact: a person vouches for it against its source, which the verification records. */
function VerifyDialog({ claim, onClose }: { claim: GtmClaim; onClose: () => void }) {
  const orgId = useGtmOrgId();
  const comparison = claim.category === GtmClaimCategory.COMPARISON;
  const [sourceUrl, setSourceUrl] = useState(claim.sourceUrl);
  const [sourceKind, setSourceKind] = useState<GtmClaimSourceKind>(
    comparison ? GtmClaimSourceKind.PRIMARY_THIRD_PARTY : claim.sourceKind || GtmClaimSourceKind.DOCS,
  );
  const onError = useGtmWriteError();
  const upsert = useUpsertClaim({ onSuccess: onClose, onError: (e) => (onClose(), onError(e)) });
  const kinds = comparison ? SOURCE_KINDS.filter((k) => k.value === GtmClaimSourceKind.PRIMARY_THIRD_PARTY) : SOURCE_KINDS;

  return (
    <PersonActionDialog
      open
      onClose={onClose}
      action="verify"
      subject={claim.text}
      busy={upsert.isPending}
      confirmDisabled={sourceUrl.trim() === ""}
      onConfirm={() =>
        upsert.mutate({ orgId, id: claim.id, expectedRevision: claim.revision, status: GtmClaimStatus.VERIFIED, sourceUrl: sourceUrl.trim(), sourceKind })
      }
    >
      <div className="space-y-3">
        <div>
          <label className="mb-1.5 block text-sm font-medium text-ink" htmlFor="gtm-verify-source">
            Source you checked it against
          </label>
          <Input id="gtm-verify-source" type="url" placeholder="https://" value={sourceUrl} onChange={(e) => setSourceUrl(e.target.value)} />
        </div>
        <div>
          <label className="mb-1.5 block text-sm font-medium text-ink" htmlFor="gtm-verify-kind">
            Kind of source
          </label>
          <Combobox
            id="gtm-verify-kind"
            options={kinds.map((k) => ({ value: String(k.value), label: k.label }))}
            value={String(sourceKind)}
            onChange={(v) => v && setSourceKind(Number(v) as GtmClaimSourceKind)}
          />
          {comparison && <p className="mt-1.5 text-xs text-ink-muted">A claim about another company needs a dated primary source of theirs.</p>}
        </div>
      </div>
    </PersonActionDialog>
  );
}

/** The business profile, read: what the strategy library's "Fits my business" goes by. */
function BusinessProfile({ positioning }: { positioning: GtmPositioning | undefined }) {
  const type = positioning?.businessType ?? GtmBusinessType.UNSPECIFIED;
  const models = positioning?.businessModels ?? [];
  return (
    <dl className="mt-4 grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[8rem_minmax(0,1fr)]" data-testid="gtm-business-profile">
      <dt className="text-ink-muted">Business type</dt>
      <dd className="text-ink">{type === GtmBusinessType.UNSPECIFIED ? <span className="text-ink-muted">Not set</span> : businessTypeLabel(type)}</dd>
      <dt className="text-ink-muted">Business models</dt>
      <dd className="flex flex-wrap gap-1.5 text-ink">
        {models.length === 0 ? <span className="text-ink-muted">Not set</span> : models.map((m) => <Tag key={m}>{businessModelLabel(m)}</Tag>)}
      </dd>
    </dl>
  );
}
