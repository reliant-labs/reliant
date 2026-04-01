import { ChevronDown, ChevronRight } from 'lucide-react';
import { LuBookMarked } from 'react-icons/lu';
import type { SkillInvocationUpdate } from '../../types/streaming';
import { useState } from 'react';

interface SkillInvocationMessageProps {
  invocation: SkillInvocationUpdate;
}

export function SkillInvocationMessage({ invocation }: SkillInvocationMessageProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  const skillLabel = invocation.skill_name || invocation.requested_name || 'unknown';
  const statusLabel = invocation.status;
  const triggerLabel = invocation.trigger;
  const headerLabel = `skills() ${skillLabel}`;

  const warningText = invocation.warnings?.length
    ? `\nWarnings:\n${invocation.warnings.map((w) => `- ${w}`).join('\n')}`
    : '';

  const detailsText = [
    invocation.message || `${statusLabel} via ${triggerLabel} selection`,
    `Status: ${statusLabel}`,
    `Trigger: ${triggerLabel}`,
    invocation.requested_name ? `Requested: ${invocation.requested_name}` : '',
    warningText,
  ]
    .filter(Boolean)
    .join('\n');

  return (
    <div className="w-full rounded-md border border-border overflow-hidden font-mono">
      <div
        className="flex items-center justify-between px-2 py-1.5 bg-muted/30 cursor-pointer hover:bg-muted/50"
        onClick={() => setIsExpanded(!isExpanded)}
        role="button"
        tabIndex={0}
        onKeyDown={(e) =>
          (e.key === 'Enter' || e.key === ' ') &&
          (e.preventDefault(), setIsExpanded(!isExpanded))
        }
        aria-expanded={isExpanded}
        aria-label={`Toggle ${headerLabel} details`}
      >
        <div className="flex items-center gap-2 flex-1 min-w-0">
          <LuBookMarked className="w-3 h-3 text-muted-foreground" />
          <span className="text-[11px] font-medium truncate">{headerLabel}</span>
        </div>

        <div className="flex items-center gap-1 shrink-0">
          {isExpanded ? (
            <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
          )}
        </div>
      </div>

      {isExpanded && (
        <div className="px-2 py-1.5 border-t border-border/30 bg-background text-[11px] text-foreground whitespace-pre-wrap">
          {detailsText}
        </div>
      )}
    </div>
  );
}
