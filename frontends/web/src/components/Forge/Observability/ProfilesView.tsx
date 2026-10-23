// Copyright (c) 2025 Reliant Labs

/**
 * Profiles: a flamegraph of where a service spent its CPU (or memory, or
 * goroutines) over the window — optionally only the samples linked to one
 * trace, which is how a slow span becomes a line of code.
 *
 * The flamegraph is speedscope, embedded as an iframe of its own static build
 * (vite-plugin-speedscope.ts), so it costs the app bundle nothing and loads
 * only here. The profile reaches it as a same-origin blob URL. Beside it, the
 * heaviest functions by self time as a plain table: a canvas flamegraph says
 * nothing to a screen reader, and the table is the answer most readers want
 * first anyway.
 */

import { useEffect, useMemo, useState } from "react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { useFlamegraph } from "@/hooks/observability-queries";
import { DEFAULT_PROFILE_TYPES, type ObsScope, type ProfileType } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { formatBytes, formatCount, formatDuration } from "./format";
import { ServiceSelect, shortId } from "./LogsView";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows } from "./ObsStates";

export function ProfilesView({
  scope,
  subject,
  range,
  service,
  profileType,
  traceId,
  navigate,
}: {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  service: string | undefined;
  profileType: string | undefined;
  traceId: string | undefined;
  navigate: ObsNavigate;
}) {
  const requestedType = profileType || "cpu";
  const flamegraph = useFlamegraph(scope, { range, service: service ?? "", profileType: requestedType, traceId });
  // The server says which types this service has; until it answers (or when it
  // lists none), offer the ones forge's producer emits.
  const types = profileTypeOptions(flamegraph.data?.availableProfileTypes ?? [], requestedType);
  const type = types.find((candidate) => candidate.id === requestedType) ?? types[0];

  return (
    <div className="space-y-3" data-testid="obs-profiles">
      <div className="flex flex-wrap items-center gap-2">
        <ServiceSelect
          scope={scope}
          range={range}
          value={service}
          includeAll={false}
          onChange={(next) => navigate({ service: next }, { replace: true })}
          id="obs-profile-service"
        />
        <label htmlFor="obs-profile-type" className="sr-only">
          Profile type
        </label>
        <select
          id="obs-profile-type"
          value={type.id}
          onChange={(event) => navigate({ profileType: event.target.value }, { replace: true })}
          className="h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-[44px]"
          data-testid="obs-profile-type"
        >
          {types.map((candidate) => (
            <option key={candidate.id} value={candidate.id}>
              {candidate.label}
            </option>
          ))}
        </select>
        {traceId && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground" data-testid="obs-profile-trace-scope">
            Only samples from trace <span className="font-mono text-foreground">{shortId(traceId)}</span>
            <button
              type="button"
              className="touch-target inline-flex items-center rounded-sm text-foreground underline underline-offset-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              onClick={() => navigate({ trace: undefined, span: undefined }, { replace: true })}
            >
              Whole service
            </button>
          </p>
        )}
      </div>

      {!service ? (
        <p
          role="status"
          data-testid="obs-profiles-pick"
          className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground"
        >
          Choose a service to see where it spent its {type.unit}.
        </p>
      ) : flamegraph.isLoading ? (
        <Card padding="none">
          <ObsLoadingRows testId="obs-profiles-loading" rows={8} />
        </Card>
      ) : flamegraph.error ? (
        <ObsError error={flamegraph.error} subject={subject} onRetry={() => void flamegraph.refetch()} retrying={flamegraph.isFetching} />
      ) : !flamegraph.data || flamegraph.data.stackCount === 0 ? (
        <p
          role="status"
          data-testid="obs-profiles-empty"
          className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground"
        >
          {traceId
            ? "No profile samples are linked to this trace. The CPU profiler samples 100 times a second, so spans shorter than about 10ms usually have none."
            : `No ${type.label.toLowerCase()} samples from ${service} in this window. Services built with forge send profiles once they run a release with the profiler enabled.`}
        </p>
      ) : (
        // On a phone the heaviest functions lead: a ranked list reads at 390px,
        // where a pan-and-zoom flamegraph is the second thing to reach for.
        <div className="grid grid-cols-[minmax(0,1fr)] gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(18rem,1fr)] max-md:[&>[data-testid=obs-top-functions]]:order-first">
          <Card padding="none" className="overflow-hidden">
            <SpeedscopeFrame
              profile={flamegraph.data.speedscopeJson}
              title={`${service} ${type.label}${traceId ? ` — trace ${shortId(traceId)}` : ""}`}
            />
          </Card>
          <TopFunctions
            profile={flamegraph.data.speedscopeJson}
            stackCount={flamegraph.data.stackCount}
            truncated={flamegraph.data.truncated}
          />
        </div>
      )}
    </div>
  );
}

