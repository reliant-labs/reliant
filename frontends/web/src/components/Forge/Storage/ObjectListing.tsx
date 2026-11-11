// Copyright (c) 2025 Reliant Labs

/**
 * ONE LEVEL OF A BUCKET: the breadcrumb to it, a narrowing by name, and its
 * "folders" (common prefixes) then objects, one page at a time.
 *
 * Only one level is ever listed (the delimiter is always "/"), there are no
 * counts or totals, and the order is the backend's lexical one; a page is
 * whatever the server returned, folders first. Paging is by opaque token,
 * forward only, so the way back is the trail this listing walked
 * (storageUrl-model trailAfterNext); the browser's Back works too, since
 * every page is a URL.
 *
 * Mounted per bucket and level (StorageBrowser keys it), so the trail and the
 * narrowing box start over when either changes.
 */

import { useEffect, useState, type FormEvent } from "react";
import { ChevronLeft, ChevronRight, File, Folder, HardDrive, RefreshCw, Search, X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import { storageErrorKind } from "@/services/controlPlane/storageBrowser/errors";
import { cn } from "@/lib/utils";

import type { AccessConfirmation } from "../AuditedAccess/accessSession-model";
import { CONTROL_FIELD } from "../controlStyles";
import { formatAgo } from "../Observability/format";
import { exactBytes, formatSize } from "./storageObject-model";
import { StorageError, StorageLoading, StorageNote } from "./StorageStates";
import {
  breadcrumbsOf,
  entryName,
  folderOf,
  listRequestOf,
  pageNumber,
  patchStorageSearch,
  prefixOf,
  previousToken,
  shownSegment,
  tokenOf,
  trailAfterNext,
  trailWith,
  type StorageSearch,
} from "./storageUrl-model";
import { useObjectListing } from "./useStorageBrowser";

export interface ObjectListingProps {
  env: { id: string; name: string };
  bucket: string;
  search: StorageSearch;
  access: AccessConfirmation | undefined;
  /** Move to another view. `replace` for a correction (a page link that does not fit) rather than a step. */
  onNavigate: (next: StorageSearch, options?: { replace?: boolean }) => void;
  href: (next: StorageSearch) => string;
  onConfirmationLost: () => void;
  /** Entries per page; 0 lets the server choose (200). */
  pageSize?: number;
}

export function ObjectListing({ env, bucket, search, access, onNavigate, href, onConfirmationLost, pageSize = 0 }: ObjectListingProps) {
  const prefix = prefixOf(search);
  const token = tokenOf(search);
  const listing = useObjectListing(listRequestOf(search, { environmentId: env.id, bucket, access, pageSize }));
  const [trail, setTrail] = useState<string[]>(() => [token]);
  const [tokenDropped, setTokenDropped] = useState(false);

  const kind = listing.error && !listing.isFetching ? storageErrorKind(listing.error) : null;
  useEffect(() => {
    if (kind === "confirmation") onConfirmationLost();
    // A page link that does not fit this bucket and level (another
    // environment's, an old one): open the first page and say why.
    if (kind === "bad-token" && token) {
      setTokenDropped(true);
      onNavigate(patchStorageSearch(search, { token: undefined }), { replace: true });
    }
  }, [kind, token, search, onNavigate, onConfirmationLost]);

  const link = (patch: Partial<StorageSearch>) => href(patchStorageSearch(search, patch));
  const goToken = (next: string) => onNavigate(patchStorageSearch(search, { token: next || undefined }));

  const page = listing.data;
  const walked = trailWith(trail, token);
  const previous = previousToken(walked, token);
  const number = pageNumber(walked, token);
  const folder = folderOf(prefix);
  const narrowing = prefix.slice(folder.length);

  return (
    <div className="min-w-0 space-y-3" data-testid="storage-listing" data-prefix={prefix}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Breadcrumb bucket={bucket} prefix={prefix} link={link} />
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <Narrow key={prefix} initial={narrowing} folder={folder} onNarrow={(typed) => onNavigate(patchStorageSearch(search, { prefix: folder + typed || undefined }))} />
          <Button
            variant="ghost"
            size="xs"
            className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0"
            onClick={() => void listing.refetch()}
            disabled={listing.isFetching}
            leftIcon={<RefreshCw className={cn("h-3 w-3", listing.isFetching && "animate-spin motion-reduce:animate-none")} />}
            data-testid="storage-refresh"
          >
            Refresh
          </Button>
        </div>
      </div>

      {tokenDropped && !token && (
        <p className="text-xs text-ink-muted" data-testid="storage-token-dropped">
          That page link didn&apos;t belong to this bucket and folder, so this is the first page.
        </p>
      )}

      {listing.error && kind !== "confirmation" && (kind !== "bad-token" || !token) ? (
        <StorageError
          error={listing.error}
          env={env.name}
          subject={prefix ? `${bucket}/${prefix}` : `the ${bucket} bucket`}
          onRetry={() => void listing.refetch()}
          retrying={listing.isFetching}
          testId="storage-list-error"
        />
      ) : !page ? (
        <Card padding="none">
          <StorageLoading rows={6} testId="storage-list-loading" label={`Listing ${prefix || bucket}`} />
        </Card>
      ) : page.prefixes.length === 0 && page.objects.length === 0 ? (
        <StorageNote testId="storage-empty" icon={<HardDrive className="h-4 w-4" />} title={emptyTitle(bucket, folder, narrowing, token)}>
          <p>
            {narrowing ? (
              <>
                Narrowing lists only the keys that start with what you typed.{" "}
                <Link href={link({ prefix: folder || undefined })} className="text-primary-ink hover:underline">
                  Show all of {folder || bucket}
                </Link>
              </>
            ) : token ? (
              "The listing ended on the page before."
            ) : (
              "A listing shows one level: the objects directly here and the folders below."
            )}
          </p>
        </StorageNote>
      ) : (
        <Card padding="none" className={cn("min-w-0 max-w-full overflow-x-auto", listing.isPlaceholderData && "opacity-60")} aria-busy={listing.isPlaceholderData}>
          <table className="min-w-full text-sm" aria-label={`${bucket}/${prefix}`} data-testid="storage-table">
            <thead>
              <tr className="text-left text-xs text-ink-muted">
                <th scope="col" className="border-b border-border px-3 py-2 font-medium">
                  Name
                </th>
                <th scope="col" className="border-b border-border px-3 py-2 text-right font-medium">
                  Size
                </th>
                <th scope="col" className="border-b border-border px-3 py-2 font-medium" title="Listings do not include content type. Open an object to read its stored type.">
                  Content type
                </th>
                <th scope="col" className="border-b border-border px-3 py-2 font-medium">
                  Modified
                </th>
                <th scope="col" className="border-b border-border px-3 py-2 font-medium">
                  ETag
                </th>
              </tr>
            </thead>
            <tbody>
              {page.prefixes.map((child) => (
                <tr key={`p:${child}`} className="hover:bg-ink/5" data-testid="storage-folder" data-prefix={child}>
                  <td className="max-w-[28rem] border-b border-border/60 px-3 py-1.5">
                    <Link href={link({ prefix: child })} className="flex min-h-[44px] min-w-0 items-center gap-2 sm:min-h-0 text-ink hover:underline" title={child}>
                      <Folder className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />
                      <span className="truncate whitespace-pre font-mono">{shownSegment(entryName(prefix, child))}/</span>
                    </Link>
                  </td>
                  <td className="border-b border-border/60 px-3 py-1.5 text-right text-ink-muted">—</td>
                  <td className="border-b border-border/60 px-3 py-1.5 text-ink-muted">Folder</td>
                  <td className="border-b border-border/60 px-3 py-1.5 text-ink-muted" />
                  <td className="border-b border-border/60 px-3 py-1.5 text-ink-muted" />
                </tr>
              ))}
              {page.objects.map((object) => {
                const open = object.key === search.object;
                return (
                  <tr key={`o:${object.key}`} className={cn("hover:bg-ink/5", open && "bg-ink/5")} data-testid="storage-object" data-key={object.key}>
                    <td className="relative max-w-[28rem] border-b border-border/60 px-3 py-1.5">
                      {open && <span aria-hidden="true" className="absolute inset-y-1 left-0 w-0.5 rounded-full bg-primary" />}
                      <Link
                        href={link({ object: object.key })}
                        aria-current={open ? "true" : undefined}
                        className={cn("flex min-h-[44px] min-w-0 items-center gap-2 sm:min-h-0 hover:underline", open ? "font-medium text-ink" : "text-ink")}
                        title={object.key}
                      >
                        <File className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />
                        <span className="truncate whitespace-pre font-mono">{shownSegment(entryName(prefix, object.key))}</span>
                      </Link>
                    </td>
                    <td className="whitespace-nowrap border-b border-border/60 px-3 py-1.5 text-right tabular-nums text-ink" title={exactBytes(object.size)}>
                      {formatSize(object.size)}
                    </td>
                    <td className="whitespace-nowrap border-b border-border/60 px-3 py-1.5 text-xs text-ink-muted" title="Not returned by ListObjects; open the object to read its stored content type.">
                      Open for type
                    </td>
                    <td
                      className="whitespace-nowrap border-b border-border/60 px-3 py-1.5 text-ink-muted"
                      title={object.updatedMs !== null ? new Date(object.updatedMs).toISOString() : undefined}
                    >
                      {object.updatedMs !== null ? formatAgo(object.updatedMs) : "—"}
                    </td>
                    <td className="max-w-[10rem] truncate border-b border-border/60 px-3 py-1.5 font-mono text-2xs text-ink-muted" title={object.etag}>
                      {object.etag || "—"}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </Card>
      )}

      {page && (token || page.nextPageToken) && (
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-ink-muted" data-testid="storage-pager">
          <span aria-live="polite" data-testid="storage-page">
            {number ? `Page ${number}` : "A later page"}
            {page.nextPageToken ? "" : " (the last)"}
          </span>
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            {token && previous === null && (
              <Button variant="ghost" size="xs" className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0" onClick={() => goToken("")} data-testid="storage-page-first">
                First page
              </Button>
            )}
            <Button
              variant="outline"
              size="xs"
            className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0"
              onClick={() => previous !== null && goToken(previous)}
              disabled={previous === null || listing.isPlaceholderData}
              leftIcon={<ChevronLeft className="h-3 w-3" />}
              data-testid="storage-page-prev"
            >
              Previous
            </Button>
            <Button
              variant="outline"
              size="xs"
            className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0"
              onClick={() => {
                setTrail(trailAfterNext(walked, token, page.nextPageToken));
                goToken(page.nextPageToken);
              }}
              disabled={!page.nextPageToken || listing.isPlaceholderData}
              rightIcon={<ChevronRight className="h-3 w-3" />}
              data-testid="storage-page-next"
            >
              Next
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

function emptyTitle(bucket: string, folder: string, narrowing: string, token: string): string {
  if (narrowing) return `Nothing in ${folder || bucket} starts with “${narrowing}”`;
  if (token) return "This page is empty";
  return folder ? `Nothing under ${folder}` : `${bucket} is empty`;
}

/** The bucket, then each segment of the prefix; the last is the level shown. */
function Breadcrumb({ bucket, prefix, link }: { bucket: string; prefix: string; link: (patch: Partial<StorageSearch>) => string }) {
  // The compact console uses a 15px rem: literal pixels keep touch targets at 44px.
  const crumbs = breadcrumbsOf(prefix);
  const item = "inline-flex min-h-[44px] min-w-[44px] max-w-full items-center break-all rounded px-1 py-0.5 sm:min-h-0 sm:min-w-0";
  return (
    <nav aria-label="Folder" className="min-w-0" data-testid="storage-breadcrumb">
      <ol className="flex min-w-0 flex-wrap items-center gap-0.5 font-mono text-sm">
        <li className="flex items-center">
          {crumbs.length === 0 ? (
            <span className={cn(item, "font-medium text-ink")} aria-current="page" data-testid="storage-crumb-root">
              {bucket}
            </span>
          ) : (
            <Link href={link({ prefix: undefined })} className={cn(item, "text-ink-muted hover:bg-ink/5 hover:text-ink")} data-testid="storage-crumb-root">
              {bucket}
            </Link>
          )}
        </li>
        {crumbs.map((crumb, index) => {
          const last = index === crumbs.length - 1;
          const label = crumb.partial ? `${crumb.segment}…` : shownSegment(crumb.segment);
          return (
            <li key={crumb.prefix} className="flex min-w-0 items-center">
              <span aria-hidden="true" className="px-0.5 text-ink-subtle">
                /
              </span>
              {last ? (
                <span
                  className={cn(item, "whitespace-pre font-medium text-ink", crumb.segment === "" && "italic text-ink-muted")}
                  aria-current="page"
                  title={crumb.partial ? `keys starting with ${crumb.prefix}` : crumb.prefix}
                  data-testid="storage-crumb"
                >
                  {label}
                </span>
              ) : (
                <Link
                  href={link({ prefix: crumb.prefix })}
                  className={cn(item, "whitespace-pre text-ink-muted hover:bg-ink/5 hover:text-ink", crumb.segment === "" && "italic")}
                  title={crumb.prefix}
                  data-testid="storage-crumb"
                >
                  {label}
                </Link>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

/** Narrow the level to keys starting with what is typed: a prefix the server lists, not a filter of the page. */
function Narrow({ initial, folder, onNarrow }: { initial: string; folder: string; onNarrow: (typed: string) => void }) {
  const [value, setValue] = useState(initial);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (value !== initial) onNarrow(value);
  };
  return (
    <form role="search" onSubmit={submit} className="relative" data-testid="storage-narrow">
      <label className="relative block">
        <span className="sr-only">Keys in {folder || "the bucket"} starting with</span>
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-muted" aria-hidden="true" />
        <input
          type="text"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder="Starts with…"
          className={cn(CONTROL_FIELD, "h-[44px] w-48 max-w-full pl-8 pr-12 font-mono text-sm sm:h-8 sm:pr-7")}
          data-testid="storage-narrow-input"
        />
      </label>
      {initial && (
        <button
          type="button"
          onClick={() => onNarrow("")}
          className="absolute right-0 top-1/2 flex min-h-[44px] min-w-[44px] -translate-y-1/2 items-center justify-center rounded p-0.5 text-ink-muted hover:text-ink sm:right-1.5 sm:min-h-0 sm:min-w-0"
          aria-label="Clear the narrowing"
          data-testid="storage-narrow-clear"
        >
          <X className="h-3 w-3" />
        </button>
      )}
    </form>
  );
}
