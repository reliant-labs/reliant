// Copyright (c) 2025 Reliant Labs

/**
 * Real-user sessions (control-plane `controlplane.v1.RumService`) in the
 * console's own terms: milliseconds, numbers, and rrweb events already parsed.
 * rumApi.ts is the only file that knows the wire shape.
 *
 * PRIVACY. A replay is masked when it is RECORDED (forge-web-runtime forces
 * maskAllInputs and maskAllText, blocks media and never records canvas), and
 * the server passes the bytes through untouched. Nothing here, or anywhere in
 * the console, can unmask one: the original text never left the browser.
 */

import type { TimeWindow } from "./types";

/** What every RumService call is scoped by: the org (checked server-side), the project, its environments and services. */
export interface RumScope {
  orgId: string;
  projectId?: string;
  /** Empty: every environment of the org (of the project, when set). */
  envIds: string[];
  /** Empty: every service. */
  services?: string[];
}

/** One browser session. */
export interface SessionSummary {
  /** The recorder's rum.sessionId. */
  sessionId: string;
  service: string;
  /** The frontend build the session ran. */
  serviceVersion: string;
  envId: string;
  envName: string;
  /** Empty: the app never identified the user. */
  userId: string;
  /** The first page's path, without query or fragment. */
  entryPath: string;
  startedMs: number;
  endedMs: number;
  pageViews: number;
  interactions: number;
  errors: number;
  /** Whether a replay was recorded. */
  hasReplay: boolean;
}

export interface ListSessionsRequest extends RumScope {
  /** The sessions that STARTED in this window. */
  window: TimeWindow;
  hasError: boolean;
  hasReplay: boolean;
  userId: string;
  /** Case-insensitive substring of the entry path. */
  pathContains: string;
  pageSize?: number;
  pageToken?: string;
}

export interface SessionList {
  /** Newest first. */
  sessions: SessionSummary[];
  /** Empty on the last page. */
  nextPageToken: string;
}

/**
 * A session read. Its window is optional: unset, the server finds the session
 * within the last 30 days and reads the span it was recorded over.
 */
export interface SessionRequest extends RumScope {
  sessionId: string;
}

export const TIMELINE_KINDS = ["navigation", "click", "request_failed", "error", "event"] as const;
export type TimelineKind = (typeof TIMELINE_KINDS)[number];

/**
 * One line of a session's timeline. Nothing in it is a raw URL or element
 * text: paths have no query or fragment, a click names its element's tag and
 * xpath, an error's message has its volatile parts masked.
 */
export interface TimelineEntry {
  timeMs: number;
  /** Null: a kind this build does not know (a newer server). */
  kind: TimelineKind | null;
  /** documentLoad / routeChange, "click", a request's method, an error's span name, a custom event's name. */
  name: string;
  path: string;
  method: string;
  /** A failed request's HTTP status; 0 for a network failure. */
  status: number;
  /** A click's element xpath. */
  selector: string;
  /** A click's element tag. */
  element: string;
  errorType: string;
  message: string;
  /** An error's fingerprint; empty until the issues worker has seen its stack. */
  fingerprint: string;
  /** The issue an error belongs to — what the Errors view opens. Empty until it is grouped. */
  issueId: string;
  traceId: string;
  spanId: string;
  durationMs: number;
  /** A custom event's properties; a navigation's `from`. */
  props: Record<string, string>;
  service: string;
}

export interface TimelineRequest extends SessionRequest {
  pageSize?: number;
  pageToken?: string;
}

export interface Timeline {
  /** Oldest first. */
  entries: TimelineEntry[];
  nextPageToken: string;
}

/**
 * One rrweb event, parsed from the stored JSON. Only what the console reads is
 * typed; the rest goes to the Replayer as recorded.
 */
export interface RrwebEvent {
  type: number;
  timestamp: number;
  data?: unknown;
  [key: string]: unknown;
}

/** rrweb's EventType values the console reads (rrweb's own enum is in @rrweb/types). */
export const RRWEB_FULL_SNAPSHOT = 2;
export const RRWEB_META = 4;

export interface ReplayRequest extends SessionRequest {
  /** The page's byte budget. Absent: the server's 4 MiB. Max 16 MiB. */
  maxBytes?: number;
  /** A next_page_token, or a resume_token to follow a session still being recorded. */
  pageToken?: string;
}

export interface ReplayPage {
  /** Whole events, in recorded order. */
  events: RrwebEvent[];
  /**
   * Events the server sent that this client could not read (not JSON, or not
   * an rrweb event). Skipped, and counted so the player can say so.
   */
  unreadable: number;
  /** Set exactly when more complete events are available now. */
  nextPageToken: string;
  /** Set whenever the page held an event: poll with it for what is recorded since. */
  resumeToken: string;
}
