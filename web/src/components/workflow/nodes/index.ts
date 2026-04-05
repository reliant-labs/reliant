/**
 * Workflow node components and type registry
 * 
 * These are shared between WorkflowBuilder and WorkflowViewer
 */

export { RunNode } from './RunNode'
export { ActionNode } from './ActionNode'
export { EventNode } from './EventNode'
export { WorkflowNode } from './WorkflowNode'
export { JoinNode } from './JoinNode'
export { LoopNode } from './LoopNode'
export { ExpandedLoopNode } from './ExpandedLoopNode'
export { SwitchNode } from './SwitchNode'
export { RouterNode } from './RouterNode'
export { getExecutionStatusClasses, getStatusIndicatorClasses, getNodeStatusStyles } from './executionStatus'
export { NodeStatusWrapper, buildHandleClassName, type NodeTheme } from './NodeStatusWrapper'

import { RunNode } from './RunNode'
import { ActionNode } from './ActionNode'
import { EventNode } from './EventNode'
import { WorkflowNode } from './WorkflowNode'
import { JoinNode } from './JoinNode'
import { LoopNode } from './LoopNode'
import { ExpandedLoopNode } from './ExpandedLoopNode'
import { SwitchNode } from './SwitchNode'
import { RouterNode } from './RouterNode'

/**
 * Node type registry for ReactFlow
 * Maps node type strings to their React components
 */
export const nodeTypes = {
  runNode: RunNode,
  actionNode: ActionNode,
  workflowNode: WorkflowNode,
  joinNode: JoinNode,
  loopNode: LoopNode,
  expandedLoopNode: ExpandedLoopNode,
  eventNode: EventNode,
  switchNode: SwitchNode,
  routerNode: RouterNode,
}