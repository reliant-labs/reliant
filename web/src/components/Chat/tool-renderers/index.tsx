/**
 * Tool renderers index - exports the main ToolContentArea component
 * and all individual renderers
 */

import { memo } from 'react';
import type { ToolRenderContext, ToolResultData } from './types';
import { ShellToolRenderer } from './ShellToolRenderer';
import { FileToolRenderer } from './FileToolRenderer';
import { ReadToolRenderer } from './ReadToolRenderer';
import { PlanToolRenderer } from './PlanToolRenderer';
import { SkillToolRenderer } from './SkillToolRenderer';
import { LoadToolRenderer } from './LoadToolRenderer';
import { GenericToolRenderer } from './GenericToolRenderer';
import { SpawnToolRenderer } from './SpawnToolRenderer';
import {
  isViewOnlyTool,
  isShellTool,
  isFileTool,
  isReadToolWithResults,
  isPlanTool,
  isSkillTool,
  isLoadToolTool,
  isSpawnTool,
} from '../../../lib/toolFormatters';

export type { ToolRenderContext, ToolResultData };

interface ToolContentAreaProps {
  ctx: ToolRenderContext;
}

/**
 * Main component that routes to the appropriate tool renderer
 * based on the tool name
 */
function ToolContentAreaComponent({ ctx }: ToolContentAreaProps) {
  const toolName = ctx.toolName;

  // View-only tools don't render content - they just open files on click
  if (isViewOnlyTool(toolName)) {
    return null;
  }

  // Route to appropriate renderer
  if (isSkillTool(toolName)) {
    return <SkillToolRenderer ctx={ctx} />;
  }

  if (isLoadToolTool(toolName)) {
    return <LoadToolRenderer ctx={ctx} />;
  }

  if (isShellTool(toolName)) {
    return <ShellToolRenderer ctx={ctx} />;
  }

  if (isFileTool(toolName)) {
    return <FileToolRenderer ctx={ctx} />;
  }

  if (isReadToolWithResults(toolName)) {
    return <ReadToolRenderer ctx={ctx} />;
  }

  if (isPlanTool(toolName)) {
    return <PlanToolRenderer ctx={ctx} />;
  }

  if (isSpawnTool(toolName)) {
    return <SpawnToolRenderer ctx={ctx} />;
  }

  // Default to generic renderer
  return <GenericToolRenderer ctx={ctx} />;
}

export const ToolContentArea = memo(ToolContentAreaComponent);

// Re-export individual renderers for direct use if needed
export { ShellToolRenderer } from './ShellToolRenderer';
export { FileToolRenderer } from './FileToolRenderer';
export { ReadToolRenderer } from './ReadToolRenderer';
export { PlanToolRenderer } from './PlanToolRenderer';
export { SkillToolRenderer } from './SkillToolRenderer';
export { LoadToolRenderer } from './LoadToolRenderer';
export { GenericToolRenderer } from './GenericToolRenderer';
export { SpawnToolRenderer } from './SpawnToolRenderer';