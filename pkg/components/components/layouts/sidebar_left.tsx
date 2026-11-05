import React from "react";

interface SidebarLeftProps {
  /** Navigation items */
  navItems: Array<{
    label: string;
    href?: string;
    icon?: React.ReactNode;
    active?: boolean;
  }>;
  /** Logo/brand element */
  brand?: React.ReactNode;
  children: React.ReactNode;
}

export default function SidebarLeft({ navItems, brand, children }: SidebarLeftProps) {
  return (
    <div className="flex h-screen overflow-hidden">
      {/* Sidebar */}
      <aside className="flex w-64 flex-col border-r border-border bg-surface">
        {brand && (
          <div className="flex h-16 shrink-0 items-center border-b border-border px-6">{brand}</div>
        )}

        <nav className="flex-1 relative overflow-y-auto px-3 py-4">
          <ul className="space-y-1">
            {navItems.map((item, i) => (
              <li key={i}>
                <a
                  href={item.href ?? "#"}
                  className={`flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium ${
                    item.active
                      ? "bg-accent-surface text-accent-ink"
                      : "text-ink-muted hover:bg-surface-muted"
                  }`}
                >
                  {item.icon && <span className="h-5 w-5 shrink-0">{item.icon}</span>}
                  {item.label}
                </a>
              </li>
            ))}
          </ul>
        </nav>
      </aside>

      {/* Main content */}
      <main className="flex-1 relative overflow-y-auto bg-surface-muted p-6">{children}</main>
    </div>
  );
}
