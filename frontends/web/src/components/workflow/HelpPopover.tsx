import { HelpCircle, X } from "lucide-react";

import Popover from "../forge-ui/popover";

interface HelpPopoverProps {
  /** Help content: a string (newlines kept) or JSX. */
  content: React.ReactNode;
  /** What the help is about; titles the panel and names the button. */
  title?: string;
  /** Size of the trigger icon. */
  iconSize?: "sm" | "md";
}

/**
 * A help icon beside a step-config field that opens its explanation: forge's
 * popover (a labelled dialog that portals, flips, follows scroll, closes on
 * Escape, an outside press or focus leaving, and hands focus back), with a
 * titled header. Help longer than a hint, so a popover and not a tooltip.
 */
export function HelpPopover({ content, title, iconSize = "sm" }: HelpPopoverProps) {
  const heading = title || "Help";
  return (
    <Popover
      trigger={<HelpCircle aria-hidden className={iconSize === "sm" ? "h-3.5 w-3.5" : "h-4 w-4"} />}
      triggerLabel={title ? `Help: ${title}` : "Help"}
      triggerClassName={`inline-flex items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-ring aria-expanded:bg-muted aria-expanded:text-foreground ${
        iconSize === "sm" ? "p-0.5" : "p-1"
      }`}
      label={heading}
      panelClassName="w-[280px]"
    >
      {({ close }) => (
        <>
          <div className="flex items-center justify-between border-b border-border px-3 py-2">
            <span className="text-sm font-medium text-foreground">{heading}</span>
            <button
              type="button"
              onClick={close}
              aria-label="Close"
              className="rounded p-0.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
            >
              <X aria-hidden className="h-3.5 w-3.5" />
            </button>
          </div>
          <div className="px-3 py-2 text-sm text-muted-foreground">
            {typeof content === "string" ? <p className="whitespace-pre-line">{content}</p> : content}
          </div>
        </>
      )}
    </Popover>
  );
}