/** speedscope's static viewer, given the profile as a blob URL it fetches same-origin. */
export function SpeedscopeFrame({ profile, title }: { profile: string; title: string }) {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    const blobUrl = URL.createObjectURL(new Blob([profile], { type: "application/json" }));
    setUrl(blobUrl);
    return () => URL.revokeObjectURL(blobUrl);
  }, [profile]);

  if (!url) return null;
  const src = `${import.meta.env.BASE_URL}speedscope/index.html#profileURL=${encodeURIComponent(url)}&title=${encodeURIComponent(title)}`;
  return (
    <>
    <iframe
      key={url}
      src={src}
      title={`Flamegraph: ${title}`}
      loading="lazy"
      referrerPolicy="no-referrer"
      className="block h-[60vh] min-h-[24rem] w-full border-0 bg-background max-md:h-[50dvh] max-md:min-h-[18rem]"
      data-testid="obs-speedscope"
    />
    {/* Inside a card on a phone the flamegraph has a few hundred pixels and
        competes with the page's own scroll. Its own tab gives it the whole
        screen and the browser's pinch-zoom. The blob stays readable there:
        same origin, and this page (which owns it) is still open. */}
    <a
      href={src}
      target="_blank"
      rel="noopener"
      className="hidden min-h-[44px] items-center justify-center border-t border-border text-sm font-medium text-foreground max-md:flex"
      data-testid="obs-speedscope-fullscreen"
    >
      Open the flamegraph full screen
    </a>
    </>
  );
}

interface SpeedscopeFile {
  shared?: { frames?: { name?: string; file?: string }[] };
  profiles?: { type?: string; unit?: string; samples?: number[][]; weights?: number[] }[];
}

export interface TopFunction {
  name: string;
  self: number;
  /** Fraction of all weight. */
  share: number;
}

/**
 * The functions with the most SELF weight (the leaf of each sample), from a
 * speedscope "sampled" profile. An evented profile, or one that does not
 * parse, yields none — the table is a companion to the flamegraph, never a
 * reason to fail.
 */
export function topFunctions(profileJson: string, limit = 15): { rows: TopFunction[]; unit: string } {
  let file: SpeedscopeFile;
  try {
    file = JSON.parse(profileJson) as SpeedscopeFile;
  } catch {
    return { rows: [], unit: "" };
  }
  const frames = file.shared?.frames ?? [];
  const self = new Map<number, number>();
  let total = 0;
  let unit = "";
  for (const profile of file.profiles ?? []) {
    if (profile.type !== "sampled" || !profile.samples) continue;
    unit = profile.unit ?? unit;
    profile.samples.forEach((stack, index) => {
      const weight = profile.weights?.[index] ?? 1;
      const leaf = stack[stack.length - 1];
      total += weight;
      if (leaf === undefined) return;
      self.set(leaf, (self.get(leaf) ?? 0) + weight);
    });
  }
  const rows = [...self.entries()]
    .sort((a, b) => b[1] - a[1])
    .slice(0, limit)
    .map(([frame, weight]) => ({ name: frames[frame]?.name ?? `frame ${frame}`, self: weight, share: total ? weight / total : 0 }));
  return { rows, unit };
}

function TopFunctions({ profile, stackCount, truncated }: { profile: string; stackCount: number; truncated: boolean }) {
  const { rows, unit } = useMemo(() => topFunctions(profile), [profile]);
  const format = (value: number) => formatProfileValue(value, unit);
  return (
    <Card padding="md" className="self-start" data-testid="obs-top-functions">
      <CardHeader
        title="Heaviest functions"
        description={`By self ${isTimeUnit(unit) ? "time" : unit || "weight"}, over ${formatCount(stackCount)} distinct stacks${truncated ? " (the heaviest kept)" : ""}`}
      />
      {rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">Open the flamegraph to explore this profile.</p>
      ) : (
        <table className="w-full table-fixed text-xs">
          <caption className="sr-only">Functions with the most self weight in this profile</caption>
          <thead>
            <tr className="border-b border-border text-left text-muted-foreground">
              <th scope="col" className="py-1 pr-2 font-medium">
                Function
              </th>
              <th scope="col" className="w-16 py-1 pr-2 text-right font-medium">
                Self
              </th>
              <th scope="col" className="w-12 py-1 text-right font-medium">
                %
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.name} className="border-b border-border/50 last:border-b-0">
                <td className="truncate py-1 pr-2 font-mono text-foreground" title={row.name}>
                  {row.name}
                </td>
                <td className="py-1 pr-2 text-right tabular-nums text-foreground">{format(row.self)}</td>
                <td className="py-1 text-right tabular-nums text-muted-foreground">{(row.share * 100).toFixed(1)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function isTimeUnit(unit: string): boolean {
  return unit === "nanoseconds" || unit === "microseconds" || unit === "milliseconds" || unit === "seconds";
}

/** A speedscope weight in its file's unit. */
export function formatProfileValue(value: number, unit: string): string {
  switch (unit) {
    case "nanoseconds":
      return formatDuration(value / 1e6);
    case "microseconds":
      return formatDuration(value / 1e3);
    case "milliseconds":
      return formatDuration(value);
    case "seconds":
      return formatDuration(value * 1000);
    case "bytes":
      return formatBytes(value);
    default:
      return formatCount(value);
  }
}

/** The picker's options: what the server says the service has, labelled; else forge's defaults. Always includes the current choice. */
export function profileTypeOptions(available: string[], current: string): ProfileType[] {
  const known = new Map(DEFAULT_PROFILE_TYPES.map((type) => [type.id, type]));
  const ids = available.length ? [...available] : DEFAULT_PROFILE_TYPES.map((type) => type.id);
  if (current && !ids.includes(current)) ids.unshift(current);
  return ids.map((id) => known.get(id) ?? { id, label: id, unit: id });
}
