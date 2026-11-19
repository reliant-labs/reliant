import { Info } from 'lucide-react';
import type { InfoUpdate } from '../../types/streaming';
import { useState } from 'react';
import { QuietRow } from './QuietRow';

interface WorkflowInfoMessageProps {
  info: InfoUpdate;
}

/** An informational notice from the run: one quiet row, its text beneath. */
export function WorkflowInfoMessage({ info }: WorkflowInfoMessageProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  const detailsText = (info.message || '').trim();
  const rawTitle = info.title?.trim() || 'Info';
  const title = rawTitle.charAt(0).toUpperCase() + rawTitle.slice(1);

  return (
    <QuietRow
      as="button"
      icon={<Info className="size-4" />}
      expanded={isExpanded}
      onToggle={detailsText ? () => setIsExpanded((open) => !open) : undefined}
      ariaLabel={`Toggle ${title} details`}
      detail={<div className="whitespace-pre-wrap text-ui text-muted-foreground">{detailsText}</div>}
    >
      <span className="truncate">{title}</span>
    </QuietRow>
  );
}
