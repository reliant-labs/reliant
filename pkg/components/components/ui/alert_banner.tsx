import React, { useState } from "react";

type AlertVariant = "info" | "success" | "warning" | "error";

/**
 * AlertBanner — page-level info/success/warning/error banner.
 *
 * `title` and `message` are ReactNode for the same reason PageHeader's are:
 * a real banner says "3 of 12 prescriptions failed — <Link>review them</Link>",
 * and a string prop forces the caller to rebuild the icon/variant/layout
 * scaffolding by hand to get one link into the sentence. A plain string still
 * satisfies ReactNode, so existing call sites are unaffected.
 *
 * Every other `<div>` attribute (`id`, `className`, `tabIndex`, `aria-*`,
 * `data-*`) and a ref reach the root, as Card's and Input's do. A banner that
 * owns its root is a banner a caller cannot name, focus or find: an error
 * summary that must take focus when a submit fails, and say what failed, had
 * to wrap this one in a second `role="alert"` box (two announcements) or
 * rebuild it. `role` defaults to "alert"; pass `role="status"` for a banner
 * that should not interrupt.
 */
interface AlertBannerProps extends Omit<React.HTMLAttributes<HTMLDivElement>, "title"> {
  variant?: AlertVariant;
  title?: React.ReactNode;
  message: React.ReactNode;
  dismissible?: boolean;
  onDismiss?: () => void;
  action?: { label: string; onClick: () => void };
}

const variantConfig: Record<
  AlertVariant,
  { bg: string; border: string; icon: string; title: string; text: string }
> = {
  info: {
    bg: "bg-accent-surface",
    border: "border-accent-border",
    icon: "text-accent",
    title: "text-accent-ink",
    text: "text-accent-ink",
  },
  success: {
    bg: "bg-success-surface",
    border: "border-success-border",
    icon: "text-success",
    title: "text-success-ink",
    text: "text-success-ink",
  },
  warning: {
    bg: "bg-warning-surface",
    border: "border-warning-border",
    icon: "text-warning",
    title: "text-warning-ink",
    text: "text-warning-ink",
  },
  error: {
    bg: "bg-danger-surface",
    border: "border-danger-border",
    icon: "text-danger",
    title: "text-danger-ink",
    text: "text-danger-ink",
  },
};

const icons: Record<AlertVariant, React.ReactNode> = {
  info: (
    <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M11.25 11.25l.041-.02a.75.75 0 011.063.852l-.708 2.836a.75.75 0 001.063.853l.041-.021M21 12a9 9 0 11-18 0 9 9 0 0118 0zm-9-3.75h.008v.008H12V8.25z"
      />
    </svg>
  ),
  success: (
    <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M9 12.75L11.25 15 15 9.75M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
      />
    </svg>
  ),
  warning: (
    <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126zM12 15.75h.007v.008H12v-.008z"
      />
    </svg>
  ),
  error: (
    <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M12 9v3.75m9-.75a9 9 0 11-18 0 9 9 0 0118 0zm-9 3.75h.008v.008H12v-.008z"
      />
    </svg>
  ),
};

const AlertBanner = React.forwardRef<HTMLDivElement, AlertBannerProps>(function AlertBanner(
  { variant = "info", title, message, dismissible, onDismiss, action, className, ...rest },
  ref,
) {
  const [visible, setVisible] = useState(true);
  const config = variantConfig[variant];

  if (!visible) return null;

  function handleDismiss() {
    setVisible(false);
    onDismiss?.();
  }

  return (
    <div
      ref={ref}
      role="alert"
      {...rest}
      className={["rounded-lg border p-4", config.bg, config.border, className ?? ""]
        .filter(Boolean)
        .join(" ")}
    >
      <div className="flex items-start gap-3">
        <div className={`flex-shrink-0 ${config.icon}`}>{icons[variant]}</div>
        <div className="flex-1 min-w-0">
          {title ? <p className={`text-sm font-semibold ${config.title}`}>{title}</p> : null}
          {/* div, not p: a composed message may itself contain block content,
              and <p><div/></p> is invalid HTML that React hydrates wrong. */}
          <div className={`text-sm ${config.text} ${title ? "mt-1" : ""}`}>{message}</div>
          {action && (
            <button
              onClick={action.onClick}
              className={`mt-2 text-sm font-medium underline ${config.title} hover:opacity-80`}
            >
              {action.label}
            </button>
          )}
        </div>
        {dismissible && (
          // aria-label, not text: the button's only child is an icon, so
          // without it a screen reader announces "button" and nothing else.
          // type="button" keeps a dismiss from submitting a surrounding form.
          <button
            type="button"
            aria-label="Dismiss"
            onClick={handleDismiss}
            className={`flex-shrink-0 ${config.icon} hover:opacity-70`}
          >
            <svg
              className="h-4 w-4"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth={2}
              stroke="currentColor"
            >
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        )}
      </div>
    </div>
  );
});

export default AlertBanner;
