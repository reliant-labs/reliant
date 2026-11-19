import { cn } from '../../lib/utils';
import { Info, AlertTriangle, ArrowLeftRight, CheckCircle, Undo2 } from 'lucide-react';
import type { Message } from '../../api/client';
import { QUIET_ROW_ICON_SLOT_CLASS } from './QuietRow';
import { ContentBlockType, DisplayStyle } from '../../gen/reliant/v1/chat_pb';
import { boldSegments, localizeNoticeTimes, providerNoticeKind } from '../../lib/providerNotice';

interface SystemNotificationMessageProps {
  message: Message;
}

/**
 * Renders messages with displayStyle (info/warning/success) with special styling.
 * These are workflow notifications that have been saved to the thread
 * (e.g., max turns reached, task completed).
 */
export function SystemNotificationMessage({ message }: SystemNotificationMessageProps) {
  const displayStyle = message.displayStyle;
  
  // Format timestamp for display
  const formatTimestamp = (timestamp: string) => {
    try {
      const date = new Date(timestamp);
      return date.toLocaleTimeString('en-US', {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      });
    } catch {
      return timestamp;
    }
  };

  // Get the text content from contentBlocks
  const content =
    (message.contentBlocks || []).find(
      (b) => b.type === ContentBlockType.TEXT,
    )?.content ||
    '';

  // Account-switch notices mark the account and model names in **bold**.
  const noticeKind = displayStyle === DisplayStyle.WARNING ? providerNoticeKind(content) : undefined;

  // A notice is a quiet row, like a tool call: its icon carries the tone,
  // the words stay in muted ink. No tinted box.
  const getIcon = () => {
    if (noticeKind === 'return') {
      return <Undo2 className="size-4 text-warning-ink" />;
    }
    if (noticeKind) {
      return <ArrowLeftRight className="size-4 text-warning-ink" />;
    }
    switch (displayStyle) {
      case DisplayStyle.WARNING:
        return <AlertTriangle className="size-4 text-warning-ink" />;
      case DisplayStyle.SUCCESS:
        return <CheckCircle className="size-4 text-success-ink" />;
      case DisplayStyle.INFO:
      default:
        return <Info className="size-4" />;
    }
  };

  return (
    <div
      data-notice-kind={noticeKind}
      data-testid="system-notice"
      className="group/notice flex min-h-7 min-w-0 items-start gap-2 py-1 text-ui text-muted-foreground"
    >
      <span className={cn(QUIET_ROW_ICON_SLOT_CLASS, 'mt-0.5')} aria-hidden="true">{getIcon()}</span>
      {/* min-w-0 break-words: a flex item never shrinks below its longest
          word by default, so an unbroken path or URL in a notice was cut off
          at the edge instead of wrapping. */}
      <div className="min-w-0 flex-1 break-words">
        {noticeKind
          ? boldSegments(localizeNoticeTimes(content)).map((seg, i) =>
              seg.bold ? (
                <strong key={i} className="font-medium text-foreground">{seg.text}</strong>
              ) : (
                <span key={i}>{seg.text}</span>
              ),
            )
          : content}
      </div>
      <div className="shrink-0 pt-0.5 text-meta tabular-nums opacity-0 transition-opacity group-hover/notice:opacity-100 touch:opacity-100">
        {formatTimestamp(message.createdAt || '')}
      </div>
    </div>
  );
}
