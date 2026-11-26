// Copyright (c) 2026 Reliant Labs

/**
 * Observability's own Logs, Traces and Profiles views inside an ML page,
 * pinned to one ML subject: its scope is the subject's attributes
 * (useMlScope), so a run's Logs tab is the logs explorer filtered to
 * `ml.run.id`, a service's Traces tab the trace explorer filtered to its
 * `service.name`. The explorer's own filters live in the component; the trace
 * it is on (an open trace, the logs or flamegraph of one) is the page's
 * `?sel=`, so it survives a reload and can be linked; a jump to another
 * explorer ("logs for this trace") opens that tab.
 */

import { useCallback, useState } from "react";

import type { RangePreset } from "@/services/controlPlane/observability/window";
import type { SubjectRef } from "@/services/controlPlane/ml/telemetry";

import { LogsView } from "../../Observability/LogsView";
import { ProfilesView } from "../../Observability/ProfilesView";
import { TraceDetail } from "../../Observability/TraceDetail";
import { TracesView } from "../../Observability/TracesView";
import { severitiesOf, type ObsNavigate, type ObsSearch } from "../../Observability/obsSearch";
import { useMlSearch } from "./links";
import { ORG_SUBJECT, useMlScope } from "./telemetry";

export type MlExplorerView = "logs" | "traces" | "profiles";

export interface MlExplorerProps {
  view: MlExplorerView;
  subject: SubjectRef;
  /** What the views call it ("llama-8b-support-v3"). */
  label: string;
  range: RangePreset;
  /** The service a profile is read for (profiles are per service), or the one a log or trace list starts on. */
  service?: string;
  /** The explorers this page has a tab for; a jump to any other stays put. */
  tabs?: readonly MlExplorerView[];
  testId?: string;
}

export function MlExplorer({ view, subject, label, range, service, tabs = ["logs", "traces", "profiles"], testId }: MlExplorerProps) {
  const [mlSearch, setMlSearch] = useMlSearch();
  const scope = useMlScope(subject, view === "logs" ? "logs" : "traces");
  const [search, setSearch] = useState<ObsSearch>(() => ({ range, service }));
  const trace = mlSearch.sel;
  const navigate = useCallback<ObsNavigate>(
    (next) => {
      const { view: nextView, ...rest } = next;
      if (nextView && nextView !== view) {
        if ((tabs as readonly string[]).includes(nextView)) setMlSearch({ tab: nextView, sel: rest.trace }, { push: true });
        return;
      }
      if ("trace" in rest) setMlSearch({ sel: rest.trace });
      setSearch((prev) => ({ ...prev, ...rest }));
    },
    [view, tabs, setMlSearch],
  );
  const r = search.range ?? range;
  return (
    <div data-testid={testId}>
      {view === "logs" ? (
        <LogsView
          scope={scope}
          subject={label}
          range={r}
          query={search.q ?? ""}
          service={search.service}
          severities={severitiesOf(search.severity)}
          traceId={trace}
          spanId={search.span}
          live={!!search.live}
          navigate={navigate}
        />
      ) : view === "traces" ? (
        <TracesView
          scope={scope}
          subject={label}
          range={r}
          rootSpanName={search.q ?? ""}
          service={search.service}
          errorsOnly={!!search.errors}
          minDuration={search.minDuration}
          latency={search.lat}
          traceId={trace}
          spanId={search.span}
          navigate={navigate}
        />
      ) : (
        <ProfilesView scope={scope} subject={label} range={r} service={search.service ?? service} profileType={search.profileType} traceId={trace} navigate={navigate} />
      )}
    </div>
  );
}

/**
 * One trace on a page of its own (a training step, an inference request):
 * Observability's trace detail, read in the org (a trace id names one trace
 * wherever it ran). Its links to other explorers stay on the page.
 */
export function MlTrace({ traceId, label, subject = ORG_SUBJECT, testId }: { traceId: string; label: string; subject?: SubjectRef; testId?: string }) {
  const scope = useMlScope(subject, "traces");
  const [span, setSpan] = useState<string | undefined>();
  const navigate = useCallback<ObsNavigate>((next) => {
    if (!next.view && "span" in next) setSpan(next.span);
  }, []);
  return (
    <div data-testid={testId}>
      <TraceDetail scope={scope} subject={label} traceId={traceId} spanId={span} range="7d" navigate={navigate} />
    </div>
  );
}
