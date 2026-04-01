import { cn } from '../../lib/utils';
import { Info, AlertTriangle, CheckCircle } from 'lucide-react';
import type { Message } from '../../api/client';
import { ContentBlockType, DisplayStyle } from '../../gen/reliant/v1/chat_pb';

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

  // Get icon based on displayStyle
  const getIcon = () => {
    switch (displayStyle) {
      case DisplayStyle.WARNING:
        return <AlertTriangle className="w-4 h-4 text-warning flex-shrink-0" />;
      case DisplayStyle.SUCCESS:
        return <CheckCircle className="w-4 h-4 text-success flex-shrink-0" />;
      case DisplayStyle.INFO:
      default:
        return <Info className="w-4 h-4 text-primary flex-shrink-0" />;
    }
  };

  // Get border/background color based on displayStyle
  const getColorClasses = () => {
    switch (displayStyle) {
      case DisplayStyle.WARNING:
        return 'border-[hsl(var(--warning)/0.3)] bg-[hsl(var(--warning)/0.08)]';
      case DisplayStyle.SUCCESS:
        return 'border-[hsl(var(--success)/0.3)] bg-[hsl(var(--success)/0.08)]';
      case DisplayStyle.INFO:
      default:
        return 'border-primary/30 bg-primary/10';
    }
  };

  // Get the text content from contentBlocks
  const content =
    (message.contentBlocks || []).find(
      (b) => b.type === ContentBlockType.TEXT,
    )?.content ||
    '';

  return (
    <div className={cn(
      "rounded-md border overflow-hidden my-2 px-3 py-2",
      getColorClasses()
    )}>
      <div className="flex items-start gap-2">
        {getIcon()}
        <div className="flex-1 min-w-0">
          <div className="flex items-center justify-between gap-2">
            <div className="text-sm text-foreground">
              {content}
            </div>
            <div className="text-xs text-muted-foreground flex-shrink-0">
              {formatTimestamp(message.createdAt || '')}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
