// Copyright (c) 2026 Reliant Labs

/**
 * How the ML fixture world AUTHORS its telemetry: each domain (training.ts,
 * fleet.ts, inference.ts, ...) answers "the series / events / logs / traces /
 * profiles / alerts of this subject" in the shapes below, and ./telemetry.ts
 * serves them through the console's ObservabilityService and AlertingService
 * handlers (handlers/observability.ts), keyed by the attributes the subject's
 * telemetry carries (services/controlPlane/ml/telemetry.ts). There is no ML
 * telemetry rpc: these are the world's own records, nothing on the wire.
 *
 * Series and events are the contract's MlSeries and MlEvent (the domain
 * messages carry them too: a finding's evidence, a run's objective); the
 * rest are plain records named after what produces them.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import type { MlAxis, MlEventKind, MlEventSchema, MlFactSchema, MlSeries, MlSeriesSchema, MlSeverity, MlSubjectSchema } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

export type SubjectInit = MessageInitShape<typeof MlSubjectSchema>;
/** A series as a plain init (never the message itself), so spreads and overrides stay inits. */
export type SeriesInit = Exclude<MessageInitShape<typeof MlSeriesSchema>, MlSeries>;
export type EventInit = MessageInitShape<typeof MlEventSchema>;
/** Any series a domain answers: an init, or a message it built. */
export type AnySeriesInit = MessageInitShape<typeof MlSeriesSchema>;
type FactInit = MessageInitShape<typeof MlFactSchema>;
type Now = () => number;

/** The subject a read names, as the domains' functions take it. */
export interface SubjectArg {
  kind: SubjectInit["kind"] & number;
  id: string;
  label: string;
}

/** What a step tracer or request tracer kept. */
export const MlTraceKind = { UNSPECIFIED: 0, TRAIN_STEP: 1, CHECKPOINT: 2, DATA_PIPELINE: 3, INFERENCE_REQUEST: 4, EVAL: 5, POD_START: 6 } as const;
export type MlTraceKind = (typeof MlTraceKind)[keyof typeof MlTraceKind];

export const MlSpanStatus = { UNSPECIFIED: 0, OK: 1, ERROR: 2 } as const;
export type MlSpanStatus = (typeof MlSpanStatus)[keyof typeof MlSpanStatus];

/** Which profiler took a capture. */
export const MlProfileKind = { UNSPECIFIED: 0, PYTORCH_PROFILER: 1, NSIGHT_SYSTEMS: 2, NSIGHT_COMPUTE: 3, MEMORY_SNAPSHOT: 4, FLIGHT_RECORDER: 5, PY_SPY: 6, CPU_CONTINUOUS: 7, GPU_CONTINUOUS: 8 } as const;
export type MlProfileKind = (typeof MlProfileKind)[keyof typeof MlProfileKind];

/** An alert rule's state; only PENDING and FIRING reach AlertingService.ListAlerts. */
export const MlAlertState = { UNSPECIFIED: 0, OK: 1, PENDING: 2, FIRING: 3, SILENCED: 4 } as const;
export type MlAlertState = (typeof MlAlertState)[keyof typeof MlAlertState];

/** One line a pod wrote. */
export interface LogLineInit {
  time?: Timestamp;
  /** -1 when the line is not from a rank (an init container, the launcher). */
  rank?: number;
  pod?: string;
  nodeId?: string;
  container?: string;
  level?: string;
  message?: string;
  /** "nccl", "xid", "oom", "traceback", "shm", "nan", "watchdog"; empty for an ordinary line. */
  highlight?: string;
  eventId?: string;
  attempt?: number;
  runId?: string;
  phaseKey?: string;
  traceId?: string;
}

/** A kept trace, as its list row. */
export interface TraceSummaryInit {
  traceId?: string;
  kind?: MlTraceKind;
  title?: string;
  start?: Timestamp;
  durationMs?: number;
  vsMedian?: number;
  dominantSpan?: string;
  subject?: SubjectInit;
  step?: bigint;
  rank?: number;
  error?: boolean;
  attributes?: Record<string, string>;
}

export interface SpanEventInit {
  time?: Timestamp;
  name?: string;
  attributes?: Record<string, string>;
}

