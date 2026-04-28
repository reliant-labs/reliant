import { BookMarked, ChevronDown, ChevronRight } from 'lucide-react';
import type { InfoUpdate } from '../../types/streaming';
import { useState } from 'react';

interface WorkflowInfoMessageProps {
  info: InfoUpdate;
}

export function WorkflowInfoMessage({ info }: WorkflowInfoMessageProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  const rawMessage = (info.message || '').trim();

  const getIcon = () => {
    return <BookMarked className="w-3 h-3 text-muted-foreground" />;
  };

  const hasTitle = info.title && info.title.trim() !== '';
  const title = hasTitle ? info.title.trim().toLowerCase() : 'info';
  const headerLabel = `${title}()`;
  const detailsText = rawMessage;

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
          {getIcon()}
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