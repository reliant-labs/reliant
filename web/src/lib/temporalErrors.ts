// Temporal wraps every failure in scaffolding frames that identify the event
// that failed. The identifiers are meaningless outside the Temporal history UI,
// and they push the actual cause off the end of the detail pane.
//
// The patterns below mirror the Error() implementations in
// go.temporal.io/sdk/internal/error.go. They must stay in sync with the SDK
// version pinned in go.mod.

const TEMPORAL_FRAMES: RegExp[] = [
  // ActivityError
  /activity error \(type: [^,)]*, scheduledEventID: \d+, startedEventID: \d+, identity: [^)]*\): ?/g,
  // ChildWorkflowExecutionError
  /child workflow execution error \(type: [^,)]*, workflowID: [^,)]*, runID: [^,)]*, initiatedEventID: \d+, startedEventID: \d+\): ?/g,
  // WorkflowExecutionError
  /workflow execution error \(type: [^,)]*, workflowID: [^,)]*, runID: [^)]*\): ?/g,
  // NexusOperationError
  /nexus operation error \(endpoint: "[^"]*", service: "[^"]*", operation: "[^"]*", operation token: "[^"]*", scheduledEventID: \d+\): ?/g,
];

// ApplicationError appends its Go type and retry disposition to every layer of
// the chain. Retryability is already conveyed by the retry badge in the header.
const APPLICATION_ERROR_SUFFIX = / \(type: [^,)]*, retryable: (?:true|false)\)/g;

// TimeoutError renders as "<msg> (type: StartToClose)". The timeout kind is
// real information, so it is reworded rather than dropped.
const TIMEOUT_SUFFIX = / \(type: (StartToClose|ScheduleToStart|ScheduleToClose|Heartbeat)\)/g;

const TIMEOUT_LABELS: Record<string, string> = {
  StartToClose: 'start-to-close',
  ScheduleToStart: 'schedule-to-start',
  ScheduleToClose: 'schedule-to-close',
  Heartbeat: 'heartbeat',
};

/**
 * Collapses runs of the same `: `-delimited segment. Each Temporal layer often
 * re-wraps the message it received, so the same sentence can appear several
 * times in one chain.
 */
function dedupeAdjacentSegments(message: string): string {
  const segments = message.split(': ');
  const kept: string[] = [];

  for (const segment of segments) {
    if (kept.length > 0 && kept[kept.length - 1].trim() === segment.trim()) {
      continue;
    }
    kept.push(segment);
  }

  return kept.join(': ');
}

/**
 * Strips Temporal's bookkeeping from an error string, leaving the causal chain
 * that actually explains the failure.
 *
 * Returns the input unchanged when nothing recognizable is found, and never
 * returns an empty string for non-empty input — if the scaffolding was the
 * whole message, the original is preserved.
 */
export function cleanTemporalErrorMessage(raw: string): string {
  if (!raw) {
    return raw;
  }

  let cleaned = raw;

  for (const frame of TEMPORAL_FRAMES) {
    cleaned = cleaned.replace(frame, '');
  }

  cleaned = cleaned.replace(APPLICATION_ERROR_SUFFIX, '');
  cleaned = cleaned.replace(
    TIMEOUT_SUFFIX,
    (_match, kind: string) => ` (${TIMEOUT_LABELS[kind]} timeout)`
  );

  cleaned = dedupeAdjacentSegments(cleaned);
  cleaned = cleaned.replace(/^[:\s]+/, '').replace(/\s+$/, '');

  return cleaned || raw;
}

/**
 * Reports whether cleaning would actually change the message, so callers can
 * decide whether offering the untouched original is worthwhile.
 */
export function hasTemporalScaffolding(raw: string): boolean {
  return cleanTemporalErrorMessage(raw) !== raw;
}
