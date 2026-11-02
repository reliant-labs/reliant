import { ToolCallStatus } from '../../../gen/reliant/v1/chat_pb';

/** Date labels belong inside original measured rows, not new virtual items. */
export function transcriptDateLabels(messages: ReadonlyArray<{ id: string; createdAt?: string }>): Map<string, string> {
  const labels = new Map<string, string>();
  let previousDay: string | undefined;
  for (const message of messages) {
    const date = new Date(message.createdAt || '');
    if (Number.isNaN(date.getTime())) continue;
    const day = date.toDateString();
    if (day !== previousDay) {
      labels.set(message.id, date.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }));
    }
    previousDay = day;
  }
  return labels;
}

/** Presentation only: never clone messages, reassign blocks, or hide live work. */
export function isQuietToolRun(executions: ReadonlyArray<{
  call: { id?: string; childWorkflowId?: string; durableStatus?: ToolCallStatus };
  result?: { is_error?: boolean };
  approval?: unknown;
  status?: string;
}>): boolean {
  return executions.length > 1 && executions.every((execution) =>
    !!execution.result && !execution.result.is_error && !execution.approval &&
    (!execution.status || execution.status === "completed") &&
    (execution.call.durableStatus === undefined ||
      execution.call.durableStatus === ToolCallStatus.UNSPECIFIED ||
      execution.call.durableStatus === ToolCallStatus.COMPLETED) &&
    !execution.call.childWorkflowId,
  );
}
