/**
 * ActivityIndicator - Displays workflow step execution that didn't save a message
 * 
 * Used to show activity like "lint", "verify", "run tests" that execute
 * but don't produce messages in the chat timeline.
 * 
 * Now with richer context extracted from outputJson based on activity type.
 */

import { memo, useState } from "react";
import {
  Loader2,
  CheckCircle2,
  XCircle,
  Terminal,
  Workflow,
  Clock,
  ChevronDown,
  ChevronRight,
  Wrench,
  Bot,
  FileText,
  GitBranch,
} from "lucide-react";
import { cn } from "../../../lib/utils";
import type { StepExecution } from "../ExecutionSidebar/types";
import {
  getStepDisplayName,
  getStepStatusColor,
  formatStepDuration,
  formatNodeId,
} from "./activityIndicators";

/**
 * Extract contextual info from step based on activity type
 */
interface ActivityContext {
  type: "llm" | "tools" | "run" | "compact" | "spawn" | "workflow" | "unknown";
  summary: string;
  expandedContent?: React.ReactNode;
  icon: typeof Terminal;
}

function extractActivityContext(step: StepExecution): ActivityContext {
  const output = step.outputJson || {};
  const activityName = step.activityName;
  
  // LLM Call - show token counts
  if (activityName.includes("CallLLM") || activityName.includes("LLM")) {
    const inputTokens = output.input_tokens as number | undefined;
    const outputTokens = output.output_tokens as number | undefined;
    const toolCalls = output.tool_calls as unknown[] | undefined;
    const cacheRead = output.cache_read_input_tokens as number | undefined;
    const cacheCreation = output.cache_creation_input_tokens as number | undefined;
    
    let summary = "LLM Call";
    if (inputTokens || outputTokens) {
      const parts = [];
      if (inputTokens) parts.push(`${inputTokens.toLocaleString()} in`);
      if (outputTokens) parts.push(`${outputTokens.toLocaleString()} out`);
      summary = parts.join(", ");
    }
    if (toolCalls?.length) {
      summary += ` → ${toolCalls.length} tool${toolCalls.length > 1 ? "s" : ""}`;
    }
    
    // Expanded content shows cache stats
    let expandedContent: React.ReactNode = null;
    if (cacheRead || cacheCreation) {
      expandedContent = (
        <div className="flex flex-col gap-1 text-xs text-muted-foreground">
          {cacheRead && <span>Cache read: {cacheRead.toLocaleString()} tokens</span>}
          {cacheCreation && <span>Cache created: {cacheCreation.toLocaleString()} tokens</span>}
        </div>
      );
    }
    
    return {
      type: "llm",
      summary,
      expandedContent,
      icon: Bot,
    };
  }
  
  // Tool Execution - show tool names
  if (activityName.includes("ExecuteTools") || activityName.includes("Tools")) {
    const toolResults = output.tool_results as Array<{ tool_name?: string; name?: string; content?: string }> | undefined;
    const toolNames = toolResults?.map(r => r.tool_name || r.name).filter(Boolean) || [];
    
    let summary = "Execute Tools";
    if (toolNames.length > 0) {
      if (toolNames.length <= 2) {
        summary = toolNames.join(", ");
      } else {
        summary = `${toolNames[0]} + ${toolNames.length - 1} more`;
      }
    }
    
    // Expanded content shows all tool names
    const expandedContent = toolNames.length > 0 ? (
      <div className="flex flex-wrap gap-1">
        {toolNames.map((name, i) => (
          <span 
            key={i}
            className="px-1.5 py-0.5 rounded bg-muted/50 font-mono text-xs"
          >
            {name}
          </span>
        ))}
      </div>
    ) : null;
    
    return {
      type: "tools",
      summary,
      expandedContent,
      icon: Wrench,
    };
  }
  
  // Run Step - show command and output
  if (activityName.includes("ExecuteRun") || activityName.includes("Run")) {
    const workingDir = output.working_dir as string | undefined;
    const exitCode = output.exit_code as number | undefined;
    const stdout = output.stdout as string | undefined;
    const stderr = output.stderr as string | undefined;
    const command = output.command as string | undefined;
    
    let summary = "";
    if (workingDir) {
      // Show last part of path
      const parts = workingDir.split("/");
      summary = `in ${parts[parts.length - 1] || parts[parts.length - 2] || workingDir}`;
    }
    if (exitCode !== undefined && exitCode !== 0) {
      summary += summary ? ` (exit ${exitCode})` : `exit ${exitCode}`;
    }
    if (!summary) {
      summary = "Run";
    }
    
    // Expanded content shows command and output
    const expandedContent = (command || stdout || stderr) ? (
      <div className="flex flex-col gap-2 text-xs font-mono">
        {command && (
          <div className="flex items-start gap-2">
            <span className="text-muted-foreground shrink-0">$</span>
            <span className="text-foreground break-all">{command}</span>
          </div>
        )}
        {stdout && (
          <pre className="bg-muted/30 rounded p-2 overflow-x-auto max-h-32 text-muted-foreground whitespace-pre-wrap">
            {stdout.slice(0, 500)}{stdout.length > 500 ? "..." : ""}
          </pre>
        )}
        {stderr && (
          <pre className="bg-destructive/10 rounded p-2 overflow-x-auto max-h-32 text-destructive/80 whitespace-pre-wrap">
            {stderr.slice(0, 500)}{stderr.length > 500 ? "..." : ""}
          </pre>
        )}
      </div>
    ) : null;
    
    return {
      type: "run",
      summary,
      expandedContent,
      icon: Terminal,
    };
  }
  
  // Compaction - show token reduction
  if (activityName.includes("Compact")) {
    const before = output.tokens_before as number | undefined;
    const after = output.tokens_after as number | undefined;
    
    let summary = "Compact Context";
    if (before && after) {
      const reduction = Math.round((1 - after / before) * 100);
      summary = `${before.toLocaleString()} → ${after.toLocaleString()} (-${reduction}%)`;
    }
    
    return {
      type: "compact",
      summary,
      icon: FileText,
    };
  }
  
  // Spawn workflow / Create worktree
  if (activityName.includes("Spawn") || activityName.includes("Worktree") || activityName.includes("Agent")) {
    return {
      type: "spawn",
      summary: "Spawning",
      icon: GitBranch,
    };
  }
  
  // Generic workflow step
  return {
    type: "unknown",
    summary: "",
    icon: Workflow,
  };
}

