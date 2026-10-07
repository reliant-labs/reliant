/**
 * The workflow viewer's execution, with every step.
 *
 * Callers hand the viewer the execution they already hold, and that one comes
 * from the chat's BASIC tree: the timeline needs only the few steps it draws,
 * so that is all it carries. The viewer is the one surface that reconstructs
 * loop iterations, node history and the activity log from EVERY step, so it
 * asks for the FULL view itself — and only while it is mounted, which is the
 * whole point: a long chat's full step history is tens of thousands of rows
 * that nobody pays for until they open the diagram.
 *
 * Until FULL arrives (and for callers with no chat to fetch from) the passed
 * execution is shown as-is, so the diagram renders its structure immediately
 * and fills in step detail when it lands.
 *
 * Shared by the desktop viewer (WorkflowViewerPanel) and the mobile one
 * (MobileChatWorkflowRoute), so both derive node status from the same tree.
 */

import { useMemo } from 'react'
import type { WorkflowExecution } from '../../Chat/ExecutionSidebar/types'
import { transformWorkflowExecution } from '../../Chat/ExecutionSidebar/transformApiData'
import { useWorkflowExecutions } from '../../../hooks/useWorkflowExecutions'
import { WorkflowExecutionView } from '../../../gen/reliant/v1/chat_pb'

export function useFullStepExecution(
  chatId: string | null | undefined,
  execution: WorkflowExecution | undefined,
): WorkflowExecution | undefined {
  const { allWorkflows } = useWorkflowExecutions(
    chatId && execution ? chatId : null,
    WorkflowExecutionView.FULL,
  )
  return useMemo(() => {
    if (!execution) return execution
    const full = allWorkflows.find((wf) => wf.id === execution.id)
    return full ? transformWorkflowExecution(full) : execution
  }, [allWorkflows, execution])
}
