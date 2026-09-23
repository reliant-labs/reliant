"use client";

import React, { useState } from "react";

import Link from "./link";

interface NavItem {
  label: string;
  href?: string;
  icon?: React.ReactNode;
  active?: boolean;
  section?: string;
}

interface UserInfo {
  name: string;
  email?: string;
  avatar?: React.ReactNode;
}

interface SidebarLayoutProps {
  brand: React.ReactNode;
  navItems: NavItem[];
  user?: UserInfo;
  children: React.ReactNode;
  headerContent?: React.ReactNode;
  /** Controlled mode: sidebar collapsed state. When omitted, uses internal state. */
  collapsed?: boolean;
  /** Controlled mode: toggle callback. When omitted, uses internal handler. */
  onToggle?: () => void;
}

export default function SidebarLayout({
  brand,
  navItems,
  user,
  children,
  headerContent,
  collapsed: controlledCollapsed,
  onToggle,
}: SidebarLayoutProps) {
  const [internalCollapsed, setInternalCollapsed] = useState(false);
  const collapsed = controlledCollapsed ?? internalCollapsed;
  const handleToggle =
    onToggle ?? (() => setInternalCollapsed((prev) => !prev));

  const sections = new Map<string, NavItem[]>();
  for (const item of navItems) {
    const key = item.section ?? "";
    if (!sections.has(key)) sections.set(key, []);
    sections.get(key)!.push(item);
  }

  return (
    <div className="flex h-screen overflow-hidden bg-surface-muted">
      {/* Sidebar */}
      <aside
        className={`flex flex-col border-r border-border bg-surface transition-all duration-200 ${
          collapsed ? "w-16" : "w-64"
        }`}
      >
        {/* Brand */}
        <div className="flex h-16 shrink-0 items-center justify-between border-b border-border px-4">
          {!collapsed && <div className="truncate">{brand}</div>}
          <button
            onClick={handleToggle}
            className="flex h-8 w-8 items-center justify-center rounded-lg text-ink-subtle transition hover:bg-surface-muted hover:text-ink-muted"
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          >
            <svg
              className="h-4 w-4"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              {collapsed ? (
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M13 5l7 7-7 7M5 5l7 7-7 7"
                />
              ) : (
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M11 19l-7-7 7-7m8 14l-7-7 7-7"
                />
              )}
            </svg>
          </button>
        </div>

        {/* Navigation */}
        <nav className="flex-1 overflow-y-auto p-3">
          {Array.from(sections.entries()).map(([section, items], si) => (
            <div key={si} className={si > 0 ? "mt-6" : ""}>
              {section && !collapsed && (
                <p className="mb-2 px-3 text-xs font-semibold uppercase tracking-wider text-ink-subtle">
                  {section}
                </p>
              )}
              {si > 0 && collapsed && (
                <div className="mx-auto my-2 h-px w-6 bg-border" />
              )}
              <ul className="space-y-1">
                {items.map((item, i) => (
                  <li key={i}>
                    <Link
                      href={item.href ?? "#"}
                      aria-current={item.active ? "page" : undefined}
                      className={`flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition ${
                        item.active
                          ? "bg-accent-surface text-accent-ink"
                          : "text-ink-muted hover:bg-surface-muted hover:text-ink"
                      } ${collapsed ? "justify-center" : ""}`}
                      title={collapsed ? item.label : undefined}
                    >
                      {item.icon && (
                        <span className="h-5 w-5 shrink-0">{item.icon}</span>
                      )}
                      {!collapsed && (
                        <span className="truncate">{item.label}</span>
                      )}
                    </Link>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </nav>

        {/* User Area */}
        {user && (
          <div className="shrink-0 border-t border-border p-3">
            <div
              className={`flex items-center gap-3 rounded-lg px-3 py-2 ${
                collapsed ? "justify-center" : ""
              }`}
            >
              <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-border text-sm font-semibold text-ink-muted">
                {user.avatar ?? user.name.charAt(0).toUpperCase()}
              </div>
              {!collapsed && (
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium text-ink">
                    {user.name}
                  </p>
                  {user.email && (
                    <p className="truncate text-xs text-ink-muted">
                      {user.email}
                    </p>
                  )}
                </div>
              )}
            </div>
          </div>
        )}
      </aside>

      {/* Main */}
      <div className="flex flex-1 flex-col overflow-hidden">
        {/* Header Bar */}
        <header className="flex h-16 shrink-0 items-center border-b border-border bg-surface px-6">
          {headerContent}
        </header>

        {/* Content */}
        <main className="flex-1 overflow-y-auto p-6">{children}</main>
      </div>
    </div>
  );
}
