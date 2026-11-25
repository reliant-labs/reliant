import React from "react";

interface ActivityItem {
  id: string;
  user: { name: string; avatar?: string };
  action: string;
  target?: string;
  timestamp: string;
  icon?: React.ReactNode;
  iconColor?: string;
  detail?: React.ReactNode;
}

interface ActivityFeedProps {
  items: ActivityItem[];
  title?: string;
  emptyMessage?: string;
}

function getInitials(name: string): string {
  return name
    .split(/\s+/)
    .map((w) => w[0])
    .filter(Boolean)
    .slice(0, 2)
    .join("")
    .toUpperCase();
}

function RelativeTime({ timestamp }: { timestamp: string }) {
  const date = new Date(timestamp);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMins / 60);
  const diffDays = Math.floor(diffHours / 24);

  let display: string;
  if (diffMins < 1) display = "just now";
  else if (diffMins < 60) display = `${diffMins}m ago`;
  else if (diffHours < 24) display = `${diffHours}h ago`;
  else if (diffDays < 7) display = `${diffDays}d ago`;
  else
    display = date.toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
    });

  return (
    <time dateTime={timestamp} className="text-xs text-ink-subtle" title={date.toLocaleString()}>
      {display}
    </time>
  );
}

export default function ActivityFeed({
  items,
  title,
  emptyMessage = "No recent activity",
}: ActivityFeedProps) {
  return (
    <div>
      {title && <h3 className="mb-4 text-sm font-semibold text-ink">{title}</h3>}
      {items.length === 0 ? (
        <p className="py-6 text-center text-sm text-ink-subtle">{emptyMessage}</p>
      ) : (
        <div className="flow-root">
          <ul className="-mb-4">
            {items.map((item, idx) => (
              <li key={item.id} className="relative pb-4">
                {idx < items.length - 1 && (
                  <span className="absolute left-5 top-5 -ml-px h-full w-0.5 bg-surface-muted" />
                )}
                <div className="relative flex items-start gap-3">
                  {/* Avatar / Icon */}
                  <div className="relative flex-shrink-0">
                    {item.icon ? (
                      <div
                        className={`flex h-10 w-10 items-center justify-center rounded-full ${
                          item.iconColor ?? "bg-surface-muted text-ink-muted"
                        }`}
                      >
                        {item.icon}
                      </div>
                    ) : item.user.avatar ? (
                      <img
                        src={item.user.avatar}
                        alt={item.user.name}
                        className="h-10 w-10 rounded-full object-cover"
                      />
                    ) : (
                      <div className="flex h-10 w-10 items-center justify-center rounded-full bg-accent-surface text-xs font-medium text-accent">
                        {getInitials(item.user.name)}
                      </div>
                    )}
                  </div>

                  {/* Content */}
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center justify-between gap-2">
                      <p className="text-sm text-ink-muted">
                        <span className="font-medium text-ink">{item.user.name}</span> {item.action}
                        {item.target && (
                          <>
                            {" "}
                            <span className="font-medium text-ink">{item.target}</span>
                          </>
                        )}
                      </p>
                      <RelativeTime timestamp={item.timestamp} />
                    </div>
                    {item.detail && <div className="mt-1">{item.detail}</div>}
                  </div>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
