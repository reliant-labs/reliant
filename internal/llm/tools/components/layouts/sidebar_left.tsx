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

export default function SidebarLeft({
  navItems,
  brand,
  children,
}: SidebarLeftProps) {
  return (
    <div className="flex h-screen overflow-hidden">
      {/* Sidebar */}
      <aside className="flex w-64 flex-col border-r border-gray-200 bg-white">
        {brand && (
          <div className="flex h-16 shrink-0 items-center border-b border-gray-200 px-6">
            {brand}
          </div>
        )}

        <nav className="flex-1 overflow-y-auto px-3 py-4">
          <ul className="space-y-1">
            {navItems.map((item, i) => (
              <li key={i}>
                <a
                  href={item.href ?? "#"}
                  className={`flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium ${
                    item.active
                      ? "bg-indigo-50 text-indigo-700"
                      : "text-gray-700 hover:bg-gray-100"
                  }`}
                >
                  {item.icon && (
                    <span className="h-5 w-5 shrink-0">{item.icon}</span>
                  )}
                  {item.label}
                </a>
              </li>
            ))}
          </ul>
        </nav>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-y-auto bg-gray-50 p-6">{children}</main>
    </div>
  );
}