/** One span, with the join keys every ML span carries. */
export interface SpanInit {
  spanId?: string;
  parentSpanId?: string;
  name?: string;
  service?: string;
  kind?: string;
  start?: Timestamp;
  durationMs?: number;
  status?: MlSpanStatus;
  statusMessage?: string;
  attributes?: Record<string, string>;
  events?: SpanEventInit[];
  runId?: string;
  phaseKey?: string;
  attempt?: number;
  rank?: number;
  nodeId?: string;
  deviceUuid?: string;
  pod?: string;
}

export interface KernelInit {
  name?: string;
  share?: number;
  calls?: bigint;
  meanUs?: number;
  category?: string;
}

export interface CollectiveInit {
  rank?: number;
  lastSeq?: bigint;
  op?: string;
  sizeBytes?: bigint;
  state?: string;
  mismatched?: boolean;
  pod?: string;
}

/** One profiler capture; `flamegraphJson` is a speedscope file. */
export interface ProfileInit {
  id?: string;
  kind?: MlProfileKind;
  trigger?: string;
  subject?: SubjectInit;
  fromStep?: bigint;
  toStep?: bigint;
  ranks?: number[];
  sizeBytes?: bigint;
  capturedAt?: Timestamp;
  openUrl?: string;
  openLabel?: string;
  summary?: {
    computeShare?: number;
    commShare?: number;
    memcpyShare?: number;
    idleShare?: number;
    commOverlap?: number;
    launchBound?: boolean;
    topKernels?: KernelInit[];
    peakMemoryGb?: number;
    notes?: FactInit[];
  };
  flamegraphJson?: string;
  collectives?: CollectiveInit[];
  capturedBy?: string;
}

/** An alert rule and its state; a pending or firing one is an AlertingService alert. */
export interface AlertRuleInit {
  id?: string;
  name?: string;
  /** As declared: "train/loss z-score > 6 for 3 steps". */
  expression?: string;
  severity?: MlSeverity;
  state?: MlAlertState;
  subject?: SubjectInit;
  declaredIn?: string;
  lastFiredAt?: Timestamp;
  fired7d?: number;
  autoAction?: string;
  autoActionEnabled?: boolean;
  routedTo?: string;
  lastAction?: string;
}

export interface SeriesQuery {
  subject?: SubjectArg;
  /** Neutral keys; one ending "/*" reads its family. */
  keys: string[];
  axis: MlAxis;
  /** A series per member: "rank", "device", "node", "pool", "replica", "revision", ... */
  groupBy?: string;
  maxPoints?: number;
  from?: Timestamp;
  to?: Timestamp;
}

export interface EventsQuery {
  subject?: SubjectArg;
  kinds: MlEventKind[];
  limit?: number;
  from?: Timestamp;
  to?: Timestamp;
}

export interface LogsQuery {
  subject?: SubjectArg;
  rank?: number;
  minLevel?: string;
  query?: string;
  highlightsOnly?: boolean;
  limit?: number;
  from?: Timestamp;
  to?: Timestamp;
}

export interface TracesQuery {
  subject?: SubjectArg;
  kind?: MlTraceKind;
  limit?: number;
}

/** A domain's telemetry: any subset, each undefined when the subject is not one of its own. */
export interface MlTelemetryDomain {
  series?: (req: SeriesQuery, now: Now) => { series: AnySeriesInit[] } | undefined;
  events?: (req: EventsQuery, now: Now) => { events: EventInit[] } | undefined;
  logs?: (req: LogsQuery, now: Now) => { lines: LogLineInit[]; truncated: boolean } | undefined;
  traces?: (req: TracesQuery, now: Now) => { traces: TraceSummaryInit[]; missingFindingId?: string } | undefined;
  trace?: (req: { traceId: string }, now: Now) => { summary?: TraceSummaryInit; spans: SpanInit[]; truncated?: boolean } | undefined;
  profiles?: (req: { subject?: SubjectArg }, now: Now) => { profiles: ProfileInit[] } | undefined;
  profile?: (req: { profileId: string }, now: Now) => { profile?: ProfileInit } | undefined;
  alerts?: (req: { subject?: SubjectArg }, now: Now) => { alerts: AlertRuleInit[] } | undefined;
}