interface ActivityIndicatorProps {
  step: StepExecution;
  workflowName?: string;
  className?: string;
}

export const ActivityIndicator = memo(function ActivityIndicator({
  step,
  workflowName,
  className,
}: ActivityIndicatorProps) {
  const [isExpanded, setIsExpanded] = useState(false);
  
  const displayName = getStepDisplayName(step.stepId);
  const color = getStepStatusColor(step);
  const duration = formatStepDuration(step.durationMs);
  const context = extractActivityContext(step);
  
  const StatusIcon = step.status === "running" 
    ? Loader2 
    : step.status === "failed" || (step.exitCode !== undefined && step.exitCode !== 0)
    ? XCircle
    : CheckCircle2;
  
  const TypeIcon = context.icon;
  
  // Get exit code if available
  const exitCode = step.exitCode;
  const showExitCode = exitCode !== undefined && exitCode !== 0;
  
  // Can expand if we have expanded content
  const canExpand = !!context.expandedContent;
  
  // Show loop context if in a loop
  const loopContext = step.loopNodeId ? `(${step.loopNodeId})` : null;

  return (
    <div className={cn("group", className)}>
      <div
        className={cn(
          "flex items-center gap-3 px-4 py-2 text-sm",
          "bg-muted/20 border-l-2",
          canExpand && "cursor-pointer hover:bg-muted/30"
        )}
        style={{ borderLeftColor: color }}
        onClick={canExpand ? () => setIsExpanded(!isExpanded) : undefined}
      >
        {/* Expand indicator */}
        {canExpand ? (
          <div className="flex-shrink-0 -ml-1">
            {isExpanded ? (
              <ChevronDown className="h-3 w-3 text-muted-foreground" />
            ) : (
              <ChevronRight className="h-3 w-3 text-muted-foreground" />
            )}
          </div>
        ) : (
          <div className="flex-shrink-0 -ml-1 w-3" /> // Spacer for alignment
        )}
        
        {/* Type icon */}
        <TypeIcon className="h-4 w-4 text-muted-foreground flex-shrink-0" />
        
        {/* Step name + context */}
        <div className="flex items-center gap-2 min-w-0 flex-1">
          <span className="font-medium truncate">{displayName}</span>
          {context.summary && (
            <span className="text-xs text-muted-foreground truncate">
              {context.summary}
            </span>
          )}
          {loopContext && (
            <span className="text-xs text-muted-foreground/60 truncate">
              {loopContext}
            </span>
          )}
          {workflowName && workflowName !== "agent" && (
            <span className="text-xs text-muted-foreground/60 truncate">
              in {formatNodeId(workflowName)}
            </span>
          )}
        </div>
        
        {/* Duration */}
        {duration && (
          <div className="flex items-center gap-1 text-xs text-muted-foreground flex-shrink-0">
            <Clock className="h-3 w-3" />
            <span>{duration}</span>
          </div>
        )}
        
        {/* Exit code */}
        {showExitCode && (
          <span className="text-xs px-1.5 py-0.5 rounded bg-destructive/10 text-destructive flex-shrink-0">
            exit {exitCode}
          </span>
        )}
        
        {/* Status icon */}
        <StatusIcon 
          className={cn(
            "h-4 w-4 flex-shrink-0",
            step.status === "running" && "animate-spin"
          )}
          style={{ color }}
        />
      </div>
      
      {/* Expanded details */}
      {isExpanded && context.expandedContent && (
        <div 
          className="ml-8 px-4 py-2 bg-muted/10 border-l-2" 
          style={{ borderLeftColor: color }}
        >
          {context.expandedContent}
        </div>
      )}
    </div>
  );
});

/**
 * Compact activity indicator for inline use
 */
interface CompactActivityIndicatorProps {
  step: StepExecution;
  className?: string;
}

export const CompactActivityIndicator = memo(function CompactActivityIndicator({
  step,
  className,
}: CompactActivityIndicatorProps) {
  const displayName = getStepDisplayName(step.stepId);
  const color = getStepStatusColor(step);
  const duration = formatStepDuration(step.durationMs);
  const context = extractActivityContext(step);
  
  const StatusIcon = step.status === "running" 
    ? Loader2 
    : step.status === "failed" || (step.exitCode !== undefined && step.exitCode !== 0)
    ? XCircle
    : CheckCircle2;

  return (
    <div
      className={cn(
        "inline-flex items-center gap-1.5 px-2 py-1 rounded-full text-xs",
        "bg-muted/50 border border-border",
        className
      )}
    >
      <StatusIcon 
        className={cn(
          "h-3 w-3",
          step.status === "running" && "animate-spin"
        )}
        style={{ color }}
      />
      <span className="font-medium">{displayName}</span>
      {context.summary && (
        <span className="text-muted-foreground">{context.summary}</span>
      )}
      {duration && (
        <span className="text-muted-foreground">{duration}</span>
      )}
    </div>
  );
});
